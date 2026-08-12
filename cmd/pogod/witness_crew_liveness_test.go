package main

import (
	"os/exec"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/scheduler"
)

// Tests for the population mg-f9e8 names: a CREW agent with auto_start = false.
//
// It had NEITHER of the classifier's two defusing witnesses, by construction:
//
//	registry       no entry          -> the restart state, absence never heals
//	witness        WitnessNoRecord   -> crew were never witnessed
//	desired state  not auto_start    -> not expected
//	=> AgentGone   -> mail-check reaped, process still running
//
// THE CLAIM SET, each item checkable at the source named, and no severity
// attached to it — a reader can price it:
//
//   - PERMANENT: no exit occurs, so neither an auto_start respawn nor the
//     suppression page fires. Recovery needs someone to re-register the
//     schedule.
//   - UNANNOUNCED: deafwatch iterates the registry — Registry.MailLoopReport
//     ranges over r.List() (internal/agent/mailloop_report.go), reached through
//     deafwatch.RegistrySource — and this population is registry-absent by
//     construction, that absence being the first of the two the classifier
//     reasoned from. The detector is armed and has produced real alerts; it does
//     not scan this set. A detector's existence is not its coverage.
//   - DETECTABLE ON DEMAND: mailLoopExclusionFor returns "" for exactly this
//     shape — not expected, not a polecat, alive, ConfiguredStateFor says ours —
//     so `pogo agent diagnose <name>` calls it a DEAF SURVIVOR
//     (internal/agent/api.go) if you already know which name to type. mg-738f's
//     own closing section calls that "detectable, not announced".
//   - THE MECHANISM IS REPRODUCED, NOT MERELY READ, and this is the one claim
//     that is stronger than the ticket said. docs/investigations/
//     registry-absent-while-alive-2026-07-17.md ran it end-to-end on Daniel's
//     host against d90676c (mg-61a0): a SIGHUP-ignoring agent survives pogod's
//     SIGTERM reparented to init, the restarted pogod's `GET /agents` returns
//     `[]` while its pid is demonstrably alive, and the sweep then deletes the
//     mail-check from memory AND disk with no error logged anywhere. It also
//     carries the control — the same agent REGISTERED, gate open, sweeps ran,
//     schedule untouched — so the repro can distinguish.
//   - REACHABLE, CONDITIONAL ON THE HARNESS BINARY. pogod runs no cleanup on
//     any exit path (no signal handler; mg-6b66 deleted its `defer StopAll` as
//     unreachable rather than let the code imply otherwise), so it never stops
//     its agents. What kills them is the PTY hangup: pogod owns the master, its
//     death force-closes it, and the agent takes SIGHUP. That coupling exists
//     BECAUSE of Setsid+Setctty (agent.go:1022), which makes the agent a
//     session leader with that PTY as its controlling terminal — the isolation
//     guarantee is what DELIVERS the hangup, not what prevents it, and
//     TestPolecatDoesNotOutlivePogod pins the death rather than the survival.
//     So "pogod never stops its agents" does NOT make every restart a survivor
//     path: the investigation measured a live polecat dying within 5s of
//     pogod's SIGTERM under the real claude harness. The margin is the
//     harness's SIGHUP disposition — a per-binary property of a third-party
//     program, across four providers, that nothing in pogo enforces or checks
//     (finding #2). TestPolecatSurvivesPogodDeathWhenItIgnoresSIGHUP is the
//     negative control.
//   - ZERO OBSERVED INSTANCES *OF THE CREW CASE*, which is narrower than the
//     ticket's blanket version. The mechanism has a reproduction; what has none
//     is this population's instance of it. Three agents filed the crew case
//     within 93 seconds and none reproduced it — one reading replicated, not
//     three confirmations.
//
// THIS FIX IS THE UNFINISHED HALF OF THAT INVESTIGATION, not a new direction.
// Its §6 "not shipped, pending a call" names the candidate: "give the
// fall-through POSITIVE LIVENESS EVIDENCE for unregistered polecats (persist
// polecat pids so a restarted pogod can probe one), making absence trustworthy
// rather than assumed". That shipped as mg-13a3 — for polecats. Crew were left
// out of the store built to survive the exposure, which is mg-f9e8. And §5.4
// binds this fix's shape: "this does not re-open mg-de08 or mg-8677 ... the fix
// is NOT to loosen the reap." Nothing here widens what counts as expected.
//
// These tests are the first execution of the claim. What they exercise is the
// CLASSIFIER, against a real spawned process and a real prompt on disk; the
// trigger (a registry that has lost a live agent) is modelled the way every test
// in witness_liveness_test.go models it, as the restart state, which for the
// registry is not a hypothesis but its ordinary condition.

// crewLiveProcess starts a real long-lived process and returns its pid together
// with a function that kills AND REAPS it, so the pid stops answering signal 0.
// Both halves matter: an unreaped child is a zombie, and a zombie still answers
// kill(pid, 0), so a test that only killed would be asserting against a witness
// that still reads alive.
//
// A real process is the point. The witness's whole job is telling OUR process
// from SOME process, and the negative arms below turn on a pid that genuinely
// stops existing — a fake pid could not exercise either. It is also the only way
// to model "an earlier pogod recorded this and died before the process did",
// which no spawn through this registry can produce.
func crewLiveProcess(t *testing.T) (int, func()) {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	done := false
	kill := func() {
		if done {
			return
		}
		done = true
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	t.Cleanup(kill)
	return cmd.Process.Pid, kill
}

// TestRegistryLiveness_AutoStartFalseCrewAliveIsNotReaped is THE acceptance test
// for mg-f9e8.
//
// A crew agent declared auto_start = false is RUNNING, started through the real
// Spawn path. The registry has no entry for it, because pogod restarted and the
// registry does not survive. Its prompt says it is not expected, which is the
// truth and must stay the answer to that question. Before the witness covered
// crew, that pair of absences was the whole input and the classifier's default
// arm called it death.
func TestRegistryLiveness_AutoStartFalseCrewAliveIsNotReaped(t *testing.T) {
	sandboxPogoHome(t)
	writeCrewPrompt(t, "pm-ondemand", false)

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(5 * time.Second)

	if _, err := reg.Spawn(agent.SpawnRequest{
		Name:    "pm-ondemand",
		Type:    agent.TypeCrew,
		Command: []string{"sleep", "600"},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// PRECONDITION, and it is the reason this population exists: the desired
	// state genuinely has nothing to say. If this ever came back expected the
	// test would pass for the wrong reason — it would be testing mg-de08's fix,
	// not this one.
	expected, err := agent.DesiredStateFor("pm-ondemand")
	if err != nil {
		t.Fatalf("DesiredStateFor(pm-ondemand): %v", err)
	}
	if expected {
		t.Fatalf("precondition: DesiredStateFor(pm-ondemand) = true; this test needs an agent that is " +
			"NOT expected, or it proves nothing about the population with neither witness")
	}

	// The successor pogod: a registry that has never heard of this agent. That
	// is not a contrived state — it is what every restart produces, permanently,
	// for every agent that survived.
	fresh, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer fresh.StopAll(2 * time.Second)
	l := registryLiveness{reg: fresh}

	for _, identity := range []string{"pm-ondemand", "crew-pm-ondemand"} {
		if got := l.AgentState(identity); got != scheduler.AgentUnknown {
			t.Errorf("AgentState(%q) = %v, want %v — a LIVE crew agent that this fleet started must never "+
				"classify GONE on the strength of two absences. Registry-absent + OUR pid alive = UNKNOWN "+
				"(mg-f9e8, mg-de08's principle applied to the population its fix excluded)",
				identity, got, scheduler.AgentUnknown)
		}
	}

	// ...and UNKNOWN does not reap. The assertion above names the reason; this
	// one is the consequence the agent actually experiences.
	s, err := scheduler.New(t.TempDir()+"/schedules.json", nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	now := time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC)
	if _, err := s.Add(scheduler.Entry{
		Agent: "pm-ondemand", ID: scheduler.MailCheckIDPrefix + "pm-ondemand", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Positive control in the same sweep: an agent that IS gone. Without it,
	// "the mail-check survived" would also be true of a sweep that did nothing.
	if _, err := s.Add(scheduler.Entry{
		Agent: "ghost-f9e8", ID: scheduler.MailCheckIDPrefix + "ghost-f9e8", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s.SetLiveness(l)
	s.SetGCGate(func(time.Time) bool { return true })

	if n := s.GCStaleMailChecks(now); n != 1 {
		t.Fatalf("GC reaped %d, want exactly 1 (the ghost, not the live crew agent)", n)
	}
	if _, ok := s.Get("pm-ondemand", scheduler.MailCheckIDPrefix+"pm-ondemand"); !ok {
		t.Error("a LIVE auto_start=false crew agent lost its mail-check: it is now deaf indefinitely, with " +
			"no exit and therefore no respawn, and nothing restores the schedule on its own (mg-f9e8)")
	}
	if _, ok := s.Get("ghost-f9e8", scheduler.MailCheckIDPrefix+"ghost-f9e8"); ok {
		t.Error("positive control: the ghost's mail-check survived, so this sweep proves nothing")
	}
}

// TestRegistryLiveness_AutoStartFalseCrewDeadIsStillReaped is the NEGATIVE ARM,
// and it is not optional: a guard observed only keeping things alive is not
// known to work. It is the same bar mg-aaf6 sets for the donereap exemption.
//
// The state is one only a witness can produce: an earlier pogod started this
// crew agent and recorded it, then died — so nothing ever ran noteWitnessExit —
// and the process has since gone. The record survives into this pogod's boot
// naming a pid that is no longer ours. That must still reap.
func TestRegistryLiveness_AutoStartFalseCrewDeadIsStillReaped(t *testing.T) {
	sandboxPogoHome(t)
	writeCrewPrompt(t, "pm-departed", false)

	pid, kill := crewLiveProcess(t)
	if err := agent.RecordAgentWitness("pm-departed", pid, agent.TypeCrew, "", ""); err != nil {
		t.Fatalf("RecordAgentWitness: %v", err)
	}

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	l := registryLiveness{reg: reg}

	// CONTROL: while the process lives, the classifier keeps it. Without this
	// the assertion below would also pass against a fix that never fired.
	if got := l.AgentState("pm-departed"); got != scheduler.AgentUnknown {
		t.Fatalf("control: AgentState(pm-departed) = %v while its process is alive, want %v", got, scheduler.AgentUnknown)
	}

	kill()

	if got := l.AgentState("pm-departed"); got != scheduler.AgentGone {
		t.Errorf("AgentState(pm-departed) = %v after its process died, want %v — a witness that outlives its "+
			"process must not keep a mail-check firing at a corpse forever (mg-8677 must not be re-entered "+
			"through mg-f9e8's fix)", got, scheduler.AgentGone)
	}

	// And the reap actually happens, not just the classification.
	s, err := scheduler.New(t.TempDir()+"/schedules.json", nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	now := time.Date(2026, 8, 12, 4, 0, 0, 0, time.UTC)
	if _, err := s.Add(scheduler.Entry{
		Agent: "pm-departed", ID: scheduler.MailCheckIDPrefix + "pm-departed", Cron: "*/10 * * * *",
	}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	s.SetLiveness(l)
	s.SetGCGate(func(time.Time) bool { return true })
	if n := s.GCStaleMailChecks(now); n != 1 {
		t.Errorf("GC reaped %d, want 1 — an auto_start=false crew agent whose process is provably gone must "+
			"still be reaped", n)
	}
}

// TestRegistryLiveness_AutoStartTrueCrewWithDeadWitnessStaysExpected is the
// regression guard for the hazard mg-f9e8's own remedy creates, checked because
// a remedy is an artifact of the same kind as the defect and is subject to it.
//
// Widening the witness to crew means an auto_start = true crew agent can now
// hold a DEAD witness, and the state that produces it is the ordinary one on
// this fleet: pogod restarts nightly, its death takes the crew with it, and
// every crew witness is a corpse from the successor's boot until
// AutoStartAgents respawns them. If a dead witness alone answered GONE, the
// whole fleet's mail loop would be reapable in that window — mg-de08 exactly,
// re-entered through the fix for mg-f9e8. The startup GC gate usually covers
// the window; mg-de08 was "usually covered" too, and one failed respawn is
// enough to leave an expected agent holding a corpse for a full pogod lifetime.
//
// So a dead witness retires a PROCESS's claim to life and hands the question to
// the desired state, which is the step that knows whether the agent should be
// running at all.
func TestRegistryLiveness_AutoStartTrueCrewWithDeadWitnessStaysExpected(t *testing.T) {
	sandboxPogoHome(t)
	writeCrewPrompt(t, "pm-restarting", true)

	pid, kill := crewLiveProcess(t)
	if err := agent.RecordAgentWitness("pm-restarting", pid, agent.TypeCrew, "", ""); err != nil {
		t.Fatalf("RecordAgentWitness: %v", err)
	}
	kill()

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	if got := (registryLiveness{reg: reg}).AgentState("pm-restarting"); got != scheduler.AgentExpected {
		t.Errorf("REGRESSION (mg-de08 via mg-f9e8): AgentState(pm-restarting) = %v, want %v — an auto_start "+
			"crew agent whose process is between incarnations must keep its mail-check; a dead witness is "+
			"not evidence that an expected agent should stop being expected", got, scheduler.AgentExpected)
	}
}

// TestRegistryLiveness_PolecatWithDeadWitnessStillGone pins that the change
// above cost the polecat case nothing. A polecat has no prompt, so the desired
// state says "not expected" and the two steps together still answer GONE — the
// recycled-pid reap mg-8677 exists for, reached by one more step in the same
// order rather than by a short-circuit.
func TestRegistryLiveness_PolecatWithDeadWitnessStillGone(t *testing.T) {
	sandboxPogoHome(t)

	pid, kill := crewLiveProcess(t)
	if err := agent.RecordPolecatWitness("cat-f9e8-dead", pid, "mg-f9e8", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}
	kill()

	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	if got := (registryLiveness{reg: reg}).AgentState("cat-f9e8-dead"); got != scheduler.AgentGone {
		t.Errorf("AgentState(cat-f9e8-dead) = %v, want %v — a dead polecat witness must still reap", got, scheduler.AgentGone)
	}
}
