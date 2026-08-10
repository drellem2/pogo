package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// recordingConditions is a conditionRaiser that keeps what it was told, so a
// test can ask which way a row went without standing up an annunciator.
//
// It is mutex-guarded because the calls arrive from the same 2s-backoff
// goroutines pogod schedules — one per exiting agent, all at once.
type recordingConditions struct {
	mu      sync.Mutex
	raised  []pogodCondition
	cleared []string
	flushes int
}

func (r *recordingConditions) Raise(c pogodCondition, _ time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.raised = append(r.raised, c)
}

func (r *recordingConditions) Clear(id string, _ time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleared = append(r.cleared, id)
}

func (r *recordingConditions) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushes++
}

func (r *recordingConditions) restartFailed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for _, c := range r.raised {
		if strings.HasPrefix(c.ID, rowA6RestartPrefix) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// wireRespawnSupervisor installs the half of pogod's OnExit hook this test is
// about, verbatim in shape: capture the generation at SCHEDULING time, sleep,
// respawn from that generation, report the outcome. The sleep is shortened —
// production waits 2s to keep a crash loop from pegging the daemon, and that
// duration is not what is under test.
//
// It deliberately uses the real predicates (ShouldRespawnAgent, which with no
// transcript scanner is today's a.ShouldRespawn()) and the real registry, so
// the errors these tests classify are the ones production classifies and not
// hand-built stand-ins.
// exits is signalled once per exit, AFTER wg.Add, carrying whether that exit
// actually scheduled a respawn. Two jobs: it lets a caller wait for every
// agent's hook to have run before waiting on the goroutines themselves (OnExit
// fires from waitAndHandle and is not ordered against StopAll's return, so a
// bare wg.Wait() could sail past an empty group and green vacuously), and it
// lets the caller assert the respawns were scheduled at all — "zero conditions"
// proves nothing if nothing tried.
func wireRespawnSupervisor(reg *agent.Registry, conds conditionRaiser, backoff time.Duration, wg *sync.WaitGroup, exits chan<- bool) {
	reg.SetOnExit(func(a *agent.Agent, _ error) {
		respawn, _ := reg.ShouldRespawnAgent(a)
		if !respawn {
			exits <- false
			return
		}
		gen := reg.Generation()
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(backoff)
			_, rerr := reg.RespawnFromGeneration(a.Name, gen)
			noteRespawnOutcome(conds, "mayor", a.Name, rerr, time.Now())
		}()
		exits <- true
	})
}

// TestRequestedFleetStopRaisesNoRestartFailedCondition is the negative arm of
// mg-0208, and the red demonstration architect asked for.
//
// The scenario is the 2026-08-09 22:12 fleet stop, reduced to its mechanism: a
// fleet of restart_on_crash crew agents, stopped deliberately, all at once. Each
// exit runs pogod's OnExit hook, which schedules a respawn; each respawn is
// refused by the shutdown latch StopAll raised before it stopped anybody. That
// refusal is teardown WORKING.
//
// Before the fix the site raised A6 on any non-nil error, so this produced one
// `restart_failed:<name>` condition per agent — mailed to the coordinator, about
// agents the operator had just stopped on purpose. Five of them were the first
// five things `pogod_condition` ever emitted.
//
// The assertion is zero. See the positive control below for why zero is not
// trivially satisfiable by deleting the alarm.
func TestRequestedFleetStopRaisesNoRestartFailedCondition(t *testing.T) {
	// The park flag ShouldRespawn consults lives on disk under POGO_HOME. Read
	// from a sandbox, not from the machine's live ~/.pogo, or a crew agent that
	// happens to be parked here would quietly opt out of the demonstration.
	sandboxPogoHome(t)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })

	conds := &recordingConditions{}
	var wg sync.WaitGroup
	// The auto_start crew of the observed stop. `cat` blocks forever, so every
	// one of these is alive and unremarkable until the fleet stop kills it.
	fleet := []string{"architect", "mayor", "doctor", "pa", "pm-onethird", "pm-pogo"}
	exits := make(chan bool, len(fleet))
	wireRespawnSupervisor(reg, conds, 50*time.Millisecond, &wg, exits)

	for _, name := range fleet {
		if _, err := reg.Spawn(agent.SpawnRequest{
			Name:           name,
			Type:           agent.TypeCrew,
			Command:        []string{"cat"},
			RestartOnCrash: true,
		}); err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
	}

	// The deliberate stop: `pogo server stop`'s drain, unchanged.
	reg.StopAll(2 * time.Second)

	// Every agent's OnExit hook must have run before the goroutines it
	// schedules can be waited on.
	attempts := 0
	for seen := 0; seen < len(fleet); seen++ {
		select {
		case respawned := <-exits:
			if respawned {
				attempts++
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("only %d of %d exit hooks ran", seen, len(fleet))
		}
	}
	// Non-vacuity: the fix is downstream of the respawn, so the respawns must
	// still be attempted for the assertion below to mean anything. This is also
	// the observed shape of the defect — one attempt per auto_start crew member.
	if attempts != len(fleet) {
		t.Fatalf("only %d of %d exits scheduled a respawn; with fewer attempts than that, "+
			"zero conditions is not evidence of anything", attempts, len(fleet))
	}

	// Let every scheduled respawn fire and be refused. wg covers exactly the
	// goroutines the hook scheduled, so this waits for the real thing rather
	// than for a guessed interval.
	wg.Wait()

	if got := conds.restartFailed(); len(got) != 0 {
		t.Errorf("a REQUESTED fleet stop raised %d restart_failed conditions %v; want 0 — "+
			"every one of these agents was stopped on purpose, and mailing the coordinator about "+
			"them teaches it that A6 fires on a normal bounce (mg-0208)", len(got), got)
	}
}

// TestGenuineRespawnFailureStillRaisesRestartFailed is the positive control.
//
// Without it, "a requested stop raises zero conditions" is satisfied equally
// well by a fix that never raises anything — and the alarm this row exists for
// (a crew agent that crashed, whose one-shot restart also failed, so it is
// simply gone) would be silently deleted rather than made readable.
//
// So: no shutdown, generation unmoved, and a respawn that genuinely fails. The
// error is a real one from the real registry, not a constructed stand-in.
func TestGenuineRespawnFailureStillRaisesRestartFailed(t *testing.T) {
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })

	a, err := reg.Spawn(agent.SpawnRequest{
		Name:           "doomed",
		Type:           agent.TypeCrew,
		Command:        []string{"true"},
		RestartOnCrash: true,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-a.Done()

	// What pogod's hook captured when it scheduled the respawn. Nothing stops
	// or starts the fleet in between: this is normal operation.
	gen := reg.Generation()

	// The respawn fails for a reason that is about this agent and not about a
	// shutdown — the registry no longer holds it, so there is nothing to
	// restart and nothing will try again.
	reg.Remove("doomed")

	_, rerr := reg.RespawnFromGeneration("doomed", gen)
	if rerr == nil {
		t.Fatal("precondition: the respawn was supposed to fail")
	}
	if agent.IsExpectedRespawnRefusal(rerr) {
		t.Fatalf("precondition: %v was classified as a guard refusal, so this control "+
			"is not exercising the genuine-failure path", rerr)
	}

	conds := &recordingConditions{}
	noteRespawnOutcome(conds, "mayor", "doomed", rerr, time.Now())

	got := conds.restartFailed()
	if len(got) != 1 || got[0] != rowA6RestartPrefix+"doomed" {
		t.Fatalf("restart_failed conditions = %v; want exactly [%s] — a genuine respawn "+
			"failure during normal operation must still reach the coordinator, or the fix is "+
			"indistinguishable from deleting the alarm",
			got, rowA6RestartPrefix+"doomed")
	}
	if conds.flushes == 0 {
		t.Error("the raise was not flushed, so it would not survive a pogod restart")
	}
}

// TestSuccessfulRespawnClearsRestartFailed pins the third branch: the row is
// still cleared when the restart works, so an A6 raised by an earlier failure
// does not outlive its cause.
func TestSuccessfulRespawnClearsRestartFailed(t *testing.T) {
	conds := &recordingConditions{}
	noteRespawnOutcome(conds, "mayor", "pa", nil, time.Now())

	if len(conds.cleared) != 1 || conds.cleared[0] != rowA6RestartPrefix+"pa" {
		t.Fatalf("cleared = %v; want [%s]", conds.cleared, rowA6RestartPrefix+"pa")
	}
	if len(conds.raised) != 0 {
		t.Fatalf("raised %v on a successful respawn", conds.raised)
	}
}

// TestGuardRefusalLeavesAnEarlierRestartFailedStanding covers the choice not to
// clear the row on a guard refusal.
//
// A `restart_failed` raised by a real failure is still true when the fleet is
// stopped afterwards — the agent really did fail to come back. Treating the
// shutdown refusal as "the restart succeeded" would silently retract a genuine
// alarm at every bounce, which is the same defect as raising a false one, in
// the other direction.
func TestGuardRefusalLeavesAnEarlierRestartFailedStanding(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"shutdown latch", agent.ErrRegistryShutDown},
		{"generation moved", fmt.Errorf("%w: scheduled in generation 3, now 4", agent.ErrRespawnSuperseded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conds := &recordingConditions{}
			noteRespawnOutcome(conds, "mayor", "architect", tc.err, time.Now())

			if len(conds.raised) != 0 {
				t.Errorf("raised %v on a guard refusal", conds.raised)
			}
			if len(conds.cleared) != 0 {
				t.Errorf("cleared %v on a guard refusal — that retracts a genuine earlier alarm",
					conds.cleared)
			}
		})
	}
}
