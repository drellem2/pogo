package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// fakeLiveness is an AgentLiveness backed by a set of agent identities that are
// considered alive. Anything not in the set is "gone".
type fakeLiveness struct {
	alive map[string]bool
}

func (f fakeLiveness) IsAlive(agent string) bool { return f.alive[agent] }

// addMailCheck inserts a mail-check-<agent> schedule for the given agent and
// fails the test on error.
func addMailCheck(t *testing.T, s *Scheduler, agent string, now time.Time) {
	t.Helper()
	if _, err := s.Add(Entry{
		Agent: agent,
		ID:    MailCheckIDPrefix + agent,
		Cron:  "*/10 * * * *",
	}, now); err != nil {
		t.Fatalf("Add mail-check for %s: %v", agent, err)
	}
}

func agentsOf(entries []Entry) map[string]bool {
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Agent] = true
	}
	return out
}

// TestGCStaleMailChecksReapsDeadAgents asserts the sweep removes mail-check
// schedules for vanished agents while leaving live ones — and leaving non
// mail-check schedules — untouched.
func TestGCStaleMailChecksReapsDeadAgents(t *testing.T) {
	now := fixedTime()
	s := newSchedulerForTest(t, nil)

	addMailCheck(t, s, "cat-live", now)
	addMailCheck(t, s, "cat-dead", now)
	addMailCheck(t, s, "cat-alsodead", now)
	// A non-mail-check schedule for a dead agent must survive — GC is scoped to
	// the mail-check prefix only.
	if _, err := s.Add(Entry{Agent: "cat-dead", ID: "daily-sweep", Cron: "0 9 * * *"}, now); err != nil {
		t.Fatal(err)
	}

	s.SetLiveness(fakeLiveness{alive: map[string]bool{"cat-live": true}})

	n := s.GCStaleMailChecks(now)
	if n != 2 {
		t.Fatalf("GCStaleMailChecks reaped %d, want 2", n)
	}

	got := agentsOf(s.List(""))
	if !got["cat-live"] {
		t.Error("live agent's mail-check was reaped")
	}
	// cat-dead must still own its non-mail-check schedule.
	if !got["cat-dead"] {
		t.Error("non-mail-check schedule for dead agent was reaped")
	}
	remaining := s.List("")
	for _, e := range remaining {
		if e.ID == MailCheckIDPrefix+"cat-dead" || e.ID == MailCheckIDPrefix+"cat-alsodead" {
			t.Errorf("dead agent mail-check %s survived GC", e.ID)
		}
	}
	if len(remaining) != 2 { // cat-live mail-check + cat-dead daily-sweep
		t.Errorf("after GC have %d entries, want 2: %+v", len(remaining), remaining)
	}
}

// TestGCNoLivenessIsNoop asserts GC does nothing when no liveness checker is
// installed — the default for most of the daemon's lifetime in tests.
func TestGCNoLivenessIsNoop(t *testing.T) {
	now := fixedTime()
	s := newSchedulerForTest(t, nil)
	addMailCheck(t, s, "cat-orphan", now)

	if n := s.GCStaleMailChecks(now); n != 0 {
		t.Fatalf("GC with nil liveness reaped %d, want 0", n)
	}
	if len(s.List("")) != 1 {
		t.Fatalf("entry removed despite nil liveness: %+v", s.List(""))
	}
}

// TestTickSweepsBeforeFiring asserts a dead agent's mail-check is reaped by the
// Tick sweep so it never fires (no delivery, hence no scheduler_fire_failed).
func TestTickSweepsBeforeFiring(t *testing.T) {
	now := fixedTime()
	rec := &recorder{}
	s := newSchedulerForTest(t, rec)
	addMailCheck(t, s, "cat-dead", now)
	s.SetLiveness(fakeLiveness{alive: map[string]bool{}}) // nobody alive

	// Advance past the first fire point; without GC this would deliver.
	s.Tick(context.Background(), now.Add(11*time.Minute))

	if fires := rec.snapshot(); len(fires) != 0 {
		t.Fatalf("dead agent's mail-check fired %d time(s), want 0: %+v", len(fires), fires)
	}
	if len(s.List("")) != 0 {
		t.Fatalf("dead agent's mail-check survived Tick sweep: %+v", s.List(""))
	}
}

// TestRemoveMailChecksForAgent covers the eager onExit path: removing by alias.
func TestRemoveMailChecksForAgent(t *testing.T) {
	now := fixedTime()
	s := newSchedulerForTest(t, nil)
	addMailCheck(t, s, "cat-bye", now)
	addMailCheck(t, s, "cat-stay", now)

	// Match on the event-identity alias even though the schedule is keyed on it;
	// pass an unrelated bare name too to prove the alias set is OR-matched.
	n := s.RemoveMailChecksForAgent(now, "bye", "cat-bye")
	if n != 1 {
		t.Fatalf("RemoveMailChecksForAgent reaped %d, want 1", n)
	}
	got := agentsOf(s.List(""))
	if got["cat-bye"] {
		t.Error("eager GC failed to remove cat-bye mail-check")
	}
	if !got["cat-stay"] {
		t.Error("eager GC wrongly removed cat-stay mail-check")
	}

	if n := s.RemoveMailChecksForAgent(now); n != 0 {
		t.Errorf("RemoveMailChecksForAgent with no aliases reaped %d, want 0", n)
	}
}

// TestRemoveMailChecksForAgentReapsSpawnRegistered proves the mg-e633
// teardown path: spawn-polecat auto-registers the mail-check addressed to the
// polecat's BARE registry name (id mail-check-<work-item-id>), and pogod's
// onExit reap — RemoveMailChecksForAgent(now, a.Name, a.EventAgent()) — removes
// it. Here the polecat's name ("spwsch") differs from its work item id
// ("mg-e633"), so the schedule id suffix is the work item id while the agent is
// the bare name; the reap must match on the bare-name alias regardless.
func TestRemoveMailChecksForAgentReapsSpawnRegistered(t *testing.T) {
	now := fixedTime()
	s := newSchedulerForTest(t, nil)

	// Mirror mailCheckRegistrar.RegisterMailCheck: agent = bare name,
	// id = mail-check-<work-item-id>.
	if _, err := s.Add(Entry{
		Agent: "spwsch",
		ID:    MailCheckIDPrefix + "mg-e633",
		Cron:  "*/10 * * * *",
	}, now); err != nil {
		t.Fatalf("Add spawn-registered mail-check: %v", err)
	}

	// pogod's onExit passes (a.Name, a.EventAgent()).
	n := s.RemoveMailChecksForAgent(now, "spwsch", "cat-spwsch")
	if n != 1 {
		t.Fatalf("RemoveMailChecksForAgent reaped %d, want 1 (spawn-registered mail-check not torn down)", n)
	}
	if len(s.List("")) != 0 {
		t.Fatalf("spawn-registered mail-check survived reap: %+v", s.List(""))
	}
}

// TestGCEmitsAgentGoneEvent asserts the sweep emits an observable
// schedule_removed event with reason "agent_gone" (acceptance: GC sweeps reuse
// the same removal-event signal as explicit rm — mg-afdb / mg-8e5d).
func TestGCEmitsAgentGoneEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(logPath)
	t.Cleanup(func() { events.SetLogPathForTesting("") })

	now := fixedTime()
	s := newSchedulerForTest(t, nil)
	addMailCheck(t, s, "cat-ghost", now)
	s.SetLiveness(fakeLiveness{alive: map[string]bool{}})

	if n := s.GCStaleMailChecks(now); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}

	ev := findScheduleRemoved(t, logPath, MailCheckIDPrefix+"cat-ghost", "agent_gone")
	if ev == nil {
		t.Fatalf("no schedule_removed/agent_gone event for cat-ghost in %s", logPath)
	}
	d := ev["details"].(map[string]any)
	if d["to"] != "cat-ghost" {
		t.Errorf("details.to: want cat-ghost, got %v", d["to"])
	}
	if d["reason"] != "agent_gone" {
		t.Errorf("details.reason: want agent_gone, got %v", d["reason"])
	}
}
