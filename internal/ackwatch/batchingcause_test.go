package ackwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mg-772f: population 1 was named "batched" and read as "several fires inside
// one long turn", but only token supersession was ever measured. These tests
// pin the two discriminators that separate the causes, and — more importantly —
// pin that neither of them moves the deficit arithmetic.
//
// As elsewhere in this package, every timeline is built by hand; the two tests
// that read a log write it themselves into t.TempDir().

// deliverAt appends a token-carrying delivery whose scheduler-side due/fired
// stamps are set explicitly, so a test can say "pogod held this one" without
// touching the event's own At.
func (t *timeline) deliverLate(held time.Duration) *timeline {
	t.seq++
	t.evs = append(t.evs, FireEvent{
		At: t.at, Kind: FireDelivered, Agent: t.agent, ID: t.id,
		Token: tokenFor(t.seq),
		Due:   t.at.Add(-held),
		Fired: t.at,
	})
	t.at = t.at.Add(t.step)
	return t
}

// deliverPunctual is deliverLate with a lag well inside the jitter the live
// fleet actually shows (p50 14s, p90 27s over 65,717 deliveries).
func (t *timeline) deliverPunctual() *timeline { return t.deliverLate(14 * time.Second) }

// The delivery-side arm. A run of fires that pogod held and flushed together is
// the hypothesis mg-772f was filed to test, and it must be visible as such —
// LateDelivery is the ONLY counter in this report that implicates the daemon
// rather than what happens to a fire after it lands.
func TestSplit_HeldFires_AreCountedAgainstDelivery(t *testing.T) {
	tl := newTimeline("architect", "mail-check-architect")
	// Six fires, each flushed a full cadence period after it was due.
	for i := 0; i < 6; i++ {
		tl.deliverLate(10 * time.Minute)
	}
	tl.complete()

	sp := onlySchedule(t, SplitPopulations(tl.events()))
	if sp.LateDelivery != 6 {
		t.Fatalf("want all 6 held fires counted delivery-side, got %d", sp.LateDelivery)
	}
	// The delivery-side count is about the fire, not about its ack outcome:
	// the first of the six was never superseded, so batched is 5 while
	// LateDelivery is 6. Tying them together would make a late fire invisible
	// whenever it happened to be the last of a run.
	if sp.Batched != 5 {
		t.Fatalf("want 5 superseded, got %d", sp.Batched)
	}
}

// The exoneration, which is what the measurement actually found: fires that
// superseded each other were delivered ON TIME, so the scheduler did not bunch
// them. A punctual fire must never be counted delivery-side, or the arm would
// implicate pogod for every batch and answer nothing.
func TestSplit_PunctualFires_ExonerateDelivery(t *testing.T) {
	tl := newTimeline("architect", "mail-check-architect")
	for i := 0; i < 6; i++ {
		tl.deliverPunctual()
	}
	tl.complete()

	sp := onlySchedule(t, SplitPopulations(tl.events()))
	if sp.Batched != 5 {
		t.Fatalf("want 5 superseded, got %d", sp.Batched)
	}
	if sp.LateDelivery != 0 {
		t.Fatalf("punctual fires must not implicate delivery, got LateDelivery=%d", sp.LateDelivery)
	}
}

// A delivery whose stamps are absent — a completion, or an event from before
// the fields existed — must not be guessed at in EITHER direction. Counting it
// late would invent a delivery-side defect; the point of Late() returning false
// is that the fire was simply not measured.
func TestSplit_UnstampedFires_AreNotMeasuredEitherWay(t *testing.T) {
	tl := newTimeline("pa", "mail-check-pa")
	for i := 0; i < 4; i++ {
		tl.deliver() // the plain builder sets no Due/Fired
	}
	tl.complete()

	sp := onlySchedule(t, SplitPopulations(tl.events()))
	if sp.Batched != 3 {
		t.Fatalf("want 3 superseded, got %d", sp.Batched)
	}
	if sp.LateDelivery != 0 {
		t.Fatalf("unstamped fires must not be counted late, got %d", sp.LateDelivery)
	}
}

// The threshold is a boundary, so both sides of it are pinned. 60s is chosen to
// sit an order of magnitude above the observed jitter and an order of magnitude
// below the shortest cadence, and a future re-tune that crosses either would
// break this.
func TestFireEvent_LateThreshold_IsExclusiveAndNeedsBothStamps(t *testing.T) {
	at := popBase
	cases := []struct {
		name string
		ev   FireEvent
		want bool
	}{
		{"exactly at the threshold is not late",
			FireEvent{Due: at, Fired: at.Add(LateDeliveryThreshold)}, false},
		{"one second past is late",
			FireEvent{Due: at, Fired: at.Add(LateDeliveryThreshold + time.Second)}, true},
		{"typical fleet jitter is not late",
			FireEvent{Due: at, Fired: at.Add(27 * time.Second)}, false},
		{"no due stamp is not measured",
			FireEvent{Fired: at.Add(time.Hour)}, false},
		{"no fired stamp is not measured",
			FireEvent{Due: at}, false},
		{"neither stamp is not measured",
			FireEvent{}, false},
	}
	for _, tc := range cases {
		if got := tc.ev.Late(); got != tc.want {
			t.Errorf("%s: Late()=%v want %v", tc.name, got, tc.want)
		}
	}
}

// The consumption-side arm, and the shape that prompted the ticket. Architect's
// 2026-08-09: 27 fires delivered punctually on their own 10-minute marks, 26 of
// the turns dead on an API error, one ack at the end when the network returned.
//
// Read without the episode this is a 4% ack rate and an inattentive agent. Read
// with it, it is a fleet outage that synthwatch had already detected, and the
// supersessions say nothing about attention at all.
func TestSplit_FiresIntoDeadTurns_AreNotEvidenceOfInattention(t *testing.T) {
	tl := newTimeline("architect", "mail-check-architect")
	for i := 0; i < 27; i++ {
		tl.deliverPunctual()
	}
	tl.complete()
	evs := tl.events()

	// synthwatch needs two failing turns before it will call it, so detection
	// LAGS the outage — here it lands between the second and third fire, and
	// clears after the last. The real 2026-08-09 episode had the same shape.
	episode := FailureEpisode{
		Agent: "architect",
		From:  popBase.Add(15 * time.Minute),
		Until: popBase.Add(27 * 10 * time.Minute),
	}

	bare := onlySchedule(t, SplitPopulations(evs))
	withEp := onlySchedule(t, SplitWithEpisodes(evs, []FailureEpisode{episode}))

	if bare.Batched != 26 || withEp.Batched != 26 {
		t.Fatalf("the episode must not change the batched count: bare=%d withEpisode=%d",
			bare.Batched, withEp.Batched)
	}
	if bare.BatchedInFailureEpisode != 0 {
		t.Fatalf("without episodes the subset must be 0, got %d", bare.BatchedInFailureEpisode)
	}
	// The supersession at the second fire happened before synthwatch had
	// detected anything, so it is correctly NOT excused: the join credits only
	// the interval the detector actually vouched for, and an outage's opening
	// minutes are indistinguishable from an ordinary busy turn.
	if withEp.BatchedInFailureEpisode != 25 {
		t.Fatalf("want 25 of 26 supersessions inside the episode, got %d",
			withEp.BatchedInFailureEpisode)
	}
	if withEp.LateDelivery != 0 {
		t.Fatalf("every fire was punctual; delivery must be exonerated, got %d", withEp.LateDelivery)
	}
}

// The load-bearing guarantee. mg-772f characterises a cause; re-scoring the
// deficit is mg-a14c's question and this change must not pre-empt it. Every
// number the alert is read off has to be byte-identical with and without the
// episodes joined in.
func TestSplit_EpisodeJoin_ChangesNoDeficitArithmetic(t *testing.T) {
	tl := newTimeline("pm-pogo", "mail-check-pm-pogo")
	for i := 0; i < 9; i++ {
		tl.deliverPunctual()
	}
	tl.complete()
	for i := 0; i < 4; i++ {
		tl.deliverPunctual()
	}
	tl.complete()
	evs := tl.events()

	eps := []FailureEpisode{{Agent: "pm-pogo", From: popBase, Until: popBase.Add(24 * time.Hour)}}
	bare := SplitPopulations(evs)
	joined := SplitWithEpisodes(evs, eps)

	if joined.BatchedInFailureEpisode == 0 {
		t.Fatal("the fixture must actually exercise the join, or this proves nothing")
	}
	b, j := onlySchedule(t, bare), onlySchedule(t, joined)
	switch {
	case bare.Delivered != joined.Delivered, bare.Completed != joined.Completed:
		t.Fatalf("delivered/completed moved: %d/%d vs %d/%d",
			bare.Delivered, bare.Completed, joined.Delivered, joined.Completed)
	case bare.Batched != joined.Batched, bare.TokenLess != joined.TokenLess,
		bare.Boundary != joined.Boundary:
		t.Fatalf("a population moved: %+v vs %+v", bare, joined)
	case bare.Deficit() != joined.Deficit():
		t.Fatalf("the deficit moved: %d vs %d", bare.Deficit(), joined.Deficit())
	case b.MeanGap != j.MeanGap, b.MaxGap != j.MaxGap, b.Rate() != j.Rate():
		t.Fatalf("a per-schedule statistic moved: %+v vs %+v", b, j)
	case j.Identity() != 0:
		t.Fatalf("the identity must still hold to zero error, got residual %g", j.Identity())
	}
}

// An episode belongs to ONE agent. A fleet-wide outage is reported by
// synthwatch as one episode per agent, so a join that ignored the name would
// excuse every agent the moment any one of them went dark.
func TestSplit_EpisodeIsScopedToItsOwnAgent(t *testing.T) {
	dark := newTimeline("architect", "mail-check-architect")
	for i := 0; i < 4; i++ {
		dark.deliverPunctual()
	}
	lit := newTimeline("pa", "mail-check-pa")
	for i := 0; i < 4; i++ {
		lit.deliverPunctual()
	}
	evs := append(dark.events(), lit.events()...)

	eps := []FailureEpisode{{Agent: "architect", From: popBase.Add(-time.Hour)}}
	rep := SplitWithEpisodes(evs, eps)

	byAgent := map[string]SchedulePopulation{}
	for _, sp := range rep.Schedules {
		byAgent[sp.Agent] = sp
	}
	if got := byAgent["architect"].BatchedInFailureEpisode; got != 3 {
		t.Fatalf("architect was dark for all 3 supersessions, got %d", got)
	}
	if got := byAgent["pa"].BatchedInFailureEpisode; got != 0 {
		t.Fatalf("pa was never dark and must not be excused, got %d", got)
	}
	if rep.BatchedInFailureEpisode != 3 {
		t.Fatalf("fleet total should be 3, got %d", rep.BatchedInFailureEpisode)
	}
}

// An episode with no clear reads as open-ended rather than closing at the last
// event we happened to read. Closing it would quietly acquit every fire past
// that point on the strength of having stopped looking.
func TestFailureEpisode_UnclosedIsOpenEnded(t *testing.T) {
	ep := FailureEpisode{Agent: "mayor", From: popBase}
	if ep.Covers(popBase.Add(-time.Second)) {
		t.Fatal("an episode must not cover time before it opened")
	}
	if !ep.Covers(popBase.Add(30 * 24 * time.Hour)) {
		t.Fatal("an unclosed episode must stay open, not lapse")
	}
	closed := FailureEpisode{Agent: "mayor", From: popBase, Until: popBase.Add(time.Hour)}
	if !closed.Covers(popBase.Add(time.Hour)) {
		t.Fatal("the closing instant is inside the episode")
	}
	if closed.Covers(popBase.Add(time.Hour + time.Second)) {
		t.Fatal("a closed episode must not cover time after it cleared")
	}
}

// The reader. Repeated detections while an episode is already open must not
// shatter one outage into slivers — synthwatch re-emits on every scan that
// still sees the agent failing, and the 2026-08-09 window carried nine
// detections for one 4h18m episode.
func TestReadFailureEpisodes_CoalescesRepeatedDetections(t *testing.T) {
	path := writeEventLog(t, []map[string]any{
		ev("2026-08-09T13:04:48Z", "synthetic_failure_detected", "architect"),
		ev("2026-08-09T13:07:18Z", "synthetic_failure_detected", "architect"),
		ev("2026-08-09T13:13:48Z", "synthetic_failure_detected", "architect"),
		ev("2026-08-09T17:22:29Z", "synthetic_failure_cleared", "architect"),
		// A second, later episode for the same agent must stay separate.
		ev("2026-08-09T20:00:00Z", "synthetic_failure_detected", "architect"),
		ev("2026-08-09T20:30:00Z", "synthetic_failure_cleared", "architect"),
	})

	eps, err := ReadFailureEpisodes(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 2 {
		t.Fatalf("want 2 episodes, got %d: %+v", len(eps), eps)
	}
	first := eps[0]
	if !first.From.Equal(mustTime(t, "2026-08-09T13:04:48Z")) {
		t.Fatalf("episode must open at the FIRST detection, got %s", first.From)
	}
	if !first.Until.Equal(mustTime(t, "2026-08-09T17:22:29Z")) {
		t.Fatalf("episode must close at the clear, got %s", first.Until)
	}
	// The whole 4h18m window is covered — the point of coalescing.
	if !first.Covers(mustTime(t, "2026-08-09T15:00:00Z")) {
		t.Fatal("a fire mid-episode must read as dark")
	}
}

// A clear with no matching detection means the detection fell before `since`.
// It is dropped rather than back-dated: inventing a start would stretch an
// episode over fires we have no evidence were dark.
func TestReadFailureEpisodes_UnmatchedClearIsDroppedNotBackdated(t *testing.T) {
	path := writeEventLog(t, []map[string]any{
		ev("2026-08-09T10:00:00Z", "synthetic_failure_cleared", "pa"),
		ev("2026-08-09T11:00:00Z", "synthetic_failure_detected", "pa"),
	})
	eps, err := ReadFailureEpisodes(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 {
		t.Fatalf("want only the genuinely-opened episode, got %d: %+v", len(eps), eps)
	}
	if !eps[0].From.Equal(mustTime(t, "2026-08-09T11:00:00Z")) {
		t.Fatalf("want the 11:00 detection, got %s", eps[0].From)
	}
	if !eps[0].Until.IsZero() {
		t.Fatalf("that episode never cleared; Until must stay zero, got %s", eps[0].Until)
	}
}

// The two event families must actually join. scheduler_fire_delivered names the
// agent in details.to and synthwatch names it in details.target; if those ever
// diverge (a `crew-` prefix on one side, say) the join silently reads zero, so
// this test walks the real field names end to end rather than constructing
// FireEvents by hand.
func TestReadTimelineAndEpisodes_JoinOnTheRealFieldNames(t *testing.T) {
	path := writeEventLog(t, []map[string]any{
		fireEv("2026-08-09T14:00:18Z", "architect", "mail-check-architect", "aa11",
			"2026-08-09T14:00:00Z", "2026-08-09T14:00:18Z"),
		ev("2026-08-09T14:03:00Z", "synthetic_failure_detected", "architect"),
		fireEv("2026-08-09T14:10:18Z", "architect", "mail-check-architect", "bb22",
			"2026-08-09T14:10:00Z", "2026-08-09T14:10:18Z"),
		fireEv("2026-08-09T14:20:18Z", "architect", "mail-check-architect", "cc33",
			"2026-08-09T14:20:00Z", "2026-08-09T14:20:18Z"),
		ev("2026-08-09T14:25:00Z", "synthetic_failure_cleared", "architect"),
	})

	evs, err := ReadFireTimeline(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("want 3 deliveries, got %d", len(evs))
	}
	// The stamps must survive the read, or Late() can never fire on real data.
	if evs[0].Due.IsZero() || evs[0].Fired.IsZero() {
		t.Fatalf("original_due/fired_at must parse off the event: %+v", evs[0])
	}
	if evs[0].Late() {
		t.Fatalf("an 18-second lag is not late: due=%s fired=%s", evs[0].Due, evs[0].Fired)
	}

	eps, err := ReadFailureEpisodes(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	sp := onlySchedule(t, SplitWithEpisodes(evs, eps))
	if sp.Batched != 2 {
		t.Fatalf("want 2 supersessions, got %d", sp.Batched)
	}
	if sp.BatchedInFailureEpisode != 2 {
		t.Fatalf("both landed inside the episode; the join failed (got %d) — check that "+
			"scheduler_fire_delivered.to still matches synthetic_failure_*.target",
			sp.BatchedInFailureEpisode)
	}
	if sp.LateDelivery != 0 {
		t.Fatalf("all three were punctual, got LateDelivery=%d", sp.LateDelivery)
	}
}

// Render must not print an acquittal it cannot support. A zero
// BatchedInFailureEpisode is ambiguous — no episodes overlapped, or the caller
// supplied none — so the dead-turn line is omitted rather than shown as 0%.
func TestRender_ReportsWhichSide_AndStaysSilentOnAnAmbiguousZero(t *testing.T) {
	tl := newTimeline("architect", "mail-check-architect")
	for i := 0; i < 5; i++ {
		tl.deliverPunctual()
	}
	tl.complete()
	evs := tl.events()

	bare := SplitPopulations(evs).Render()
	if !strings.Contains(bare, "WHICH SIDE") {
		t.Fatalf("the delivery/consumption split must be reported:\n%s", bare)
	}
	if !strings.Contains(bare, "Delivery is EXONERATED") {
		t.Fatalf("punctual fires must exonerate delivery in the render:\n%s", bare)
	}
	if strings.Contains(bare, "dead turns") {
		t.Fatalf("with no episodes supplied, a 0 must not read as an acquittal:\n%s", bare)
	}

	eps := []FailureEpisode{{Agent: "architect", From: popBase.Add(-time.Hour)}}
	joined := SplitWithEpisodes(evs, eps).Render()
	if !strings.Contains(joined, "dead turns") {
		t.Fatalf("a real episode must be named in the render:\n%s", joined)
	}
	if !strings.Contains(joined, "FAILING") {
		t.Fatalf("the render must say what the episode means:\n%s", joined)
	}
}

// --- fixtures -------------------------------------------------------------

func ev(ts, evType, target string) map[string]any {
	return map[string]any{
		"schema_version": 1, "timestamp": ts, "event_type": evType, "agent": "pogod",
		"details": map[string]any{"target": target},
	}
}

func fireEv(ts, to, id, token, due, fired string) map[string]any {
	return map[string]any{
		"schema_version": 1, "timestamp": ts,
		"event_type": "scheduler_fire_delivered", "agent": "pogod",
		"details": map[string]any{
			"to": to, "schedule_id": id, "fire_token": token,
			"original_due": due, "fired_at": fired,
		},
	}
}

func writeEventLog(t *testing.T, lines []map[string]any) string {
	t.Helper()
	var buf strings.Builder
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteString("\n")
	}
	path := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// The remedy is an artifact of the same kind as the defect, so it is subject to
// that defect. mg-772f is about a zero that was read as health — `nudge_sent`
// counting 647 clean deliveries into a dead fleet — and LateDelivery == 0 has
// exactly the same two readings. A window whose fires carry no due/fired stamps
// must therefore NOT render as an exoneration, because nothing looked.
func TestRender_UnstampedWindow_IsUnmeasuredNotExonerated(t *testing.T) {
	tl := newTimeline("architect", "mail-check-architect")
	for i := 0; i < 5; i++ {
		tl.deliver() // no Due/Fired — a pre-stamp log, or stamps that would not parse
	}
	tl.complete()

	rep := SplitPopulations(tl.events())
	if rep.DeliveryMeasured != 0 {
		t.Fatalf("fixture must carry no stamps, got DeliveryMeasured=%d", rep.DeliveryMeasured)
	}
	if rep.LateDelivery != 0 {
		t.Fatalf("unstamped fires cannot be late, got %d", rep.LateDelivery)
	}
	out := rep.Render()
	if !strings.Contains(out, "UNMEASURED") {
		t.Fatalf("a zero with no denominator must say so:\n%s", out)
	}
	if strings.Contains(out, "EXONERATED") {
		t.Fatalf("absence of evidence rendered as evidence of absence:\n%s", out)
	}
}

// ...and the converse: a window that DID measure punctuality must carry its
// denominator into the acquittal, so the reader can tell 5-of-5 from 5000-of-5000.
func TestRender_MeasuredWindow_CarriesItsDenominator(t *testing.T) {
	tl := newTimeline("architect", "mail-check-architect")
	for i := 0; i < 5; i++ {
		tl.deliverPunctual()
	}
	tl.complete()

	rep := SplitPopulations(tl.events())
	if rep.DeliveryMeasured != 5 {
		t.Fatalf("want 5 measurable fires, got %d", rep.DeliveryMeasured)
	}
	out := rep.Render()
	if !strings.Contains(out, "EXONERATED") {
		t.Fatalf("punctual fires must exonerate delivery:\n%s", out)
	}
	if !strings.Contains(out, "all 5 fires") {
		t.Fatalf("the acquittal must name how many fires it rests on:\n%s", out)
	}
}

// The population-1 legend must state what was MEASURED and not why. Its old
// wording — "several fires inside one turn" — asserted a cause nothing in the
// report had checked, and mg-772f measured that cause wrong for 51.5% of the
// fleet's superseded fires. Cause is claimed in the WHICH SIDE block, which is
// the only part that measures one.
func TestRender_Population1Legend_StatesTheFactNotTheCause(t *testing.T) {
	tl := newTimeline("mayor", "mail-check-mayor")
	for i := 0; i < 8; i++ {
		tl.deliverPunctual()
	}
	rendered := SplitPopulations(tl.complete().events()).Render()

	line := ""
	for _, l := range strings.Split(rendered, "\n") {
		if strings.Contains(l, "1. batched") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no population-1 line in the render:\n%s", rendered)
	}
	if !strings.Contains(line, "superseded before redemption") {
		t.Fatalf("the legend must state the measured fact, got %q", line)
	}
	for _, forbidden := range []string{"inside one turn", "one long turn", "busy"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("the legend asserts an unmeasured cause (%q): %q", forbidden, line)
		}
	}
}
