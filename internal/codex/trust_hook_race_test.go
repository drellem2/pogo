package codex

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
)

// These tests drive the REAL hook loop against a REAL Agent on a REAL PTY —
// agent.Registry.Spawn forks a scripted shell whose output timing we control to
// the tenth of a second. Only the timing budget is injected
// (watchForTrustDialog takes it as a parameter) so the loop can be exercised on
// a sub-second budget instead of the production one.
//
// They are the codex half of drellem2/pogo#91 and mirror
// internal/cursor/trust_hook_race_test.go and
// internal/claude/trust_hook_race_test.go deliberately: the defect being removed
// is the same shape in all three providers, so it is worth being able to diff
// the files and see the same controls.
//
// What this reproduces and what it does not: the mechanism is faithful — a
// dialog that renders after the hook's budget has elapsed is never dismissed —
// and TestLateRenderingDialogIsNeverDismissed is the positive control that
// fails-by-design under the old fixed-guess shape. What is NOT reproduced is the
// production trigger: a genuinely CPU-starved host under concurrent spawns
// pushing the real Codex TUI past 12 seconds. The scripted delay stands in for
// the starvation, not the other way round.

const (
	// dialogLine is the real Codex directory-trust dialog body (0.132.0). Codex
	// draws it glyph-by-glyph so the spaces vanish under StripANSI; the spaced
	// form is used here because matchesTrustDialog collapses before matching, so
	// either form must work and the readable one is the better test input.
	dialogLine = "Working with untrusted contents comes with higher risk of prompt injection."
	// composerLine is the status-box row carrying promptReadySentinel, as Codex
	// draws it once the composer is up.
	composerLine = "model:       gpt-5.5   " + promptReadySentinel
	// answeredMarker is printed by the script only after it reads a line from
	// the PTY — i.e. only if the hook actually answered the dialog.
	answeredMarker = "POGO-DIALOG-ANSWERED"
)

// spawnScripted forks `sh -c script` on a real PTY under a real Registry and
// returns the live Agent. The provider is a copy of the real one with both
// lifecycle hooks removed and the initial nudge disabled, so the only thing
// touching this PTY is the hook the test drives itself.
func spawnScripted(t *testing.T, name, script string) *agent.Agent {
	t.Helper()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh to script a PTY with: %v", err)
	}

	p := Provider
	p.PostSpawnHook = nil
	p.SessionHook = nil
	p.Nudge.NeedsInitialNudge = false

	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte("# persona\n"), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(5 * time.Second) })
	reg.RegisterProvider(&p)

	a, err := reg.Spawn(agent.SpawnRequest{
		Name:       name,
		Type:       agent.TypePolecat,
		Command:    []string{sh, "-c", script},
		PromptFile: promptFile,
		Dir:        dir,
		Provider:   &p,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return a
}

// sawWithin polls the agent's PTY output for want until timeout.
func sawWithin(a *agent.Agent, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Contains(agent.StripANSI(a.RecentOutput(8192)), []byte(want)) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// lateDialogScript renders the trust dialog only after delay, then blocks
// reading the PTY. It prints answeredMarker if and only if something answers.
//
// A plain canonical-mode `read` suffices — unlike cursor, which sends a bare "a"
// accelerator with no terminator and needs the line discipline dropped to
// min=1. Codex's accept key is "\r", which ICRNL turns into a newline, so an
// ordinary read completes.
func lateDialogScript(delay string) string {
	return "sleep " + delay + "\n" +
		"printf '" + dialogLine + "\\n'\n" +
		"read _ignored\n" +
		"printf '" + answeredMarker + "\\n'\n" +
		"sleep 30\n"
}

// TestLateRenderingDialogIsNeverDismissed is the POSITIVE CONTROL for the codex
// half of drellem2/pogo#91: it reproduces the defect rather than the fix.
//
// The hook is given a budget SHORTER than the dialog's render delay — which is
// exactly what a fixed 12s wall-clock guess becomes on a host loaded enough to
// push the TUI past it. The hook returns, the dialog renders into an empty room,
// and nothing ever answers it. No composer follows, so the readiness sentinel
// never matches and the polecat is stalled until a human answers by hand.
//
// If this test ever starts failing — i.e. a too-short budget still gets the
// dialog dismissed — the mechanism is wrong and the rest of this file is resting
// on a bad premise.
func TestLateRenderingDialogIsNeverDismissed(t *testing.T) {
	a := spawnScripted(t, "late-ctl", lateDialogScript("0.7"))

	// Budget expires well before the dialog renders at ~0.7s.
	watchForTrustDialog(a, 250*time.Millisecond, 50*time.Millisecond)

	// The dialog does render — the script is not broken, the hook just wasn't
	// watching any more.
	if !sawWithin(a, dialogLine, 3*time.Second) {
		t.Fatal("script never rendered the dialog: the control proves nothing")
	}
	if sawWithin(a, answeredMarker, 1*time.Second) {
		t.Error("dialog was answered after the hook's budget expired — the " +
			"late-render mechanism this fix is built on does not hold")
	}
}

// TestLateRenderingDialogIsDismissedWithinTheColdStartBudget is the same
// scenario with the shipped shape: a budget tied to the provider's own
// cold-start budget rather than an independent 12s guess. The dialog renders
// late and is still dismissed.
func TestLateRenderingDialogIsDismissedWithinTheColdStartBudget(t *testing.T) {
	a := spawnScripted(t, "late-fix", lateDialogScript("0.7"))

	watchForTrustDialog(a, 5*time.Second, 50*time.Millisecond)

	if !sawWithin(a, answeredMarker, 3*time.Second) {
		t.Errorf("late-rendering trust dialog was not dismissed; PTY:\n%s",
			agent.StripANSI(a.RecentOutput(4096)))
	}
}

// TestHookReturnsEarlyOnceComposerIsUp pins the early exit. Watching for the
// full cold-start budget would be a real cost if the hook always spent it; it
// does not, because a rendered status box resolves the hook immediately.
func TestHookReturnsEarlyOnceComposerIsUp(t *testing.T) {
	a := spawnScripted(t, "early-out",
		"printf '"+composerLine+"\\n'\nsleep 30\n")

	start := time.Now()
	watchForTrustDialog(a, 10*time.Second, 50*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("hook took %v to notice the composer was already up; it must "+
			"return early rather than poll out the whole budget", elapsed)
	}
}

// TestEchoedTaskIsNotTypedInto is why composerReady had to come with the longer
// window — and why this ticket was split off #91 rather than folded into it.
//
// trustDialogMarker matches PTY *text*, and the harness echoes the nudged task
// into the TUI. A task that merely quotes the dialog matches. At the old 12s
// budget the hook had usually expired before the echo; at the cold-start budget
// it is still watching. On an already-trusted directory (Respawn re-enters the
// same Dir; Codex persists trust in ~/.codex/config.toml) there is no dialog to
// find — so an unguarded hook would match the echo and press Enter into the live
// composer, submitting a half-typed nudge.
func TestEchoedTaskIsNotTypedInto(t *testing.T) {
	echoed := "Investigate why Working with untrusted contents appears at spawn"

	// Precondition: the echoed task really does look like the dialog.
	if !matchesTrustDialog([]byte(echoed)) {
		t.Fatal("precondition changed: the echoed task no longer matches " +
			"trustDialogMarker — if the marker got stricter this guard may be " +
			"redundant, but verify before deleting composerReady")
	}

	a := spawnScripted(t, "echo-guard",
		"printf '"+composerLine+"\\n'\n"+
			"printf '"+echoed+"\\n'\n"+
			"read _ignored\n"+
			"printf 'POGO-TYPED-INTO-COMPOSER\\n'\n"+
			"sleep 30\n")

	watchForTrustDialog(a, 3*time.Second, 50*time.Millisecond)

	if sawWithin(a, "POGO-TYPED-INTO-COMPOSER", 1*time.Second) {
		t.Error("hook sent Enter into a live composer after matching the " +
			"echoed task — composerReady must gate it off")
	}
}

// TestWatchTerminatesWhenTheAgentExits proves the widened watch still ends with
// its agent. Every other test here proves the hook FIRES, and all of them pass
// on an implementation that never stops — which is exactly the cost of widening
// the window, since the hook runs once per spawn and a watcher outliving its
// agent is a leak that compounds.
//
// The agent here renders NEITHER marker — no dialog, no composer — so the only
// two ways out of the loop are the deadline and a.Done(). The budget is set far
// longer than the test is willing to wait, which means a hook that returns
// promptly can only have returned via a.Done().
func TestWatchTerminatesWhenTheAgentExits(t *testing.T) {
	// Silent, then exits on its own well inside the budget.
	a := spawnScripted(t, "exit-ctl", "printf 'working\\n'\nsleep 0.5\nexit 0\n")

	// A budget the test would never sit through, so returning early cannot be
	// the deadline firing.
	const budget = 90 * time.Second

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchForTrustDialog(a, budget, 50*time.Millisecond)
	}()

	select {
	case <-done:
		// Returned without the deadline: the a.Done() arm did it.
	case <-time.After(15 * time.Second):
		t.Fatal("watchForTrustDialog did not return after the agent exited — " +
			"the watcher outlives its agent, and at one per spawn that leak " +
			"compounds. The a.Done() arm of the select must end the watch.")
	}

	// Guard the premise: if the script had rendered a composer marker the hook
	// would have returned via composerReady and this test would prove nothing.
	if composerReady(a.RecentOutput(8192)) {
		t.Error("premise broken: the scripted agent rendered a composer-ready " +
			"marker, so the early return proves nothing about agent exit")
	}
	if matchesTrustDialog(a.RecentOutput(8192)) {
		t.Error("premise broken: the scripted agent rendered something matching " +
			"the trust-dialog marker, so the early return proves nothing about " +
			"agent exit")
	}
}

// TestTrustDialogTimeoutIsTheColdStartBudget is the regression pin for the codex
// half of drellem2/pogo#91. The bug was a fixed wall-clock guess that could
// expire before a loaded host rendered the dialog. The bound must be the
// provider's own cold-start budget, so there is one timeout concept rather than
// two that disagree.
func TestTrustDialogTimeoutIsTheColdStartBudget(t *testing.T) {
	if want := Provider.Nudge.InitialNudgeTimeout; TrustDialogTimeout != want {
		t.Errorf("TrustDialogTimeout = %v, want the cold-start budget %v — the "+
			"hook that unblocks the composer must not stop watching before the "+
			"spawn path stops waiting for it", TrustDialogTimeout, want)
	}
	if TrustDialogTimeout <= 12*time.Second {
		t.Errorf("TrustDialogTimeout = %v: back at or below the fixed 12s that "+
			"let a late-rendering dialog go undismissed", TrustDialogTimeout)
	}
}
