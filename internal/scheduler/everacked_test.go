package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The blind spot EverAcked closes (mg-00d6).
//
// Completion() SKIPS a schedule that is not CompletionTracked, before the stall
// test, so a skipped schedule contributes to Schedules and to nothing else —
// not Stalled, not the ratio, and UnackedStreak is never even read. That skip
// is right for a schedule nobody ever taught to ack. It was catastrophically
// wrong for a re-registered one, because re-registration zeroes FiresCompleted
// and therefore made every re-registered schedule look like it had never acked.
//
// The window does not close on its own: it closes on the next successful ack.
// For a healthy agent that is ~10 minutes. For an agent that never comes back
// it is indefinite — which is the case the detector exists for.

// TestReregistration_PreservesTheEverAckedBit is the unit-level property: the
// counters still reset (that is pinned separately and ackwatch depends on it),
// and the one bit of history does not.
func TestReregistration_PreservesTheEverAckedBit(t *testing.T) {
	s := carryScheduler(t)
	now := time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)
	if _, err := s.Add(mailCheck("architect"), now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	at := now.Add(10 * time.Minute)
	s.Tick(context.Background(), at)
	e, _ := s.Get("architect", "mail-check-architect")
	if _, err := s.Ack("architect", "mail-check-architect", e.PendingToken, at.Add(time.Second)); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	before, _ := s.Get("architect", "mail-check-architect")
	if !before.EverAcked {
		t.Fatalf("EverAcked is false right after an accepted ack")
	}

	// The boot path: re-register with the same --id, as every crew agent does.
	if _, err := s.Add(mailCheck("architect"), at.Add(time.Hour)); err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	after, _ := s.Get("architect", "mail-check-architect")
	if after.FiresCompleted != 0 {
		t.Errorf("FiresCompleted = %d after re-registration, want 0 — the reset must stay (mg-49b1)", after.FiresCompleted)
	}
	if !after.EverAcked {
		t.Errorf("EverAcked lost across re-registration — this schedule has proven it can ack and the boot path erased the proof")
	}
	if !after.CompletionTracked() {
		t.Errorf("CompletionTracked() = false after re-registration; Completion() will SKIP this entry before ever reading its UnackedStreak")
	}
}

// TestCompletion_CountsAReregisteredScheduleThatStopsAcking is the ticket's
// central case: a schedule that acked, was re-registered, and whose agent then
// never came back. Before the bit it could not be counted as stalled no matter
// how high UnackedStreak climbed.
func TestCompletion_CountsAReregisteredScheduleThatStopsAcking(t *testing.T) {
	s := carryScheduler(t)
	now := time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)
	if _, err := s.Add(mailCheck("architect"), now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	at := now
	for i := 0; i < 3; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
		e, _ := s.Get("architect", "mail-check-architect")
		if _, err := s.Ack("architect", "mail-check-architect", e.PendingToken, at.Add(time.Second)); err != nil {
			t.Fatalf("Ack %d: %v", i, err)
		}
	}

	// Bounce: the agent restarts and re-registers. Then it never comes back.
	at = at.Add(time.Hour)
	if _, err := s.Add(mailCheck("architect"), at); err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	for i := 0; i < 6; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
	}

	e, _ := s.Get("architect", "mail-check-architect")
	if e.UnackedStreak < DefaultStallThreshold {
		t.Fatalf("setup: UnackedStreak = %d, want >= %d", e.UnackedStreak, DefaultStallThreshold)
	}

	stats := s.Completion("", 0)
	if stats.Tracked != 1 {
		t.Errorf("Tracked = %d, want 1 — a schedule that acked three times is not UNKNOWN just because it was re-registered", stats.Tracked)
	}
	if stats.TrackedReset != 1 {
		t.Errorf("TrackedReset = %d, want 1 — the ratio's denominator is thin here and the roll-up has to say so", stats.TrackedReset)
	}
	if stats.Stalled != 1 {
		t.Errorf("Stalled = %d, want 1 — UnackedStreak is %d and climbing, and this is the agent-never-returns case the detector exists for",
			stats.Stalled, e.UnackedStreak)
	}
}

// TestCompletion_StillSkipsAScheduleThatGenuinelyNeverAcked is the other half:
// the bit must not turn the gate off. A recipient whose prompts never mention
// `ack` is still UNKNOWN, not failing — accusing it is what the gate prevents.
func TestCompletion_StillSkipsAScheduleThatGenuinelyNeverAcked(t *testing.T) {
	s := carryScheduler(t)
	now := time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)
	if _, err := s.Add(mailCheck("quiet-agent"), now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	at := now
	for i := 0; i < 6; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
	}
	// Re-register too, so the test also covers a never-acked entry going through
	// the carry path — the bit must not be manufactured out of nothing.
	if _, err := s.Add(mailCheck("quiet-agent"), at.Add(time.Minute)); err != nil {
		t.Fatalf("re-Add: %v", err)
	}

	e, _ := s.Get("quiet-agent", "mail-check-quiet-agent")
	if e.EverAcked {
		t.Errorf("EverAcked = true for a schedule that never acked once")
	}
	stats := s.Completion("", 0)
	if stats.Schedules != 1 {
		t.Fatalf("Schedules = %d, want 1", stats.Schedules)
	}
	if stats.Tracked != 0 || stats.Stalled != 0 {
		t.Errorf("Tracked/Stalled = %d/%d, want 0/0 — a schedule that never acked is UNKNOWN, not failing",
			stats.Tracked, stats.Stalled)
	}
}

// TestLoad_BackfillsEverAckedFromExistingCompletions covers the migration. A
// schedules.json written before the bit existed carries the completions but not
// the bit; without the backfill every already-tracked schedule on the box would
// read as never-acked until its next ack, reintroducing the blind spot once.
func TestLoad_BackfillsEverAckedFromExistingCompletions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	legacy := map[string]any{
		"version": StateVersion,
		"schedules": []map[string]any{{
			"id":              "mail-check-mayor",
			"agent":           "mayor",
			"cron":            "*/10 * * * *",
			"delivery":        "nudge",
			"message":         "check your mail with mg mail list mayor",
			"next_fire":       "2026-08-11T02:10:00Z",
			"created_at":      "2026-08-01T00:00:00Z",
			"fires_delivered": 39,
			"fires_completed": 39,
			// deliberately no "ever_acked" — this file predates the field
		}},
	}
	blob, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s, err := New(path, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e, ok := s.Get("mayor", "mail-check-mayor")
	if !ok {
		t.Fatalf("entry not loaded")
	}
	if !e.EverAcked {
		t.Errorf("EverAcked = false for a legacy entry with 39 completions — the next re-registration would erase its tracked status for good")
	}
}
