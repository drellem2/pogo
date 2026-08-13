package ackwatch

import (
	"strings"
	"testing"
	"time"
)

// The 2026-08-09 total outage (mg-e2a4), as a fixture. Nothing here reads a live
// scheduler or a real events.log — same rule as ackwatch_test.go, same three
// tickets behind it.
//
// The reason this file exists at all is that the detector already FIRED during
// this incident and was useless, so a test asserting "we detected it" would have
// passed throughout. Every assertion below is therefore about one of the two
// things that were actually wrong: whether the ABSOLUTE arm can see a failure
// with no outlier in it, and whether the notice reached a recipient that was not
// itself inside the outage.

// outageAt is 16:12:59Z, the sample that fired mid-outage reporting
// deficit_count: 0, eligible: 9, fleet_count: 1, escalated: false,
// notified: "mayor".
var outageAt = time.Date(2026, 8, 9, 16, 12, 59, 0, time.UTC)

// stalledSample builds one row of the 17:21Z `pogo schedule list` reading. Every
// schedule carried the SAME 27 unacked fires — 27 x 10 minutes = 4.5 hours — and
// that uniformity is the whole point: it is what leaves the peer-relative arm
// with nothing to compare.
func stalledSample(agent string, completed, delivered int) Sample {
	s := mailCheck(agent, completed, delivered)
	s.CreatedAt = outageAt.Add(-72 * time.Hour)
	s.UnackedStreak = 27
	s.LastCompletion = outageAt.Add(-4*time.Hour - 30*time.Minute)
	return s
}

// deadFleet is the measured table, verbatim:
//
//	mail-check-pm-onethird  190/705   ⚠ 27 unacked
//	mail-check-architect      5/46    ⚠ 27 unacked
//	mail-check-doctor         4/44    ⚠ 27 unacked
//	mail-check-pa          1288/1868  ⚠ 27 unacked
//	mail-check-pm-pogo       49/287   ⚠ 27 unacked
//	mail-check-mg-3969        2/29    ⚠ 27 unacked
func deadFleet() []Sample {
	return []Sample{
		stalledSample("pm-onethird", 190, 705),
		stalledSample("architect", 5, 46),
		stalledSample("doctor", 4, 44),
		stalledSample("pa", 1288, 1868),
		stalledSample("pm-pogo", 49, 287),
		stalledSample("mg-3969", 2, 29),
	}
}

// deadFleetAgents is the running roster during the outage. Every one of these
// read `status=running` with `last-activity=just now` throughout, because PTY
// animation is output — that is precisely why the liveness gate does not
// weaken this case.
var deadFleetAgents = []string{
	"architect", "doctor", "mayor", "mg-3969", "pa", "pm-onethird", "pm-pogo",
}

// deadWindow is the events-log measurement over 13:20-17:20Z: 251
// scheduler_fire_delivered against 3 scheduler_fire_completed, spread over the
// seven agents that were up.
func deadWindow() *Recent {
	r := &Recent{
		Window:          4 * time.Hour,
		Delivered:       251,
		Completed:       3,
		Schedules:       7,
		Agents:          deadFleetAgents,
		ByAgent:         map[string]AgentFires{},
		LastCompletedAt: outageAt.Add(-2*time.Hour - 45*time.Minute),
	}
	// 36 deliveries each, one schedule each; the three completions land on `pa`,
	// so every other agent completed nothing at all.
	r.BySchedule = map[string]ScheduleFires{}
	for i, a := range deadFleetAgents {
		f := AgentFires{Delivered: 36, Schedules: 1}
		if i == 4 { // pa
			f.Completed = 3
			f.Delivered = 35
		}
		r.ByAgent[a] = f
		// One schedule per agent, so the per-schedule breakdown the COHORT arm
		// reads is the per-agent one under the mail-check id (mg-c232).
		r.BySchedule[scheduleKey(a, "mail-check-"+a)] = ScheduleFires{
			Delivered: f.Delivered, Completed: f.Completed,
		}
	}
	r.BySchedule[scheduleKey("pa", "mail-check-pa")] = ScheduleFires{
		Delivered: 35, Completed: 3, LastCompletedAt: r.LastCompletedAt,
	}
	return r
}

// upSince marks every named agent as running, and running long enough to be
// judged — up for a day, comfortably past any window. A test that needs the
// too-young case builds its own map.
func upSince(agents []string) map[string]time.Time {
	if agents == nil {
		// nil in, nil out: "the caller did not supply the set" has to stay
		// distinguishable from "the set is empty", because they are different
		// findings — blind versus a fleet that is not there.
		return nil
	}
	out := make(map[string]time.Time, len(agents))
	for _, a := range agents {
		// Long before any fixture's `Now`, so these agents are always old enough
		// to judge. A test that needs the too-young case builds its own map.
		out[a] = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return out
}

func outageSnap(recent *Recent) Snapshot {
	return Snapshot{
		Now: outageAt, Samples: deadFleet(), Recent: recent,
		RunningSince: upSince(deadFleetAgents),
	}
}

// The defining test. It asserts the BLIND SPOT first and the coverage second,
// because the blind spot is the claim that justifies a second arm existing:
//
//   - deficit_count is 0, reproducing the measured event detail exactly. Not an
//     arithmetic bug — a peer-relative test keys on dispersion, and a total
//     outage has no outlier, so the median falls with the members and no gap
//     clears MinGap. The worse the failure, the less it looks like a deficit.
//   - fleet_count is 1, also as measured. The cohort arm DID see it, and was
//     still useless: see the routing tests below for why.
//   - the blackout arm fires, from the absolute rate alone.
func TestBlackout_TheUniformOutageThePeerRelativeArmCannotSee(t *testing.T) {
	rep := Detect(outageSnap(deadWindow()), DefaultParams())

	if rep.Suppressed {
		t.Fatalf("unexpected suppression: %s", rep.SuppressReason)
	}
	if len(rep.Deficits) != 0 {
		t.Fatalf("want deficit_count 0 (the measured value on 2026-08-09 — a uniform "+
			"outage has no outlier for a peer test to find), got %d: %+v",
			len(rep.Deficits), rep.Deficits)
	}
	if len(rep.Fleet) != 1 {
		t.Fatalf("want fleet_count 1 (also measured), got %d", len(rep.Fleet))
	}
	if rep.BlackoutBlind != "" {
		t.Fatalf("absolute arm declined to judge a 251/3 window: %s", rep.BlackoutBlind)
	}
	bo := rep.Blackout
	if bo == nil {
		t.Fatal("no blackout finding on a fleet that completed 3 of 251 fires — " +
			"the absolute arm is the ONLY arm that can see this shape")
	}
	if bo.Delivered != 251 || bo.Completed != 3 {
		t.Errorf("blackout = %d/%d, want 3/251", bo.Completed, bo.Delivered)
	}
	if got := bo.Rate; got < 0.011 || got > 0.013 {
		t.Errorf("rate = %v, want ~0.012", got)
	}
	if bo.Window != "4h0m0s" {
		t.Errorf("window = %q, want the caller's measured window", bo.Window)
	}
	if bo.Schedules != 7 {
		t.Errorf("schedules = %d, want 7", bo.Schedules)
	}
	// The population has to travel with the finding, because the routing decision
	// downstream is "is my recipient in this list".
	if len(bo.RunningJudged) != 7 {
		t.Errorf("running judged = %v, want all 7 running agents", bo.RunningJudged)
	}
	// `pa` acked three times, so it is judged but not silent. Everything else
	// completed nothing at all.
	if len(bo.StalledAgents) != 6 {
		t.Errorf("stalled agents = %v, want the 6 that completed NOTHING", bo.StalledAgents)
	}
	for _, a := range bo.StalledAgents {
		if a == "pa" {
			t.Error("pa completed 3 fires; it is not one of the silent agents")
		}
	}
	if bo.StalledAgents[0] != "architect" {
		t.Errorf("stalled agents should be sorted, got %v", bo.StalledAgents)
	}
}

// The liveness gate, which is the difference between an alarm and a nightly
// false alarm. Measured on this box: between 00:00 and 09:30 on 2026-08-09 the
// scheduler delivered ~30 mail-check fires an hour and completed ZERO, every
// hour, all night — because no crew agent was running (all six spawned at
// 09:35Z). Without the gate this arm mails a person at 4am every night, and an
// alarm that cries wolf nightly is worse than the silence it replaced.
func TestBlackout_AnAbsentFleetIsNotADeadOne(t *testing.T) {
	overnight := deadWindow()

	// Nobody running: the identical counters must produce no finding.
	rep := Detect(Snapshot{
		Now: outageAt, Samples: deadFleet(), Recent: overnight,
		RunningSince: map[string]time.Time{},
	}, DefaultParams())
	if rep.Blackout != nil {
		t.Fatal("an empty fleet produced a blackout: overnight, every night")
	}
	if !strings.Contains(rep.BlackoutBlind, "not RUNNING") {
		t.Errorf("BlackoutBlind = %q, want it to name the liveness gate", rep.BlackoutBlind)
	}

	// Two running agents is not a fleet either — one wedged agent holding several
	// schedules is the peer arm's job, and it reaches a different recipient.
	rep = Detect(Snapshot{
		Now: outageAt, Samples: deadFleet(), Recent: overnight,
		RunningSince: upSince([]string{"architect", "doctor"}),
	}, DefaultParams())
	if rep.Blackout != nil {
		t.Error("two agents were reported as a fleet blackout")
	}

	// Three is, and the rate is then computed over THOSE THREE only — an absolute
	// rate over the running subset, still no comparison of anyone to anyone.
	rep = Detect(Snapshot{
		Now: outageAt, Samples: deadFleet(), Recent: overnight,
		RunningSince: upSince([]string{"architect", "doctor", "mayor"}),
	}, DefaultParams())
	if rep.Blackout == nil {
		t.Fatalf("three running agents completing nothing is a blackout; blind=%q", rep.BlackoutBlind)
	}
	if rep.Blackout.Delivered != 108 {
		t.Errorf("delivered = %d, want the running subset's 108 (3 x 36), not the fleet total",
			rep.Blackout.Delivered)
	}
	if len(rep.Blackout.RunningJudged) != 3 {
		t.Errorf("judged = %v, want only the running three", rep.Blackout.RunningJudged)
	}

	// And an agent that is running but was never delivered to in the window is
	// not evidence of anything either way.
	rep = Detect(Snapshot{
		Now: outageAt, Samples: deadFleet(), Recent: overnight,
		RunningSince: upSince([]string{"architect", "doctor", "someone-with-no-schedule"}),
	}, DefaultParams())
	if rep.Blackout != nil {
		t.Error("an agent with no deliveries was counted toward the fleet floor")
	}
}

// Severity has to reach the part that travels. A person seeing only the subject
// line must learn that the fleet is dead, not that N schedules are below a peer
// median — the fleet arm's own subject during this incident.
func TestBlackout_OwnsTheSubjectLine(t *testing.T) {
	rep := Detect(outageSnap(deadWindow()), DefaultParams())
	subj := rep.MailSubject()
	if !strings.Contains(subj, "FLEET BLACKOUT") {
		t.Errorf("subject %q must lead with the fleet-level fact", subj)
	}
	if !strings.Contains(subj, "251") {
		t.Errorf("subject %q should carry the absolute numbers", subj)
	}

	// And the total case says NONE rather than a percentage that rounds to zero.
	total := deadWindow()
	total.Completed = 0
	total.LastCompletedAt = time.Time{}
	for a, f := range total.ByAgent {
		f.Completed = 0
		total.ByAgent[a] = f
	}
	rep = Detect(outageSnap(total), DefaultParams())
	if subj := rep.MailSubject(); !strings.Contains(subj, "NONE completed") {
		t.Errorf("subject %q should say NONE when nothing completed at all", subj)
	}
	if body := rep.Render(); !strings.Contains(body, "NOT ONE fire completed") {
		t.Error("body should state that nothing completed in the whole window")
	}
}

// The remedy is an artifact of the same kind as the defect, so it is subject to
// that defect. Enumerated here: the ways this arm could ALSO fail to see, or
// could see something that is not there.
func TestBlackout_TheWaysTheAbsoluteArmMustNotFire(t *testing.T) {
	// spread builds a window with `completed` completions across three running
	// agents, each holding one schedule.
	spread := func(delivered, completed int) *Recent {
		r := &Recent{
			Window: 3 * time.Hour, Delivered: delivered, Completed: completed,
			Schedules: 3, Agents: []string{"architect", "doctor", "mayor"},
			ByAgent: map[string]AgentFires{
				"architect": {Delivered: delivered / 3, Schedules: 1, Completed: completed},
				"doctor":    {Delivered: delivered / 3, Schedules: 1},
				"mayor":     {Delivered: delivered - 2*(delivered/3), Schedules: 1},
			},
		}
		return r
	}
	running := []string{"architect", "doctor", "mayor"}

	for _, tc := range []struct {
		name       string
		recent     *Recent
		running    []string
		wantBlind  string // substring; "" means the arm judged
		wantFiring bool
	}{
		{
			// A working fleet, measured absolutely. 48% is what a BUSY fleet
			// reads — an agent whose turns run longer than its cadence cannot
			// score high however perfectly it behaves — and it must not alarm.
			name: "a busy but working fleet is not a blackout", running: running,
			recent:     spread(251, 120),
			wantFiring: false,
		},
		{
			// The sleeping-laptop case. Nothing fires while the host is
			// suspended, so a quiet window is a SMALL window — ineligible, not
			// alarming. This is the arm's whole defence against a napping box,
			// and it is why no separate sleep gate is needed.
			name: "a slept-through window is too small to judge", running: running,
			recent:     spread(6, 0),
			wantBlind:  "under the 24 needed",
			wantFiring: false,
		},
		{
			// THE dangerous one. A window that could not be read has zero
			// completions in it, and a zero read as a measurement is a false
			// blackout that mails a person at 3am. It must arrive as blindness.
			name: "an unreadable window is blind, NOT a measurement of zero", running: running,
			recent: &Recent{Window: 3 * time.Hour, Delivered: 60, Completed: 0, Schedules: 3,
				Err: "open events.log: permission denied"},
			wantBlind:  "could not be measured",
			wantFiring: false,
		},
		{
			// The mirror of it: no measurement offered at all must not read as
			// calm. A caller that forgot to wire RecentFires would otherwise
			// reproduce this whole ticket silently.
			name: "a missing window is blind, NOT calm", running: running,
			recent:     nil,
			wantBlind:  "no window measurement was supplied",
			wantFiring: false,
		},
		{
			// And a caller that forgot the LIVENESS half is blind too, rather
			// than falling back to the whole fleet — the fallback is the nightly
			// false alarm.
			name: "a missing running set is blind, NOT the whole fleet", running: nil,
			recent:     spread(90, 0),
			wantBlind:  "running-agent set was not supplied",
			wantFiring: false,
		},
		{
			// Exactly at the threshold is not below it.
			name: "a rate exactly at the floor does not fire", running: running,
			recent:     spread(99, 11),
			wantFiring: false,
		},
		{
			name: "a rate below the floor fires", running: running,
			recent:     spread(99, 9),
			wantFiring: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := Detect(Snapshot{
				Now: outageAt, Samples: deadFleet(), Recent: tc.recent,
				RunningSince: upSince(tc.running),
			}, DefaultParams())
			if (rep.Blackout != nil) != tc.wantFiring {
				t.Fatalf("blackout fired = %v, want %v (%+v)", rep.Blackout != nil, tc.wantFiring, rep.Blackout)
			}
			if tc.wantBlind == "" {
				if rep.BlackoutBlind != "" {
					t.Errorf("arm reported itself blind unexpectedly: %s", rep.BlackoutBlind)
				}
				return
			}
			if !strings.Contains(rep.BlackoutBlind, tc.wantBlind) {
				t.Errorf("BlackoutBlind = %q, want it to contain %q", rep.BlackoutBlind, tc.wantBlind)
			}
			// A blind arm must be visible in the rendered report too. "The peer
			// arm found nothing and the absolute arm never ran" is precisely the
			// reading a dead fleet produced, and it must not be indistinguishable
			// from a clean bill of health.
			if body := rep.Render(); !strings.Contains(body, "Fleet-blackout arm did not judge") {
				t.Errorf("render() hides the blind arm:\n%s", body)
			}
		})
	}
}

// A suppression still wins: after a wake or a restart the traffic describes a
// regime that has just ended. This is the one gate the absolute arm keeps, and
// the cost is bounded at SettleAfter rather than at MinFires' three-plus hours.
func TestBlackout_IsSuppressedByAFreshDisruptionLikeEverythingElse(t *testing.T) {
	s := outageSnap(deadWindow())
	s.LastDisruption = outageAt.Add(-2 * time.Minute)
	s.DisruptionReason = "system_wake"
	rep := Detect(s, DefaultParams())
	if !rep.Suppressed {
		t.Fatal("a 2-minute-old wake must suppress the whole report")
	}
	if rep.Blackout != nil {
		t.Error("the absolute arm must not fire through a suppression")
	}

	// ...and it comes back as soon as the settle window has passed, which is what
	// distinguishes this from the MinFires blindness the arm exists to escape.
	s.LastDisruption = outageAt.Add(-31 * time.Minute)
	if rep := Detect(s, DefaultParams()); rep.Blackout == nil {
		t.Error("31 minutes after a wake the arm must judge again")
	}
}

// The counter-reset case, which is the reason this arm reads events rather than
// the counters it already has in hand. A nightly redeploy zeroes every counter,
// MinFires then holds every schedule ineligible for over three hours, and an
// outage inside that window is invisible to anything reading the table.
func TestBlackout_SeesAnOutageThatStartsRightAfterTheNightlyRedeploy(t *testing.T) {
	// Every schedule re-registered 40 minutes ago: past SettleAfter, nowhere near
	// MinFires. The peer arm cannot judge a single one of them.
	fresh := make([]Sample, 0, 6)
	for _, s := range deadFleet() {
		s.CreatedAt = outageAt.Add(-40 * time.Minute)
		s.FiresDelivered = 4
		s.FiresCompleted = 0
		s.LastCompletion = time.Time{}
		fresh = append(fresh, s)
	}
	rep := Detect(Snapshot{
		Now:          outageAt,
		Samples:      fresh,
		Recent:       deadWindow(),
		RunningSince: upSince(deadFleetAgents),
	}, DefaultParams())

	if rep.Eligible != 0 {
		t.Fatalf("fixture is wrong: want 0 eligible for the counter-based arms, got %d", rep.Eligible)
	}
	if len(rep.Deficits) != 0 || len(rep.Fleet) != 0 {
		t.Fatalf("fixture is wrong: the counter arms must be blind here, got %d/%d",
			len(rep.Deficits), len(rep.Fleet))
	}
	if rep.Blackout == nil {
		t.Fatal("a dead fleet 40 minutes after a redeploy must still be reported — " +
			"this is the MinFires blind spot the events window exists to escape")
	}
}

// The measured false positive, pinned. On 2026-08-09 the six crew agents were
// spawned at 09:35Z. At 10:00Z the 3-hour window still reached back to 07:00Z —
// two and a half hours in which the scheduler delivered to their schedules while
// none of them existed — and the arm read 6 completions against ~90 deliveries,
// 6.7%, a blackout on a fleet that had just come up healthy.
//
// So an agent is judged only once it has been RUNNING for the whole window. This
// is the second half of the liveness gate and it is not optional: without it the
// arm produces a false alarm after every spawn, and a redeploy spawns the whole
// crew at once (mg-42ac made that nightly).
func TestBlackout_AnAgentYoungerThanTheWindowIsNotJudged(t *testing.T) {
	w := deadWindow()
	w.Window = 3 * time.Hour

	// Every agent came up 40 minutes ago: nothing before that is theirs.
	young := map[string]time.Time{}
	for _, a := range deadFleetAgents {
		young[a] = outageAt.Add(-40 * time.Minute)
	}
	rep := Detect(Snapshot{
		Now: outageAt, Samples: deadFleet(), Recent: w, RunningSince: young,
	}, DefaultParams())
	if rep.Blackout != nil {
		t.Fatal("a fleet that came up 40 minutes ago was judged over a 3-hour window: " +
			"that is the 10:00Z false positive, reproduced")
	}
	if !strings.Contains(rep.BlackoutBlind, "too young to judge") {
		t.Errorf("BlackoutBlind = %q, want it to name the uptime gate", rep.BlackoutBlind)
	}

	// A zero start time is "I do not know when this began", which is not evidence
	// of having been up. Treated as too young rather than as eligible.
	unknown := map[string]time.Time{}
	for _, a := range deadFleetAgents {
		unknown[a] = time.Time{}
	}
	if rep := Detect(Snapshot{
		Now: outageAt, Samples: deadFleet(), Recent: w, RunningSince: unknown,
	}, DefaultParams()); rep.Blackout != nil {
		t.Error("agents with unknown start times were judged as though they had been up")
	}

	// Mixed: three old enough, four not. Three is the floor, so it fires — and the
	// rate is computed over those three alone.
	mixed := map[string]time.Time{}
	for i, a := range deadFleetAgents {
		if i < 3 {
			mixed[a] = outageAt.Add(-24 * time.Hour)
		} else {
			mixed[a] = outageAt.Add(-10 * time.Minute)
		}
	}
	rep = Detect(Snapshot{
		Now: outageAt, Samples: deadFleet(), Recent: w, RunningSince: mixed,
	}, DefaultParams())
	if rep.Blackout == nil {
		t.Fatalf("three long-running agents completing nothing is a blackout; blind=%q",
			rep.BlackoutBlind)
	}
	if len(rep.Blackout.RunningJudged) != 3 {
		t.Errorf("judged = %v, want only the three that were up for the window",
			rep.Blackout.RunningJudged)
	}
}
