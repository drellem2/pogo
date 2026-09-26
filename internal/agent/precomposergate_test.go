package agent

import (
	"strings"
	"testing"
	"time"
)

// These pin mg-2037's spawn-time half: once a provider hook reports a screen
// pogo will not answer, the agent package's own blind CRs stand down. Measured
// on Claude Code 2.1.283 in a sandbox HOME, the initial nudge's best-effort
// delivery onto the "Detected a custom API key" prompt selected "No" and wrote
// the key into customApiKeyResponses.rejected — pogo answering the operator's
// billing question for them, permanently.

const testGate = "Detected a custom API key"

// TestPreComposerGateFirstWins: the first gate named is the one reported.
func TestPreComposerGateFirstWins(t *testing.T) {
	a := &Agent{Name: "gate-first"}
	if g := a.PreComposerGate(); g != "" {
		t.Fatalf("fresh agent reports gate %q", g)
	}
	a.HoldAtPreComposerGate("")
	if g := a.PreComposerGate(); g != "" {
		t.Fatalf("an empty gate name must not be recorded; got %q", g)
	}
	a.HoldAtPreComposerGate(testGate)
	a.HoldAtPreComposerGate("something else")
	if g := a.PreComposerGate(); g != testGate {
		t.Fatalf("PreComposerGate = %q, want %q", g, testGate)
	}
}

// initialNudgeAgainstSilentHarness drives a wait-ready nudge to its deadline
// against a harness that never draws a composer, optionally with a gate held,
// and returns the error, what reached the PTY (cat + tty echo), and the log.
func initialNudgeAgainstSilentHarness(t *testing.T, name string, gate string) (error, string, string) {
	t.Helper()
	readLog := captureLog(t)

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })

	a, err := reg.Spawn(SpawnRequest{
		Name:    name,
		Type:    TypePolecat,
		Command: []string{"bash", "-c", "echo parked-on-a-gate; cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a.nudge.PromptReadySentinel = "? for shortcuts"
	a.nudge.PromptReadyAlternates = nil
	a.HoldAtPreComposerGate(gate)

	nerr := a.NudgeWithMode("kickoff-msg", NudgeWaitReady, 1500*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	return nerr, string(a.RecentOutput(4096)), readLog()
}

// TestInitialNudgeWithheldAtPreComposerGate is the fix: nothing is typed.
func TestInitialNudgeWithheldAtPreComposerGate(t *testing.T) {
	nerr, output, logged := initialNudgeAgainstSilentHarness(t, "gate-held", testGate)

	if nerr == nil || !strings.Contains(nerr.Error(), testGate) {
		t.Errorf("a withheld kickoff must return an error naming the gate; got %v", nerr)
	}
	if strings.Contains(output, "kickoff-msg") {
		t.Errorf("the kickoff was typed onto a screen pogo does not answer; PTY:\n%s", output)
	}
	if !strings.Contains(logged, testGate) || !strings.Contains(logged, "withheld") {
		t.Errorf("a withheld kickoff must be logged with the gate's name; log:\n%s", logged)
	}
	if strings.Contains(logged, "not seen within") {
		t.Errorf("the stale-sentinel diagnosis must not be logged for a known gate; log:\n%s", logged)
	}
}

// TestInitialNudgeDeliveredWithoutGate is the POSITIVE CONTROL: the same
// harness with no gate held still gets the best-effort delivery. Without it,
// the test above could pass on a harness whose PTY never shows input at all.
func TestInitialNudgeDeliveredWithoutGate(t *testing.T) {
	nerr, output, _ := initialNudgeAgainstSilentHarness(t, "gate-none", "")
	if nerr != nil {
		t.Errorf("best-effort delivery should return nil; got %v", nerr)
	}
	if !strings.Contains(output, "kickoff-msg") {
		t.Errorf("control: best-effort delivery did not reach the PTY; got %q", output)
	}
}

// TestVerifyStartAndRenudge_HeldAtGateSendsNothing: the start-verify renudge's
// bare CR is an answer on the gate, so it stands down.
func TestVerifyStartAndRenudge_HeldAtGateSendsNothing(t *testing.T) {
	a, readAll, _ := newRenudgeTestAgent(t, "mg-test")
	a.HoldAtPreComposerGate(testGate)
	verifier, _ := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("renudge wrote %q to an agent parked on a gate pogo does not answer", got)
	}
}

// TestVerifyStartAndRenudge_GateIgnoredOnceComposerSeen: a gate a human has
// since answered through attach no longer holds anything back — the composer
// was seen, so the ordinary recovery applies.
func TestVerifyStartAndRenudge_GateIgnoredOnceComposerSeen(t *testing.T) {
	a, readAll, _ := newRenudgeTestAgent(t, "mg-test")
	a.HoldAtPreComposerGate(testGate)
	a.promptReadySeen.Store(true)
	verifier, _ := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 2)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "\r\r" {
		t.Errorf("expected the ordinary 2 renudges once the composer was seen; got %q", got)
	}
}
