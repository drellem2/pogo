package cursor

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
// They are the cursor half of drellem2/pogo#91 and mirror
// internal/claude/trust_hook_race_test.go deliberately: the defect being removed
// is the same shape in both providers, so it is worth being able to diff the two
// files and see the same four controls.
//
// What this reproduces and what it does not: the mechanism is faithful — a
// dialog that renders after the hook's budget has elapsed is never dismissed —
// and TestLateRenderingDialogIsNeverDismissed is the positive control that
// fails-by-design under the old fixed-guess shape. What is NOT reproduced is the
// production trigger: a genuinely CPU-starved host under concurrent spawns
// pushing the real Cursor TUI past 12 seconds. The scripted delay stands in for
// the starvation, not the other way round.

const (
	// dialogLine is the real Cursor trust-dialog header (2026.07.09-a3815c0).
	dialogLine = "Workspace Trust Required"
	// composerLine is the real composer placeholder — promptReadySentinel as
	// Cursor draws it.
	composerLine = "> " + promptReadySentinel
	// answeredMarker is printed by the script only after it reads a byte from
	// the PTY — i.e. only if the hook actually answered the dialog.
	answeredMarker = "POGO-DIALOG-ANSWERED"
)

// readOneByte makes the scripted shell able to observe Cursor's accept key.
//
// It is needed here and not in claude's equivalent because the two providers
// answer with different keys: claude sends "\r", which ICRNL turns into a
// newline and so completes an ordinary canonical-mode `read`. Cursor sends the
// bare "a" accelerator (see trustDialogAccept) with no terminator, and a
// canonical-mode read would block forever waiting for a newline that never
// comes. Dropping the line discipline to min=1 makes a single byte readable,
// which is what the hook actually sends.
const readOneByte = "stty -icanon min 1 time 0 2>/dev/null\n" +
	"dd bs=1 count=1 >/dev/null 2>&1\n"

// spawnScripted forks `sh -c script` on a real PTY under a real Registry and
// returns the live Agent. The provider is a copy of the real one with both
// lifecycle hooks removed, so the only thing touching this PTY is the hook the
// test drives itself.
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

// sawWithin polls the agent's PTY output for want until timeout. It reads the
// same width the hook does — a helper that reads less can be blinded by a burst
// exactly as the gate was in mg-9270, and TestBurstCannotHideTheComposerFromTheGate
// scripts a burst on purpose.
func sawWithin(a *agent.Agent, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Contains(agent.StripANSI(a.RecentOutput(composerScanBytes)), []byte(want)) {
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
		readOneByte +
		"printf '" + answeredMarker + "\\n'\n" +
		"sleep 30\n"
}

// TestLateRenderingDialogIsNeverDismissed is the POSITIVE CONTROL for the
// cursor half of drellem2/pogo#91: it reproduces the defect rather than the fix.
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
// does not, because a rendered composer resolves the hook immediately.
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
// window. TestEchoedTaskLooksLikeTheTrustDialog already pins the two predicates
// against strings; this drives the actual loop against a real PTY and asserts
// that nothing is sent.
//
// trustDialogMarker matches PTY *text*, and Cursor echoes the argv-delivered
// task into the TUI. A task that merely quotes the dialog matches. At the old
// 12s budget the hook had usually expired before the echo; at the cold-start
// budget it is still watching. On an already-trusted workspace (Respawn
// re-enters the same Dir; Cursor persists trust per workspace) there is no
// dialog to find — so an unguarded hook would match the echo and type a stray
// "a" into the live composer, corrupting the next nudge.
func TestEchoedTaskIsNotTypedInto(t *testing.T) {
	echoed := "Investigate the dialog offering [a] Trust this workspace"

	// Precondition: the echoed task really does look like the dialog.
	if !matchesTrustDialog([]byte(echoed)) {
		t.Fatal("precondition changed: the echoed task no longer matches " +
			"trustDialogMarker — if the marker got stricter this guard may be " +
			"redundant, but verify before deleting composerReady")
	}

	a := spawnScripted(t, "echo-guard",
		"printf '"+composerLine+"\\n'\n"+
			"printf '"+echoed+"\\n'\n"+
			readOneByte+
			"printf 'POGO-TYPED-INTO-COMPOSER\\n'\n"+
			"sleep 30\n")

	watchForTrustDialog(a, 3*time.Second, 50*time.Millisecond)

	if sawWithin(a, "POGO-TYPED-INTO-COMPOSER", 1*time.Second) {
		t.Error("hook sent the accept key into a live composer after matching " +
			"the echoed task — composerReady must gate it off")
	}
}

// burstFiller is one line of PTY noise: 60 bytes carrying no marker either
// predicate looks for. 200 of them is ~12KB — comfortably over the 8KB the gate
// used to read, and comfortably under the 64KB ring, so the placeholder printed
// before the burst is pushed out of the old read window while still being
// retained by the buffer. That gap is the whole defect.
const burstFiller = "BURST-FILLER-0123456789-abcdefghijklmnopqrstuvwxyz-BURSTFILL"

// TestBurstCannotHideTheComposerFromTheGate drives the real loop against a real
// PTY over the shape of mg-9270.
//
// The gate is fed one read per tick. The composer placeholder is not a permanent
// screen feature — Cursor replaces it when a turn starts — so a marker a tick
// misses is a marker no tick sees again. Script the placeholder, then ~12KB of
// output on top of it, and under the old 8KB read EVERY tick is looking at a
// window the placeholder has already scrolled out of. The gate never closes; the
// hook watches its whole budget with the echoed task in view; and the echoed task
// here quotes the dialog, which is the production consequence — a stray "a" typed
// into a live composer.
//
// The premise is asserted both ways before the hook runs, so a test that passes
// says something: the placeholder must be invisible at 8KB and visible at the
// shipped width. Reverting composerScanBytes to 8192 fails this test.
func TestBurstCannotHideTheComposerFromTheGate(t *testing.T) {
	echoed := "Investigate the dialog offering [a] Trust this workspace"

	// Precondition: the echoed task really does look like the dialog, so the
	// only thing standing between the hook and a keystroke is the gate.
	if !matchesTrustDialog([]byte(echoed)) {
		t.Fatal("precondition changed: the echoed task no longer matches " +
			"trustDialogMarker — this test would prove nothing")
	}

	a := spawnScripted(t, "burst-gate",
		"printf '"+composerLine+"\\n'\n"+
			"i=0; while [ $i -lt 200 ]; do printf '%s\\n' '"+burstFiller+"'; i=$((i+1)); done\n"+
			"printf '"+echoed+"\\n'\n"+
			readOneByte+
			"printf 'POGO-TYPED-INTO-COMPOSER\\n'\n"+
			"sleep 30\n")

	// The echoed task is printed last, so seeing it means the whole burst has
	// landed and the buffer has stopped moving. Waiting for it is what makes the
	// premise assertions below deterministic rather than a race with the script.
	if !sawWithin(a, echoed, 10*time.Second) {
		t.Fatalf("script never got through the burst; PTY tail:\n%s",
			agent.StripANSI(a.RecentOutput(2048)))
	}

	// Premise, side one: at the old width the placeholder is GONE. This is the
	// defect, reproduced — every tick of the old loop saw this view.
	if composerReady(a.RecentOutput(8192)) {
		t.Fatal("premise broken: the burst did not push the composer placeholder " +
			"out of an 8KB read, so this test is not exercising mg-9270 — grow " +
			"burstFiller or the loop count")
	}
	// Premise, side two: the ring still HAS it, so the fix has something to find.
	// Deliberately asked for at the RING's capacity rather than at
	// composerScanBytes — this is a fact about the buffer, not about the gate's
	// choice, so narrowing the gate must fail the assertions below rather than
	// quietly invalidating the premise here.
	if !composerReady(a.RecentOutput(agent.OutputRingBytes)) {
		t.Fatal("premise broken: the placeholder is not in the full ring either — " +
			"the burst overflowed 64KB and no read width could recover it")
	}

	start := time.Now()
	watchForTrustDialog(a, 3*time.Second, 50*time.Millisecond)
	elapsed := time.Since(start)

	// The gate closed: the hook resolved on an early tick instead of watching
	// the burst-obscured screen for its whole budget.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("hook watched for %v of a 3s budget: the gate never closed, "+
			"which is the burst hiding the composer from every poll", elapsed)
	}
	// And the consequence the gate exists to prevent did not happen.
	if sawWithin(a, "POGO-TYPED-INTO-COMPOSER", 1*time.Second) {
		t.Error("hook sent the accept key into a live composer: with the " +
			"placeholder hidden by the burst, the echoed task was all it could " +
			"see, and it acted on it")
	}
}

// TestPostTurnComposerSatisfiesTheGate drives the real loop over the OTHER half
// of mg-9270: the transition itself.
//
// Once Cursor's first turn starts, the pre-turn placeholder is gone from the
// screen for good and "Add a follow-up" is in its place. A hook that only knows
// the pre-turn spelling has nothing left to match on a respawn into an
// already-trusted workspace, so it watches out its full 30s budget with the
// echoed task in view. The post-turn placeholder must close the gate on its own.
func TestPostTurnComposerSatisfiesTheGate(t *testing.T) {
	echoed := "Investigate the dialog offering [a] Trust this workspace"

	// No pre-turn placeholder anywhere: the turn has already replaced it.
	a := spawnScripted(t, "post-turn",
		"printf '> Add a follow-up\\n'\n"+
			"printf '"+echoed+"\\n'\n"+
			readOneByte+
			"printf 'POGO-TYPED-INTO-COMPOSER\\n'\n"+
			"sleep 30\n")

	start := time.Now()
	watchForTrustDialog(a, 3*time.Second, 50*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 1500*time.Millisecond {
		t.Errorf("hook watched for %v of a 3s budget with a post-turn composer "+
			"on screen: the gate does not recognise the placeholder Cursor shows "+
			"for all but the first moments of a run", elapsed)
	}
	if sawWithin(a, "POGO-TYPED-INTO-COMPOSER", 1*time.Second) {
		t.Error("hook sent the accept key into a live post-turn composer")
	}
}

// TestWatchTerminatesWhenTheAgentExits is the NEGATIVE-SIDE control the rest of
// this file needs to mean anything.
//
// Every other test here proves the hook FIRES. All of them pass just as well on
// an implementation that never stops — and "never stops" is the specific cost of
// this fix: the budget went from a fixed 12s to the 30s cold-start budget, and
// the hook runs once per spawn. A watcher that outlives its agent is a goroutine
// leak that compounds with every spawn, so the widened window is only safe if
// agent exit ends the watch.
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
	if composerReady(a.RecentOutput(composerScanBytes)) {
		t.Error("premise broken: the scripted agent rendered a composer-ready " +
			"marker, so the early return proves nothing about agent exit")
	}
	if matchesTrustDialog(a.RecentOutput(composerScanBytes)) {
		t.Error("premise broken: the scripted agent rendered something matching " +
			"the trust-dialog marker, so the early return proves nothing about " +
			"agent exit")
	}
}

// TestTrustDialogTimeoutIsTheColdStartBudget is the regression pin for the
// cursor half of drellem2/pogo#91. The bug was a fixed wall-clock guess that
// could expire before a loaded host rendered the dialog. The bound must be the
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
