package firstturn

import (
	"path/filepath"
	"testing"
	"time"
)

// The positive control, run against the outage itself.
//
// testdata/outage-2026-08-11.jsonl is a verbatim slice of this box's
// ~/.pogo/events.log, trimmed to the scheduler fire traffic addressed to the
// five crew agents across two windows of 2026-08-11:
//
//	02:00-04:00Z   61 fires DELIVERED, 0 completed   (the outage)
//	19:15-20:00Z   20 fires delivered, 16 COMPLETED  (the recovery)
//
// pogod restarted at 02:01:33Z and spawned all five; they completed nothing for
// the next 17 hours. It is a fixture rather than a live read on purpose —
// mg-6092, mg-e8e7 and mg-5336 are three separate tickets for tests that read
// the developer's live ~/.pogo, and this package does not add a fourth.
const (
	realSpawn    = "2026-08-11T02:01:33Z"
	realRecovery = "2026-08-11T19:20:37Z"
)

func fixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "outage-2026-08-11.jsonl")
}

func realCrew(started time.Time) []Agent {
	names := []string{"architect", "mayor", "pa", "pm-onethird", "pm-pogo"}
	out := make([]Agent, 0, len(names))
	for _, n := range names {
		out = append(out, Agent{Name: n, Identity: "crew-" + n, StartedAt: started})
	}
	return out
}

// TestReplay_TheDetectorGoesRedOnTheRealOutage is the ticket's bar: shown to
// fire on the condition, against the recorded condition, end to end through the
// real events reader.
//
// The comparison that matters is the timing. ackwatch's blackout arm — which
// DID fire, 33 times, correctly — could not speak until one full 3h window had
// elapsed since the spawn, and its first post-bounce firing on this outage was
// at 05:03:36Z. This arm is red at 02:46:33Z.
func TestReplay_TheDetectorGoesRedOnTheRealOutage(t *testing.T) {
	spawn := at(realSpawn)
	now := spawn.Add(DefaultGrace)

	agents := realCrew(spawn)
	since := EarliestStart(agents, now, DefaultLookback)
	ev, readErr := ReadEvidence(fixture(t), since, now)
	if readErr != "" {
		t.Fatalf("ReadEvidence: %s", readErr)
	}
	rep := Detect(Attach(agents, ev, now, len(agents), DefaultLookback, readErr), DefaultParams())

	if rep.State != StateDark {
		t.Fatalf("state = %s, want dark at %s — 61 fires were delivered into this window and none completed",
			rep.State, now.Format(time.RFC3339))
	}
	if !rep.Fleet {
		t.Errorf("Fleet = false; all five judged agents are findings, and that is what routes the notice outside the fleet")
	}
	if len(rep.Findings) != 5 {
		t.Fatalf("findings = %d (%v), want all five", len(rep.Findings), Names(rep.Findings))
	}
	for _, f := range rep.Findings {
		if f.Delivered < DefaultMinDeliveries {
			t.Errorf("%s: delivered = %d, below MinDeliveries — the fixture should show fires reaching it", f.Name, f.Delivered)
		}
	}
	// The number this arm exists to beat.
	blackoutFirstFired := at("2026-08-11T05:03:36Z")
	if !now.Before(blackoutFirstFired) {
		t.Errorf("this arm fires at %s, no earlier than ackwatch's blackout arm did (%s) — the whole justification is the 2h17m",
			now.Format(time.RFC3339), blackoutFirstFired.Format(time.RFC3339))
	}
}

// TestReplay_StaysGreenThroughTheRecovery. The same reader, the same fixture,
// the window in which the fleet actually came back: every agent acked within
// minutes of its spawn, and the arm must clear.
func TestReplay_StaysGreenThroughTheRecovery(t *testing.T) {
	spawn := at(realRecovery)
	now := spawn.Add(DefaultGrace)

	agents := realCrew(spawn)
	since := EarliestStart(agents, now, DefaultLookback)
	ev, readErr := ReadEvidence(fixture(t), since, now)
	if readErr != "" {
		t.Fatalf("ReadEvidence: %s", readErr)
	}
	rep := Detect(Attach(agents, ev, now, len(agents), DefaultLookback, readErr), DefaultParams())

	if rep.State != StateCalm {
		t.Fatalf("state = %s, want calm — every agent acked within minutes of this spawn (findings: %v)",
			rep.State, Names(rep.Findings))
	}
	if len(rep.Judged) != 5 {
		t.Errorf("judged = %v, want all five judged and cleared rather than skipped", rep.Judged)
	}
}

// TestReplay_TheOutageWindowContainsCompletionsForTHEPREVIOUSINCARNATION is the
// trap this arm has to be immune to, restated against real data.
//
// The events log for 2026-08-11 is full of completions — 16 of them are in this
// very fixture. All of them belong to the incarnation that started at 19:20:37Z.
// A detector that asked "has this agent acked at all recently" instead of "since
// it was spawned" reads green across the whole outage on exactly this evidence.
func TestReplay_CompletionsBelongToAnIncarnation(t *testing.T) {
	spawn := at(realSpawn)
	// A vantage point AFTER the recovery, judging the OUTAGE incarnation.
	now := at("2026-08-11T19:55:00Z")

	agents := realCrew(spawn)
	ev, readErr := ReadEvidence(fixture(t), spawn, now)
	if readErr != "" {
		t.Fatalf("ReadEvidence: %s", readErr)
	}
	var completions int
	for _, e := range ev {
		if !e.FirstCompletion.IsZero() {
			completions++
		}
	}
	if completions == 0 {
		t.Fatal("fixture holds no completions at all; the trap this test describes cannot be present")
	}
	// Every one of them is after 19:20:37Z, so an agent spawned at 02:01:33Z that
	// is still running at 19:55 HAS completed a turn since its spawn — the arm
	// correctly clears. The point being pinned is that the join is by TIME, not
	// by agent name alone.
	rep := Detect(Attach(agents, ev, now, len(agents), DefaultLookback, readErr), DefaultParams())
	if rep.State != StateCalm {
		t.Fatalf("state = %s, want calm: from this vantage the 02:01 incarnation has since acked", rep.State)
	}
	// And from a vantage BEFORE those completions, the same agents are dark.
	early := at("2026-08-11T04:00:00Z")
	evEarly, _ := ReadEvidence(fixture(t), spawn, early)
	repEarly := Detect(Attach(realCrew(spawn), evEarly, early, 5, DefaultLookback, ""), DefaultParams())
	if repEarly.State != StateDark {
		t.Fatalf("state at 04:00Z = %s, want dark — the completions that clear this are 15 hours in its future",
			repEarly.State)
	}
}

// TestReadEvidence_AMissingLogIsBlindNotEmpty. events.ScanFile deliberately
// treats a nonexistent path as "no events yet"; left unhandled that arrives here
// as "nobody completed anything", which is this arm's finding — the detector
// would alarm on its own blindness.
func TestReadEvidence_AMissingLogIsBlindNotEmpty(t *testing.T) {
	ev, readErr := ReadEvidence(filepath.Join(t.TempDir(), "nope.jsonl"), time.Time{}, time.Time{})
	if readErr == "" {
		t.Fatal("missing log read as a clean measurement; that is the absence-as-evidence error one level up")
	}
	if ev != nil {
		t.Errorf("evidence = %v, want nil alongside the error", ev)
	}
}
