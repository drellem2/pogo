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

// fireEvent builds one scheduler fire event as the scheduler writes it: the
// recipient under `to`, the schedule under `schedule_id`.
func fireEvent(typ, agent, id string, at time.Time) events.Event {
	return events.Event{
		EventType: typ, Agent: "pogod",
		Timestamp: at.Format(time.RFC3339Nano),
		Details:   map[string]any{"to": agent, "schedule_id": "mail-check-" + id},
	}
}

// RecentFires is the ABSOLUTE arm's only input, and it reads events rather than
// the counters for the reason in the package header: a re-registration zeroes the
// counters and the nightly redeploy guarantees one.
func TestRecentFiresCountsTheWindowedTraffic(t *testing.T) {
	var evs []events.Event
	// Six schedules delivered to over the last half hour, nothing completed —
	// the 2026-08-09 shape, in miniature.
	// Offset off `base` because the window is half-open — [now-window, now) — so a
	// fire stamped exactly at `now` belongs to the next sample, not this one.
	for i := 0; i < 5; i++ {
		at := base.Add(-time.Duration(i*10+1) * time.Minute)
		for _, a := range []string{"architect", "pa", "mayor", "doctor", "pm-pogo", "pm-onethird"} {
			evs = append(evs, fireEvent("scheduler_fire_delivered", a, a, at))
		}
	}
	// One completion, and one that is OUTSIDE the window and must not be counted.
	evs = append(evs,
		fireEvent("scheduler_fire_completed", "pa", "pa", base.Add(-25*time.Minute)),
		fireEvent("scheduler_fire_completed", "pa", "pa", base.Add(-3*time.Hour)),
		fireEvent("scheduler_fire_delivered", "pa", "pa", base.Add(-3*time.Hour)),
	)
	path := writeEvents(t, evs)

	got := RecentFires(path, base, time.Hour)
	if got.Err != "" {
		t.Fatalf("unexpected error: %s", got.Err)
	}
	if got.Delivered != 30 || got.Completed != 1 {
		t.Errorf("got %d/%d, want 1/30 — events outside the window must not count",
			got.Completed, got.Delivered)
	}
	if got.Schedules != 6 {
		t.Errorf("Schedules = %d, want 6 distinct", got.Schedules)
	}
	if len(got.Agents) != 6 || got.Agents[0] != "architect" {
		t.Errorf("Agents = %v, want the 6 delivered-to agents, sorted", got.Agents)
	}
	if want := base.Add(-25 * time.Minute); !got.LastCompletedAt.Equal(want) {
		t.Errorf("LastCompletedAt = %s, want the newest IN-window completion %s",
			got.LastCompletedAt, want)
	}
	if got.Window != time.Hour {
		t.Errorf("Window = %s, want the requested hour", got.Window)
	}

	// It has to be judgeable end to end, or the wiring is decorative.
	rep := Detect(Snapshot{
		Now: base, Samples: deadFleet(), Recent: &got,
		RunningSince: upSince(got.Agents),
	}, DefaultParams())
	if rep.Blackout == nil {
		t.Fatalf("30 delivered / 1 completed across 6 running agents is a blackout; blind=%q",
			rep.BlackoutBlind)
	}
	// And the per-agent breakdown is what the liveness gate reads, so it has to
	// be populated by the reader rather than left for the detector to guess.
	if len(got.ByAgent) != 6 {
		t.Errorf("ByAgent has %d entries, want one per delivered-to agent", len(got.ByAgent))
	}
	if f := got.ByAgent["pa"]; f.Completed != 1 || f.Schedules != 1 {
		t.Errorf("pa = %+v, want 1 completion on 1 schedule", f)
	}
}

// A log it cannot read must come back as a FAILED MEASUREMENT, never as a
// measurement of zero. Zero completions is what a blackout looks like, so an
// unreadable log that returned a bare zero would mail a person about a healthy
// fleet — the same detector-credibility failure in the opposite direction.
func TestRecentFiresOnMissingLogIsBlindNotZero(t *testing.T) {
	got := RecentFires(filepath.Join(t.TempDir(), "nope.log"), base, time.Hour)
	// It has to be RecentFires that names this, because the events layer treats a
	// nonexistent path as "no events yet" and returns nil (events.ScanFile) — so
	// an absent log arrives at the detector looking like a calm one.
	if got.Err == "" {
		t.Fatalf("missing log returned a clean measurement: %+v", got)
	}
	if got.Delivered != 0 || got.Completed != 0 {
		t.Errorf("a failed measurement must carry no counts: %+v", got)
	}
	rep := Detect(Snapshot{
		Now: base, Samples: deadFleet(), Recent: &got, RunningSince: upSince(deadFleetAgents),
	}, DefaultParams())
	if rep.Blackout != nil {
		t.Error("an unreadable log must not produce a blackout finding")
	}
	if rep.BlackoutBlind == "" {
		t.Error("...and it must not produce silence either")
	}
}

// A window of zero takes the package default, so a caller that forgets the
// argument gets an hour rather than a measurement of nothing.
func TestRecentFiresDefaultsTheWindow(t *testing.T) {
	got := RecentFires(writeEvents(t, nil), base, 0)
	if got.Window != DefaultBlackoutWindow {
		t.Errorf("Window = %s, want %s", got.Window, DefaultBlackoutWindow)
	}
}
