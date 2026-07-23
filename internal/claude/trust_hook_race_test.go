package claude

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
// the tenth of a second. Only the timing budget is injected (watchForTrustDialog
// takes it as a parameter) so the loop can be exercised on a sub-second budget
// instead of the production one.
//
// What this reproduces and what it does not: the mechanism is faithful — a
// dialog that renders after the hook's budget has elapsed is never dismissed —
// and TestLateRenderingDialogIsNeverDismissed is the positive control that
// fails-by-design under the old fixed-guess shape. What is NOT reproduced here
// is the production trigger: a genuinely CPU-starved host under concurrent
// spawns pushing the real Claude Code TUI past 8 seconds. That race was not
// constructed; the dialog is not even reachable on this machine (~/.claude.json
// carries a blanket "/" entry with hasTrustDialogAccepted: true). The scripted
// delay stands in for the starvation, not the other way round.

const (
	// dialogLine is the real Claude Code trust-dialog prompt.
	dialogLine = "Quick safety check: Is this a project you created or one you trust?"
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
func lateDialogScript(delay string) string {
	return "sleep " + delay + "\n" +
		"printf '" + dialogLine + "\\n'\n" +
		"read -r _\n" +
		"printf '" + answeredMarker + "\\n'\n" +
		"sleep 30\n"
}

// TestLateRenderingDialogIsNeverDismissed is the POSITIVE CONTROL for
// drellem2/macguffin#25: it reproduces the defect rather than the fix.
//
// The hook is given a budget SHORTER than the dialog's render delay — which is
// exactly what a fixed 8s wall-clock guess becomes on a host loaded enough to
// push the TUI past it. The hook returns, the dialog renders into an empty
// room, and nothing ever answers it. That is the stall CloverRoss hit 3/3: no
// composer, ready-sentinel never matches, kickoff prompt never delivered.
//
// If this test ever starts failing — i.e. a too-short budget still gets the
// dialog dismissed — the mechanism described in the ticket is wrong and the
// rest of this file is resting on a bad premise.
func TestLateRenderingDialogIsNeverDismissed(t *testing.T) {
	a := spawnScripted(t, "late-ctl", lateDialogScript("0.7"))

	// Budget expires well before the dialog renders at ~0.7s.
	watchForTrustDialog(a, 250*time.Millisecond, 50*time.Millisecond)

	// The dialog does render — the script is not broken, the hook just wasn't
	// watching any more.
	if !sawWithin(a, "safety check", 3*time.Second) {
		t.Fatal("script never rendered the dialog: the control proves nothing")
	}
	if sawWithin(a, answeredMarker, 1*time.Second) {
		t.Error("dialog was answered after the hook's budget expired — the " +
			"late-render mechanism in drellem2/macguffin#25 does not hold")
	}
}

// TestLateRenderingDialogIsDismissedWithinTheNudgeBudget is the same scenario
// with the shipped shape: a budget tied to the initial-nudge cold-start budget
// rather than an independent 8s guess. The dialog renders late and is still
// dismissed.
func TestLateRenderingDialogIsDismissedWithinTheNudgeBudget(t *testing.T) {
	a := spawnScripted(t, "late-fix", lateDialogScript("0.7"))

	watchForTrustDialog(a, 5*time.Second, 50*time.Millisecond)

	if !sawWithin(a, answeredMarker, 3*time.Second) {
		t.Errorf("late-rendering trust dialog was not dismissed; PTY:\n%s",
			agent.StripANSI(a.RecentOutput(4096)))
	}
}

// TestHookReturnsEarlyOnceComposerIsUp pins the early exit. Watching for the
// full 60s budget would be a real cost if the hook always spent it; it does not,
// because a rendered composer resolves the hook immediately.
func TestHookReturnsEarlyOnceComposerIsUp(t *testing.T) {
	a := spawnScripted(t, "early-out",
		"printf '? for shortcuts\\n'\nsleep 30\n")

	start := time.Now()
	watchForTrustDialog(a, 10*time.Second, 50*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("hook took %v to notice the composer was already up; it must "+
			"return early rather than poll out the whole budget", elapsed)
	}
}

// TestEchoedPromptIsNotTypedInto is why composerReady had to come with the
// longer window.
//
// trustDialogMarker matches PTY *text*, and Claude echoes its kickoff prompt
// into the TUI. A prompt that merely mentions a "safety check" matches. At the
// old 8s budget the hook had almost always expired before the prompt was
// echoed; at the initial-nudge budget it is still watching. On an already-
// trusted worktree (Respawn re-enters the same Dir; Claude persists trust per
// path) there is no dialog to find — so an unguarded hook would match the echo
// and press Enter into the live composer, submitting a half-typed nudge.
//
// The composer is up here, so the hook must return without sending anything.
func TestEchoedPromptIsNotTypedInto(t *testing.T) {
	echoed := "Investigate the Quick safety check dialog and report back"

	// Precondition: the echoed prompt really does look like the dialog.
	if !matchesTrustDialog([]byte(echoed)) {
		t.Fatal("precondition changed: the echoed prompt no longer matches " +
			"trustDialogMarker — if the marker got stricter this guard may be " +
			"redundant, but verify before deleting composerReady")
	}

	a := spawnScripted(t, "echo-guard",
		"printf '? for shortcuts\\n'\n"+
			"printf '"+echoed+"\\n'\n"+
			"read -r _\n"+
			"printf 'POGO-TYPED-INTO-COMPOSER\\n'\n"+
			"sleep 30\n")

	watchForTrustDialog(a, 3*time.Second, 50*time.Millisecond)

	if sawWithin(a, "POGO-TYPED-INTO-COMPOSER", 1*time.Second) {
		t.Error("hook pressed Enter into a live composer after matching the " +
			"echoed prompt — composerReady must gate it off")
	}
}
