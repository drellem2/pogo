package scheduler

import (
	"os"
	"path/filepath"
	"testing"
)

// legacySchedulesJSON is a ~/.pogo/schedules.json exactly as a pre-mg-fa53 pogod
// wrote it: version 1, and NOT ONE entry carries a "kind" field. It mirrors the
// real live fleet — a mail-check loop, both crew sweeps, a triage sweep, and a
// gate-lift reminder — so the migration is exercised against the shapes that
// were actually on disk when this field landed. The mg-fa53 safety bar is that
// loading this file drops, corrupts, or silently disables nothing.
const legacySchedulesJSON = `{
  "version": 1,
  "schedules": [
    {
      "id": "mail-check-pm-pogo",
      "agent": "pm-pogo",
      "cron": "*/10 * * * *",
      "next_fire": "2026-07-17T19:10:00Z",
      "replay_policy": "once",
      "delivery": "nudge",
      "message": "check your mail",
      "created_at": "2026-07-17T18:00:00Z"
    },
    {
      "id": "sweep-morning-pm-pogo",
      "agent": "pm-pogo",
      "cron": "0 9 * * *",
      "next_fire": "2026-07-18T09:00:00Z",
      "replay_policy": "once",
      "delivery": "nudge",
      "created_at": "2026-07-17T18:00:00Z"
    },
    {
      "id": "sweep-evening-pm-pogo",
      "agent": "pm-pogo",
      "cron": "0 18 * * *",
      "next_fire": "2026-07-18T18:00:00Z",
      "replay_policy": "once",
      "delivery": "nudge",
      "created_at": "2026-07-17T18:00:00Z"
    },
    {
      "id": "triage-sweep-pa",
      "agent": "pa",
      "cron": "30 8 * * *",
      "next_fire": "2026-07-18T08:30:00Z",
      "replay_policy": "once",
      "delivery": "nudge",
      "created_at": "2026-07-17T18:00:00Z"
    },
    {
      "id": "gate-lift-onethird-mayor",
      "agent": "mayor",
      "cron": "0 9 14 7 *",
      "next_fire": "2027-07-14T09:00:00Z",
      "replay_policy": "once",
      "delivery": "nudge",
      "message": "GATE LIFT",
      "created_at": "2026-07-05T18:48:31Z"
    },
    {
      "id": "sch-deadbeef",
      "agent": "crew-research",
      "cron": "*/15 * * * *",
      "next_fire": "2026-07-17T19:15:00Z",
      "replay_policy": "once",
      "delivery": "nudge",
      "created_at": "2026-07-17T18:00:00Z"
    }
  ]
}`

// TestLegacyNoKindScheduleMigration is the mg-fa53 safety-bar test: a
// schedules.json written before the Kind field existed loads with EVERY entry
// intact and the correct kind inferred from its id. A dropped or misclassified
// entry here is the mg-de08 outage in miniature — a live schedule silently
// disabled by the migration meant to be invisible.
func TestLegacyNoKindScheduleMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	if err := os.WriteFile(path, []byte(legacySchedulesJSON), 0o644); err != nil {
		t.Fatalf("write legacy schedules.json: %v", err)
	}

	s, err := New(path, nil)
	if err != nil {
		t.Fatalf("New over legacy file: %v", err)
	}

	// Every entry must survive the load — nothing dropped.
	got := s.List("")
	if len(got) != 6 {
		t.Fatalf("loaded %d entries, want 6 (an entry was dropped by migration)", len(got))
	}

	wantKind := map[string]ScheduleKind{
		"mail-check-pm-pogo":       KindMailCheck,
		"sweep-morning-pm-pogo":    KindSweep,
		"sweep-evening-pm-pogo":    KindSweep,
		"triage-sweep-pa":          KindSweep,
		"gate-lift-onethird-mayor": KindGateLift,
		"sch-deadbeef":             KindOther,
	}
	for _, e := range got {
		want, ok := wantKind[e.ID]
		if !ok {
			t.Errorf("unexpected entry id %q after load", e.ID)
			continue
		}
		if e.Kind != want {
			t.Errorf("entry %q: inferred kind %q, want %q", e.ID, e.Kind, want)
		}
		// The rest of the entry must round-trip unchanged — a migration that
		// mangles the agent or the cron is as bad as one that drops the entry.
		if e.Agent == "" || e.Cron == "" || e.NextFire.IsZero() {
			t.Errorf("entry %q lost a field on load: agent=%q cron=%q next_fire=%v",
				e.ID, e.Agent, e.Cron, e.NextFire)
		}
	}
}

// TestLegacyMailCheckReapedStructurallyAfterMigration proves the reap now keys
// on Kind, not the id prefix, AND that the migration wires legacy entries into
// that structural path correctly: a legacy no-kind mail-check for a GONE agent
// is reaped, while the legacy no-kind crew sweeps for that SAME gone agent
// survive. That survival was a naming accident before mg-fa53 (the sweeps merely
// lacked the "mail-check-" prefix the reap string-matched); it is now a typed
// guarantee — and this test would fail if inference ever misclassified a sweep
// as a mail-check.
func TestLegacyMailCheckReapedStructurallyAfterMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	if err := os.WriteFile(path, []byte(legacySchedulesJSON), 0o644); err != nil {
		t.Fatalf("write legacy schedules.json: %v", err)
	}
	s, err := New(path, nil)
	if err != nil {
		t.Fatalf("New over legacy file: %v", err)
	}

	// pm-pogo is GONE; every other agent is unknown/alive. Only pm-pogo's
	// mail-check should be reaped.
	s.SetLiveness(fakeLiveness{alive: map[string]bool{
		"pa": true, "mayor": true, "crew-research": true,
	}})

	now := fixedTime()
	reaped := s.GCStaleMailChecks(now)
	if reaped != 1 {
		t.Fatalf("reaped %d schedules, want exactly 1 (pm-pogo's mail-check)", reaped)
	}

	surviving := make(map[string]bool)
	for _, e := range s.List("") {
		surviving[e.ID] = true
	}
	if surviving["mail-check-pm-pogo"] {
		t.Error("mail-check-pm-pogo survived the reap for a GONE agent")
	}
	// The crew sweeps for the SAME gone agent must survive — the mg-de08 lesson,
	// now structural.
	for _, id := range []string{"sweep-morning-pm-pogo", "sweep-evening-pm-pogo"} {
		if !surviving[id] {
			t.Errorf("%s was reaped — a KindSweep must be immune to the mail-check reap", id)
		}
	}
}

// TestInferKind pins the id→kind classification directly, including the exact
// boundary the safety bar depends on: ONLY the "mail-check-" prefix yields
// KindMailCheck, so no sweep or reminder is ever newly swept into the reap.
func TestInferKind(t *testing.T) {
	cases := []struct {
		id   string
		want ScheduleKind
	}{
		{"mail-check-pm-pogo", KindMailCheck},
		{"mail-check-mg-fa53", KindMailCheck},
		{"sweep-morning-pm-pogo", KindSweep},
		{"sweep-evening-pm-dealdesk", KindSweep},
		{"triage-sweep-pa", KindSweep},
		{"gate-lift-onethird-mayor", KindGateLift},
		{"sch-deadbeef", KindOther},
		{"research-poll", KindOther},
		{"", KindOther},
		// A name that merely CONTAINS "mail-check" but is not prefixed by it is
		// NOT a mail-check — the reap must not catch it.
		{"pre-mail-check-thing", KindOther},
	}
	for _, c := range cases {
		if got := inferKind(c.id); got != c.want {
			t.Errorf("inferKind(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}
