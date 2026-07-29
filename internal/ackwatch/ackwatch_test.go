package ackwatch

import (
	"strings"
	"testing"
	"time"
)

// Every test in this file builds its snapshot by hand. Nothing here reads
// ~/.pogo, a live scheduler, or a real events.log — mg-6092, mg-e8e7 and
// mg-5336 are three tickets for that mistake and the ticket for this package
// explicitly forbids a fourth.

var base = time.Date(2026, 7, 29, 1, 52, 0, 0, time.UTC)

const cadence = 10 * time.Minute

// mailCheck builds a mail-check sample with a counter old enough to be
// eligible.
func mailCheck(agent string, completed, delivered int) Sample {
	return Sample{
		Agent:          agent,
		ID:             "mail-check-" + agent,
		Kind:           "mail-check",
		Cadence:        cadence,
		CreatedAt:      base.Add(-72 * time.Hour),
		FiresDelivered: delivered,
		FiresCompleted: completed,
	}
}

// observedFleet reproduces the 2026-07-29 01:52 reading that this package was
// filed on. It is a FIXTURE: the live counters were erased by the 03:01 bounce
// and cannot be re-read, which is exactly why it is written down here.
func observedFleet() []Sample {
	return []Sample{
		mailCheck("architect", 751, 757),
		mailCheck("pa", 753, 757),
		mailCheck("pm-onethird", 751, 757),
		mailCheck("pm-pogo", 270, 757),
	}
}

func snap(samples []Sample) Snapshot {
	return Snapshot{Now: base, Samples: samples}
}

func TestDetectsTheObservedFleet(t *testing.T) {
	rep := Detect(snap(observedFleet()), DefaultParams())

	if rep.Suppressed {
		t.Fatalf("unexpected suppression: %s", rep.SuppressReason)
	}
	if len(rep.Deficits) != 1 {
		t.Fatalf("want exactly 1 deficit, got %d: %+v", len(rep.Deficits), rep.Deficits)
	}
	f := rep.Deficits[0]
	if f.Agent != "pm-pogo" {
		t.Errorf("flagged the wrong agent: %s", f.Agent)
	}
	if f.Kind != KindDeficit {
		t.Errorf("Kind = %q, want %q", f.Kind, KindDeficit)
	}
	if f.Peers != 3 {
		t.Errorf("Peers = %d, want 3", f.Peers)
	}
	if got := f.Rate; got < 0.35 || got > 0.36 {
		t.Errorf("Rate = %.3f, want ~0.357", got)
	}
	if f.PeerMedian < 0.99 {
		t.Errorf("PeerMedian = %.3f, want ~0.992", f.PeerMedian)
	}
	if len(rep.Fleet) != 0 {
		t.Errorf("a single bad agent must not read as a fleet fault: %+v", rep.Fleet)
	}
}

// The healthy peers must never be flagged. 751/757 vs 753/757 is a spread of
// well under one point; a detector that fires on that gets muted within a week.
func TestHealthyPeersAreQuiet(t *testing.T) {
	rep := Detect(snap([]Sample{
		mailCheck("architect", 751, 757),
		mailCheck("pa", 753, 757),
		mailCheck("pm-onethird", 751, 757),
		mailCheck("mayor", 749, 757),
	}), DefaultParams())

	if rep.Actionable() {
		t.Fatalf("healthy fleet produced findings: %s", rep.Render())
	}
	if rep.Eligible != 4 {
		t.Errorf("Eligible = %d, want 4", rep.Eligible)
	}
}

// The floor must do real work, not be arithmetically implied by the gap. With
// peers at 100% and a candidate at 80%, the 20-point gap clears MinGap exactly
// — and the finding is still suppressed, by the floor alone. If MinGap and
// Floor are ever retuned such that 1-MinGap <= Floor, this test fails and says
// why: the floor would have become decorative.
func TestFloorIsTheBindingGateForAHighPerformingCohort(t *testing.T) {
	perfectPeers := func(completed int) []Sample {
		return []Sample{
			mailCheck("architect", 757, 757),
			mailCheck("pa", 757, 757),
			mailCheck("pm-onethird", 757, 757),
			mailCheck("pm-pogo", completed, 757),
		}
	}

	// 606/757 = 80%: gap 20 points, clears MinGap; rate is above the floor.
	rep := Detect(snap(perfectPeers(606)), DefaultParams())
	if rep.Actionable() {
		t.Fatalf("80%% completion is above the floor and must not be a finding "+
			"(if this fails, MinGap and Floor have drifted apart — see Params.Floor):\n%s", rep.Render())
	}

	// 500/757 = 66%: below the floor as well, so it fires.
	rep = Detect(snap(perfectPeers(500)), DefaultParams())
	if len(rep.Deficits) != 1 {
		t.Fatalf("66%% completion against perfect peers must fire, got %d findings", len(rep.Deficits))
	}
}

// The gap test must suppress per-agent findings when EVERYONE is low — that is
// a scheduler or fleet fault, and naming four agents would bury the one fact
// that matters.
func TestFleetWideDeficitIsOneFindingNotFour(t *testing.T) {
	rep := Detect(snap([]Sample{
		mailCheck("architect", 300, 757),
		mailCheck("pa", 310, 757),
		mailCheck("pm-onethird", 295, 757),
		mailCheck("pm-pogo", 305, 757),
	}), DefaultParams())

	if len(rep.Deficits) != 0 {
		t.Errorf("a uniformly-low fleet must produce no per-agent findings, got %d", len(rep.Deficits))
	}
	if len(rep.Fleet) != 1 {
		t.Fatalf("want 1 fleet finding, got %d", len(rep.Fleet))
	}
	if rep.Fleet[0].Schedules != 4 {
		t.Errorf("Schedules = %d, want 4", rep.Fleet[0].Schedules)
	}
	if !rep.Actionable() {
		t.Error("a whole cohort below the floor must be actionable")
	}
	if !strings.Contains(rep.Render(), "SCHEDULER or FLEET fault") {
		t.Error("the fleet render must say who to suspect")
	}
}

// ---- suppression: the counters are reset by an action every agent performs ----

func TestSuppressedAfterSystemWake(t *testing.T) {
	s := snap(observedFleet())
	s.LastDisruption = base.Add(-5 * time.Minute)
	s.DisruptionReason = "system_wake"

	rep := Detect(s, DefaultParams())
	if !rep.Suppressed {
		t.Fatal("a wake 5m ago must suppress the report")
	}
	if rep.Actionable() {
		t.Error("a suppressed report must carry no findings")
	}
	if !strings.Contains(rep.SuppressReason, "system_wake") {
		t.Errorf("SuppressReason should name the event: %q", rep.SuppressReason)
	}
	if !strings.Contains(rep.Render(), "not a clean bill of health") {
		t.Error("a suppressed render must not read as a clean scan")
	}
}

func TestWakeOlderThanSettleDoesNotSuppress(t *testing.T) {
	s := snap(observedFleet())
	s.LastDisruption = base.Add(-DefaultSettleAfter - time.Minute)
	s.DisruptionReason = "system_wake"

	rep := Detect(s, DefaultParams())
	if rep.Suppressed {
		t.Fatal("a wake older than SettleAfter must not suppress forever")
	}
	if len(rep.Deficits) != 1 {
		t.Errorf("want the deficit back, got %d findings", len(rep.Deficits))
	}
}

// The nightly-redeploy storm. Re-registration zeroes the counter (measured
// 2026-07-29 03:03: mail-check-mayor 6/7 before, — after), and mg-42ac made the
// redeploy nightly. A naive absolute floor would flag the whole crew every
// morning; here nothing is even eligible.
func TestFreshlyReRegisteredFleetProducesNothing(t *testing.T) {
	var samples []Sample
	for _, a := range []string{"architect", "pa", "pm-onethird", "pm-pogo"} {
		s := mailCheck(a, 0, 0)
		s.CreatedAt = base.Add(-2 * time.Minute)
		samples = append(samples, s)
	}
	rep := Detect(snap(samples), DefaultParams())

	if rep.Actionable() {
		t.Fatalf("a just-bounced fleet must produce no findings: %s", rep.Render())
	}
	if rep.SkippedFresh != 4 {
		t.Errorf("SkippedFresh = %d, want 4", rep.SkippedFresh)
	}
	if rep.Eligible != 0 {
		t.Errorf("Eligible = %d, want 0", rep.Eligible)
	}
}

// Past the settle window but still short of MinFires: a few hours into the day
// after a bounce. Still not eligible — MinFires is the longer of the two gates
// in wall-clock terms and is what actually carries the post-redeploy window.
func TestSettledButTooFewFiresIsNotJudged(t *testing.T) {
	var samples []Sample
	for i, a := range []string{"architect", "pa", "pm-onethird", "pm-pogo"} {
		s := mailCheck(a, 8-i, 9)
		s.CreatedAt = base.Add(-90 * time.Minute)
		samples = append(samples, s)
	}
	// pm-pogo at 5/9 = 56%, peers at 89%. Would fire if MinFires let it.
	rep := Detect(snap(samples), DefaultParams())
	if rep.Actionable() {
		t.Fatalf("9 fires is not a sample: %s", rep.Render())
	}
	if rep.SkippedFewFires != 4 {
		t.Errorf("SkippedFewFires = %d, want 4", rep.SkippedFewFires)
	}
}

// A mid-morning fleet where three agents bounced and one did not. The
// accumulated straggler is not comparable to the fresh three, and must be left
// UNJUDGED rather than compared against them.
func TestMixedFreshAndAccumulatedCountersAreNotCompared(t *testing.T) {
	fresh := func(agent string, completed, delivered int) Sample {
		s := mailCheck(agent, completed, delivered)
		s.CreatedAt = base.Add(-5 * time.Hour)
		return s
	}
	samples := []Sample{
		fresh("architect", 30, 30),
		fresh("pa", 30, 30),
		fresh("pm-onethird", 29, 30),
		// Never re-registered: three days of counters, and a real deficit.
		mailCheck("pm-pogo", 150, 757),
	}
	rep := Detect(snap(samples), DefaultParams())

	if len(rep.Deficits) != 0 {
		t.Errorf("a 757-fire counter must not be judged against 30-fire peers: %+v", rep.Deficits)
	}
	if rep.SkippedNoPeers != 1 {
		t.Errorf("SkippedNoPeers = %d, want 1 (the straggler is unjudged, not healthy)", rep.SkippedNoPeers)
	}
	if !strings.Contains(rep.Render(), "no comparable peers 1") {
		t.Errorf("the render must state what it could not evaluate:\n%s", rep.Render())
	}
}

// ---- cohort rules ----

func TestDifferentCadencesAreNotPeers(t *testing.T) {
	hourly := mailCheck("pm-pogo", 30, 84)
	hourly.Cadence = time.Hour
	samples := []Sample{
		mailCheck("architect", 751, 757),
		mailCheck("pa", 753, 757),
		mailCheck("pm-onethird", 751, 757),
		hourly,
	}
	rep := Detect(snap(samples), DefaultParams())
	if len(rep.Deficits) != 0 {
		t.Errorf("an hourly schedule has no 10-minute peers: %+v", rep.Deficits)
	}
	if rep.SkippedNoPeers != 1 {
		t.Errorf("SkippedNoPeers = %d, want 1", rep.SkippedNoPeers)
	}
}

func TestDifferentKindsAreNotPeers(t *testing.T) {
	sweep := mailCheck("pm-pogo", 100, 757)
	sweep.Kind = "sweep"
	sweep.ID = "sweep-morning-pm-pogo"
	samples := append(observedFleet()[:3], sweep)
	rep := Detect(snap(samples), DefaultParams())
	if len(rep.Deficits) != 0 {
		t.Errorf("a sweep is not a mail-check peer: %+v", rep.Deficits)
	}
}

func TestTooFewPeersLeavesTheScheduleUnjudged(t *testing.T) {
	rep := Detect(snap([]Sample{
		mailCheck("architect", 751, 757),
		mailCheck("pm-pogo", 270, 757),
	}), DefaultParams())
	if rep.Actionable() {
		t.Fatalf("one neighbour is not a comparison: %s", rep.Render())
	}
	if rep.SkippedNoPeers != 2 {
		t.Errorf("SkippedNoPeers = %d, want 2", rep.SkippedNoPeers)
	}
}

func TestOneShotSchedulesAreNotRated(t *testing.T) {
	one := mailCheck("pm-pogo", 0, 100)
	one.Cadence = 0
	samples := append(observedFleet()[:3], one)
	rep := Detect(snap(samples), DefaultParams())
	if rep.SkippedNotRecurring != 1 {
		t.Errorf("SkippedNotRecurring = %d, want 1", rep.SkippedNotRecurring)
	}
	if rep.Actionable() {
		t.Errorf("a non-recurring entry has no rate: %s", rep.Render())
	}
}

// ---- never-acked ----

// A schedule with hundreds of fires and zero acks, whose peers all ack, IS a
// finding — deliberately going beyond scheduler.CompletionTracked's
// "untracked = unknown", because the cohort supplies the evidence that acking
// is expected here.
func TestNeverAckedInAnAckAwareCohortIsAFinding(t *testing.T) {
	samples := append(observedFleet()[:3], mailCheck("pm-pogo", 0, 757))
	rep := Detect(snap(samples), DefaultParams())

	if len(rep.Deficits) != 1 {
		t.Fatalf("want 1 finding, got %d", len(rep.Deficits))
	}
	if rep.Deficits[0].Kind != KindNeverAcked {
		t.Errorf("Kind = %q, want %q", rep.Deficits[0].Kind, KindNeverAcked)
	}
	if !strings.Contains(rep.Render(), "NEVER ACKED") {
		t.Errorf("render should distinguish never-acked:\n%s", rep.Render())
	}
}

// ...but a cohort where nobody acks proves nothing. Those schedules may simply
// never have been told to. Silence from all of them is unknown, not failure.
func TestNoOneAcksMeansUnknownNotFailure(t *testing.T) {
	rep := Detect(snap([]Sample{
		mailCheck("architect", 0, 757),
		mailCheck("pa", 0, 757),
		mailCheck("pm-onethird", 0, 757),
		mailCheck("pm-pogo", 0, 757),
	}), DefaultParams())

	if len(rep.Deficits) != 0 {
		t.Errorf("an entirely ack-unaware cohort must not be accused: %+v", rep.Deficits)
	}
	if rep.SkippedNoPeers != 4 {
		t.Errorf("SkippedNoPeers = %d, want 4", rep.SkippedNoPeers)
	}
	if len(rep.Fleet) != 0 {
		t.Errorf("nor is it a fleet fault: %+v", rep.Fleet)
	}
}

// ---- report plumbing ----

func TestFingerprintIgnoresRateDrift(t *testing.T) {
	a := Detect(snap(observedFleet()), DefaultParams())
	drifted := observedFleet()
	drifted[3].FiresCompleted = 271
	drifted[3].FiresDelivered = 760
	b := Detect(snap(drifted), DefaultParams())

	if a.Fingerprint() != b.Fingerprint() {
		t.Errorf("a drifting rate must be the same finding: %q vs %q", a.Fingerprint(), b.Fingerprint())
	}
	if a.Fingerprint() == "" {
		t.Error("fingerprint of a non-empty report must not be empty")
	}
}

func TestFingerprintChangesWhenAScheduleJoins(t *testing.T) {
	a := Detect(snap(observedFleet()), DefaultParams())
	more := append(observedFleet(), mailCheck("pm-second", 200, 757))
	b := Detect(snap(more), DefaultParams())
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("a new failing schedule must read as new news")
	}
}

func TestDetectIsDeterministic(t *testing.T) {
	want := Detect(snap(observedFleet()), DefaultParams()).Fingerprint()
	for i := 0; i < 20; i++ {
		if got := Detect(snap(observedFleet()), DefaultParams()).Fingerprint(); got != want {
			t.Fatalf("iteration %d: %q != %q", i, got, want)
		}
	}
}

func TestMailSubjectNamesTheSchedule(t *testing.T) {
	rep := Detect(snap(observedFleet()), DefaultParams())
	subj := rep.MailSubject()
	if !strings.Contains(subj, "mail-check-pm-pogo") {
		t.Errorf("subject should name the schedule: %q", subj)
	}
	if !strings.Contains(subj, "36%") {
		t.Errorf("subject should carry the rate: %q", subj)
	}
}

func TestRenderNamesPeersAndTheSpinnerTrap(t *testing.T) {
	out := Detect(snap(observedFleet()), DefaultParams()).Render()
	for _, want := range []string{
		"COMPLETION DEFICIT",
		"mail-check-pm-pogo",
		"mail-check-architect",    // the peers it was compared against
		"--immediate",             // the nudge mode that actually reaches a spinner
		"measures completed WORK", // why health=healthy proved nothing
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

func TestZeroParamsFallBackToDefaults(t *testing.T) {
	got := Params{}.withDefaults()
	if got != DefaultParams() {
		t.Errorf("withDefaults() = %+v, want %+v", got, DefaultParams())
	}
	// A partially-specified Params keeps its explicit field and defaults the rest.
	got = Params{MinFires: 5}.withDefaults()
	if got.MinFires != 5 {
		t.Errorf("MinFires = %d, want 5 (explicit value must survive)", got.MinFires)
	}
	if got.Floor != DefaultFloor {
		t.Errorf("Floor = %v, want the default %v", got.Floor, DefaultFloor)
	}
}

func TestMedianNotMean(t *testing.T) {
	// Two dead peers and two live ones: the mean would be ~50% and hide the
	// candidate; the median of the four peers is what decides.
	got := medianRate([]Sample{
		mailCheck("a", 100, 100),
		mailCheck("b", 100, 100),
		mailCheck("c", 0, 100),
	})
	if got != 1.0 {
		t.Errorf("medianRate = %v, want 1.0", got)
	}
}

func TestEmptyFleetIsQuiet(t *testing.T) {
	rep := Detect(snap(nil), DefaultParams())
	if rep.Actionable() || rep.Suppressed {
		t.Errorf("an empty snapshot is neither a finding nor a suppression: %+v", rep)
	}
	if !strings.Contains(rep.Render(), "no completion deficit") {
		t.Errorf("unexpected render:\n%s", rep.Render())
	}
}
