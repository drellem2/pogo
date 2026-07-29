package agent

import (
	"testing"
)

// Start-verification versus dispatch-time claiming (mg-7254).
//
// These tests exist because the ownership fix nearly took the mg-feb3 recovery
// net down with it, silently. verifyStartAndRenudge gated on the HARD
// started-signal "the work item left available/", which was sound only while the
// POLECAT was the thing that claimed. pogod now claims at spawn, so that signal
// is true from the first instant of every dispatch carrying an --id — and a
// watcher that reads it would conclude, on every spawn forever, that there is
// nothing to recover.
//
// Nothing would have reported that. No log line, no event, no failing test: the
// renudge simply stops happening, and the only symptom is polecats that wedge on
// the mg-ce61 paste-buffer bug and stay wedged. That is a worse failure than the
// one mg-7254 fixed, which is why the coupling is pinned here rather than left
// to be rediscovered.

// claimedAtSpawnAgent is newRenudgeTestAgent plus the flag pogod sets when it
// took the claim itself.
func claimedAtSpawnAgent(t *testing.T, workItemID string) (*Agent, func() string) {
	t.Helper()
	a, readAll, _ := newRenudgeTestAgent(t, workItemID)
	a.ClaimedAtSpawn = true
	return a, readAll
}

// TestStartedSignal_ClaimedAtSpawnDoesNotUseTheClaim is the guard: for a polecat
// whose claim pogod took, the claim verifier must not be consulted at all. If it
// is, pogod's own claim is being read as the polecat's first protocol action.
func TestStartedSignal_ClaimedAtSpawnDoesNotUseTheClaim(t *testing.T) {
	a, _ := claimedAtSpawnAgent(t, "mg-test")
	verifier, count := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)

	started, reason, ok := reg.startedSignal(a)
	if !ok {
		t.Fatalf("startedSignal declined for a claimed-at-spawn polecat (reason=%q); it must "+
			"still be watched, just on a different signal", reason)
	}
	// Invoke the signal: startedSignal returns a closure, so the verifier is only
	// reached by calling it. Asserting on the reason alone would pass for a
	// closure that consulted the claim anyway.
	if _, err := started(); err != nil {
		t.Fatalf("started(): %v", err)
	}
	if reason == reasonWorkItemUnclaimed {
		t.Errorf("startedSignal chose the claim signal (%q) for a polecat whose claim pogod "+
			"took at spawn — that signal is true from the first instant and the mg-feb3 "+
			"auto-renudge would never fire again", reason)
	}
	if reason != reasonNoReadyComposer {
		t.Errorf("reason = %q, want %q (the ready-composer fallback)", reason, reasonNoReadyComposer)
	}
	if c := count(); c != 0 {
		t.Errorf("the claim verifier was consulted %d time(s) for a claimed-at-spawn polecat; "+
			"want 0 — pogod must not mistake its own claim for the agent's first action", c)
	}
}

// TestStartedSignal_ClaimedAtSpawnStillRenudgesAWedgedAgent is the behavioural
// half: a claimed-at-spawn polecat whose composer never rendered must still draw
// its recovery CRs. Asserting only "the claim signal was not chosen" would pass
// for a watcher that had stopped watching altogether, which is the regression
// this file is about.
func TestStartedSignal_ClaimedAtSpawnStillRenudgesAWedgedAgent(t *testing.T) {
	a, readAll := claimedAtSpawnAgent(t, "mg-test")
	// promptReadySeen is left false: the harness never rendered a composer. The
	// claim verifier reports "started" — as it now always would — so a watcher
	// still reading it would deliver nothing.
	verifier, _ := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "\r\r\r" {
		t.Errorf("expected 3 recovery CRs for a claimed-at-spawn polecat that never rendered "+
			"a composer, got %q — start-verification has been silently disabled for every "+
			"dispatch pogod claims for", got)
	}
}

// TestStartedSignal_ClaimedAtSpawnNoRenudgeOnceComposerAppears is the negative
// control for the test above: the fallback must not renudge a polecat that IS
// up. Without this, a watcher that renudged unconditionally would pass.
func TestStartedSignal_ClaimedAtSpawnNoRenudgeOnceComposerAppears(t *testing.T) {
	a, readAll := claimedAtSpawnAgent(t, "mg-test")
	a.markPromptReady()
	verifier, _ := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("renudged a claimed-at-spawn polecat whose composer had rendered, got %q", got)
	}
}

// TestStartedSignal_UnclaimedAtSpawnKeepsTheClaimSignal pins the other side of
// the branch. When pogod did NOT take the claim — a fail-open claim, or a caller
// spawning through Registry.Spawn directly — the polecat is still the only thing
// that can claim, so the hard signal means what it always meant and must be kept.
// Losing it there would weaken every dispatch, not just the claimed ones.
func TestStartedSignal_UnclaimedAtSpawnKeepsTheClaimSignal(t *testing.T) {
	a, _, _ := newRenudgeTestAgent(t, "mg-test") // ClaimedAtSpawn stays false
	verifier, count := countingVerifier([]verifyCall{{started: true}})
	reg := fastRenudgeRegistry(verifier, 3)

	started, reason, ok := reg.startedSignal(a)
	if !ok {
		t.Fatalf("startedSignal declined for an ordinary polecat (reason=%q)", reason)
	}
	if _, err := started(); err != nil {
		t.Fatalf("started(): %v", err)
	}
	if reason != reasonWorkItemUnclaimed {
		t.Errorf("reason = %q, want %q — a polecat whose claim pogod did not take must still "+
			"be watched on the hard claim signal", reason, reasonWorkItemUnclaimed)
	}
	if c := count(); c != 1 {
		t.Errorf("claim verifier consulted %d time(s), want 1", c)
	}
}

// TestSpawnMarksClaimedAtSpawn ties the flag to the mechanism that sets it: a
// dispatch whose claim pogod took must carry the flag through to the Agent. The
// flag is what startedSignal reads, so a spawn path that forgets to set it
// reintroduces the silent regression with every test above still passing.
func TestSpawnMarksClaimedAtSpawn(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	a, err := reg.Spawn(SpawnRequest{
		Name:           "flagged",
		Type:           TypePolecat,
		Command:        []string{"cat"},
		WorkItemID:     "mg-flag",
		ClaimedAtSpawn: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(0)
	if !a.ClaimedAtSpawn {
		t.Error("SpawnRequest.ClaimedAtSpawn did not reach the Agent; start-verification " +
			"cannot tell pogod's claim from the polecat's")
	}
}
