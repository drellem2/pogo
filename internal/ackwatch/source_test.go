package ackwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/scheduler"
)

// These tests build scheduler entries and an events log in a temp dir. Nothing
// reads ~/.pogo — see the note atop ackwatch_test.go.

func TestSampleEntriesReadsTheCounters(t *testing.T) {
	created := base.Add(-72 * time.Hour)
	entries := []scheduler.Entry{{
		ID: "mail-check-pm-pogo", Agent: "pm-pogo",
		Kind: scheduler.KindMailCheck, Cron: "*/10 * * * *",
		CreatedAt:      created,
		FiresDelivered: 757, FiresCompleted: 270, UnackedStreak: 8,
		LastCompletion: base.Add(-90 * time.Minute),
	}}

	got := SampleEntries(entries, base)
	if len(got) != 1 {
		t.Fatalf("want 1 sample, got %d", len(got))
	}
	s := got[0]
	if s.Cadence != 10*time.Minute {
		t.Errorf("Cadence = %s, want 10m — the cohort key depends on it", s.Cadence)
	}
	if s.Kind != string(scheduler.KindMailCheck) {
		t.Errorf("Kind = %q, want %q", s.Kind, scheduler.KindMailCheck)
	}
	if s.FiresDelivered != 757 || s.FiresCompleted != 270 || s.UnackedStreak != 8 {
		t.Errorf("counters not carried through: %+v", s)
	}
	if !s.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %s, want %s — the freshness gate depends on it", s.CreatedAt, created)
	}
}

// A one-shot has no cadence, which is what makes it ineligible downstream.
func TestSampleEntriesGivesOneShotsNoCadence(t *testing.T) {
	got := SampleEntries([]scheduler.Entry{{
		ID: "wake-me", Agent: "pa", OneShot: true, NextFire: base.Add(time.Hour),
	}}, base)
	if got[0].Cadence != 0 {
		t.Errorf("Cadence = %s, want 0", got[0].Cadence)
	}
}

// A legacy entry written before Kind existed must still land in a cohort rather
// than in an empty-string bucket of its own.
func TestSampleEntriesDefaultsMissingKind(t *testing.T) {
	got := SampleEntries([]scheduler.Entry{{
		ID: "ad-hoc", Agent: "pa", Cron: "*/10 * * * *",
	}}, base)
	if got[0].Kind != string(scheduler.KindOther) {
		t.Errorf("Kind = %q, want %q", got[0].Kind, scheduler.KindOther)
	}
}

// An end-to-end shape check: entries in, findings out, no store involved.
func TestSampleEntriesFeedsDetect(t *testing.T) {
	mk := func(agent string, completed int) scheduler.Entry {
		return scheduler.Entry{
			ID: "mail-check-" + agent, Agent: agent,
			Kind: scheduler.KindMailCheck, Cron: "*/10 * * * *",
			CreatedAt:      base.Add(-72 * time.Hour),
			FiresDelivered: 757, FiresCompleted: completed,
		}
	}
	entries := []scheduler.Entry{
		mk("architect", 751), mk("pa", 753), mk("pm-onethird", 751), mk("pm-pogo", 270),
	}
	rep := Detect(Snapshot{Now: base, Samples: SampleEntries(entries, base)}, DefaultParams())
	if len(rep.Deficits) != 1 || rep.Deficits[0].Agent != "pm-pogo" {
		t.Fatalf("want the pm-pogo deficit, got %+v", rep.Deficits)
	}
}

// writeEvents writes a JSONL events log at a temp path.
func writeEvents(t *testing.T, evs []events.Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range evs {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLastDisruptionFindsTheNewestWake(t *testing.T) {
	path := writeEvents(t, []events.Event{
		{EventType: "system_wake", Agent: "pogod", Timestamp: base.Add(-90 * time.Minute).Format(time.RFC3339Nano)},
		{EventType: "nudge_sent", Agent: "pogod", Timestamp: base.Add(-20 * time.Minute).Format(time.RFC3339Nano)},
		{EventType: "system_wake", Agent: "pogod", Timestamp: base.Add(-10 * time.Minute).Format(time.RFC3339Nano)},
	})

	at, reason := LastDisruption(path, base)
	if reason != DisruptionEventType {
		t.Errorf("reason = %q, want %q", reason, DisruptionEventType)
	}
	if want := base.Add(-10 * time.Minute); !at.Equal(want) {
		t.Errorf("at = %s, want the NEWEST wake %s", at, want)
	}
}

func TestLastDisruptionIgnoresWakesOutsideTheWindow(t *testing.T) {
	path := writeEvents(t, []events.Event{
		{EventType: "system_wake", Agent: "pogod", Timestamp: base.Add(-DisruptionWindow - time.Hour).Format(time.RFC3339Nano)},
	})
	at, reason := LastDisruption(path, base)
	if !at.IsZero() || reason != "" {
		t.Errorf("stale wake suppressed: at=%s reason=%q", at, reason)
	}
}

// A missing log means no suppression. This fails toward alerting rather than
// toward silence, which is the correct direction for a detector whose whole
// premise is that silence hid a fault for a week.
func TestLastDisruptionOnMissingLogSuppressesNothing(t *testing.T) {
	at, reason := LastDisruption(filepath.Join(t.TempDir(), "nope.log"), base)
	if !at.IsZero() || reason != "" {
		t.Errorf("missing log produced a suppression: at=%s reason=%q", at, reason)
	}
}

func TestLastDisruptionOnEmptyLog(t *testing.T) {
	at, _ := LastDisruption(writeEvents(t, nil), base)
	if !at.IsZero() {
		t.Errorf("empty log produced a suppression at %s", at)
	}
}
