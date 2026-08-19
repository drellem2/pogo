package firstturn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	ev, readErr := ReadEvidence(fixture(t), agents, now, DefaultLookback)
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
	ev, readErr := ReadEvidence(fixture(t), agents, now, DefaultLookback)
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
	ev, readErr := ReadEvidence(fixture(t), agents, now, DefaultLookback)
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
	evEarly, _ := ReadEvidence(fixture(t), realCrew(spawn), early, DefaultLookback)
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
	ev, readErr := ReadEvidence(filepath.Join(t.TempDir(), "nope.jsonl"), nil, time.Time{}, 0)
	if readErr == "" {
		t.Fatal("missing log read as a clean measurement; that is the absence-as-evidence error one level up")
	}
	if ev != nil {
		t.Errorf("evidence = %v, want nil alongside the error", ev)
	}
}

// The mg-21ad fixture: a STAGGERED crew population, which is the shape the
// arm's whole test suite lacked.
//
// testdata/staggered-respawn-2026-08-19.jsonl is a verbatim slice of this box's
// ~/.pogo/events.log — every scheduler fire addressed to `mayor` or `pa` in two
// windows of 2026-08-19:
//
//	06:54-07:05Z   the tail of the incarnation mayor was ABOUT to replace
//	15:20-16:12Z   the incarnation that was then reported as never having run
//
// `pa` spawned 2026-08-19T06:54:22.596216Z and stayed up. `mayor` was respawned
// ALONE at 15:20:07.597717Z, completed a fire at 15:32:21Z and three more after
// that, and at 16:11:16Z first-turn mailed "mayor has completed nothing since
// it spawned 51m0s ago".
//
// The two windows are what makes the fixture a trap rather than a sample: the
// first holds mayor completions at 07:02:48Z, from the incarnation before the
// one being judged. A reader anchored at the population's OLDEST spawn finds
// those first, hands back a FirstCompletion 8h17m before the spawn it will be
// compared against, and Completed() reads it as never.
//
// The fixture is trimmed to those two windows, so the delivery counts here are
// the trimmed ones. The live notice's own denominator — "56 fires delivered
// since" against 20 actually delivered post-spawn — came from the same
// mis-anchoring and is quoted in ReadEvidence rather than reproduced here.
const (
	staggeredPaSpawn    = "2026-08-19T06:54:22.596216Z"
	staggeredMayorSpawn = "2026-08-19T15:20:07.597717Z"
	staggeredNotice     = "2026-08-19T16:11:16Z"
	staggeredMayorFirst = "2026-08-19T15:32:21.843777Z"
)

func staggeredFixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "staggered-respawn-2026-08-19.jsonl")
}

func staggeredCrew() []Agent {
	return []Agent{
		{Name: "pa", Identity: "crew-pa", StartedAt: at(staggeredPaSpawn)},
		{Name: "mayor", Identity: "crew-mayor", StartedAt: at(staggeredMayorSpawn)},
	}
}

// TestReplay_ARespawnedAgentIsMeasuredFromItsOwnSpawn is mg-21ad, replayed end
// to end through the real reader against the real traffic.
//
// The assertion that matters is the FirstCompletion timestamp, not the verdict:
// a verdict can come out right for the wrong reason, and the wrong reason here
// is that mayor's evidence was another agent's window.
func TestReplay_ARespawnedAgentIsMeasuredFromItsOwnSpawn(t *testing.T) {
	now := at(staggeredNotice)
	agents := staggeredCrew()

	ev, readErr := ReadEvidence(staggeredFixture(t), agents, now, DefaultLookback)
	if readErr != "" {
		t.Fatalf("ReadEvidence: %s", readErr)
	}

	mayor := ev["mayor"]
	spawn := at(staggeredMayorSpawn)
	if mayor.FirstCompletion.Before(spawn) {
		t.Fatalf("mayor FirstCompletion = %s, BEFORE its spawn %s — that is evidence from an incarnation that no longer exists, and it is what made the false positive",
			mayor.FirstCompletion.Format(time.RFC3339Nano), spawn.Format(time.RFC3339Nano))
	}
	if want := at(staggeredMayorFirst); !mayor.FirstCompletion.Equal(want) {
		t.Errorf("mayor FirstCompletion = %s, want %s (the first completion at or after its own spawn)",
			mayor.FirstCompletion.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
	// Delivered is printed verbatim in the notice ("delivered N fires since"),
	// so an anchor that is too early is a false number in an operator's hands
	// as well as a false verdict. Five deliveries reached mayor after 15:20:07Z
	// in this fixture; seven reached it after pa's 06:54:22Z.
	if mayor.Delivered != 5 {
		t.Errorf("mayor Delivered = %d, want 5 — the count since ITS spawn, not since the oldest agent's", mayor.Delivered)
	}

	rep := Detect(Attach(agents, ev, now, len(agents), DefaultLookback, readErr), DefaultParams())
	if len(rep.Findings) != 0 {
		t.Fatalf("findings = %v at %s, want none: mayor completed 4 fires in this window and pa completed 7",
			Names(rep.Findings), now.Format(time.RFC3339))
	}
	if rep.State != StateCalm {
		t.Fatalf("state = %s, want calm (blind reason %q)", rep.State, rep.BlindReason)
	}
	if len(rep.Judged) != 2 {
		t.Errorf("judged = %v, want both agents judged and cleared rather than skipped", rep.Judged)
	}
	if len(rep.Misanchored) != 0 {
		t.Errorf("misanchored = %v, want none — the reader anchors per agent, so the impossible reading should never be constructed", rep.Misanchored)
	}
}

// TestReplay_TheOldPopulationWideAnchorIsWhatFired pins the defect itself
// rather than only its absence: read the SAME fixture with a single
// population-wide anchor — what pogod passed until mg-21ad — and mayor comes
// back holding a completion from before it existed.
//
// Without this, a future refactor that reinstates a shared window fails no test
// here; the fix above would still be present and would still be bypassed.
func TestReplay_TheOldPopulationWideAnchorIsWhatFired(t *testing.T) {
	now := at(staggeredNotice)
	// The old call: every agent given the OLDEST spawn in the population.
	shared := []Agent{
		{Name: "pa", Identity: "crew-pa", StartedAt: at(staggeredPaSpawn)},
		{Name: "mayor", Identity: "crew-mayor", StartedAt: at(staggeredPaSpawn)},
	}
	ev, readErr := ReadEvidence(staggeredFixture(t), shared, now, DefaultLookback)
	if readErr != "" {
		t.Fatalf("ReadEvidence: %s", readErr)
	}
	spawn := at(staggeredMayorSpawn)
	if !ev["mayor"].FirstCompletion.Before(spawn) {
		t.Fatalf("the fixture no longer contains a mayor completion before %s; it cannot demonstrate the mis-anchoring it exists for",
			spawn.Format(time.RFC3339Nano))
	}

	// Fold that evidence onto the agent's REAL spawn, which is the join the
	// watcher performs, and confirm the arm now refuses it instead of alarming.
	agents := staggeredCrew()
	rep := Detect(Attach(agents, ev, now, len(agents), DefaultLookback, ""), DefaultParams())
	if len(rep.Findings) != 0 {
		t.Fatalf("findings = %v — evidence from before an agent's spawn was turned into a finding again, which is exactly mg-21ad",
			Names(rep.Findings))
	}
	if !inRoster("mayor", rep.Misanchored) {
		t.Errorf("misanchored = %v, want mayor: an impossible reading must be named as unjudgeable, not swallowed", rep.Misanchored)
	}
}

// TestReadEvidence_AgentsOutsideThePopulationGetNoEvidence. The anchor map is
// also the filter: an agent with no known spawn — absent from the registry,
// beyond the lookback, or a polecat this arm never judges — accumulates no
// counts at all, rather than counts measured from an arbitrary floor that would
// then be read as if they meant something.
func TestReadEvidence_AgentsOutsideThePopulationGetNoEvidence(t *testing.T) {
	now := at(staggeredNotice)
	only := []Agent{{Name: "mayor", Identity: "crew-mayor", StartedAt: at(staggeredMayorSpawn)}}
	ev, readErr := ReadEvidence(staggeredFixture(t), only, now, DefaultLookback)
	if readErr != "" {
		t.Fatalf("ReadEvidence: %s", readErr)
	}
	if _, ok := ev["pa"]; ok {
		t.Errorf("evidence carries pa (%+v), which was not in the population", ev["pa"])
	}
	if ev["mayor"].Delivered != 5 {
		t.Errorf("mayor Delivered = %d, want 5", ev["mayor"].Delivered)
	}

	// And an agent whose spawn is older than the lookback is unjudgeable, so it
	// gets no anchor: the window that would prove "nothing since spawn" is not
	// in the read.
	stale := []Agent{{Name: "mayor", Identity: "crew-mayor", StartedAt: at(staggeredMayorSpawn)}}
	ev2, _ := ReadEvidence(staggeredFixture(t), stale, now, 10*time.Minute)
	if _, ok := ev2["mayor"]; ok {
		t.Errorf("evidence carries mayor (%+v) despite its spawn predating the lookback", ev2["mayor"])
	}
}

// The mg-9d55 fixture: one rotation, mid-lifetime, on real traffic.
//
// testdata/rotation-2026-08-14.jsonl and its `.1` are ONE verbatim slice of this
// box's ~/.pogo/events.log — every crew spawn record and every scheduler fire
// addressed to `mayor` or `pm-pogo` between 02:00 and 04:00 on 2026-08-14 — cut
// in two at 03:00:23.149507Z and written out as a rotated pair. The cut is not
// invented either: it is one of the 5,434 instants in this box's recorded
// history at which a rotation would have made this arm report an agent that was
// completing turns as having completed none. (Swept over ~/.pogo/events.log and
// events.log.1, 72,478 crew fire records from 2026-04-25 to 2026-08-19, by
// checking for every event instant in every crew incarnation's life whether a
// rotation there leaves the agent with no completion and >= MinDeliveries
// deliveries in the surviving log.)
//
// What the two agents actually did after 03:00:23Z:
//
//	mayor    delivered 03:00:23Z and 03:15:33Z, completed 03:17:33Z
//	pm-pogo  delivered 03:02:03Z and 03:12:23Z, completed 03:23:38Z
//
// Both had completed repeatedly since their 02:01:31Z spawn — mayor six times,
// pm-pogo five — and every one of those completions is in the ROTATED half.
const (
	rotationSpawn    = "2026-08-14T02:01:31.679312Z"
	rotationPmSpawn  = "2026-08-14T02:01:34.385242Z"
	rotationBoundary = "2026-08-14T03:00:23.149507Z"
	// The sample instant: after two fires have been delivered to each agent
	// post-rotation and before either has completed one, which is the window in
	// which the truncated read is a finding.
	rotationNotice     = "2026-08-14T03:16:00Z"
	rotationMayorFirst = "2026-08-14T02:15:32.094338Z"
	rotationPmFirst    = "2026-08-14T02:04:07.313024Z"
)

func rotationFixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "rotation-2026-08-14.jsonl")
}

// wholeLogFromRotationFixture writes the two fixture halves back into a single
// unrotated log, which is the control: the same records, the same reader, no
// rotation. Concatenating them is also the assertion that the pair IS the whole
// slice — nothing was dropped when it was cut.
func wholeLogFromRotationFixture(t *testing.T) string {
	t.Helper()
	live := rotationFixture(t)
	out := filepath.Join(t.TempDir(), "events.log")
	var buf []byte
	for _, p := range []string{live + ".1", live} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		buf = append(buf, b...)
	}
	if err := os.WriteFile(out, buf, 0o644); err != nil {
		t.Fatalf("write control log: %v", err)
	}
	return out
}

func rotationCrew() []Agent {
	return []Agent{
		{Name: "mayor", Identity: "crew-mayor", StartedAt: at(rotationSpawn)},
		{Name: "pm-pogo", Identity: "crew-pm-pogo", StartedAt: at(rotationPmSpawn)},
	}
}

// TestReplay_ARotationInsideAnAgentsLifetimeHidesItsCompletions is mg-9d55,
// replayed end to end through the real reader against the real traffic.
//
// The rotated read must agree with the unrotated one record for record. It is
// asserted as an equality against the control rather than as a verdict, because
// a verdict can come out right for the wrong reason: the wrong reason here is
// that the reader saw a window shorter than the agent's life and reported the
// shortfall as the agent's silence.
func TestReplay_ARotationInsideAnAgentsLifetimeHidesItsCompletions(t *testing.T) {
	now := at(rotationNotice)
	agents := rotationCrew()

	control, cerr := ReadEvidence(wholeLogFromRotationFixture(t), agents, now, DefaultLookback)
	if cerr != "" {
		t.Fatalf("ReadEvidence(control): %s", cerr)
	}
	rotated, rerr := ReadEvidence(rotationFixture(t), agents, now, DefaultLookback)
	if rerr != "" {
		t.Fatalf("ReadEvidence(rotated): %s", rerr)
	}

	// The control is the ground truth, and it has to hold the completions this
	// test is about — otherwise the fixture has stopped demonstrating anything.
	if want := at(rotationMayorFirst); !control["mayor"].FirstCompletion.Equal(want) {
		t.Fatalf("control mayor FirstCompletion = %s, want %s — the fixture no longer holds the pre-rotation completions",
			control["mayor"].FirstCompletion.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
	if want := at(rotationPmFirst); !control["pm-pogo"].FirstCompletion.Equal(want) {
		t.Fatalf("control pm-pogo FirstCompletion = %s, want %s", control["pm-pogo"].FirstCompletion.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}

	for _, name := range []string{"mayor", "pm-pogo"} {
		if got, want := rotated[name], control[name]; got != want {
			t.Errorf("%s: rotated read = %+v, unrotated read = %+v — a rotation inside the agent's lifetime changed the measurement, which is mg-9d55",
				name, got, want)
		}
	}

	rep := Detect(Attach(agents, rotated, now, len(agents), DefaultLookback, rerr), DefaultParams())
	if len(rep.Findings) != 0 {
		t.Fatalf("findings = %v at %s, want none: mayor completed 6 fires since its spawn and pm-pogo 5, all of them in the rotated half",
			Names(rep.Findings), now.Format(time.RFC3339))
	}
	if rep.State != StateCalm {
		t.Fatalf("state = %s, want calm (blind reason %q)", rep.State, rep.BlindReason)
	}
	if len(rep.Judged) != 2 {
		t.Errorf("judged = %v, want both agents judged and cleared", rep.Judged)
	}
	if len(rep.Truncated) != 0 {
		t.Errorf("truncated = %v, want none: the retained pair spans both spawns", rep.Truncated)
	}
}

// TestReplay_TheLiveLogAloneIsWhatFired pins the defect itself rather than only
// its absence: read the SAME records with the rotated half withheld — what
// ReadEvidence did until mg-9d55 — and both agents come back having completed
// nothing, at a moment when both had completed repeatedly.
//
// Without this, a change that narrowed the read back to one path fails no test
// here; the fix above would still be present and would still be bypassed.
func TestReplay_TheLiveLogAloneIsWhatFired(t *testing.T) {
	now := at(rotationNotice)
	agents := rotationCrew()

	// The old read: the live log only, with the rotated chunk out of reach.
	live := filepath.Join(t.TempDir(), "events.log")
	b, err := os.ReadFile(rotationFixture(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(live, b, 0o644); err != nil {
		t.Fatalf("write live-only log: %v", err)
	}

	ev, readErr := ReadEvidence(live, agents, now, DefaultLookback)
	if readErr != "" {
		t.Fatalf("ReadEvidence: %s", readErr)
	}
	for _, name := range []string{"mayor", "pm-pogo"} {
		if !ev[name].FirstCompletion.IsZero() {
			t.Fatalf("%s FirstCompletion = %s from the live half alone; the fixture no longer demonstrates the truncation it exists for",
				name, ev[name].FirstCompletion.Format(time.RFC3339Nano))
		}
		if ev[name].Delivered < DefaultMinDeliveries {
			t.Fatalf("%s Delivered = %d in the live half, below MinDeliveries — the fixture cannot reach a finding and so cannot show the false positive",
				name, ev[name].Delivered)
		}
	}

	rep := Detect(Attach(agents, ev, now, len(agents), DefaultLookback, ""), DefaultParams())
	if rep.State != StateDark || len(rep.Findings) != 2 {
		t.Fatalf("state = %s findings = %v, want dark on both — this is the false positive being pinned, not a passing case",
			rep.State, Names(rep.Findings))
	}
	// And it does not stop at two per-agent notices. Every judged agent is a
	// finding, so the report crosses into its FLEET branch, which escalates
	// outside the fleet on its first sample: a rotation alone would have mailed
	// "no crew member has completed a turn" about a crew completing turns every
	// few minutes.
	if !rep.Fleet {
		t.Errorf("Fleet = false; both judged agents are findings, and the fleet branch is what changes the routing")
	}
}

// TestReadEvidence_EvidenceThatCannotReachTheSpawnIsNotJudged is the check on
// the remedy rather than on the defect. Reading the rotated files makes the
// window longer; it does not make it unbounded. When rotation has DISCARDED
// records and the survivors still begin after an agent's spawn, the counts are
// floors over a shorter window than the agent's life — the same shape as the
// bug — and the agent must be declined rather than judged.
func TestReadEvidence_EvidenceThatCannotReachTheSpawnIsNotJudged(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "events.log")
	spawn := at("2026-08-14T02:01:31.679312Z")
	now := spawn.Add(3 * time.Hour)

	// A live log whose records all postdate the spawn, plus a full set of
	// retained rotated slots — the state that means rotation has dropped the
	// oldest chunk (events.LogSpilled).
	if err := os.WriteFile(live, deliveries("mayor", spawn.Add(2*time.Hour), 3), 0o644); err != nil {
		t.Fatalf("write live log: %v", err)
	}
	for i := 1; i <= 5; i++ {
		p := fmt.Sprintf("%s.%d", live, i)
		if err := os.WriteFile(p, deliveries("mayor", spawn.Add(time.Duration(2-i)*time.Minute+2*time.Hour), 1), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	agents := []Agent{{Name: "mayor", Identity: "crew-mayor", StartedAt: spawn}}
	ev, readErr := ReadEvidence(live, agents, now, DefaultLookback)
	if readErr != "" {
		t.Fatalf("ReadEvidence: %s", readErr)
	}
	if !ev["mayor"].Truncated {
		t.Fatalf("mayor evidence not marked truncated; the surviving log begins after its spawn, so its counts are a floor and cannot answer \"since it spawned\"")
	}

	rep := Detect(Attach(agents, ev, now, len(agents), DefaultLookback, ""), DefaultParams())
	if len(rep.Findings) != 0 {
		t.Errorf("findings = %v — a window shorter than the agent's life was turned into a finding, which is the defect this fix exists to remove, reappearing in the fix",
			Names(rep.Findings))
	}
	if !inRoster("mayor", rep.Truncated) {
		t.Errorf("truncated = %v, want mayor named: a short reading must be reported as unjudgeable, not swallowed", rep.Truncated)
	}
	if inRoster("mayor", rep.Judged) {
		t.Errorf("judged = %v — an agent whose evidence does not reach its spawn was counted as judged", rep.Judged)
	}
}

// deliveries renders n scheduler_fire_delivered records addressed to `to`, one
// per minute from start, as a JSONL chunk.
func deliveries(to string, start time.Time, n int) []byte {
	var b strings.Builder
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(&b, `{"schema_version":1,"timestamp":%q,"event_type":"scheduler_fire_delivered","agent":"pogod","details":{"to":%q}}`+"\n", ts, to)
	}
	return []byte(b.String())
}

// TestReadEvidence_AnUnreadableRotatedChunkIsBlindNotEmpty. The live log's
// absence has been blindness since mg-3cbb; a rotated chunk is now equally
// load-bearing evidence, and a hole in it is equally a failed measurement. A
// chunk that exists and cannot be read must not come back as a short count that
// looks like a reading — that is the same absence-as-evidence error the whole
// package is built against, and widening the read is what created a second
// place for it to happen.
func TestReadEvidence_AnUnreadableRotatedChunkIsBlindNotEmpty(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "events.log")
	spawn := at("2026-08-14T02:01:31.679312Z")
	now := spawn.Add(2 * time.Hour)
	if err := os.WriteFile(live, deliveries("mayor", spawn.Add(time.Hour), 3), 0o644); err != nil {
		t.Fatalf("write live log: %v", err)
	}
	rotated := live + ".1"
	if err := os.WriteFile(rotated, deliveries("mayor", spawn, 3), 0o000); err != nil {
		t.Fatalf("write rotated chunk: %v", err)
	}
	if _, err := os.ReadFile(rotated); err == nil {
		t.Skip("this user can read a 0000-mode file (root?), so the chunk is not actually unreadable")
	}

	agents := []Agent{{Name: "mayor", Identity: "crew-mayor", StartedAt: spawn}}
	ev, readErr := ReadEvidence(live, agents, now, DefaultLookback)
	if readErr == "" {
		t.Fatalf("unreadable rotated chunk read as a clean measurement (evidence %+v); a hole in the window is not an empty window", ev)
	}
	if ev != nil {
		t.Errorf("evidence = %v, want nil alongside the error", ev)
	}
	if rep := Detect(Attach(agents, ev, now, len(agents), DefaultLookback, readErr), DefaultParams()); rep.State != StateBlind {
		t.Errorf("state = %s, want blind", rep.State)
	}
}
