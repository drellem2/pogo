package progresswatch

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

type sentMail struct{ to, from, subject, body string }

type recorder struct {
	mu      sync.Mutex
	mails   []sentMail
	evts    []events.Event
	mailErr error
}

func (r *recorder) send(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mails = append(r.mails, sentMail{to, from, subject, body})
	return r.mailErr
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evts = append(r.evts, e)
}

func (r *recorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.evts {
		out = append(out, e.EventType)
	}
	return out
}

func (r *recorder) has(t string) bool {
	for _, got := range r.types() {
		if got == t {
			return true
		}
	}
	return false
}

func (r *recorder) boxes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.mails {
		out = append(out, m.to)
	}
	return out
}

// fixture drives the watcher from a snapshot the test controls, with the
// interval disabled so every Check is a sample.
func fixture(t *testing.T, rec *recorder, snap *Snapshot, err *error, opts Options) *Watcher {
	t.Helper()
	opts.Enabled = true
	opts.Emit = rec.emit
	opts.Mail = rec.send
	opts.Interval = time.Nanosecond
	opts.Source = func(now time.Time) (Snapshot, error) {
		if err != nil && *err != nil {
			return Snapshot{}, *err
		}
		s := *snap
		s.Now = now
		return s, nil
	}
	return New(opts)
}

// TestHoldDownDelaysTheFirstMail: CPU is an instantaneous reading and seven
// workers can all be between things for one sample. The finding must survive a
// second look before anybody is mailed.
func TestHoldDownDelaysTheFirstMail(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: 10 * time.Minute})

	w.Check(now)
	if len(rec.mails) != 0 {
		t.Fatalf("mailed on the first sample: %v", rec.mails)
	}
	if !rec.has(EventPending) {
		t.Errorf("entering the hold-down must be on the event spine, got %v", rec.types())
	}

	w.Check(now.Add(11 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("expected one mail after the hold-down, got %d", len(rec.mails))
	}
	if rec.mails[0].to != DefaultNotifyTo {
		t.Errorf("notified %q, want the coordinator", rec.mails[0].to)
	}
	if !rec.has(EventStalled) {
		t.Errorf("expected %s, got %v", EventStalled, rec.types())
	}
}

// TestPendingIsEmittedOncePerRun: one line for entering the hold-down, not one
// per sample. A detector that logs every tick is one nobody reads.
func TestPendingIsEmittedOncePerRun(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: time.Hour})
	for i := 0; i < 5; i++ {
		w.Check(now.Add(time.Duration(i) * time.Minute))
	}
	var pending int
	for _, ty := range rec.types() {
		if ty == EventPending {
			pending++
		}
	}
	if pending != 1 {
		t.Errorf("pending emitted %d times, want 1", pending)
	}
}

// TestFlapRestartsTheHoldDown: an interruption is not progress toward the
// threshold. The clock restarts rather than accumulating.
func TestFlapRestartsTheHoldDown(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: 10 * time.Minute})

	w.Check(now)
	snap.LastProgress = now.Add(-time.Minute) // a merge lands
	w.Check(now.Add(6 * time.Minute))
	snap.LastProgress = now.Add(-31 * time.Minute) // and the condition returns
	w.Check(now.Add(7 * time.Minute))
	if len(rec.mails) != 0 {
		t.Fatalf("the hold-down accumulated across a break: %v", rec.mails)
	}
	w.Check(now.Add(18 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("expected one mail once the run itself reached the hold-down, got %d", len(rec.mails))
	}
}

// TestOpenEpisodeStaysQuietUntilRenotify. mg-70f3 counted 49 pages and 44
// all-clears in one log for one fault; this is the number that stops that.
func TestOpenEpisodeStaysQuietUntilRenotify(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: -1, RenotifyAfter: time.Hour})

	w.Check(now)
	w.Check(now.Add(5 * time.Minute))
	w.Check(now.Add(30 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("an unchanged open episode mailed %d times, want 1", len(rec.mails))
	}
	w.Check(now.Add(61 * time.Minute))
	if len(rec.mails) != 2 {
		t.Fatalf("expected a renotify after an hour, got %d mails", len(rec.mails))
	}
}

// TestClearMailReachesEveryoneWhoWasAlarmed. An all-clear that goes to fewer
// mailboxes than the alarm leaves somebody holding an open incident forever.
func TestClearMailReachesEveryoneWhoWasAlarmed(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{
		HoldDown: -1, EscalateAfter: 15 * time.Minute, RenotifyAfter: 6 * time.Hour})

	w.Check(now)
	if got := rec.boxes(); len(got) != 1 || got[0] != DefaultNotifyTo {
		t.Fatalf("first alarm went to %v, want the coordinator alone", got)
	}
	// Crossing EscalateAfter must reach the human without waiting out the
	// six-hour renotify floor.
	w.Check(now.Add(16 * time.Minute))
	if got := rec.boxes(); len(got) != 3 || got[2] != DefaultEscalateTo {
		t.Fatalf("escalation went to %v, want the human pulled in immediately", got)
	}
	snap.LastProgress = now.Add(38 * time.Minute)
	w.Check(now.Add(40 * time.Minute))
	if got := rec.boxes(); len(got) != 5 {
		t.Fatalf("clear went to %v, want both boxes told again", got)
	}
	last := rec.mails[len(rec.mails)-1]
	if !strings.Contains(last.subject, "cleared") {
		t.Errorf("clear subject = %q", last.subject)
	}
	if !strings.Contains(last.body, "landed something") {
		t.Errorf("the clear must say what cleared it: %q", last.body)
	}
}

// TestClearEmitsTheGenericEpisodeClose is the mg-55b2 contract: the notifier
// coalesces every incident class on ONE event type, discriminated by kind.
func TestClearEmitsTheGenericEpisodeClose(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: -1})
	w.Check(now)
	snap.LastProgress = now.Add(-time.Minute)
	w.Check(now.Add(20 * time.Minute))

	for _, e := range rec.evts {
		if e.EventType == IncidentEpisodeClearedEvent {
			if e.Details["kind"] != EpisodeKind {
				t.Errorf("kind = %v, want %q", e.Details["kind"], EpisodeKind)
			}
			return
		}
	}
	t.Fatalf("no %s emitted, got %v", IncidentEpisodeClearedEvent, rec.types())
}

// TestNoClearMailWithoutAnAlarm: a condition that never reached the hold-down
// was never announced, so there is nothing to un-announce.
func TestNoClearMailWithoutAnAlarm(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: time.Hour})
	w.Check(now)
	snap.LastProgress = now.Add(-time.Minute)
	w.Check(now.Add(5 * time.Minute))
	if len(rec.mails) != 0 {
		t.Fatalf("mailed a clear for an episode nobody was told about: %v", rec.mails)
	}
}

// TestBlindSampleDoesNotCloseAnOpenEpisode is the rule that matters most for
// this detector's own failure mode: going blind mid-incident must not be
// reported as the incident being over.
func TestBlindSampleDoesNotCloseAnOpenEpisode(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: -1, RenotifyAfter: time.Hour})
	w.Check(now)
	if len(rec.mails) != 1 {
		t.Fatalf("expected the alarm, got %d mails", len(rec.mails))
	}

	snap.CoresKnown = false
	snap.CoresError = "process table unreadable"
	w.Check(now.Add(6 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("a blind sample sent mail: %v", rec.mails[1:])
	}
	if !rec.has(EventError) {
		t.Errorf("blindness must be on the event spine, got %v", rec.types())
	}

	// And the hold-down clock survived it: when sight returns and the condition
	// still holds, the episode is the SAME one — no second alarm before
	// renotify.
	snap.CoresKnown = true
	snap.CoresError = ""
	w.Check(now.Add(12 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("the episode re-opened after a blind gap: %v", rec.mails[1:])
	}
}

// TestSourceErrorIsReportedAndMailsNothing.
func TestSourceErrorIsReportedAndMailsNothing(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	srcErr := errors.New("registry unavailable")
	w := fixture(t, rec, &snap, &srcErr, Options{HoldDown: -1})
	w.Check(now)
	if len(rec.mails) != 0 {
		t.Fatalf("mailed on a failed sample: %v", rec.mails)
	}
	if !rec.has(EventError) {
		t.Errorf("expected %s, got %v", EventError, rec.types())
	}
}

// TestUndeliverableFindingIsRecorded: a finding that was measured and could not
// be delivered is the detector failing, and must not be silent.
func TestUndeliverableFindingIsRecorded(t *testing.T) {
	rec := &recorder{mailErr: errors.New("no such mailbox")}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: -1})
	w.Check(now)
	if !rec.has(EventError) {
		t.Errorf("expected a delivery failure on the spine, got %v", rec.types())
	}
	if !rec.has(EventStalled) {
		t.Errorf("the finding itself must still be recorded, got %v", rec.types())
	}
}

// TestIntervalThrottlesSampling.
func TestIntervalThrottlesSampling(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	var samples int
	w := New(Options{
		Enabled:  true,
		Emit:     rec.emit,
		Mail:     rec.send,
		Interval: 5 * time.Minute,
		HoldDown: -1,
		Source: func(n time.Time) (Snapshot, error) {
			samples++
			s := snap
			s.Now = n
			return s, nil
		},
	})
	w.Check(now)
	w.Check(now.Add(time.Minute))
	w.Check(now.Add(2 * time.Minute))
	if samples != 1 {
		t.Errorf("sampled %d times inside one interval, want 1", samples)
	}
	w.Check(now.Add(6 * time.Minute))
	if samples != 2 {
		t.Errorf("sampled %d times across two intervals, want 2", samples)
	}
}

// TestDisabledWatcherDoesNothing, and a nil one does not panic — pogod leaves
// the pointer nil when the registry did not load.
func TestDisabledWatcherDoesNothing(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: -1})
	w.enabled = false
	w.Check(now)
	if len(rec.mails) != 0 || len(rec.evts) != 0 {
		t.Fatalf("a disabled watcher acted: mails=%v events=%v", rec.mails, rec.types())
	}
	var nilw *Watcher
	nilw.Check(now)
}

// TestMailCarriesTheMeasurementAndTheDetectorNote. A reader who has never met
// this detector needs to know what it measured and that it did nothing.
func TestMailCarriesTheMeasurementAndTheDetectorNote(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	w := fixture(t, rec, &snap, nil, Options{HoldDown: -1})
	w.Check(now)
	body := rec.mails[0].body
	for _, want := range []string{
		"worker subtrees at 0.10 of 10 cores",
		"nothing landed in 31m",
		"REPORT-ONLY",
		"waiting on the same remote",
		"p1, p2, p3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mail body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(rec.mails[0].subject, "STALLED") {
		t.Errorf("the subject must carry numbers, not a state token: %q", rec.mails[0].subject)
	}
}

// TestPersistentBlindnessReportsItself is the remedy held to the standard of
// the defect. This package exists because an instrument can answer the question
// it was built for, truthfully, while the question that matters goes unanswered
// and nothing says so. A detector that goes blind and only whispers into
// events.log is that same shape one level up.
func TestPersistentBlindnessReportsItself(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	snap.CoresKnown = false
	snap.CoresError = "process table unreadable"
	w := fixture(t, rec, &snap, nil, Options{HoldDown: -1, BlindFor: 30 * time.Minute})

	w.Check(now)
	w.Check(now.Add(20 * time.Minute))
	if len(rec.mails) != 0 {
		t.Fatalf("mailed about blindness before the threshold: %v", rec.mails)
	}

	w.Check(now.Add(31 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("30 minutes measuring nothing went unreported, got %d mails", len(rec.mails))
	}
	m := rec.mails[0]
	if !strings.Contains(m.subject, "measured NOTHING") {
		t.Errorf("subject must name the detector's own failure: %q", m.subject)
	}
	for _, want := range []string{"reporting ITSELF", "process table unreadable", "absence of an alarm means nothing"} {
		if !strings.Contains(m.body, want) {
			t.Errorf("blind mail missing %q:\n%s", want, m.body)
		}
	}

	// And it does not repeat inside the renotify floor.
	w.Check(now.Add(45 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("blind notice repeated inside the renotify floor: %d mails", len(rec.mails))
	}
}

// TestSightRestartsTheBlindClock: a detector that recovers stops complaining,
// and a later blind run starts its own clock rather than resuming the old one.
func TestSightRestartsTheBlindClock(t *testing.T) {
	rec := &recorder{}
	snap := incident()
	snap.CoresKnown = false
	snap.CoresError = "process table unreadable"
	w := fixture(t, rec, &snap, nil, Options{HoldDown: -1, BlindFor: 30 * time.Minute,
		// The condition itself must not fire, so the only mail this test can
		// see is the blind notice.
		RenotifyAfter: 24 * time.Hour})

	w.Check(now)
	w.Check(now.Add(25 * time.Minute))

	snap.CoresKnown = true
	snap.CoresError = ""
	snap.WorkerCores = 4.0 // busy, so no finding either
	w.Check(now.Add(26 * time.Minute))

	snap.CoresKnown = false
	snap.CoresError = "process table unreadable"
	w.Check(now.Add(27 * time.Minute))
	w.Check(now.Add(50 * time.Minute))
	if len(rec.mails) != 0 {
		t.Fatalf("the blind clock survived a sighted sample: %v", rec.mails)
	}
}
