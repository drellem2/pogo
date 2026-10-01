package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// Tests for mg-fcb49, the remainder of mg-38433's scope item 3: the liveness
// re-check covered a park then a WAKE inside the respawn backoff, but a park
// left DOWN fell to noteRespawnOutcome's default branch and raised A6 "<name>
// CRASHED and its restart FAILED — that agent is gone" about an agent its
// operator had just parked on purpose. Plus the two wiring pins PR #223's
// review asked for.

// crash kills h's agent outright — no Stop, so StopRequested stays false and
// the exit is the unexpected one A6's CRASHED wording was written for — and
// returns once the exit hook has scheduled the respawn.
func (h *respawnHarness) crash(t *testing.T, a *agent.Agent) {
	t.Helper()
	if err := syscall.Kill(a.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %s: %v", a.Name, err)
	}
	select {
	case scheduled := <-h.exits:
		if !scheduled {
			t.Fatalf("precondition: crashing %s scheduled no respawn", a.Name)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the exit hook for %s never ran", a.Name)
	}
	if requested, _ := a.StopRequested(); requested {
		t.Fatalf("precondition: a SIGKILL read as a requested stop")
	}
}

// TestParkInsideTheBackoffRaisesNoRestartFailed: the agent exits, the respawn
// is scheduled, and inside the backoff the operator parks it. The park
// backstop in respawn() refuses — correctly — and the agent stays down, which
// is what the park asked for. A6 says "its restart FAILED"; nothing failed.
//
// Decided for a genuine crash too (the "crash" arm): the agent really did
// crash, but A6 is the recovery-failure row, not the crash row — the crash is
// agent_crashed in events.log, and A6's own body says the crash "was handled
// correctly". See noteRespawnOutcome.
//
// Positive control, same harness: the same crash with no park and a respawn
// that genuinely fails still raises A6, with the CRASHED wording unchanged.
func TestParkInsideTheBackoffRaisesNoRestartFailed(t *testing.T) {
	sandboxPogoHome(t)

	for _, tc := range []struct {
		name  string
		agent string // distinct per arm: the park flag is on disk and outlives the arm
		exit  func(h *respawnHarness, t *testing.T, a *agent.Agent)
	}{
		{"crash then park", "architect", func(h *respawnHarness, t *testing.T, a *agent.Agent) { h.crash(t, a) }},
		{"requested stop then park", "pm-pogo", func(h *respawnHarness, t *testing.T, a *agent.Agent) { h.stop(t, a.Name) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conds := &recordingConditions{}
			h := newRespawnHarness(t, conds, 300*time.Millisecond)
			a := h.spawn(t, tc.agent)
			tc.exit(h, t, a)
			if _, err := h.reg.Park(tc.agent, 2*time.Second); err != nil {
				t.Fatalf("Park inside the backoff: %v", err)
			}

			rerr := h.outcome(t)
			if !errors.Is(rerr, agent.ErrRespawnParked) {
				t.Fatalf("precondition: the deferred respawn returned %v, want ErrRespawnParked — "+
					"the park backstop this test is about did not fire", rerr)
			}
			if got := conds.restartFailed(); len(got) != 0 {
				t.Errorf("raised %v (%+v) about an agent its operator parked — a restart declined on "+
					"instruction is not a restart that FAILED (mg-fcb49)", got, conds.raised)
			}
			if cur := h.reg.Get(tc.agent); cur != nil && cur.Alive() {
				t.Error("the parked agent is running — the park backstop did not hold")
			}
		})
	}

	t.Run("positive control: crash, no park, restart genuinely fails", func(t *testing.T) {
		conds := &recordingConditions{}
		h := newRespawnHarness(t, conds, 300*time.Millisecond)
		a := h.spawn(t, "doomed")
		h.crash(t, a)
		h.reg.Remove("doomed")

		rerr := h.outcome(t)
		if rerr == nil || errors.Is(rerr, agent.ErrRespawnParked) || errors.Is(rerr, agent.ErrRespawnAgentAlive) ||
			agent.IsExpectedRespawnRefusal(rerr) {
			t.Fatalf("precondition: want a genuine respawn failure, got %v", rerr)
		}
		got := conds.restartFailed()
		if len(got) != 1 {
			t.Fatalf("restart_failed = %v; want exactly one — a crash whose restart genuinely fails "+
				"must still reach the coordinator, or the fix is deleting the alarm", got)
		}
		if c := conds.raised[0]; !strings.Contains(c.Subject, "CRASHED") || !strings.Contains(c.Detail, "unexpected exit") {
			t.Errorf("a real crash's A6 lost its crash wording:\nsubject: %s\ndetail: %s", c.Subject, c.Detail)
		}
	})
}

// TestParkedRefusalYieldsToLiveness: a park then a WAKE inside the backoff
// leaves the agent running while the park flag still exists (Wake clears it
// last), so the respawn can see ErrRespawnParked about a live agent. The
// liveness re-check must still win and CLEAR the row — the parked branch only
// declines to raise; it must not shadow the clear.
func TestParkedRefusalYieldsToLiveness(t *testing.T) {
	parked := fmt.Errorf("agent %q %w", "architect", agent.ErrRespawnParked)

	conds := &recordingConditions{}
	noteRespawnOutcome(conds, "mayor", respawnOutcome{
		Agent: "architect", Err: parked, AliveNow: func() bool { return true },
	}, time.Now())
	if !containsString(conds.cleared, rowA6RestartPrefix+"architect") {
		t.Errorf("cleared = %v; a parked refusal about a RUNNING agent must still clear the row", conds.cleared)
	}

	// Down and parked: no raise, and the row is left alone (not cleared) — an
	// earlier real A6 is not disproved by a park.
	down := &recordingConditions{}
	noteRespawnOutcome(down, "mayor", respawnOutcome{
		Agent: "architect", Err: parked, AliveNow: func() bool { return false },
	}, time.Now())
	if len(down.raised) != 0 || len(down.cleared) != 0 {
		t.Errorf("parked + down: raised=%v cleared=%v; want neither", down.raised, down.cleared)
	}
}

// onExitHookSource returns the body of pogod's SetOnExit hook, comments
// stripped, so an ordering assertion cannot be satisfied by prose.
func onExitHookSource(t *testing.T) string {
	t.Helper()
	src := stripGoComments(readSourceFile(t, "main.go"))
	start := strings.Index(src, "agentRegistry.SetOnExit(func(")
	if start < 0 {
		t.Fatal("main.go no longer installs an OnExit hook via agentRegistry.SetOnExit — update this pin")
	}
	end := strings.Index(src[start:], "\n\t})\n")
	if end < 0 {
		t.Fatal("could not find the end of the OnExit hook in main.go")
	}
	return src[start : start+end]
}

// TestProductionSchedulerLivenessCarriesTheReapHolds pins PR #223's first
// unpinned wiring: the scheduler's GC sweep must consult the same holds the
// OnExit hook places. Every behavioural test builds its own registryLiveness,
// so dropping `holds:` from the production SetLiveness call left the suite
// green while re-opening #217 branch (b): the heartbeat sweep would reap a
// held mail-check inside the very window the hold exists for.
func TestProductionSchedulerLivenessCarriesTheReapHolds(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "main.go"))
	if !strings.Contains(src, "s.SetLiveness(registryLiveness{reg: agentRegistry, holds: mailCheckHolds})") {
		t.Error("pogod's scheduler liveness is not registryLiveness{reg: agentRegistry, holds: mailCheckHolds} — " +
			"without the holds the GC sweep reaps a requested stop's held mail-check (drellem2/pogo#217)")
	}
	hook := onExitHookSource(t)
	if !strings.Contains(hook, "reapMailChecksAfterExit(sched, mailCheckHolds,") {
		t.Error("the OnExit hook does not place its holds in mailCheckHolds — the liveness above would be " +
			"reading a set nobody writes")
	}
}

// TestRequestedStopHoldIsPlacedBeforeTheRegistryRemove pins PR #223's second:
// the hold must be in place before the registration goes. registryLiveness
// answers from the registry first and the holds second, so between a Remove
// and a later hold a heartbeat sweep sees an unregistered, unheld agent and
// reaps the row (mg-fcb49).
func TestRequestedStopHoldIsPlacedBeforeTheRegistryRemove(t *testing.T) {
	hook := onExitHookSource(t)
	hold := strings.Index(hook, "reapMailChecksAfterExit(")
	remove := strings.Index(hook, "agentRegistry.Remove(a.Name)")
	if hold < 0 || remove < 0 {
		t.Fatalf("OnExit hook lost a landmark (reapMailChecksAfterExit at %d, agentRegistry.Remove at %d) — "+
			"update this pin", hold, remove)
	}
	if hold > remove {
		t.Error("the OnExit hook removes the agent from the registry BEFORE placing the mail-check hold — " +
			"a GC sweep in between reaps the row the hold exists to keep (mg-fcb49)")
	}
}
