package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
)

// TestSchedulerStallWindows_PendingWakes pins the provider's filter for gh
// #235: only UNFIRED one-shots still due after now count, under either alias
// form (event identity or bare name); recurring crons, fired one-shots that
// linger until GC, and missed (past-due) one-shots contribute nothing.
func TestSchedulerStallWindows_PendingWakes(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"), nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	created := now.Add(-10 * time.Minute)
	add := func(e scheduler.Entry) {
		t.Helper()
		if _, err := s.Add(e, created); err != nil {
			t.Fatalf("Add %s: %v", e.ID, err)
		}
	}
	// Counted: one under the event identity, one under the bare name.
	add(scheduler.Entry{Agent: "crew-pm-x", ID: "wake-identity", OneShot: true, NextFire: now.Add(20 * time.Minute), Message: "wake"})
	add(scheduler.Entry{Agent: "pm-x", ID: "wake-bare", OneShot: true, NextFire: now.Add(40 * time.Minute), Message: "wake"})
	// Not counted.
	add(scheduler.Entry{Agent: "pm-x", ID: "wake-fired", OneShot: true, NextFire: now.Add(5 * time.Minute), LastFire: now.Add(-time.Minute), Message: "wake"})
	add(scheduler.Entry{Agent: "pm-x", ID: "wake-missed", OneShot: true, NextFire: now.Add(-time.Minute), Message: "wake"})
	add(scheduler.Entry{Agent: "pm-x", ID: "recurring", Cron: "*/30 * * * *", Message: "tick"})
	add(scheduler.Entry{Agent: "pm-other", ID: "wake-other", OneShot: true, NextFire: now.Add(20 * time.Minute), Message: "wake"})

	got := schedulerStallWindows{sched: s}.PendingWakesForAgent("crew-pm-x", now)
	if len(got) != 2 {
		t.Fatalf("PendingWakesForAgent returned %d wakes (%+v), want 2 (identity + bare alias)", len(got), got)
	}
	want := map[time.Time]bool{now.Add(20 * time.Minute): true, now.Add(40 * time.Minute): true}
	for _, w := range got {
		if !want[w.NextFire] {
			t.Errorf("unexpected wake NextFire=%v", w.NextFire)
		}
		if !w.CreatedAt.Equal(created) {
			t.Errorf("wake CreatedAt = %v, want %v", w.CreatedAt, created)
		}
		if !w.LastFire.IsZero() {
			t.Errorf("wake LastFire = %v, want zero", w.LastFire)
		}
	}

	// Positive control for the fired filter: the same fired entry is in List,
	// so its absence above is the filter, not a failed Add.
	if e, ok := s.Get("pm-x", "wake-fired"); !ok || e.LastFire.IsZero() {
		t.Fatalf("control: fired one-shot not stored with LastFire (ok=%v, %+v)", ok, e)
	}

	if got := (schedulerStallWindows{}).PendingWakesForAgent("crew-pm-x", now); got != nil {
		t.Errorf("nil scheduler: got %+v, want nil", got)
	}
}
