package ackwatch

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/scheduler"
)

// The correlation this file exists to prevent (mg-00d6).
//
// The comment on the fleet arm in ackwatch.go names `pogo schedule completion`'s
// Tracked as one of two compensating controls for the ack-aware gap. Both the
// gate here (ackAwareCohort) and that control read the same predicate: has this
// schedule ever acked. While that predicate was FiresCompleted > 0, a boot-path
// re-registration zeroed it — so the primary and its named backstop failed
// together, for the same reason, at the same moment. A backstop that shares a
// trigger with the thing it backs up is redundancy in name only.
//
// The defect is read from SOURCE. mg-00d6's fleet-wide operational story was
// withdrawn by its author for want of a supporting measurement, and the blind
// case is bounded — see
// TestBounce_TheBlindCaseNeedsAZEROEDMAJORITY_NotJustABounce. What is not
// bounded is the tail: the state clears only on an ack, so for a schedule whose
// agent never returns it does not clear at all, and that is the case these
// tests are built on.
//
// A unit test on the EverAcked bit alone passes while that hole stays open,
// which is why this test drives a REAL scheduler through a real bounce and then
// interrogates BOTH controls from the one resulting state.
//
// This test builds its own scheduler under t.TempDir(). It reads no ~/.pogo and
// no live daemon — see the note at the top of source.go.

// bounceFleet is the crew's mail-check population: four agents on the same
// */10 cadence, the cohort shape the fleet arm is written for.
var bounceFleet = []string{"architect", "pa", "pm-onethird", "pm-pogo"}

func bounceMailCheck(agent string) scheduler.Entry {
	return scheduler.Entry{
		Agent:    agent,
		ID:       "mail-check-" + agent,
		Cron:     "*/10 * * * *",
		Delivery: scheduler.DeliveryNudge,
		Message:  "Check your mail with mg mail list " + agent + " and handle any unread messages.",
	}
}

// TestBounceDoesNotBlindBothControlsAtOnce is the correlation property. It is
// deliberately NOT parameterised over the two controls separately: the defect
// was that they degrade TOGETHER, so the assertion has to be made against one
// shared state.
func TestBounceDoesNotBlindBothControlsAtOnce(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"),
		scheduler.DelivererFunc(func(ctx context.Context, e scheduler.Entry, at time.Time) error { return nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for _, a := range bounceFleet {
		if _, err := s.Add(bounceMailCheck(a), start); err != nil {
			t.Fatalf("Add %s: %v", a, err)
		}
	}

	// A healthy day: 25 fires each, every one acked. 25 clears MinFires (20), so
	// these schedules are eligible for the ratio arm on their own merits.
	at := start
	for i := 0; i < 25; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
		for _, a := range bounceFleet {
			e, ok := s.Get(a, "mail-check-"+a)
			if !ok || e.PendingToken == "" {
				t.Fatalf("cycle %d: no fire outstanding for %s", i, a)
			}
			if _, err := s.Ack(a, "mail-check-"+a, e.PendingToken, at.Add(time.Second)); err != nil {
				t.Fatalf("cycle %d Ack %s: %v", i, a, err)
			}
		}
	}

	// THE NIGHTLY BOUNCE. Every crew agent restarts and re-registers its
	// schedules with the same --id, exactly as the startup procedure instructs.
	bounce := at.Add(30 * time.Minute)
	for _, a := range bounceFleet {
		if _, err := s.Add(bounceMailCheck(a), bounce); err != nil {
			t.Fatalf("re-Add %s: %v", a, err)
		}
	}
	for _, a := range bounceFleet {
		e, _ := s.Get(a, "mail-check-"+a)
		if e.FiresCompleted != 0 {
			t.Fatalf("setup: %s kept FiresCompleted=%d across the bounce; this test is only meaningful against the reset (mg-49b1 pins it)",
				a, e.FiresCompleted)
		}
	}

	// The agents never come back. Fires keep being delivered on time and nothing
	// acks them — the 2026-07-22 shape, entered from a bounce.
	at = bounce
	for i := 0; i < 25; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
	}
	now := at.Add(time.Minute)

	// CONTROL 1 — `pogo schedule completion`, the one ackwatch's comment names.
	stats := s.Completion("", 0)
	if stats.Schedules != len(bounceFleet) {
		t.Fatalf("Schedules = %d, want %d", stats.Schedules, len(bounceFleet))
	}
	if stats.Tracked != len(bounceFleet) {
		t.Errorf("Tracked = %d, want %d — the bounce moved the whole fleet OUT of the ack-aware population instead of driving its ratio to zero, so the one shape this signal recognises became the shape it cannot see",
			stats.Tracked, len(bounceFleet))
	}
	if stats.Stalled != len(bounceFleet) {
		t.Errorf("Stalled = %d, want %d — every schedule has an unbounded unacked streak and none of them is being counted",
			stats.Stalled, len(bounceFleet))
	}
	if stats.TrackedReset != len(bounceFleet) {
		t.Errorf("TrackedReset = %d, want %d — the roll-up has to disclose that its ratio denominator was reset by the bounce",
			stats.TrackedReset, len(bounceFleet))
	}
	// The positive control for CONTROL 1: what the pre-fix predicate
	// (FiresCompleted > 0) would have counted on this same state. If this ever
	// stops being zero the state under test is no longer the post-bounce state
	// and the assertions above have quietly stopped meaning anything.
	preFix := 0
	for _, e := range s.List("") {
		if e.FiresCompleted > 0 {
			preFix++
		}
	}
	if preFix != 0 {
		t.Errorf("%d schedule(s) still have live completions — this fixture is supposed to reproduce the state where the OLD predicate counted nothing, and it no longer does", preFix)
	}

	// CONTROL 2 — ackwatch's own ratio arms, gated by ackAwareCohort.
	samples := SampleEntries(s.List(""), now)
	rep := Detect(Snapshot{
		Now:              now,
		Samples:          samples,
		LastDisruption:   bounce,
		DisruptionReason: "pogod restart",
	}, DefaultParams())

	if rep.Suppressed {
		t.Fatalf("report suppressed %s after the bounce: %s — the settle window should be long past",
			now.Sub(bounce), rep.SuppressReason)
	}
	if rep.Eligible != len(bounceFleet) {
		t.Fatalf("Eligible = %d of %d scanned (fresh=%d fewFires=%d notRecurring=%d) — this test needs the ratio arm to actually run",
			rep.Eligible, rep.Scanned, rep.SkippedFresh, rep.SkippedFewFires, rep.SkippedNotRecurring)
	}
	if rep.SkippedNoPeers > 0 {
		t.Errorf("SkippedNoPeers = %d — the cohort read as not-ack-aware because the bounce zeroed every FiresCompleted, so both arms declined to judge a fleet that is completing nothing",
			rep.SkippedNoPeers)
	}
	if len(rep.Fleet) != 1 {
		t.Fatalf("Fleet findings = %d, want 1 — a fleet of four schedules delivering 25 fires each and completing none is the founding case for this arm; got %+v",
			len(rep.Fleet), rep.Fleet)
	}
	if got := rep.Fleet[0].Schedules; got != len(bounceFleet) {
		t.Errorf("fleet finding covers %d schedules, want %d", got, len(bounceFleet))
	}
	if got := rep.Fleet[0].Median; got != 0 {
		t.Errorf("fleet median = %v, want 0", got)
	}
}

// TestBounceOfANeverAckingFleetStaysUnjudged is the other half, and it is the
// reason the fix is one BIT and not a preserved counter. A fleet whose prompts
// simply never mention `ack` must still read as unknown after a bounce — the
// ack-aware gate exists to keep this detector from accusing it, and a change
// that made the gate always-true would have "fixed" the correlation by
// deleting the guard.
func TestBounceOfANeverAckingFleetStaysUnjudged(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"),
		scheduler.DelivererFunc(func(ctx context.Context, e scheduler.Entry, at time.Time) error { return nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for _, a := range bounceFleet {
		if _, err := s.Add(bounceMailCheck(a), start); err != nil {
			t.Fatalf("Add %s: %v", a, err)
		}
	}
	at := start
	for i := 0; i < 25; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
	}
	bounce := at.Add(30 * time.Minute)
	for _, a := range bounceFleet {
		if _, err := s.Add(bounceMailCheck(a), bounce); err != nil {
			t.Fatalf("re-Add %s: %v", a, err)
		}
	}
	at = bounce
	for i := 0; i < 25; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
	}
	now := at.Add(time.Minute)

	if stats := s.Completion("", 0); stats.Tracked != 0 || stats.Stalled != 0 {
		t.Errorf("Tracked/Stalled = %d/%d, want 0/0 — nothing here has ever acked, so nothing here is evidence",
			stats.Tracked, stats.Stalled)
	}

	rep := Detect(Snapshot{Now: now, Samples: SampleEntries(s.List(""), now)}, DefaultParams())
	if len(rep.Fleet) != 0 || len(rep.Deficits) != 0 {
		t.Errorf("accused a fleet that was never taught to ack: fleet=%+v deficits=%+v", rep.Fleet, rep.Deficits)
	}
	if rep.SkippedNoPeers != len(bounceFleet) {
		t.Errorf("SkippedNoPeers = %d, want %d — these are UNJUDGED, and that has to be visible rather than reading as a clean bill of health",
			rep.SkippedNoPeers, len(bounceFleet))
	}
}

// TestSampleTrackedMirrorsCompletionTracked is a drift guard, and it exists
// because this fix's own shape is the defect it repairs.
//
// The predicate "has this schedule ever acked" is written TWICE — once as
// scheduler.Entry.CompletionTracked and once as Sample.Tracked — because Detect
// is pure over a Snapshot and does not import the scheduler. mg-00d6 is a
// ticket about one predicate at two call sites drifting into opposite
// consequences, so leaving two hand-copied definitions with nothing comparing
// them would reproduce the fault in the remedy. This compares them over the
// whole truth table of the two inputs they read.
func TestSampleTrackedMirrorsCompletionTracked(t *testing.T) {
	ref := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		completed int
		everAcked bool
	}{
		{"never acked", 0, false},
		{"acking now", 4, false}, // pre-migration on-disk shape
		{"acked, then re-registered", 0, true},
		{"acked, re-registered, acking again", 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := scheduler.Entry{
				Agent: "architect", ID: "mail-check-architect",
				Cron: "*/10 * * * *", Delivery: scheduler.DeliveryNudge,
				FiresDelivered: 25, FiresCompleted: tc.completed, EverAcked: tc.everAcked,
			}
			samples := SampleEntries([]scheduler.Entry{e}, ref)
			if len(samples) != 1 {
				t.Fatalf("SampleEntries returned %d samples", len(samples))
			}
			if got, want := samples[0].Tracked(), e.CompletionTracked(); got != want {
				t.Errorf("Sample.Tracked() = %v but Entry.CompletionTracked() = %v — the two copies of this predicate have drifted, which is the mg-00d6 fault reproduced inside its own fix",
					got, want)
			}
		})
	}
}

// stripEverAcked returns the samples with the ack-history bit cleared, which is
// exactly the state the pre-mg-00d6 predicate saw after a bounce. It is the
// POSITIVE CONTROL: a test that only asserts the fixed behaviour cannot tell a
// working guard from a guard whose precondition never occurs, and this package
// has a standing rule about a silent correct outcome being indistinguishable
// from a control that is not running.
func stripEverAcked(samples []Sample) []Sample {
	out := make([]Sample, len(samples))
	copy(out, samples)
	for i := range out {
		out[i].EverAcked = false
	}
	return out
}

// TestBounce_PreFixPredicateWentSilentOnTheSameState pins the fault the fix
// removes. Same post-bounce state as TestBounceDoesNotBlindBothControlsAtOnce,
// one bit cleared: the ratio arms stop judging entirely.
//
// The direction matters and is easy to state backwards. The fix does not make
// the detector noisier — it makes it ABLE TO SPEAK about a fleet that has
// stopped completing work. Without the bit the same dead fleet produces
// silence, and silence is what 2026-07-22 already had.
func TestBounce_PreFixPredicateWentSilentOnTheSameState(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"),
		scheduler.DelivererFunc(func(ctx context.Context, e scheduler.Entry, at time.Time) error { return nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now, samples := bouncedThenAbandoned(t, s)

	live := Detect(Snapshot{Now: now, Samples: samples}, DefaultParams())
	if len(live.Fleet) != 1 || live.SkippedNoPeers != 0 {
		t.Fatalf("control setup is wrong: with the bit the arm must judge; fleet=%d skippedNoPeers=%d",
			len(live.Fleet), live.SkippedNoPeers)
	}

	blind := Detect(Snapshot{Now: now, Samples: stripEverAcked(samples)}, DefaultParams())
	if blind.SkippedNoPeers != len(bounceFleet) {
		t.Errorf("SkippedNoPeers = %d, want %d — without the bit every schedule must read as not-ack-aware; if it does not, this test is no longer controlling anything",
			blind.SkippedNoPeers, len(bounceFleet))
	}
	if len(blind.Fleet) != 0 || len(blind.Deficits) != 0 {
		t.Errorf("the pre-fix predicate is not silent here (fleet=%+v deficits=%+v), so the fault this test pins is not the fault the fix removes",
			blind.Fleet, blind.Deficits)
	}
}

// TestBlackoutArmIsIndependentOfTheBounce asserts, rather than asserts IN PROSE,
// the independence that turned out to be load-bearing.
//
// ackwatch's absolute arm fired 33 consecutive correct FLEET BLACKOUT alarms
// through the 2026-08-11 outage (recorded at config.go's FirstTurnConfig) while
// the ratio arms above were taking SkippedNoPeers. That is not luck: the arm
// reads Snapshot.Recent — a window counted from the EVENTS LOG — and never
// touches Sample, Tracked, the cohorts or the counters, so a re-registration
// cannot reset its input. It was the surviving cover for the gap mg-00d6
// closes, and a comment claiming independence is an untested claim.
//
// The bit is stripped here deliberately: the arm must judge the dead fleet
// whether or not the ack-history bit exists at all.
func TestBlackoutArmIsIndependentOfTheBounce(t *testing.T) {
	s, err := scheduler.New(filepath.Join(t.TempDir(), "schedules.json"),
		scheduler.DelivererFunc(func(ctx context.Context, e scheduler.Entry, at time.Time) error { return nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now, samples := bouncedThenAbandoned(t, s)

	// The events-log window over the post-bounce stretch: 25 fires to each of
	// four agents, none completed. Counters zeroed, log intact — that asymmetry
	// is the arm's whole reason for reading events instead of entries.
	recent := &Recent{
		Window:    4 * time.Hour,
		Delivered: 100,
		Completed: 0,
		Schedules: len(bounceFleet),
		Agents:    bounceFleet,
		ByAgent:   map[string]AgentFires{},
	}
	for _, a := range bounceFleet {
		recent.ByAgent[a] = AgentFires{Delivered: 25, Schedules: 1}
	}
	running := map[string]time.Time{}
	for _, a := range bounceFleet {
		running[a] = now.Add(-48 * time.Hour)
	}

	rep := Detect(Snapshot{
		Now:          now,
		Samples:      stripEverAcked(samples),
		Recent:       recent,
		RunningSince: running,
	}, DefaultParams())

	if rep.BlackoutBlind != "" {
		t.Fatalf("the absolute arm declined to judge: %s", rep.BlackoutBlind)
	}
	if rep.Blackout == nil {
		t.Fatalf("no blackout finding on a fleet that was delivered 100 fires and completed none — this arm is the cover that survived the bounce, and it must not have acquired a dependency on the ack-history bit")
	}
	if rep.Blackout.Completed != 0 || rep.Blackout.Delivered != 100 {
		t.Errorf("blackout finding reads %d/%d, want 0/100", rep.Blackout.Completed, rep.Blackout.Delivered)
	}
	// And the ratio arms are still blind on this same snapshot, which is what
	// makes the independence a real partition rather than a coincidence.
	if rep.SkippedNoPeers != len(bounceFleet) {
		t.Errorf("SkippedNoPeers = %d, want %d — the point of this test is that the absolute arm speaks WHILE the ratio arms cannot",
			rep.SkippedNoPeers, len(bounceFleet))
	}
}

// bouncedThenAbandoned drives s to the state both tests above need: a fleet that
// acked its way past MinFires, was re-registered by the nightly bounce, and then
// never came back. Returns the sample time and the resulting samples.
func bouncedThenAbandoned(t *testing.T, s *scheduler.Scheduler) (time.Time, []Sample) {
	t.Helper()
	start := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	for _, a := range bounceFleet {
		if _, err := s.Add(bounceMailCheck(a), start); err != nil {
			t.Fatalf("Add %s: %v", a, err)
		}
	}
	at := start
	for i := 0; i < 25; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
		for _, a := range bounceFleet {
			e, _ := s.Get(a, "mail-check-"+a)
			if _, err := s.Ack(a, "mail-check-"+a, e.PendingToken, at.Add(time.Second)); err != nil {
				t.Fatalf("Ack %s: %v", a, err)
			}
		}
	}
	bounce := at.Add(30 * time.Minute)
	for _, a := range bounceFleet {
		if _, err := s.Add(bounceMailCheck(a), bounce); err != nil {
			t.Fatalf("re-Add %s: %v", a, err)
		}
	}
	at = bounce
	for i := 0; i < 25; i++ {
		at = at.Add(10 * time.Minute)
		s.Tick(context.Background(), at)
	}
	now := at.Add(time.Minute)
	return now, SampleEntries(s.List(""), now)
}

// TestBounce_TheBlindCaseNeedsAZEROEDMAJORITY_NotJustABounce is the narrowing
// (mg-00d6, raised by pm-pogo after the ticket's fleet-wide framing was
// withdrawn by its author). It is here because it bounds the defect, and a
// defect whose bound is not written down gets re-filed at its original size.
//
// Zeroing requires a BOOT: an agent that dies without booting never
// re-registers, so its counters are never zeroed and its schedule stays
// tracked. ackAwareCohort needs a MAJORITY (tracked*2 > len(peers)), so
// survivors carry the cohort. The blind case is therefore not "a bounce", it is
// "a majority of one cohort zeroed and none of that majority acking since".
//
// Both directions are asserted, because only the pair states the bound.
func TestBounce_TheBlindCaseNeedsAZEROEDMAJORITY_NotJustABounce(t *testing.T) {
	// Three healthy peers that never rebooted, plus one that bounced and died.
	// Every sample here has the ack-history bit CLEARED, so this measures the
	// PRE-FIX predicate: the question is whether survivors alone hold the
	// cohort, independent of anything mg-00d6 changed.
	survivorsHold := []Sample{
		mailCheck("architect", 240, 250),
		mailCheck("pa", 244, 250),
		mailCheck("pm-onethird", 241, 250),
		bouncedSample("pm-pogo"),
	}
	rep := Detect(snap(survivorsHold), DefaultParams())
	if rep.SkippedNoPeers != 0 {
		t.Errorf("SkippedNoPeers = %d — three surviving peers are a majority of any candidate's cohort, so the arms must still judge; if they do not, the narrowing is wrong and the defect is bigger than mg-00d6 now claims",
			rep.SkippedNoPeers)
	}
	if len(rep.Deficits) != 1 || rep.Deficits[0].Agent != "pm-pogo" {
		t.Fatalf("want exactly one deficit on pm-pogo, got %+v", rep.Deficits)
	}
	if rep.Deficits[0].Kind != KindNeverAcked {
		t.Errorf("Kind = %v, want %v — with its bit stripped the bounced schedule reads as never-acked, which is the mislabel the EverAcked bit corrects; the FINDING is emitted either way, and that is the difference between ackwatch's use of this predicate and Completion()'s",
			rep.Deficits[0].Kind, KindNeverAcked)
	}

	// Flip the majority: three bounced, one survivor. Now the cohort goes.
	majorityZeroed := []Sample{
		mailCheck("architect", 240, 250),
		bouncedSample("pa"),
		bouncedSample("pm-onethird"),
		bouncedSample("pm-pogo"),
	}
	blind := Detect(snap(majorityZeroed), DefaultParams())
	if blind.SkippedNoPeers != len(majorityZeroed) {
		t.Errorf("SkippedNoPeers = %d, want %d — a zeroed majority is the condition that actually blinds this gate, and pinning it is what keeps the blind case from being restated as 'any bounce'",
			blind.SkippedNoPeers, len(majorityZeroed))
	}
	if len(blind.Fleet) != 0 || len(blind.Deficits) != 0 {
		t.Errorf("expected silence from the ratio arms on a zeroed majority, got fleet=%+v deficits=%+v", blind.Fleet, blind.Deficits)
	}
}

// bouncedSample is a schedule that booted, re-registered (counters zeroed), and
// has been delivered to ever since without acking. The ack-history bit is left
// false: this fixture is for measuring the PRE-FIX predicate's behaviour.
func bouncedSample(agent string) Sample {
	s := mailCheck(agent, 0, 250)
	s.UnackedStreak = 250
	return s
}
