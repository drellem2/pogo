package ackwatch

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// As in ackwatch_test.go: every timeline here is built by hand. The one test
// that reads a file writes that file itself, into t.TempDir().

var popBase = time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)

// timeline is a tiny builder for a single schedule's fire history. Each call to
// deliver/complete advances the clock by one cadence period, which is the
// spacing that matters: a batch is exactly "deliveries with no complete between
// them".
type timeline struct {
	agent, id string
	at        time.Time
	step      time.Duration
	evs       []FireEvent
	seq       int
}

func newTimeline(agent, id string) *timeline {
	return &timeline{agent: agent, id: id, at: popBase, step: 10 * time.Minute}
}

// deliver appends a token-carrying delivery.
func (t *timeline) deliver() *timeline {
	t.seq++
	t.evs = append(t.evs, FireEvent{
		At: t.at, Kind: FireDelivered, Agent: t.agent, ID: t.id,
		Token: tokenFor(t.seq),
	})
	t.at = t.at.Add(t.step)
	return t
}

// deliverNoToken appends a delivery that carried no token at all — population 2.
func (t *timeline) deliverNoToken() *timeline {
	t.evs = append(t.evs, FireEvent{At: t.at, Kind: FireDelivered, Agent: t.agent, ID: t.id})
	t.at = t.at.Add(t.step)
	return t
}

// complete acks the most recent token.
func (t *timeline) complete() *timeline {
	t.evs = append(t.evs, FireEvent{
		At: t.at, Kind: FireCompleted, Agent: t.agent, ID: t.id, Token: tokenFor(t.seq),
	})
	t.at = t.at.Add(time.Second)
	return t
}

func tokenFor(n int) string { return string(rune('a'+n%26)) + "0000000" }

func (t *timeline) events() []FireEvent { return t.evs }

func onlySchedule(t *testing.T, rep PopulationReport) SchedulePopulation {
	t.Helper()
	if len(rep.Schedules) != 1 {
		t.Fatalf("want exactly 1 schedule, got %d", len(rep.Schedules))
	}
	return rep.Schedules[0]
}

// A diligent agent that acks every fire before the next arrives has NO deficit
// and a mean gap of exactly 1. This is the control: if it does not read 100%,
// nothing else in the file means anything.
func TestSplit_AcksEveryFire_NoDeficit(t *testing.T) {
	tl := newTimeline("pa", "mail-check-pa")
	for i := 0; i < 12; i++ {
		tl.deliver().complete()
	}
	rep := SplitPopulations(tl.events())
	sp := onlySchedule(t, rep)

	if sp.Delivered != 12 || sp.Completed != 12 {
		t.Fatalf("want 12/12, got %d/%d", sp.Completed, sp.Delivered)
	}
	if rep.Deficit() != 0 {
		t.Fatalf("want no deficit, got %d", rep.Deficit())
	}
	if sp.Batched != 0 || sp.TokenLess != 0 || sp.Boundary != 0 {
		t.Fatalf("want an empty split, got batched=%d tokenless=%d boundary=%d",
			sp.Batched, sp.TokenLess, sp.Boundary)
	}
	if sp.MeanGap != 1 || sp.MaxGap != 1 {
		t.Fatalf("want meanGap=1 maxGap=1, got %.2f/%d", sp.MeanGap, sp.MaxGap)
	}
}

// Population 1. The mayor's shape from the 2026-07-29 storm: eight fires land
// during one long turn, the agent acks once when the turn ends. Seven of the
// eight are recorded as misses and NOT ONE of them was ackable — the scheduler
// superseded each token as the next fire went out.
//
// This is the case the whole ticket turns on: read as diligence it says the
// agent ignored 7 of 8 instructions; read as delivery it says the fleet handed
// it 8 copies of one instruction while it was busy.
func TestSplit_BatchedFires_AreNotTheAgentsFailure(t *testing.T) {
	tl := newTimeline("mayor", "mail-check-mayor")
	for i := 0; i < 8; i++ {
		tl.deliver()
	}
	tl.complete()

	sp := onlySchedule(t, SplitPopulations(tl.events()))
	if sp.Delivered != 8 || sp.Completed != 1 {
		t.Fatalf("want 1/8, got %d/%d", sp.Completed, sp.Delivered)
	}
	if sp.Batched != 7 {
		t.Fatalf("want 7 batched (all but the redeemed one), got %d", sp.Batched)
	}
	if sp.Boundary != 0 {
		t.Fatalf("the batch was acked, so nothing is outstanding; got boundary=%d", sp.Boundary)
	}
	if sp.MeanGap != 8 {
		t.Fatalf("want meanGap=8 (one ack per 8 fires), got %.2f", sp.MeanGap)
	}
	// The entire deficit is mechanism, with nothing left over to call diligence.
	if got := sp.Batched + sp.TokenLess + sp.Boundary; got != sp.Delivered-sp.Completed {
		t.Fatalf("split not exhaustive: %d accounted vs deficit %d", got, sp.Delivered-sp.Completed)
	}
}

// Population 2. A fire that carried no token cannot be acked by anyone, so it
// must not be attributed to the agent's attention — and must not make the NEXT
// fire look superseded either.
func TestSplit_TokenLessFires_AreUnclosableAndDoNotDistortTheGap(t *testing.T) {
	tl := newTimeline("doctor", "mail-check-doctor")
	tl.deliverNoToken().deliverNoToken().deliver().complete()

	sp := onlySchedule(t, SplitPopulations(tl.events()))
	if sp.Delivered != 3 || sp.Completed != 1 {
		t.Fatalf("want 1/3, got %d/%d", sp.Completed, sp.Delivered)
	}
	if sp.TokenLess != 2 {
		t.Fatalf("want 2 token-less, got %d", sp.TokenLess)
	}
	if sp.Batched != 0 {
		t.Fatalf("a token-less fire supersedes nothing; want 0 batched, got %d", sp.Batched)
	}
	if sp.MeanGap != 1 {
		t.Fatalf("the one ackable fire was acked promptly; want meanGap=1, got %.2f", sp.MeanGap)
	}
}

// Population 2, characterised. On this fleet every token-less fire predates
// mg-a754, so the population is a pre-feature ERA and no fix addresses it. The
// report has to be able to say which of the two it is looking at, because the
// remedies are opposite: nothing, versus a scheduler defect.
func TestSplit_TokenLessHistorical_VersusLive(t *testing.T) {
	historical := newTimeline("doctor", "mail-check-doctor")
	historical.deliverNoToken().deliverNoToken().deliver().complete()
	repH := SplitPopulations(historical.events())
	if !repH.TokenLessIsHistorical() {
		t.Fatalf("token-less fires all precede the first token-carrying one; want historical")
	}

	live := newTimeline("doctor", "mail-check-doctor")
	live.deliver().complete().deliverNoToken()
	repL := SplitPopulations(live.events())
	if repL.TokenLessIsHistorical() {
		t.Fatalf("a token-less fire AFTER tokens shipped is a live mechanism, not history")
	}
	if !strings.Contains(repL.Render(), "Population 2 is LIVE") {
		t.Fatalf("a live population 2 must say so:\n%s", repL.Render())
	}
	if !strings.Contains(repH.Render(), "Population 2 is HISTORICAL") {
		t.Fatalf("a historical population 2 must say so:\n%s", repH.Render())
	}
}

// Population 3. 2fcc's observation: against a schedule that has only just
// started, the deficit is dominated by how many fires happened to be
// outstanding when you looked. It is bounded at ONE, so its distortion is
// 1/delivered — negligible for a long-lived schedule, and the entire reading
// for a short-lived one.
func TestSplit_BoundaryArtefact_IsBoundedAtOnePerSchedule(t *testing.T) {
	short := newTimeline("ddf7", "mail-check-mg-ddf7")
	short.deliver() // delivered, never acked, window ends
	sp := onlySchedule(t, SplitPopulations(short.events()))
	if sp.Boundary != 1 || sp.Batched != 0 {
		t.Fatalf("want boundary=1 batched=0, got boundary=%d batched=%d", sp.Boundary, sp.Batched)
	}
	if sp.Rate() != 0 {
		t.Fatalf("a single outstanding fire reads 0%%: got %.2f", sp.Rate())
	}

	long := newTimeline("pa", "mail-check-pa")
	for i := 0; i < 200; i++ {
		long.deliver().complete()
	}
	long.deliver() // same artefact, same size, against 201 fires
	lp := onlySchedule(t, SplitPopulations(long.events()))
	if lp.Boundary != 1 {
		t.Fatalf("the artefact is one fire regardless of history, got %d", lp.Boundary)
	}
	if lp.Rate() < 0.99 {
		t.Fatalf("against 201 fires the same artefact is negligible; got %.4f", lp.Rate())
	}
}

// THE ARGUMENT, as a test. rate == 1/meanGap - boundary/delivered holds exactly,
// for every shape, because it is algebra rather than an empirical fit. Measured
// against the live log on 2026-07-30 the residual was 0 across all 114
// schedules; here it is asserted against every shape the split can produce, so
// a future edit that makes the ratio mean something else fails loudly.
func TestSplit_RatioIsTheReciprocalAttentionGap_Exactly(t *testing.T) {
	shapes := map[string][]FireEvent{
		"acks every fire": func() []FireEvent {
			tl := newTimeline("a", "s")
			for i := 0; i < 9; i++ {
				tl.deliver().complete()
			}
			return tl.events()
		}(),
		"one long batch, acked": func() []FireEvent {
			tl := newTimeline("b", "s")
			for i := 0; i < 20; i++ {
				tl.deliver()
			}
			return tl.complete().events()
		}(),
		"ragged batches": func() []FireEvent {
			tl := newTimeline("c", "s")
			for _, n := range []int{1, 4, 2, 9, 1, 3} {
				for i := 0; i < n; i++ {
					tl.deliver()
				}
				tl.complete()
			}
			return tl.events()
		}(),
		"trailing outstanding fire": func() []FireEvent {
			tl := newTimeline("d", "s")
			tl.deliver().complete().deliver().deliver()
			return tl.events()
		}(),
		"never acks": func() []FireEvent {
			tl := newTimeline("e", "s")
			for i := 0; i < 15; i++ {
				tl.deliver()
			}
			return tl.events()
		}(),
		"token-less mixed in": func() []FireEvent {
			tl := newTimeline("f", "s")
			tl.deliverNoToken().deliver().deliver().complete().deliverNoToken().deliver()
			return tl.events()
		}(),
	}
	for name, evs := range shapes {
		t.Run(name, func(t *testing.T) {
			sp := onlySchedule(t, SplitPopulations(evs))
			// The identity is stated over the TOKEN-CARRYING fires: a fire
			// nothing could ack is not part of the attention gap, so it is
			// subtracted from the denominator before the identity is applied.
			ackable := SchedulePopulation{
				Delivered: sp.Delivered - sp.TokenLess,
				Completed: sp.Completed,
				Boundary:  sp.Boundary,
				MeanGap:   sp.MeanGap,
			}
			if resid := ackable.Identity(); math.Abs(resid) > 1e-12 {
				t.Fatalf("residual %.15f — the ratio has stopped being an inverted attention gap\n"+
					"  rate=%.6f meanGap=%.6f boundary=%d ackable=%d",
					resid, ackable.Rate(), sp.MeanGap, sp.Boundary, ackable.Delivered)
			}
			// And the split is exhaustive: mechanism accounts for the whole
			// deficit, leaving no residue to call negligence.
			if got := sp.Batched + sp.TokenLess + sp.Boundary; got != sp.Delivered-sp.Completed {
				t.Fatalf("split not exhaustive: accounted %d, deficit %d",
					got, sp.Delivered-sp.Completed)
			}
		})
	}
}

// THE REJECTED REPAIR, as a test. mg-ddf7 arrived requiring the denominator to
// count deliveries rather than dues, on the reasoning that "a batch of eight is
// ONE delivery". Measurement showed the denominator was ALREADY deliveries (see
// the header of populations.go), and that excluding the batch — equivalently,
// counting one ackable opportunity per batch — is CIRCULAR. A fire is "batched"
// precisely when nothing acked it before the next one arrived, so subtracting
// the batched fires from the denominator subtracts the deficit from its own
// denominator, and what is left is
//
//	completed / (completed + boundary)   ==   A / (A + b),  b in {0,1}
//
// a function of the ACK COUNT ALONE. It says nothing whatever about the ratio of
// work asked to work done: any schedule with 20 acks reads >= 95% whether the
// agent is wedged or perfect, which is above the 0.75 Floor and inside the 0.20
// MinGap of any healthy peer, so the finding disappears.
//
// That is the mg-7254 failure in the opposite direction — a signal pinned
// HEALTHY instead of pinned UNHEALTHY — and it is why the repair was rejected
// rather than landed. This test pins the algebra so nobody lands it later
// believing it separates the two cases.
func TestSplit_ExcludingBatchedFires_IsCircular_AndPinsTheSignalHealthy(t *testing.T) {
	// Two agents with the SAME batch structure, one of which then dies.
	batchesOf8 := func(tl *timeline, rounds int) *timeline {
		for round := 0; round < rounds; round++ {
			for i := 0; i < 8; i++ {
				tl.deliver()
			}
			tl.complete()
		}
		return tl
	}
	busyButHealthy := batchesOf8(newTimeline("mayor", "mail-check-mayor"), 20)
	wedged := batchesOf8(newTimeline("pm-pogo", "mail-check-pm-pogo"), 20)
	for i := 0; i < 200; i++ { // 200 fires into a void: unambiguously broken
		wedged.deliver()
	}

	hp := onlySchedule(t, SplitPopulations(busyButHealthy.events()))
	wp := onlySchedule(t, SplitPopulations(wedged.events()))

	// exclBatched IS the proposed repair: denominator = delivered - batched.
	exclBatched := func(sp SchedulePopulation) float64 {
		den := sp.Delivered - sp.Batched - sp.TokenLess
		if den <= 0 {
			return 0
		}
		return float64(sp.Completed) / float64(den)
	}

	// 1. The repair's denominator collapses to completed+boundary, exactly.
	for _, sp := range []SchedulePopulation{hp, wp} {
		if got, want := sp.Delivered-sp.Batched-sp.TokenLess, sp.Completed+sp.Boundary; got != want {
			t.Fatalf("%s: batch-excluded denominator is %d, want completed+boundary=%d — "+
				"if this ever differs, the circularity argument needs redoing", sp.ID, got, want)
		}
	}

	// 2. So both agents read healthy, and the 200 dead fires are invisible.
	healthyExcl, wedgedExcl := exclBatched(hp), exclBatched(wp)
	if healthyExcl != 1 {
		t.Fatalf("want the healthy agent pinned at 100%%, got %.4f", healthyExcl)
	}
	if wedgedExcl < 0.95 {
		t.Fatalf("want the WEDGED agent also reading healthy (>=95%%), got %.4f", wedgedExcl)
	}
	if gap := healthyExcl - wedgedExcl; gap >= DefaultMinGap {
		t.Fatalf("under the repair the peer gap is %.4f, which would still clear MinGap %.2f — "+
			"the pinning claim would then be wrong", gap, DefaultMinGap)
	}
	if wedgedExcl < DefaultFloor {
		t.Fatalf("under the repair the wedged agent reads %.4f, below Floor %.2f — "+
			"it would still be caught, and the pinning claim would be wrong", wedgedExcl, DefaultFloor)
	}

	// 3. maxGap separates them, and unlike the ratio it does not saturate: the
	//    healthy agent's run is bounded by its own turn length, the wedged one's
	//    climbs without bound. This is the statistic the design in
	//    docs/investigations/ack-deficit-populations-2026-07-30.md builds on.
	if !(wp.MaxGap > hp.MaxGap*2) {
		t.Fatalf("maxGap must separate them: healthy=%d wedged=%d", hp.MaxGap, wp.MaxGap)
	}
}

// CALM: the shipped ratio works, and the rejected repair would have destroyed
// it. Healthy peers on a quiet fleet ack every fire before the next arrives
// (measured mean gap 1.01 for pa, architect and pm-onethird), so a wedged agent
// stands out by 50 points and the detector fires correctly.
//
// This is the reading everyone has of this metric, and it is the reading that
// makes the metric look fine. It is also the ONLY regime in which it is fine —
// see the storm test below.
func TestSplit_CalmFleet_ShippedRatioSeparates_RepairDestroysIt(t *testing.T) {
	healthy := newTimeline("pa", "mail-check-pa")
	for i := 0; i < 60; i++ {
		healthy.deliver().complete()
	}
	// pm-pogo's calm-window shape, from the events log: 60 delivered, 29 acked,
	// mean gap 2.07 — it acks, just far too rarely.
	wedged := newTimeline("pm-pogo", "mail-check-pm-pogo")
	for round := 0; round < 29; round++ {
		wedged.deliver().deliver().complete()
	}
	wedged.deliver().deliver()

	hp := onlySchedule(t, SplitPopulations(healthy.events()))
	wp := onlySchedule(t, SplitPopulations(wedged.events()))

	if gap := hp.Rate() - wp.Rate(); gap < DefaultMinGap {
		t.Fatalf("in CALM the shipped ratio must separate healthy (%.3f) from wedged (%.3f) "+
			"by at least MinGap %.2f; got %.3f", hp.Rate(), wp.Rate(), DefaultMinGap, gap)
	}
	if wp.Rate() >= DefaultFloor {
		t.Fatalf("in CALM the wedged agent must sit below Floor %.2f; got %.3f",
			DefaultFloor, wp.Rate())
	}

	// And the repair erases exactly that finding.
	exclBatched := func(sp SchedulePopulation) float64 {
		den := sp.Delivered - sp.Batched - sp.TokenLess
		if den <= 0 {
			return 0
		}
		return float64(sp.Completed) / float64(den)
	}
	if got := exclBatched(wp); got < DefaultFloor {
		t.Fatalf("the repair is supposed to lift the wedged agent above Floor %.2f "+
			"(that is the objection to it); got %.3f", DefaultFloor, got)
	}
	if gap := exclBatched(hp) - exclBatched(wp); gap >= DefaultMinGap {
		t.Fatalf("under the repair the calm true positive should vanish; gap still %.3f", gap)
	}
}

// STORM: NEITHER statistic separates them. This is the ticket's central claim,
// and it is the reason the metric is least trustworthy exactly when it is most
// consulted — nobody reads ack ratios on a quiet afternoon.
//
// A storm batches fires (inflating the deficit artificially) at the same moment
// agents are genuinely struggling (inflating it truthfully). Here a
// busy-but-perfectly-diligent agent acking once per 8-fire batch and a wedged
// agent that has stopped entirely both sit far below the Floor, within MinGap of
// each other. The ratio has stopped carrying information: it is pinned
// UNHEALTHY for both. The rejected repair pins both HEALTHY. Only the run length
// still separates them.
func TestSplit_StormFleet_NeitherRatioNorRepairSeparates_OnlyMaxGapDoes(t *testing.T) {
	// Diligent, but its turns run 8 cadence periods long.
	busyButHealthy := newTimeline("mayor", "mail-check-mayor")
	for round := 0; round < 20; round++ {
		for i := 0; i < 8; i++ {
			busyButHealthy.deliver()
		}
		busyButHealthy.complete()
	}
	// Same batching, then stops acking altogether.
	wedged := newTimeline("pm-pogo", "mail-check-pm-pogo")
	for round := 0; round < 20; round++ {
		for i := 0; i < 8; i++ {
			wedged.deliver()
		}
		wedged.complete()
	}
	for i := 0; i < 200; i++ {
		wedged.deliver()
	}

	hp := onlySchedule(t, SplitPopulations(busyButHealthy.events()))
	wp := onlySchedule(t, SplitPopulations(wedged.events()))

	// Both are flagged: the diligent agent is a FALSE positive, in the same
	// report as the true one, and nothing in the numbers tells them apart.
	if hp.Rate() >= DefaultFloor {
		t.Fatalf("the busy-but-healthy agent should read below Floor %.2f in a storm "+
			"(that is the false positive); got %.3f", DefaultFloor, hp.Rate())
	}
	if wp.Rate() >= DefaultFloor {
		t.Fatalf("the wedged agent should read below Floor %.2f; got %.3f", DefaultFloor, wp.Rate())
	}
	if gap := hp.Rate() - wp.Rate(); gap >= DefaultMinGap {
		t.Fatalf("in a STORM the ratio must FAIL to separate them (gap < MinGap %.2f) — "+
			"if it separates, this ticket's central claim is wrong; gap was %.3f",
			DefaultMinGap, gap)
	}

	// The one statistic that still discriminates, by 10x.
	if !(wp.MaxGap >= hp.MaxGap*10) {
		t.Fatalf("maxGap must still separate them in a storm: healthy=%d wedged=%d",
			hp.MaxGap, wp.MaxGap)
	}
}

// Fires from different schedules must not be interleaved into one timeline —
// each agent's attention gap is its own. A storm is precisely the condition
// under which many schedules fire in the same minute, so a grouping bug here
// would corrupt every number the storm produced.
func TestSplit_SchedulesAreClassifiedIndependently(t *testing.T) {
	var evs []FireEvent
	good := newTimeline("pa", "mail-check-pa")
	for i := 0; i < 4; i++ {
		good.deliver().complete()
	}
	bad := newTimeline("pm-pogo", "mail-check-pm-pogo")
	for i := 0; i < 4; i++ {
		bad.deliver()
	}
	evs = append(evs, good.events()...)
	evs = append(evs, bad.events()...)

	rep := SplitPopulations(evs)
	if len(rep.Schedules) != 2 {
		t.Fatalf("want 2 schedules, got %d", len(rep.Schedules))
	}
	byID := map[string]SchedulePopulation{}
	for _, s := range rep.Schedules {
		byID[s.ID] = s
	}
	if r := byID["mail-check-pa"].Rate(); r != 1 {
		t.Fatalf("pa acked every fire; want 1.0, got %.3f", r)
	}
	if r := byID["mail-check-pm-pogo"].Rate(); r != 0 {
		t.Fatalf("pm-pogo acked nothing; want 0.0, got %.3f", r)
	}
	if byID["mail-check-pa"].Batched != 0 {
		t.Fatalf("pa's fires must not be superseded by pm-pogo's")
	}
}

// A completion whose delivery fell before the window is counted as an orphan and
// excluded, rather than credited — otherwise a window edge could push a rate
// above 1 and the whole table would stop being readable.
func TestSplit_OrphanAck_IsExcludedNotCredited(t *testing.T) {
	evs := []FireEvent{
		{At: popBase, Kind: FireCompleted, Agent: "pa", ID: "mail-check-pa", Token: "old"},
		{At: popBase.Add(time.Minute), Kind: FireDelivered, Agent: "pa", ID: "mail-check-pa", Token: "new"},
		{At: popBase.Add(2 * time.Minute), Kind: FireCompleted, Agent: "pa", ID: "mail-check-pa", Token: "new"},
	}
	sp := onlySchedule(t, SplitPopulations(evs))
	if sp.OrphanAcks != 1 {
		t.Fatalf("want 1 orphan ack, got %d", sp.OrphanAcks)
	}
	if sp.Completed != 1 || sp.Delivered != 1 {
		t.Fatalf("want 1/1 from the in-window pair, got %d/%d", sp.Completed, sp.Delivered)
	}
	if sp.Rate() > 1 {
		t.Fatalf("rate must never exceed 1; got %.3f", sp.Rate())
	}
}

// An empty window reports "nothing measured", not "nothing wrong". Same rule as
// Report.renderCoverage: this package exists because a silence was read as an
// all-clear.
func TestSplit_EmptyWindow_SaysNothingMeasured(t *testing.T) {
	rep := SplitPopulations(nil)
	out := rep.Render()
	if !strings.Contains(out, "Nothing measured") {
		t.Fatalf("an empty window must not read as clean:\n%s", out)
	}
	if rep.Deficit() != 0 || rep.Delivered != 0 {
		t.Fatalf("want an empty report, got %+v", rep)
	}
}

// The render names all three populations and the mechanism framing, because the
// framing is the deliverable: a reader who takes the number for diligence draws
// the opposite conclusion from the correct one.
func TestPopulationReport_RenderNamesTheMechanisms(t *testing.T) {
	tl := newTimeline("mayor", "mail-check-mayor")
	for i := 0; i < 8; i++ {
		tl.deliver()
	}
	out := tl.complete().events()
	rendered := SplitPopulations(out).Render()
	for _, want := range []string{
		"MECHANISM, not diligence",
		"batched", "token-less", "boundary",
		"one redeemable token",
		"unclosable BY THE AGENT",
		"not an agent property",
		"reciprocal of the mean attention gap",
		"does not saturate",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("render is missing %q:\n%s", want, rendered)
		}
	}
}

// ReadFireTimeline reads a log this test writes. It is the only filesystem
// touch in the package and it takes an explicit path — no POGO_HOME, no live
// events.log (mg-6092/mg-e8e7/mg-5336).
func TestReadFireTimeline_ParsesDeliveriesAndCompletions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	lines := []map[string]any{
		{"schema_version": 1, "timestamp": "2026-07-29T15:00:00Z",
			"event_type": "scheduler_fire_delivered", "agent": "pogod",
			"details": map[string]any{"to": "pa", "schedule_id": "mail-check-pa", "fire_token": "aa11"}},
		{"schema_version": 1, "timestamp": "2026-07-29T15:00:30Z",
			"event_type": "scheduler_fire_completed", "agent": "pogod",
			"details": map[string]any{"to": "pa", "schedule_id": "mail-check-pa", "fire_token": "aa11"}},
		// A delivery with no token — population 2, and it must survive parsing
		// as an empty token rather than being dropped.
		{"schema_version": 1, "timestamp": "2026-07-29T15:10:00Z",
			"event_type": "scheduler_fire_delivered", "agent": "pogod",
			"details": map[string]any{"to": "pa", "schedule_id": "mail-check-pa"}},
		// Noise the reader must ignore.
		{"schema_version": 1, "timestamp": "2026-07-29T15:11:00Z",
			"event_type": "nudge_sent", "agent": "pogod",
			"details": map[string]any{"to": "pa", "fire_token": "zz99"}},
		// Outside the window, on the far side of until.
		{"schema_version": 1, "timestamp": "2026-07-29T18:00:00Z",
			"event_type": "scheduler_fire_delivered", "agent": "pogod",
			"details": map[string]any{"to": "pa", "schedule_id": "mail-check-pa", "fire_token": "bb22"}},
	}
	var buf strings.Builder
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	since := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	until := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	evs, err := ReadFireTimeline(path, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("want 3 in-window fire events, got %d: %+v", len(evs), evs)
	}
	// Sorted by time, deliveries and completions interleaved correctly — the
	// split is meaningless if the merge of the two reads loses the ordering.
	if evs[0].Kind != FireDelivered || evs[1].Kind != FireCompleted || evs[2].Kind != FireDelivered {
		t.Fatalf("events out of order: %+v", evs)
	}
	if evs[2].Token != "" {
		t.Fatalf("the token-less delivery must parse with an empty token, got %q", evs[2].Token)
	}
	sp := onlySchedule(t, SplitPopulations(evs))
	if sp.Delivered != 2 || sp.Completed != 1 || sp.TokenLess != 1 {
		t.Fatalf("want 1/2 with one token-less, got %d/%d tokenless=%d",
			sp.Completed, sp.Delivered, sp.TokenLess)
	}
}

// A missing log is not an empty measurement. events.ReadFiltered returns nil for
// a nonexistent path, so the caller gets an empty timeline — the render then has
// to say "nothing measured" rather than implying a clean fleet.
func TestReadFireTimeline_MissingLogYieldsNothingMeasured(t *testing.T) {
	evs, err := ReadFireTimeline(filepath.Join(t.TempDir(), "absent.log"), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("a missing log is not an error here: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("want no events, got %d", len(evs))
	}
	if !strings.Contains(SplitPopulations(evs).Render(), "Nothing measured") {
		t.Fatal("an unread log must not render as a clean fleet")
	}
}
