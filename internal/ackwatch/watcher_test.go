package ackwatch

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

type mailRec struct {
	mu   sync.Mutex
	sent []struct{ To, From, Subject, Body string }
	err  error
}

func (m *mailRec) send(to, from, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, struct{ To, From, Subject, Body string }{to, from, subject, body})
	return m.err
}

func (m *mailRec) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *mailRec) recipients() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sent))
	for _, s := range m.sent {
		out = append(out, s.To)
	}
	return out
}

type eventRec struct {
	mu  sync.Mutex
	evs []events.Event
}

func (e *eventRec) emit(ev events.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evs = append(e.evs, ev)
}

func (e *eventRec) types() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.evs))
	for _, ev := range e.evs {
		out = append(out, ev.EventType)
	}
	return out
}

// fixtureSource returns a SourceFunc yielding the given samples with no
// disruption. No live scheduler and no ~/.pogo read — see the note atop
// ackwatch_test.go.
func fixtureSource(samples []Sample) SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Samples: samples}, nil
	}
}

func newTestWatcher(t *testing.T, opts Options) (*Watcher, *mailRec, *eventRec) {
	t.Helper()
	mail := &mailRec{}
	ev := &eventRec{}
	opts.Enabled = true
	if opts.Source == nil {
		opts.Source = fixtureSource(observedFleet())
	}
	opts.Mail = mail.send
	opts.Emit = ev.emit
	return New(opts), mail, ev
}

func TestWatcherMailsTheMayorOnADeficit(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{})
	w.Check(base)

	if mail.count() != 1 {
		t.Fatalf("want 1 mail, got %d", mail.count())
	}
	got := mail.sent[0]
	if got.To != DefaultNotifyTo {
		t.Errorf("To = %q, want %q", got.To, DefaultNotifyTo)
	}
	if got.From != mailFrom {
		t.Errorf("From = %q, want %q", got.From, mailFrom)
	}
	if !strings.Contains(got.Subject, "mail-check-pm-pogo") {
		t.Errorf("subject should name the schedule: %q", got.Subject)
	}
	if !strings.Contains(got.Body, "REPORT-ONLY") {
		t.Error("body must say it acted on nothing")
	}
	if !strings.Contains(got.Body, "pogo check-acks") {
		t.Error("body must say how to re-check")
	}
	if types := ev.types(); len(types) != 1 || types[0] != "ack_watch_fired" {
		t.Errorf("events = %v, want [ack_watch_fired]", types)
	}
}

// A healthy fleet is MAIL-silent but not LOG-silent: the control records that it
// ran and declined. Before mg-ddf7 this path emitted nothing, which made "ran,
// considered everything, found nothing" indistinguishable from "was never wired
// up" — and this fleet's events log contains zero ack_watch_* events of any
// kind, so that ambiguity was live rather than theoretical.
func TestWatcherIsMailSilentButRecordsTheClearRunOnAHealthyFleet(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{
		Source: fixtureSource([]Sample{
			mailCheck("architect", 751, 757),
			mailCheck("pa", 753, 757),
			mailCheck("pm-onethird", 751, 757),
		}),
	})
	w.Check(base)
	if mail.count() != 0 {
		t.Errorf("healthy fleet mailed %d time(s)", mail.count())
	}
	if types := ev.types(); len(types) != 1 || types[0] != "ack_watch_clear" {
		t.Fatalf("events = %v, want [ack_watch_clear]", types)
	}
	d := ev.evs[0].Details
	if d["scanned"] != 3 || d["eligible"] != 3 {
		t.Errorf("want scanned=3 eligible=3, got scanned=%v eligible=%v", d["scanned"], d["eligible"])
	}
}

// The storm case, which is the one the event exists for. A burst of fresh
// short-lived schedules is correctly NOT judged — too few fires, counters too
// young, no comparable peers — and the resulting silence is the right answer.
// Without the counts alongside it, that silence reads as a clean bill of health
// for 40 schedules of which 3 were actually looked at.
func TestWatcherClearEventDistinguishesJudgedFromMerelyUnjudged(t *testing.T) {
	samples := []Sample{
		mailCheck("architect", 751, 757),
		mailCheck("pa", 753, 757),
		mailCheck("pm-onethird", 751, 757),
	}
	// Twelve polecats spawned into the storm: eligible-looking but unjudgeable.
	for _, name := range []string{"c76a", "f00a", "86e7", "8595", "2fcc", "fd39",
		"8792", "bce0", "d631", "7254", "ebee", "d578"} {
		s := mailCheck(name, 0, 6) // six fires, none acked
		s.CreatedAt = base.Add(-time.Hour)
		samples = append(samples, s)
	}
	w, mail, ev := newTestWatcher(t, Options{Source: fixtureSource(samples)})
	w.Check(base)

	if mail.count() != 0 {
		t.Fatalf("a burst of under-sampled schedules must not mail; got %d", mail.count())
	}
	types := ev.types()
	if len(types) != 1 || types[0] != "ack_watch_clear" {
		t.Fatalf("events = %v, want [ack_watch_clear]", types)
	}
	d := ev.evs[0].Details
	if d["scanned"] != 15 {
		t.Errorf("want scanned=15, got %v", d["scanned"])
	}
	if d["eligible"] != 3 {
		t.Errorf("want eligible=3 — the twelve polecats are under MinFires, got %v", d["eligible"])
	}
	if d["skipped_few_fires"] != 12 {
		t.Errorf("want skipped_few_fires=12, got %v", d["skipped_few_fires"])
	}
	// The whole point: the record must let a reader tell these apart. 3 of 15 is
	// not an all-clear, and the difference is only visible because the counts
	// ride the event.
	if d["scanned"] == d["eligible"] {
		t.Error("scanned must not equal eligible here, or the test proves nothing")
	}
}

// A clear run is recorded on EVERY sample, not only when the fleet transitions
// from deficient to healthy. A transition-only emit goes quiet during exactly
// the long calm in which a reader most needs to know the control is still alive.
func TestWatcherRecordsEveryClearRunNotOnlyTransitions(t *testing.T) {
	w, _, ev := newTestWatcher(t, Options{
		Interval: time.Minute,
		Source: fixtureSource([]Sample{
			mailCheck("architect", 751, 757),
			mailCheck("pa", 753, 757),
			mailCheck("pm-onethird", 751, 757),
		}),
	})
	w.Check(base)
	w.Check(base.Add(2 * time.Minute))
	w.Check(base.Add(4 * time.Minute))

	types := ev.types()
	if len(types) != 3 {
		t.Fatalf("want one ack_watch_clear per sample, got %v", types)
	}
	for i, ty := range types {
		if ty != "ack_watch_clear" {
			t.Fatalf("event %d = %q, want ack_watch_clear", i, ty)
		}
	}
}

// A suppressed report and a clear report are DIFFERENT events. Collapsing them
// would be this package's own bug: "we declined to look" and "we looked and
// found nothing" are the two observations it exists to keep apart.
func TestWatcherSuppressedAndClearAreDistinctEvents(t *testing.T) {
	healthy := []Sample{
		mailCheck("architect", 751, 757),
		mailCheck("pa", 753, 757),
		mailCheck("pm-onethird", 751, 757),
	}
	w, _, ev := newTestWatcher(t, Options{
		Interval: time.Minute,
		Source: func(now time.Time) (Snapshot, error) {
			snap := Snapshot{Now: now, Samples: healthy}
			if now.Equal(base) {
				snap.LastDisruption = now.Add(-time.Minute)
				snap.DisruptionReason = "system_wake"
			}
			return snap, nil
		},
	})
	w.Check(base)                      // just woken: suppressed
	w.Check(base.Add(2 * time.Minute)) // settled: clear

	types := ev.types()
	if len(types) != 2 || types[0] != "ack_watch_suppressed" || types[1] != "ack_watch_clear" {
		t.Fatalf("events = %v, want [ack_watch_suppressed ack_watch_clear]", types)
	}
}

// The coarse throttle: one sample per interval, not one per heartbeat tick.
func TestWatcherThrottlesToItsInterval(t *testing.T) {
	var calls int
	w, mail, _ := newTestWatcher(t, Options{
		Interval: time.Hour,
		Source: func(now time.Time) (Snapshot, error) {
			calls++
			return Snapshot{Now: now, Samples: observedFleet()}, nil
		},
	})
	for i := 0; i < 20; i++ {
		w.Check(base.Add(time.Duration(i) * 30 * time.Second))
	}
	if calls != 1 {
		t.Errorf("source called %d times in 10 minutes of ticks, want 1", calls)
	}
	if mail.count() != 1 {
		t.Errorf("mails = %d, want 1", mail.count())
	}
}

// An unchanged finding set stays quiet until RenotifyAfter, then repeats.
func TestUnchangedFindingsRenotifyOnlyPeriodically(t *testing.T) {
	w, mail, _ := newTestWatcher(t, Options{
		Interval:      30 * time.Minute,
		RenotifyAfter: 6 * time.Hour,
	})
	for i := 0; i < 12; i++ { // six hours of half-hourly samples
		w.Check(base.Add(time.Duration(i) * 30 * time.Minute))
	}
	if mail.count() != 1 {
		t.Fatalf("want 1 mail over the renotify window, got %d", mail.count())
	}
	w.Check(base.Add(6*time.Hour + time.Minute))
	if mail.count() != 2 {
		t.Errorf("want a repeat past RenotifyAfter, got %d mails", mail.count())
	}
}

// A rate that drifts is the SAME finding. Fingerprinting the numbers would mail
// every interval and get the sender filtered — an alert nobody reads reproduces
// the very bug this package exists to fix.
func TestDriftingRateDoesNotRemail(t *testing.T) {
	completed := 270
	w, mail, _ := newTestWatcher(t, Options{
		Interval: 30 * time.Minute,
		Source: func(now time.Time) (Snapshot, error) {
			s := observedFleet()
			completed++
			s[3].FiresCompleted = completed
			s[3].FiresDelivered = 757 + completed - 270
			return Snapshot{Now: now, Samples: s}, nil
		},
	})
	for i := 0; i < 8; i++ {
		w.Check(base.Add(time.Duration(i) * 30 * time.Minute))
	}
	if mail.count() != 1 {
		t.Errorf("a drifting rate mailed %d times, want 1", mail.count())
	}
}

// A deficit that clears and later recurs is news again.
func TestClearedThenRecurredIsNewsAgain(t *testing.T) {
	bad := true
	w, mail, _ := newTestWatcher(t, Options{
		Interval: 30 * time.Minute,
		Source: func(now time.Time) (Snapshot, error) {
			s := observedFleet()
			if !bad {
				s[3].FiresCompleted = 750
			}
			return Snapshot{Now: now, Samples: s}, nil
		},
	})
	w.Check(base)
	bad = false
	w.Check(base.Add(time.Hour))
	bad = true
	w.Check(base.Add(2 * time.Hour))

	if mail.count() != 2 {
		t.Errorf("want 2 mails (initial + recurrence), got %d", mail.count())
	}
}

// A standing finding the fleet has not cleared also reaches `human`. The mayor
// is itself a crew agent and can have this exact defect (mg-d385), so an alert
// routed only there is an alert that may reach nobody.
func TestStandingFindingEscalatesToHuman(t *testing.T) {
	w, mail, _ := newTestWatcher(t, Options{
		Interval:      30 * time.Minute,
		RenotifyAfter: time.Hour,
		EscalateAfter: 24 * time.Hour,
	})
	w.Check(base)
	if got := mail.recipients(); len(got) != 1 || got[0] != DefaultNotifyTo {
		t.Fatalf("first notice went to %v, want [%s]", got, DefaultNotifyTo)
	}

	w.Check(base.Add(25 * time.Hour))
	got := mail.recipients()
	if len(got) != 3 {
		t.Fatalf("recipients = %v, want mayor then mayor+human", got)
	}
	if got[1] != DefaultNotifyTo || got[2] != DefaultEscalateTo {
		t.Errorf("escalated recipients = %v, want [%s %s]", got[1:], DefaultNotifyTo, DefaultEscalateTo)
	}
	if !strings.Contains(mail.sent[2].Body, "ESCALATED") {
		t.Error("escalated body must say so")
	}
}

func TestNegativeEscalateAfterDisablesEscalation(t *testing.T) {
	w, mail, _ := newTestWatcher(t, Options{
		Interval:      30 * time.Minute,
		RenotifyAfter: time.Hour,
		EscalateAfter: -1,
	})
	w.Check(base)
	w.Check(base.Add(100 * time.Hour))
	for _, to := range mail.recipients() {
		if to == DefaultEscalateTo {
			t.Fatalf("escalation was disabled but %s was mailed", to)
		}
	}
}

// A restart is the same class of event as a system_wake — both leave the
// counters describing a regime that no longer exists — and shares one
// suppression rather than getting a mechanism each.
func TestRestartSuppressesLikeAWake(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{StartedAt: base.Add(-5 * time.Minute)})
	w.Check(base)

	if mail.count() != 0 {
		t.Errorf("a restart 5m ago must suppress the notice, got %d mails", mail.count())
	}
	types := ev.types()
	if len(types) != 1 || types[0] != "ack_watch_suppressed" {
		t.Fatalf("events = %v, want [ack_watch_suppressed]", types)
	}
	// The silence must be recorded, or "suppressed after a bounce" and "ran and
	// found nothing" collapse into the same absence.
	if reason, _ := ev.evs[0].Details["reason"].(string); !strings.Contains(reason, "pogod restart") {
		t.Errorf("suppression reason = %q, want it to name the restart", reason)
	}
}

func TestRestartSuppressionExpires(t *testing.T) {
	w, mail, _ := newTestWatcher(t, Options{StartedAt: base.Add(-DefaultSettleAfter - time.Minute)})
	w.Check(base)
	if mail.count() != 1 {
		t.Errorf("want the notice once the settle window passes, got %d mails", mail.count())
	}
}

// The more recent of (system_wake, process start) wins — one suppression fed by
// two inputs.
func TestSourceWakeSuppressesEvenWithAnOldStart(t *testing.T) {
	w, mail, _ := newTestWatcher(t, Options{
		StartedAt: base.Add(-48 * time.Hour),
		Source: func(now time.Time) (Snapshot, error) {
			return Snapshot{
				Now: now, Samples: observedFleet(),
				LastDisruption:   now.Add(-2 * time.Minute),
				DisruptionReason: "system_wake",
			}, nil
		},
	})
	w.Check(base)
	if mail.count() != 0 {
		t.Errorf("a wake 2m ago must suppress even when the process is old, got %d mails", mail.count())
	}
}

// A source error is not a clean scan. A blind detector must be visible in the
// event log, not indistinguishable from a quiet one.
func TestSourceErrorEmitsRatherThanGoingQuiet(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{
		Source: func(now time.Time) (Snapshot, error) {
			return Snapshot{}, errUnreadable{}
		},
	})
	w.Check(base)
	if mail.count() != 0 {
		t.Errorf("a failed read must not mail findings, got %d", mail.count())
	}
	if types := ev.types(); len(types) != 1 || types[0] != "ack_watch_error" {
		t.Errorf("events = %v, want [ack_watch_error]", types)
	}
}

type errUnreadable struct{}

func (errUnreadable) Error() string { return "cannot read scheduler state" }

// A mail failure is recorded on the event, because a notice that reaches nobody
// is this ticket's bug one level up.
func TestMailFailureIsRecordedOnTheEvent(t *testing.T) {
	w, mail, ev := newTestWatcher(t, Options{})
	mail.err = errUnreadable{}
	w.Check(base)

	if len(ev.evs) != 1 {
		t.Fatalf("events = %v, want one", ev.types())
	}
	if _, ok := ev.evs[0].Details["mail_error_"+DefaultNotifyTo]; !ok {
		t.Errorf("details missing mail_error_%s: %+v", DefaultNotifyTo, ev.evs[0].Details)
	}
}

func TestDisabledWatcherDoesNothing(t *testing.T) {
	mail := &mailRec{}
	ev := &eventRec{}
	w := New(Options{Source: fixtureSource(observedFleet()), Mail: mail.send, Emit: ev.emit})
	w.Check(base)
	if mail.count() != 0 || len(ev.types()) != 0 {
		t.Errorf("a disabled watcher acted: %d mails, %v events", mail.count(), ev.types())
	}
}

func TestNilWatcherCheckIsSafe(t *testing.T) {
	var w *Watcher
	w.Check(base) // must not panic
}

func TestFiredEventNamesTheSchedules(t *testing.T) {
	w, _, ev := newTestWatcher(t, Options{})
	w.Check(base)

	d := ev.evs[0].Details
	ids, ok := d["schedules"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "mail-check-pm-pogo" {
		t.Errorf("schedules detail = %v, want [mail-check-pm-pogo]", d["schedules"])
	}
	if d["deficit_count"] != 1 {
		t.Errorf("deficit_count = %v, want 1", d["deficit_count"])
	}
	if d["eligible"] != 4 {
		t.Errorf("eligible = %v, want 4", d["eligible"])
	}
}
