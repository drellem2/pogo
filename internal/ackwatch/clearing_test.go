package ackwatch

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// mg-c232: an alarm that cannot clear after the fault clears.
//
// The cohort arm triggered on the LIFETIME completion median, which is monotone
// in past damage. On 2026-08-10 two outages that had already ENDED — the crew
// stopped 01:56-12:40Z, a network loss 14:24-17:15Z — put ~80 dead fires into
// every 10-minute schedule, and "1 whole cohort below the completion floor"
// stayed escalated to the mayor for 61 hours while the fleet was demonstrably
// healthy.
//
// The assertions that matter here are the NEGATIVE ones. A test that only shows
// the arm firing during an outage would have passed on every day of that
// escalation, because the arm was firing the whole time — it was the not-firing
// afterwards that was missing. So every fixture below is measured twice: once
// during the fault, once after it, on counters that are IDENTICAL across the
// pair.

// outageDamagedFleet is the 2026-08-10 shape. Each schedule carries ~80 dead
// fires from the two ended outages against roughly a day of registration, which
// is what put the cohort median at 26%.
func outageDamagedFleet() []Sample {
	agents := []string{"architect", "mayor", "pa", "pm-onethird", "pm-pogo"}
	out := make([]Sample, 0, len(agents))
	for _, a := range agents {
		s := mailCheck(a, 38, 144) // 26%: 144 fires in a day, 80 of them dead
		s.CreatedAt = base.Add(-24 * time.Hour)
		out = append(out, s)
	}
	return out
}

func damagedAgents() []string {
	out := make([]string, 0)
	for _, s := range outageDamagedFleet() {
		out = append(out, s.Agent)
	}
	return out
}

// TestCohort_AnEndedOutageStopsFiring is requirement 2 of mg-c232, verified by
// construction: induce the gap, restore, and show the alarm goes quiet WITHOUT a
// counter reset.
//
// The counter identity is asserted rather than assumed. Re-registering a
// schedule with the same --id zeroes its counters (scheduler.Add), and "fixing"
// this by bouncing the schedules would hide the signal rather than correct it —
// so the second half of this test would pass trivially if the samples differed,
// and the check below is what stops that from being how it passes.
func TestCohort_AnEndedOutageStopsFiring(t *testing.T) {
	agents := damagedAgents()

	during := outageDamagedFleet()
	rep := Detect(windowedSnap(during, darkWindow(agents)), DefaultParams())
	if len(rep.Fleet) != 1 {
		t.Fatalf("a cohort completing nothing over the window must fire: fleet=%d blind=%q notMeasured=%v",
			len(rep.Fleet), rep.FleetBlind, rep.FleetNotMeasured)
	}

	// THE SAME COUNTERS. Same FiresDelivered, same FiresCompleted, same
	// CreatedAt — nothing was re-registered, nothing was zeroed. Only the recent
	// window changed, because the outage ended.
	after := outageDamagedFleet()
	for i := range after {
		if after[i] != during[i] {
			t.Fatalf("fixture drift at %d: the recovery half must run on identical counters, "+
				"or it is measuring a reset rather than a recovery\nduring: %+v\nafter:  %+v",
				i, during[i], after[i])
		}
	}

	rep = Detect(windowedSnap(after, healthyWindow(agents, base.Add(-3*time.Minute))), DefaultParams())
	if len(rep.Fleet) != 0 {
		t.Fatalf("the cohort is completing 17 of 18 fires per schedule right now and the alarm "+
			"is still up — this is mg-c232, and the lifetime median is still the trigger: %+v",
			rep.Fleet)
	}
	if rep.Actionable() {
		t.Fatalf("nothing should be actionable after recovery:\n%s", rep.Render())
	}
	// And the lifetime median really is still terrible, so the test above is not
	// passing because the fixture recovered on paper too.
	if m := medianRate(after); m >= DefaultFloor {
		t.Fatalf("lifetime median %v is above the floor, so this fixture no longer reproduces "+
			"the state where the OLD trigger would still be firing", m)
	}
}

// TestCohort_LifetimeAloneNoLongerFires is the other half of the pair: with no
// window offered, a cohort whose lifetime median is far below the old floor
// produces no finding at all, and says WHY.
//
// It fails closed rather than falling back to the median, because a fallback
// would reinstate the removed trigger on exactly the days the events log cannot
// be read.
func TestCohort_LifetimeAloneNoLongerFires(t *testing.T) {
	rep := Detect(snap(outageDamagedFleet()), DefaultParams())

	if len(rep.Fleet) != 0 {
		t.Fatalf("a lifetime median of 26%% must not raise a cohort finding on its own: %+v", rep.Fleet)
	}
	if rep.FleetBlind == "" {
		t.Fatal("the cohort arm declined to judge and did not say so — blind and calm must " +
			"never be the same observation")
	}
	if body := rep.Render(); !strings.Contains(body, "Cohort arm did not judge") {
		t.Errorf("the blind cohort arm must be visible in the body:\n%s", body)
	}
}

// TestCohort_AnAbsentFleetIsNotDark. "Fires delivered, nothing completed" is
// also what a fleet that is not RUNNING looks like — every night on this box
// between midnight and 09:30. The liveness gate is the blackout arm's, shared
// through windowView so the two arms cannot disagree about the same minutes.
func TestCohort_AnAbsentFleetIsNotDark(t *testing.T) {
	agents := damagedAgents()
	s := windowedSnap(outageDamagedFleet(), darkWindow(agents))
	s.RunningSince = map[string]time.Time{} // nobody is up

	rep := Detect(s, DefaultParams())
	if len(rep.Fleet) != 0 {
		t.Fatalf("a fleet that is not running is not a dark cohort: %+v", rep.Fleet)
	}
	if len(rep.FleetNotMeasured) != 1 {
		t.Fatalf("the cohort was not judged and must say so, got %v", rep.FleetNotMeasured)
	}

	// And the same again with the crew up, but only for the last 40 minutes: a
	// spawn inside the window cannot speak for the whole of it.
	s.RunningSince = map[string]time.Time{}
	for _, a := range agents {
		s.RunningSince[a] = base.Add(-40 * time.Minute)
	}
	if rep := Detect(s, DefaultParams()); len(rep.Fleet) != 0 {
		t.Fatalf("agents up for 40 minutes cannot speak for a 3-hour window: %+v", rep.Fleet)
	}
}

// TestCohort_DarkCohortInsideAWorkingFleet is what this arm still adds over the
// blackout arm. Blackout aggregates every schedule of every running agent, so
// one cohort going dark is averaged away by the rest; a cohort is judged on its
// own traffic.
func TestCohort_DarkCohortInsideAWorkingFleet(t *testing.T) {
	agents := []string{"architect", "mayor", "pa", "pm-onethird"}
	sweepCadence := time.Hour

	var samples []Sample
	recent := &Recent{
		Window:          DefaultBlackoutWindow,
		ByAgent:         map[string]AgentFires{},
		BySchedule:      map[string]ScheduleFires{},
		LastCompletedAt: base.Add(-2 * time.Minute),
	}
	for _, a := range agents {
		// A healthy mail-check…
		mc := mailCheck(a, 700, 720)
		samples = append(samples, mc)
		// …and a sweep on a different cadence that has stopped completing. Its
		// lifetime counters are healthy — the fault started an hour ago — so
		// nothing but the window can see this.
		sw := Sample{
			Agent: a, ID: "sweep-" + a, Kind: "sweep", Cadence: sweepCadence,
			CreatedAt: base.Add(-72 * time.Hour), FiresDelivered: 70, FiresCompleted: 68,
			EverAcked: true,
		}
		samples = append(samples, sw)

		recent.Delivered += 18 + 3
		recent.Completed += 17
		recent.Schedules += 2
		recent.Agents = append(recent.Agents, a)
		recent.ByAgent[a] = AgentFires{Delivered: 21, Completed: 17, Schedules: 2}
		recent.BySchedule[scheduleKey(a, mc.ID)] = ScheduleFires{
			Delivered: 18, Completed: 17, LastCompletedAt: recent.LastCompletedAt}
		recent.BySchedule[scheduleKey(a, sw.ID)] = ScheduleFires{Delivered: 3}
	}

	rep := Detect(Snapshot{
		Now: base, Samples: samples, Recent: recent, RunningSince: upSince(agents),
	}, DefaultParams())

	// The fleet aggregate is 68 of 84 — nowhere near a blackout.
	if rep.Blackout != nil {
		t.Fatalf("the fleet as a whole is completing 81%% of its fires and must not read as "+
			"a blackout: %+v", rep.Blackout)
	}
	if rep.BlackoutBlind != "" {
		t.Fatalf("the blackout arm should have judged and found nothing: %s", rep.BlackoutBlind)
	}
	// 12 sweep fires in the window, under BlackoutMinDeliveries, so this cohort
	// is reported as NOT MEASURED rather than as dark or as healthy. That is the
	// honest answer for a cadence the window is too short to judge, and it is
	// stated so nobody reads the quiet as a clean bill of health.
	if len(rep.Fleet) != 0 {
		t.Fatalf("12 fires is under the deliveries floor and must not produce a finding: %+v", rep.Fleet)
	}
	var sweepNote string
	for _, why := range rep.FleetNotMeasured {
		if strings.HasPrefix(why, "sweep/") {
			sweepNote = why
		}
	}
	if sweepNote == "" {
		t.Fatalf("the sweep cohort was neither judged nor reported as unmeasurable: %v", rep.FleetNotMeasured)
	}
	if !strings.Contains(rep.Render(), "Cohort not measured") {
		t.Errorf("an unmeasured cohort must be visible in the body:\n%s", rep.Render())
	}
}

// ---- the peer arm's one-way recency veto ----

// TestPeerArm_RecoveryRetiresALifetimeFinding. A schedule that was dark this
// morning sits below its peers for as long as the lifetime denominator takes to
// dilute the damage — the same cannot-clear defect at a smaller amplitude. The
// window retires it.
func TestPeerArm_RecoveryRetiresALifetimeFinding(t *testing.T) {
	agents := []string{"architect", "pa", "pm-onethird", "pm-pogo"}
	samples := []Sample{
		mailCheck("architect", 140, 144),
		mailCheck("pa", 141, 144),
		mailCheck("pm-onethird", 139, 144),
		mailCheck("pm-pogo", 50, 144), // was down for ten hours this morning
	}

	// Nothing offered: the lifetime rule stands, as it did before mg-c232.
	rep := Detect(snap(samples), DefaultParams())
	if len(rep.Deficits) != 1 {
		t.Fatalf("control: the lifetime rule must raise this, got %d", len(rep.Deficits))
	}
	if rep.RetiredRecovered != 0 {
		t.Errorf("nothing can be retired with no window: %d", rep.RetiredRecovered)
	}

	// The same counters, and a window in which pm-pogo is completing its fires.
	rep = Detect(windowedSnap(samples, healthyWindow(agents, base.Add(-time.Minute))), DefaultParams())
	if len(rep.Deficits) != 0 {
		t.Fatalf("pm-pogo is completing 17 of 18 fires right now; its lifetime gap is this "+
			"morning's outage: %+v", rep.Deficits)
	}
	if rep.RetiredRecovered != 1 {
		t.Errorf("RetiredRecovered = %d, want 1 — a retired finding must be counted, or a "+
			"newly quiet report cannot be told from a detector that stopped looking",
			rep.RetiredRecovered)
	}
	if body := rep.Render(); !strings.Contains(body, "are completing fires again in the recent window") {
		t.Errorf("the retirement must be visible in the body:\n%s", body)
	}
}

// TestPeerArm_TheVetoCannotAcquitADeafAgent is the direction that matters. The
// 2026-07-29 founding case — pm-pogo broken from its first fire — is still
// broken in any window, so the veto must not reach it. If this ever fails, the
// recency gate has become a mute button.
func TestPeerArm_TheVetoCannotAcquitADeafAgent(t *testing.T) {
	agents := []string{"architect", "pa", "pm-onethird", "pm-pogo"}
	w := healthyWindow(agents, base.Add(-time.Minute))
	// pm-pogo is deaf in the window too — that is what "always broken" means.
	w.Completed -= 17
	w.ByAgent["pm-pogo"] = AgentFires{Delivered: 18, Schedules: 1}
	w.BySchedule[scheduleKey("pm-pogo", "mail-check-pm-pogo")] = ScheduleFires{Delivered: 18}

	rep := Detect(windowedSnap(observedFleet(), w), DefaultParams())
	if len(rep.Deficits) != 1 {
		t.Fatalf("the founding case must still fire, got %d (retired=%d)",
			len(rep.Deficits), rep.RetiredRecovered)
	}
	if rep.Deficits[0].Agent != "pm-pogo" {
		t.Errorf("flagged %s, want pm-pogo", rep.Deficits[0].Agent)
	}
}

// TestPeerArm_TheVetoIsOneWay. Feeding the window a schedule that looks bad in
// it but fine over its lifetime must change nothing: the window may retire a
// finding and may never raise one. That asymmetry is what makes it safe to
// consult a statistic here that is too noisy to trigger on — a healthy agent in
// a long turn legitimately reads 0 completions over three hours.
func TestPeerArm_TheVetoIsOneWay(t *testing.T) {
	agents := []string{"architect", "pa", "pm-onethird", "mayor"}
	healthy := []Sample{
		mailCheck("architect", 751, 757),
		mailCheck("pa", 753, 757),
		mailCheck("pm-onethird", 751, 757),
		mailCheck("mayor", 749, 757),
	}
	// Every one of them mid-turn: nothing completed in the window at all.
	rep := Detect(windowedSnap(healthy, darkWindow(agents)), DefaultParams())
	if len(rep.Deficits) != 0 {
		t.Fatalf("a dark window must not manufacture a per-schedule finding: %+v", rep.Deficits)
	}
	// The cohort arm DOES fire here, and that is correct and separate: a whole
	// cohort completing nothing for three hours is the fleet-level claim, made by
	// the arm whose threshold was swept for exactly this regime.
	if len(rep.Fleet) != 1 {
		t.Errorf("the cohort arm judges the same window and should fire: %+v", rep.Fleet)
	}
}

// TestPeerArm_ASmallWindowDoesNotAcquit. Below RecoveryMinFires a window is not
// evidence of recovery, so the lifetime finding stands. This is the floor that
// stops two lucky acks from clearing a real deficit.
func TestPeerArm_ASmallWindowDoesNotAcquit(t *testing.T) {
	agents := []string{"architect", "pa", "pm-onethird", "pm-pogo"}
	w := healthyWindow(agents, base.Add(-time.Minute))
	w.BySchedule[scheduleKey("pm-pogo", "mail-check-pm-pogo")] = ScheduleFires{
		Delivered: DefaultRecoveryMinFires - 1, Completed: DefaultRecoveryMinFires - 1,
		LastCompletedAt: base.Add(-time.Minute),
	}

	rep := Detect(windowedSnap(observedFleet(), w), DefaultParams())
	if len(rep.Deficits) != 1 {
		t.Fatalf("%d fires is not a recovery, so the finding must stand; got %d deficits (retired=%d)",
			DefaultRecoveryMinFires-1, len(rep.Deficits), rep.RetiredRecovered)
	}
}

// ---- the watcher: an alarm that clears must also release its escalation ----

// TestWatcher_RecoveryDropsTheEscalationClock. The 61-hour escalation is what
// this ticket was filed on, so the clock behaviour is asserted end to end: a
// cohort finding that ages past EscalateAfter reaches the escalation box, and
// once the window recovers the clock is dropped — a later, unrelated outage
// starts a fresh 24 hours rather than escalating on the first sample.
// The fixture is a dark mail-check cohort inside a fleet that is otherwise
// working, so the AGE clock is what is under test: a blackout escalates on its
// first sample by design (mg-e2a4) and would mask every assertion here.
func TestWatcher_RecoveryDropsTheEscalationClock(t *testing.T) {
	agents := damagedAgents()

	var mu sync.Mutex
	window := darkMailChecksInAWorkingFleet(agents, time.Time{})
	src := func(now time.Time) (Snapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		return Snapshot{
			Now: now, Samples: append(outageDamagedFleet(), healthySweeps(agents)...),
			Recent: window, RunningSince: upSince(agents),
		}, nil
	}

	w, mail, ev := newTestWatcher(t, Options{
		Source: src, Interval: time.Minute, RenotifyAfter: time.Minute,
		EscalateAfter: 24 * time.Hour,
	})

	start := base
	w.Check(start)
	if mail.count() == 0 {
		t.Fatal("a dark cohort must mail")
	}
	for _, to := range mail.recipients() {
		if to == DefaultEscalateTo {
			t.Fatal("a fresh finding must not escalate on age")
		}
	}

	// 25 hours of the same outage: now it escalates.
	w.Check(start.Add(25 * time.Hour))
	escalated := false
	for _, to := range mail.recipients() {
		if to == DefaultEscalateTo {
			escalated = true
		}
	}
	if !escalated {
		t.Fatalf("a finding outstanding for 25h must reach %s: %v", DefaultEscalateTo, mail.recipients())
	}

	// The outage ends. Same counters throughout — the fleet recovered, nobody
	// re-registered anything.
	mu.Lock()
	window = workingFleet(agents, start.Add(26*time.Hour))
	mu.Unlock()
	before := mail.count()
	w.Check(start.Add(26 * time.Hour))
	if mail.count() != before {
		t.Fatalf("a recovered fleet must not mail: %+v", mail.recipients()[before:])
	}
	if !hasEvent(ev, "ack_watch_clear") {
		t.Errorf("a clear sample must be recorded: %v", ev.types())
	}

	// A NEW outage a day later starts its own 24 hours. If the clock survived
	// the clear, this sample would escalate immediately.
	mu.Lock()
	window = darkMailChecksInAWorkingFleet(agents, start.Add(26*time.Hour))
	mu.Unlock()
	before = mail.count()
	w.Check(start.Add(50 * time.Hour))
	if mail.count() == before {
		t.Fatal("a new outage must mail")
	}
	for _, to := range mail.recipients()[before:] {
		if to == DefaultEscalateTo {
			t.Fatal("the escalation clock survived the all-clear, so a fresh finding " +
				"inherits the age of one the fleet already fixed")
		}
	}
}

// healthySweeps is a second cohort on a different cadence, so the fleet has
// traffic the mail-check cohort's darkness cannot account for. Without it a dark
// mail-check cohort IS the fleet, and the blackout arm — which escalates on its
// first sample by design — fires alongside and hides the age clock.
func healthySweeps(agents []string) []Sample {
	out := make([]Sample, 0, len(agents))
	for _, a := range agents {
		out = append(out, Sample{
			Agent: a, ID: "sweep-" + a, Kind: "sweep", Cadence: 30 * time.Minute,
			CreatedAt:      base.Add(-72 * time.Hour),
			FiresDelivered: 144, FiresCompleted: 143, EverAcked: true,
		})
	}
	return out
}

// workingFleet: both cohorts completing their fires.
func workingFleet(agents []string, at time.Time) *Recent {
	return twoCohortWindow(agents, at, 17)
}

// darkMailChecksInAWorkingFleet: the mail-check cohort completes nothing while
// the sweeps keep working, so the fleet aggregate (30 of 120) stays well clear
// of the blackout threshold and only the cohort arm can see the fault.
func darkMailChecksInAWorkingFleet(agents []string, at time.Time) *Recent {
	return twoCohortWindow(agents, at, 0)
}

func twoCohortWindow(agents []string, at time.Time, mailCompleted int) *Recent {
	r := &Recent{
		Window: DefaultBlackoutWindow, ByAgent: map[string]AgentFires{},
		BySchedule: map[string]ScheduleFires{}, LastCompletedAt: at,
	}
	for _, a := range agents {
		// 18 mail-check fires (10m over 3h) and 6 sweep fires (30m over 3h).
		r.Delivered += 24
		r.Completed += mailCompleted + 6
		r.Schedules += 2
		r.Agents = append(r.Agents, a)
		r.ByAgent[a] = AgentFires{Delivered: 24, Completed: mailCompleted + 6, Schedules: 2}
		mc := ScheduleFires{Delivered: 18, Completed: mailCompleted}
		if mailCompleted > 0 {
			mc.LastCompletedAt = at
		}
		r.BySchedule[scheduleKey(a, "mail-check-"+a)] = mc
		r.BySchedule[scheduleKey(a, "sweep-"+a)] = ScheduleFires{
			Delivered: 6, Completed: 6, LastCompletedAt: at}
	}
	return r
}

func hasEvent(ev *eventRec, want string) bool {
	for _, got := range ev.types() {
		if got == want {
			return true
		}
	}
	return false
}
