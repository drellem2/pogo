package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
)

// TestSchedulePauser_RoundTrip verifies the park/wake scheduler adapter
// against a real scheduler: pause removes every schedule addressed to any
// alias of the agent, and restore re-adds them with a freshly-computed next
// fire for recurring entries (a fire time that came due during the park must
// not replay as a missed fire).
func TestSchedulePauser_RoundTrip(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	add := func(agentName, id, cron string) {
		t.Helper()
		if _, err := s.Add(scheduler.Entry{ID: id, Agent: agentName, Cron: cron}, now); err != nil {
			t.Fatalf("Add(%s/%s): %v", agentName, id, err)
		}
	}
	add("pm-idle", "mail-check-pm-idle", "*/10 * * * *")
	add("crew-pm-idle", "daily-sweep", "0 9 * * *")
	add("crew-other", "keep-me", "0 9 * * *")

	p := schedulePauser{sched: s}
	paused, err := p.PauseForAgent("pm-idle", "crew-pm-idle")
	if err != nil {
		t.Fatalf("PauseForAgent: %v", err)
	}
	if len(paused) != 2 {
		t.Fatalf("paused %d entries, want 2", len(paused))
	}
	if got := len(s.List("")); got != 1 {
		t.Fatalf("%d schedules left after pause, want 1 (crew-other untouched)", got)
	}

	restored, err := p.RestoreForAgent(paused)
	if err != nil {
		t.Fatalf("RestoreForAgent: %v", err)
	}
	if restored != 2 {
		t.Fatalf("restored %d entries, want 2", restored)
	}
	if _, ok := s.Get("pm-idle", "mail-check-pm-idle"); !ok {
		t.Error("mail-check-pm-idle not restored")
	}
	e, ok := s.Get("crew-pm-idle", "daily-sweep")
	if !ok {
		t.Fatal("daily-sweep not restored")
	}
	// The restored recurring entry's fire time is recomputed from wall-clock
	// now (inside RestoreForAgent), not replayed from the recorded one.
	if e.NextFire.Before(time.Now().Add(-time.Minute)) {
		t.Errorf("restored NextFire = %v, want a future fire computed at restore time", e.NextFire)
	}
}
