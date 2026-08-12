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

// spentClock is a clock in the state a goroutine starved past its budget wakes
// into, made deterministic. The FIRST reading is the instant trustDialogWatch
// computes its deadline from; every later reading is past that deadline. So the
// loop's first tick finds the budget spent while the REAL timer it was handed has
// not fired — which is precisely the ambiguous moment the old loop resolved with
// select's uniform tie-break.
//
// Unsynchronised on purpose: the closure is read only by the watch loop, on one
// goroutine, and the channel that hands the outcome back carries the
// happens-before to the test.
func spentClock(t0 time.Time, budget time.Duration) func() time.Time {
	reads := 0
	return func() time.Time {
		reads++
		if reads == 1 {
			return t0
		}
		return t0.Add(budget + time.Second)
	}
}

// TestSpentBudgetBeatsAReadyTicker pins the priority between the two arms that
// used to be equal candidates (mg-effc).
//
// The dialog is on screen and matchable, so the scan branch WOULD press Enter;
// the budget is spent, so the deadline branch must win. Both were ready at once
// in production — a goroutine starved past its 250ms budget by a host at load —
// and Go picks uniformly at random among ready select cases, so the scan branch
// ran about half the time per iteration and, because time.After delivers once
// while the ticker keeps firing, nearly always within a few iterations. That is
// how TestLateRenderingDialogIsNeverDismissed came out 12/12 the wrong way and
// failed a merge gate on a branch that was fine.
//
// The starvation is not scripted here — it is expressed through the clock, which
// is the only part of it the loop can observe. The real budget is far longer than
// this test will wait for, so an implementation that resolves this state by
// waiting out its timer fails the deadline below rather than passing slowly.
//
// Deleting the spent-budget check in trustDialogWatch's ticker arm fails this
// test on the outcome AND on the keypress.
func TestSpentBudgetBeatsAReadyTicker(t *testing.T) {
	a := spawnScripted(t, "spent-budget", lateDialogScript("0"))

	// Wait for the dialog to actually land, so the assertions below are about
	// the tie-break rather than about an empty buffer.
	if !sawWithin(a, "safety check", 10*time.Second) {
		t.Fatal("script never rendered the dialog: the test proves nothing")
	}
	// Premise, side one: the scan branch really would match this and act on it.
	if !matchesTrustDialog(a.RecentOutput(composerScanBytes)) {
		t.Fatal("premise broken: the rendered dialog no longer matches " +
			"trustDialogMarker, so declining to answer it proves nothing")
	}
	// Premise, side two: no composer marker, so the hook cannot be resolving on
	// composerReady instead.
	if composerReady(a.RecentOutput(composerScanBytes)) {
		t.Fatal("premise broken: the scripted agent rendered a composer-ready " +
			"marker, so the outcome below says nothing about the budget")
	}

	const budget = 60 * time.Second
	got := make(chan trustWatchOutcome, 1)
	go func() {
		got <- trustDialogWatch(a, budget, 20*time.Millisecond, spentClock(time.Now(), budget))
	}()

	var outcome trustWatchOutcome
	select {
	case outcome = <-got:
	case <-time.After(10 * time.Second):
		t.Fatalf("watch did not return within 10s of a %v budget it was told was "+
			"already spent: it is waiting out the real timer instead of measuring "+
			"the deadline", budget)
	}

	if outcome != trustWatchDrift {
		t.Errorf("outcome = %v, want %v: a tick that finds the budget spent must "+
			"take the deadline path, not the scan path", outcome, trustWatchDrift)
	}
	if sawWithin(a, answeredMarker, 3*time.Second) {
		t.Error("the dialog was answered after the budget was spent — the scan " +
			"branch is still winning the tie-break against the deadline, which " +
			"is the mg-effc race")
	}
}

// TestSpentBudgetPrefersAnExitedAgentOverADriftSample covers the OTHER
// tie-break in the same select, whose exposure the mg-effc fix widened.
//
// Every spent-budget wakeup now routes through spentBudgetOutcome, so an agent
// that exits during a starvation window reaches it with its done channel already
// closed. A watch that ends because its agent died is inconclusive — it is not
// evidence that the trust-dialog sentinel has drifted — so the exit has to win
// there rather than be left to another coin flip.
//
// This is asserted on the function and not end-to-end on purpose. A scripted
// agent that has already exited never reaches this decision: its closed done
// channel is the only ready arm of trustDialogWatch's outer select when the loop
// starts, so it wins before the first tick. Mutating spentBudgetOutcome to return
// drift unconditionally passed a scenario version of this test 4/4 — which is why
// the decision takes the channel as a parameter.
func TestSpentBudgetPrefersAnExitedAgentOverADriftSample(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	if got := spentBudgetOutcome(exited); got != trustWatchInconclusive {
		t.Errorf("spentBudgetOutcome(closed) = %v, want %v: an agent that has "+
			"already exited must not become a drift sample", got, trustWatchInconclusive)
	}

	// And the live-agent side, so the test cannot pass by always answering
	// inconclusive — which would silence the drift detector entirely.
	live := make(chan struct{})
	if got := spentBudgetOutcome(live); got != trustWatchDrift {
		t.Errorf("spentBudgetOutcome(open) = %v, want %v: a spent budget on a live "+
			"agent IS the drift signature and has to be recorded", got, trustWatchDrift)
	}
}

// TestLateRenderingDialogIsDismissedWithinTheNudgeBudget is the same scenario
// with the shipped shape: a budget tied to the initial-nudge cold-start budget
// rather than an independent 8s guess. The dialog renders late and is still
// dismissed.
//
// The budget is 15s against a 0.7s render, and the margin is the point rather
// than the number. Now that a spent budget deterministically declines (mg-effc),
// this test is the one that inverts if the hook's goroutine is starved past its
// budget — so the gap between render and budget has to be wider than any
// plausible starvation. 5s was ~7x the render delay, and this fleet has measured
// gate wall-clock inflating 1.8x-6.8x under contention; 15s is ~21x. It costs
// nothing on a healthy run because the watch returns the moment it dismisses.
func TestLateRenderingDialogIsDismissedWithinTheNudgeBudget(t *testing.T) {
	a := spawnScripted(t, "late-fix", lateDialogScript("0.7"))

	watchForTrustDialog(a, 15*time.Second, 50*time.Millisecond)

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

// burstFiller is one line of PTY noise: 60 bytes carrying no marker either
// predicate looks for. 200 of them is ~12KB — comfortably over the 8KB the gate
// used to read, and comfortably under the 64KB ring, so the composer marker
// printed before the burst is pushed out of the old read window while still being
// retained by the buffer. That gap is the whole defect.
const burstFiller = "BURST-FILLER-0123456789-abcdefghijklmnopqrstuvwxyz-BURSTFILL"

// TestBurstCannotHideTheComposerFromTheGate drives the real loop against a real
// PTY over the shape of mg-9270.
//
// The gate is fed one read per tick, so a marker a tick misses is a marker no
// tick sees again. Claude's Ink TUI repaints continuously — it is the provider
// most able to put 8KB between two 250ms polls. Script the composer footer, then
// ~12KB on top of it, and under the old 8KB read EVERY tick is looking at a
// window the footer has already scrolled out of. The gate never closes, the hook
// watches its whole budget with the echoed kickoff prompt in view, and the prompt
// here mentions a "safety check" — which is the production consequence: Enter
// pressed into a live composer, submitting a half-typed nudge.
//
// The premise is asserted both ways before the hook runs, so a test that passes
// says something: the marker must be invisible at 8KB and visible at the shipped
// width. Reverting composerScanBytes to 8192 fails this test.
func TestBurstCannotHideTheComposerFromTheGate(t *testing.T) {
	echoed := "Investigate the quick safety check dialog that blocks a fresh worktree"

	// Precondition: the echoed prompt really does look like the dialog, so the
	// only thing standing between the hook and a keypress is the gate.
	if !matchesTrustDialog([]byte(echoed)) {
		t.Fatal("precondition changed: the echoed prompt no longer matches " +
			"trustDialogMarker — this test would prove nothing")
	}

	a := spawnScripted(t, "burst-gate",
		"printf '? for shortcuts\\n'\n"+
			"i=0; while [ $i -lt 200 ]; do printf '%s\\n' '"+burstFiller+"'; i=$((i+1)); done\n"+
			"printf '"+echoed+"\\n'\n"+
			"read -r _\n"+
			"printf 'POGO-TYPED-INTO-COMPOSER\\n'\n"+
			"sleep 30\n")

	// The echoed prompt is printed last, so seeing it means the whole burst has
	// landed and the buffer has stopped moving. Waiting for it is what makes the
	// premise assertions below deterministic rather than a race with the script.
	if !sawWithin(a, echoed, 10*time.Second) {
		t.Fatalf("script never got through the burst; PTY tail:\n%s",
			agent.StripANSI(a.RecentOutput(2048)))
	}

	// Premise, side one: at the old width the composer marker is GONE. This is
	// the defect, reproduced — every tick of the old loop saw this view.
	if composerReady(a.RecentOutput(8192)) {
		t.Fatal("premise broken: the burst did not push the composer marker out " +
			"of an 8KB read, so this test is not exercising mg-9270 — grow " +
			"burstFiller or the loop count")
	}
	// Premise, side two: the ring still HAS it, so the fix has something to find.
	// Deliberately asked for at the RING's capacity rather than at
	// composerScanBytes — this is a fact about the buffer, not about the gate's
	// choice, so narrowing the gate must fail the assertions below rather than
	// quietly invalidating the premise here.
	if !composerReady(a.RecentOutput(agent.OutputRingBytes)) {
		t.Fatal("premise broken: the composer marker is not in the full ring " +
			"either — the burst overflowed 64KB and no read width could recover it")
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
		t.Error("hook pressed Enter into a live composer: with the footer hidden " +
			"by the burst, the echoed prompt was all it could see, and it acted " +
			"on it")
	}
}

// TestWatchTerminatesWhenTheAgentExits is the NEGATIVE-SIDE control the rest of
// this file needs to mean anything.
//
// Every other test here proves the hook FIRES. All of them pass just as well on
// an implementation that never stops — and "never stops" is the specific cost of
// this fix: the budget went from a fixed 8s to the 60s initial-nudge budget, and
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
