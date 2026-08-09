package ackwatch

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The routing half of mg-e2a4. These are the tests the incident asked for by
// name: the fleet arm FIRED on 2026-08-09 at 16:12:59 and was still useless, so
// an assertion that the detector noticed would have passed throughout the
// outage. What has to be asserted instead is that a recipient OUTSIDE the
// stalled population was written to, on the first sample, with no timer in the
// way.

// deadFleetSource yields the measured outage: the uniform counter table plus the
// 251/3 events window. Nothing here reads live state.
func deadFleetSource() SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		return Snapshot{
			Now: now, Samples: deadFleet(), Recent: deadWindow(),
			RunningSince: upSince(deadFleetAgents),
		}, nil
	}
}

// findEvent returns the first event of the given type.
func findEvent(t *testing.T, ev *eventRec, typ string) map[string]any {
	t.Helper()
	ev.mu.Lock()
	defer ev.mu.Unlock()
	for _, e := range ev.evs {
		if e.EventType == typ {
			return e.Details
		}
	}
	t.Fatalf("no %s event; got %v", typ, ev.types())
	return nil
}

// THE positive control. Not "did we detect it" — the detector detected it during
// the whole incident. The property is that something reachable from outside the
// outage was written to, on the FIRST sample, while `escalate_after` still sat at
// its 24-hour default.
func TestBlackout_WritesToARecipientOutsideTheStalledFleetOnTheFirstSample(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{
		Source:        deadFleetSource(),
		NotifyTo:      "mayor",
		EscalateTo:    "human",
		EscalateAfter: 24 * time.Hour, // the default that produced escalated:false
	})
	w.Check(outageAt)

	got := mail.recipients()
	if len(got) != 2 {
		t.Fatalf("recipients = %v, want both the coordinator and the escalation box", got)
	}

	// The load-bearing assertion, stated as the property rather than as a name:
	// at least one recipient must not be a member of the population the finding
	// is about. "Also mail human" is not the fix — a recipient outside the outage
	// is, and this is what distinguishes them.
	rep := Detect(outageSnap(deadWindow()), DefaultParams())
	if rep.Blackout == nil {
		t.Fatal("fixture no longer produces a blackout")
	}
	var outside []string
	for _, to := range got {
		if !inPopulation(to, rep.Blackout.StalledAgents) {
			outside = append(outside, to)
		}
	}
	if len(outside) == 0 {
		t.Fatalf("every recipient %v is inside the stalled population %v — "+
			"a detector inside the job cannot report the job not running",
			got, rep.Blackout.StalledAgents)
	}

	d := findEvent(t, ev, "ack_watch_fired")
	if d["escalated"] != true {
		t.Error("escalated must be true on the FIRST blackout sample; " +
			"the measured incident recorded escalated=false for 4.5 hours")
	}
	if d["blackout"] != true {
		t.Error("the event must be greppable as a blackout")
	}
	if d["escalate_to"] != "human" {
		t.Errorf("escalate_to = %v, want the resolved escalation box", d["escalate_to"])
	}
	// The measured detail that made 16:12:59 look like a delivered alarm was a
	// bare `notified: "mayor"`. Whether the recipient was inside the outage is now
	// recorded either way.
	if d["notify_to_stalled"] != true {
		t.Error("mayor IS one of the stalled agents here; the event must say so")
	}
	if d["escalate_to_stalled"] != false {
		t.Error("human is not in the population; the event must say so")
	}

	body := mail.sent[0].Body
	if !strings.Contains(body, "ESCALATED IMMEDIATELY, NOT ON A TIMER") {
		t.Errorf("body must say why it escalated without waiting:\n%s", body)
	}
	if !strings.Contains(body, "CONFIRMED: mayor is one of the stalled agents") {
		t.Errorf("body must name the coordinator as a patient:\n%s", body)
	}
	if !strings.Contains(body, "FLEET BLACKOUT") {
		t.Error("body must carry the finding")
	}
}

// A negative EscalateAfter is the documented way to turn AGE-based escalation
// off. It must not turn this off: "one agent lags its peers" and "nothing has
// completed a turn in hours" do not share an escalation clock.
func TestBlackout_EscalatesEvenWithAgeEscalationDisabled(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{
		Source:        deadFleetSource(),
		NotifyTo:      "mayor",
		EscalateTo:    "human",
		EscalateAfter: -1,
	})
	w.Check(outageAt)

	if got := mail.recipients(); len(got) != 2 || got[1] != "human" {
		t.Fatalf("recipients = %v, want the escalation box despite escalate_after<0", got)
	}
	if d := findEvent(t, ev, "ack_watch_fired"); d["escalated"] != true {
		t.Error("escalated must be true")
	}
}

// The renotify defect, measured. A 4.5-hour outage inside a 6-hour renotify
// window produced ONE notice for an arbitrarily severe event — and a second
// identical outage starting 30 minutes later would have been suppressed
// entirely.
func TestBlackout_RenotifiesInsideTheOldSixHourShadow(t *testing.T) {
	w, mail, _ := newTestWatcher(t, Options{
		Source:        deadFleetSource(),
		NotifyTo:      "mayor",
		EscalateTo:    "human",
		Interval:      30 * time.Minute,
		RenotifyAfter: 6 * time.Hour,
		// BlackoutRenotifyAfter left at zero: the DEFAULT has to be the thing
		// that fixes this, not a value a test supplies.
	})

	// 12:50Z to 17:20Z, sampled on the watcher's own 30-minute interval.
	start := time.Date(2026, 8, 9, 12, 50, 0, 0, time.UTC)
	for at := start; !at.After(start.Add(4*time.Hour + 30*time.Minute)); at = at.Add(30 * time.Minute) {
		w.Check(at)
	}

	toEscalation := 0
	for _, to := range mail.recipients() {
		if to == "human" {
			toEscalation++
		}
	}
	// Ten samples in 4.5 hours; under the 6h window exactly one of them mails.
	if toEscalation < 8 {
		t.Fatalf("the escalation box heard %d time(s) across a 4.5-hour total outage; "+
			"a 6-hour renotify window swallows the whole incident in one notice", toEscalation)
	}

	// The ORDINARY window is untouched. A standing per-schedule deficit must not
	// start mailing every half hour just because this arm exists.
	w2, mail2, _ := newTestWatcher(t, Options{
		Source:        fixtureSource(observedFleet()),
		Interval:      30 * time.Minute,
		RenotifyAfter: 6 * time.Hour,
	})
	for at := base; !at.After(base.Add(4*time.Hour + 30*time.Minute)); at = at.Add(30 * time.Minute) {
		w2.Check(at)
	}
	if mail2.count() != 1 {
		t.Errorf("a peer-relative deficit mailed %d times in 4.5 hours, want 1 — "+
			"the blackout window must not leak into the ordinary one", mail2.count())
	}
}

// The recipient-of-last-resort check. If the escalation box is itself an agent in
// the outage, the alarm has nowhere outside to go — and that must be REPORTED
// rather than quietly mailed, because it is the same defect one level in.
func TestBlackout_ReportsAnEscalationBoxThatIsItselfInsideTheOutage(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{
		Source:     deadFleetSource(),
		NotifyTo:   "mayor",
		EscalateTo: "architect", // a crew agent, and one of the stalled ones
	})
	w.Check(outageAt)

	d := findEvent(t, ev, "ack_watch_fired")
	if d["escalate_to_stalled"] != true {
		t.Fatal("an escalation box inside the stalled population must be recorded as such")
	}
	if body := mail.sent[0].Body; !strings.Contains(body, "has no\nrecipient outside the outage") {
		t.Errorf("body must say the escalation has no recipient outside the outage:\n%s", body)
	}
}

// The worst state, which the old code recorded only as a `mail_error_*` key
// buried in the details of an event named "fired". A blackout notice that
// reached nobody gets its own greppable event type.
func TestBlackout_ThatReachedNobodyEmitsItsOwnEvent(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{
		Source:     deadFleetSource(),
		NotifyTo:   "mayor",
		EscalateTo: "human",
	})
	mail.err = errors.New("no_such_mailbox")
	w.Check(outageAt)

	types := ev.types()
	found := false
	for _, ty := range types {
		if ty == EventBlackoutUnreported {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want a %s", types, EventBlackoutUnreported)
	}
	d := findEvent(t, ev, EventBlackoutUnreported)
	if !strings.Contains(d["recipients"].(string), "human") {
		t.Errorf("the unreported event must name who refused it: %v", d["recipients"])
	}

	// A PARTIAL failure is not this state: the notice did leave the machine.
	// Built with New directly, because newTestWatcher installs its own recorder
	// over Options.Mail and a per-recipient outcome is the whole point here.
	ev2 := &eventRec{}
	reached := []string{}
	w2 := New(Options{
		Enabled:    true,
		Source:     deadFleetSource(),
		NotifyTo:   "mayor",
		EscalateTo: "human",
		Emit:       ev2.emit,
		Mail: func(to, from, subject, body string) error {
			if to == "mayor" {
				return errors.New("no_such_mailbox")
			}
			reached = append(reached, to)
			return nil
		},
	})
	w2.Check(outageAt)
	if len(reached) != 1 || reached[0] != "human" {
		t.Fatalf("fixture is wrong: want the escalation box reached, got %v", reached)
	}
	for _, ty := range ev2.types() {
		if ty == EventBlackoutUnreported {
			t.Error("a notice that reached the escalation box is not unreported")
		}
	}
}

// The clear path has to say which ARMS ran. "The peer arm found nothing" is the
// reading a dead fleet produced, so a clear that does not record whether the
// absolute arm looked is the same ambiguity one level up.
func TestBlackout_ClearEventRecordsWhetherTheAbsoluteArmJudged(t *testing.T) {
	healthy := []Sample{
		mailCheck("architect", 751, 757),
		mailCheck("pa", 753, 757),
		mailCheck("pm-onethird", 751, 757),
	}

	// No window offered — the arm did not run, and the clear says so.
	w, _, ev := newTestWatcher(t, Options{Source: fixtureSource(healthy)})
	w.Check(base)
	d := findEvent(t, ev, "ack_watch_clear")
	if d["blackout_judged"] != false {
		t.Error("blackout_judged must be false when no window was supplied")
	}
	if _, ok := d["blackout_blind"]; !ok {
		t.Error("the clear must carry the reason the arm did not judge")
	}

	// Window offered and healthy — the arm ran and found nothing.
	w2, _, ev2 := newTestWatcher(t, Options{Source: func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Samples: healthy, RunningSince: upSince([]string{"architect", "pa", "pm-onethird"}), Recent: &Recent{
			Window: 3 * time.Hour, Delivered: 54, Completed: 50, Schedules: 3,
			Agents: []string{"architect", "pa", "pm-onethird"},
			ByAgent: map[string]AgentFires{
				"architect":   {Delivered: 18, Completed: 17, Schedules: 1},
				"pa":          {Delivered: 18, Completed: 17, Schedules: 1},
				"pm-onethird": {Delivered: 18, Completed: 16, Schedules: 1},
			},
		}}, nil
	}})
	w2.Check(base)
	d2 := findEvent(t, ev2, "ack_watch_clear")
	if d2["blackout_judged"] != true {
		t.Error("blackout_judged must be true when the arm actually looked")
	}
	if _, ok := d2["blackout_blind"]; ok {
		t.Errorf("a judged arm must not report a blind reason: %v", d2["blackout_blind"])
	}
}
