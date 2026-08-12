package reviewdecl

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

type sentMail struct{ to, from, subject, body string }

// recorder captures the two side-effect channels a Watcher has, so a test can
// assert on both what was mailed and what was emitted.
type recorder struct {
	mu     sync.Mutex
	mails  []sentMail
	events []events.Event
	err    error
}

func (r *recorder) mail(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.mails = append(r.mails, sentMail{to, from, subject, body})
	return nil
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) eventsOfType(t string) []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.events {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

func newTestWatcher(rec *recorder, src SourceFunc, opts ...func(*Options)) *Watcher {
	o := Options{Enabled: true, Source: src, Mail: rec.mail, Emit: rec.emit}
	for _, f := range opts {
		f(&o)
	}
	return New(o)
}

func fixed(items ...Item) SourceFunc {
	return func() ([]Item, error) { return items, nil }
}

var t0 = time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)

// TestWatcherMailsAFinding is the runner's whole job.
func TestWatcherMailsAFinding(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, fixed(review("mg-0001", after, "")))
	w.Check(t0)

	if len(rec.mails) != 1 {
		t.Fatalf("mails = %d, want 1", len(rec.mails))
	}
	m := rec.mails[0]
	if m.to != DefaultNotifyTo {
		t.Errorf("to = %q, want the coordinator %q — the only agent that files review tickets", m.to, DefaultNotifyTo)
	}
	if m.from != mailFrom {
		t.Errorf("from = %q, want %q", m.from, mailFrom)
	}
	if !strings.Contains(m.subject, "unprotected") {
		t.Errorf("subject = %q, want the unprotected count", m.subject)
	}
	if !strings.Contains(m.body, "mg-0001") {
		t.Errorf("body does not name the ticket:\n%s", m.body)
	}
	if !strings.Contains(m.body, "pogo check-review-decl") {
		t.Errorf("body does not tell the reader how to reproduce it:\n%s", m.body)
	}
	if !strings.Contains(m.body, "REPORT-ONLY") {
		t.Errorf("body does not say nothing was written:\n%s", m.body)
	}
}

// TestWatcherNeverEscalatesToHuman. mg-253e: "Severity is genuinely low — do not
// inflate it." A missed declaration costs one recoverable round, and copying
// `human` on it would spend a scarce reader and teach them to filter every
// sibling detector alongside this one. There is deliberately no seam for it, and
// this pins that no future edit quietly adds one.
func TestWatcherNeverEscalatesToHuman(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, fixed(review("mg-0001", after, "")))
	// Many samples, far apart: nothing about persistence may widen the recipient set.
	for i := 0; i < 20; i++ {
		w.Check(t0.Add(time.Duration(i) * 25 * time.Hour))
	}
	for _, m := range rec.mails {
		if m.to == "human" {
			t.Fatalf("a review-declaration finding escalated to `human` after %d notices", len(rec.mails))
		}
	}
	if len(rec.mails) == 0 {
		t.Fatal("precondition: nothing was mailed at all, so this proves nothing")
	}
}

// TestWatcherIsQuietWhenThereIsNothingToReport.
func TestWatcherIsQuietWhenThereIsNothingToReport(t *testing.T) {
	rec := &recorder{}
	newTestWatcher(rec, fixed(review("mg-0001", after, "mg-aaf6"))).Check(t0)
	if len(rec.mails) != 0 {
		t.Errorf("a clean scan mailed: %+v", rec.mails)
	}
}

// TestWatcherEmitsAPositiveRecordOnEveryRun, INCLUDING the clean ones.
//
// "The detector ran and found nothing" and "the detector has not run since the
// last restart" are the two states this lineage keeps confusing, and an absence
// cannot distinguish them. It is also this detector's own version of the defect
// it reports: a guard whose only evidence is silence.
func TestWatcherEmitsAPositiveRecordOnEveryRun(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, fixed(review("mg-0001", after, "mg-aaf6")), func(o *Options) {
		o.Interval = time.Minute
	})
	w.Check(t0)
	w.Check(t0.Add(2 * time.Minute))

	ran := rec.eventsOfType("review_decl_watch_ran")
	if len(ran) != 2 {
		t.Fatalf("review_decl_watch_ran fired %d times over 2 clean samples, want 2", len(ran))
	}
	d := ran[0].Details
	for _, key := range []string{"scanned", "population", "declared", "missing", "unprotected", "boundary"} {
		if _, ok := d[key]; !ok {
			t.Errorf("the positive record carries no %q — an operator asking whether the detector "+
				"was seeing anything would have to re-read mail bodies", key)
		}
	}
	if d["scanned"] != 1 {
		t.Errorf("scanned = %v, want 1", d["scanned"])
	}
}

// TestWatcherThrottlesToItsInterval. The heartbeat ticks every 30s; the sweep
// must not.
func TestWatcherThrottlesToItsInterval(t *testing.T) {
	rec := &recorder{}
	var calls int
	w := newTestWatcher(rec, func() ([]Item, error) {
		calls++
		return nil, nil
	}, func(o *Options) { o.Interval = time.Hour })

	for i := 0; i < 60; i++ {
		w.Check(t0.Add(time.Duration(i) * 30 * time.Second))
	}
	if calls != 1 {
		t.Errorf("source called %d times over 30 minutes of ticks at a 1h interval, want 1", calls)
	}
	w.Check(t0.Add(time.Hour + time.Second))
	if calls != 2 {
		t.Errorf("source called %d times after the interval elapsed, want 2", calls)
	}
}

// TestWatcherDoesNotReMailAnUnchangedFindingSet. Mailing every interval trains
// the reader to filter the sender, and a muted detector is worse than none
// because it also manufactures the feeling of coverage.
func TestWatcherDoesNotReMailAnUnchangedFindingSet(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, fixed(review("mg-0001", after, "")), func(o *Options) {
		o.Interval = time.Minute
		o.RenotifyAfter = 24 * time.Hour
	})
	for i := 0; i < 10; i++ {
		w.Check(t0.Add(time.Duration(i) * 2 * time.Minute))
	}
	if len(rec.mails) != 1 {
		t.Errorf("mails = %d over 10 samples of an unchanged finding, want 1", len(rec.mails))
	}
}

// TestWatcherReMailsAfterRenotify. Mailing only on change lets a finding nobody
// actioned fall permanently silent.
func TestWatcherReMailsAfterRenotify(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, fixed(review("mg-0001", after, "")), func(o *Options) {
		o.Interval = time.Minute
		o.RenotifyAfter = 6 * time.Hour
	})
	w.Check(t0)
	w.Check(t0.Add(time.Hour))
	if len(rec.mails) != 1 {
		t.Fatalf("mails = %d before renotify elapsed, want 1", len(rec.mails))
	}
	w.Check(t0.Add(7 * time.Hour))
	if len(rec.mails) != 2 {
		t.Errorf("mails = %d after renotify elapsed, want 2 — a finding nobody cleared must keep costing someone something", len(rec.mails))
	}
}

// TestWatcherMailsAgainWhenTheFindingSetCHANGES. A new unprotected review is
// news, and news with a short shelf life: the round it affects is running now.
func TestWatcherMailsAgainWhenTheFindingSetChanges(t *testing.T) {
	rec := &recorder{}
	var items []Item
	w := newTestWatcher(rec, func() ([]Item, error) { return items, nil }, func(o *Options) {
		o.Interval = time.Minute
	})
	items = []Item{review("mg-0001", after, "")}
	w.Check(t0)
	items = append(items, review("mg-0002", after, ""))
	w.Check(t0.Add(2 * time.Minute))
	if len(rec.mails) != 2 {
		t.Errorf("mails = %d, want 2 — a NEW finding is news even inside the renotify window", len(rec.mails))
	}
}

// TestWatcherTreatsAClearedFindingAsNewsIfItRecurs. Clearing the fingerprint on
// a clean run is what stops a resolved-and-recurred finding from being
// suppressed as "unchanged" forever.
func TestWatcherTreatsAClearedFindingAsNewsIfItRecurs(t *testing.T) {
	rec := &recorder{}
	var items []Item
	w := newTestWatcher(rec, func() ([]Item, error) { return items, nil }, func(o *Options) {
		o.Interval = time.Minute
		o.RenotifyAfter = 30 * 24 * time.Hour
	})
	items = []Item{review("mg-0001", after, "")}
	w.Check(t0)
	items = []Item{review("mg-0001", after, "mg-aaf6")} // fixed
	w.Check(t0.Add(2 * time.Minute))
	items = []Item{review("mg-0001", after, "")} // and broken again
	w.Check(t0.Add(4 * time.Minute))
	if len(rec.mails) != 2 {
		t.Errorf("mails = %d, want 2 — a finding that cleared and recurred is news again", len(rec.mails))
	}
}

// TestWatcherReportsAnUnreadableStoreAndDoesNotMailAClean. A store that cannot
// be read must not render as a clean scan — that is this detector reproducing,
// inside itself, the silent absence it was built to catch.
func TestWatcherReportsAnUnreadableStoreAndDoesNotMailAClean(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, func() ([]Item, error) { return nil, errors.New("store gone") })
	w.Check(t0)

	if len(rec.mails) != 0 {
		t.Errorf("a failed scan mailed a report: %+v", rec.mails)
	}
	errs := rec.eventsOfType("review_decl_watch_error")
	if len(errs) != 1 {
		t.Fatalf("review_decl_watch_error fired %d times, want 1 — a blind detector must be visible "+
			"in the event log rather than indistinguishable from a quiet one", len(errs))
	}
	if len(rec.eventsOfType("review_decl_watch_ran")) != 0 {
		t.Error("a scan that never read the store emitted a positive record of having run")
	}
}

// TestWatcherRecordsAMailFailure. A notice that reaches nobody is this
// detector's own failure mode, one level up.
func TestWatcherRecordsAMailFailure(t *testing.T) {
	rec := &recorder{err: errors.New("no such mailbox")}
	newTestWatcher(rec, fixed(review("mg-0001", after, ""))).Check(t0)

	fired := rec.eventsOfType("review_decl_watch_fired")
	if len(fired) != 1 {
		t.Fatalf("review_decl_watch_fired fired %d times, want 1", len(fired))
	}
	if _, ok := fired[0].Details["mail_error"]; !ok {
		t.Error("a failed send left no record: the finding was detected and reached nobody, silently")
	}
}

// TestWatcherIsInertWhenDisabled, and when it has no way to report.
func TestWatcherIsInert(t *testing.T) {
	rec := &recorder{}
	var called bool
	src := func() ([]Item, error) { called = true; return nil, nil }

	New(Options{Enabled: false, Source: src, Mail: rec.mail, Emit: rec.emit}).Check(t0)
	New(Options{Enabled: true, Source: nil, Mail: rec.mail, Emit: rec.emit}).Check(t0)
	New(Options{Enabled: true, Source: src, Mail: nil, Emit: rec.emit}).Check(t0)
	var nilW *Watcher
	nilW.Check(t0)

	if called {
		t.Error("a disabled or unreportable watcher sampled the store")
	}
	if len(rec.mails) != 0 || len(rec.events) != 0 {
		t.Errorf("an inert watcher had side effects: mails=%v events=%v", rec.mails, rec.events)
	}
}

// TestWatcherConsumesItsSlotEvenWhenTheSampleFails. due() records the run BEFORE
// sampling, so a store that errors every time cannot turn a 30-minute sweep into
// a per-tick one.
func TestWatcherConsumesItsSlotEvenWhenTheSampleFails(t *testing.T) {
	rec := &recorder{}
	var calls int
	w := newTestWatcher(rec, func() ([]Item, error) {
		calls++
		return nil, errors.New("store gone")
	}, func(o *Options) { o.Interval = time.Hour })

	for i := 0; i < 20; i++ {
		w.Check(t0.Add(time.Duration(i) * 30 * time.Second))
	}
	if calls != 1 {
		t.Errorf("a failing source was sampled %d times in 10 minutes at a 1h interval, want 1", calls)
	}
}

// TestWatcherAppliesTheConventionBoundaryByDefault. A runner constructed without
// an explicit boundary must not judge the store's history — that is the 31 false
// findings mayor's dispatch note existed to prevent.
func TestWatcherAppliesTheConventionBoundaryByDefault(t *testing.T) {
	rec := &recorder{}
	newTestWatcher(rec, fixed(review("mg-0001", before, ""))).Check(t0)
	if len(rec.mails) != 0 {
		t.Fatalf("the default watcher reported a PRE-CONVENTION ticket: %q", rec.mails[0].subject)
	}
	ran := rec.eventsOfType("review_decl_watch_ran")
	if len(ran) != 1 {
		t.Fatalf("no positive record: %+v", rec.events)
	}
	if got := ran[0].Details["boundary"]; got != ConventionLandedAt.Format(time.RFC3339) {
		t.Errorf("boundary = %v, want the convention date — the record must say which boundary it applied", got)
	}
	if ran[0].Details["pre_convention"] != 1 {
		t.Errorf("pre_convention = %v, want 1 — the exclusion must be counted, not silent", ran[0].Details["pre_convention"])
	}
}

// TestWatcherHasNoWriteSeam. The Options struct is the runner's entire contract
// with the outside world. Report-only is a boundary, not an implementation
// stage, so it is asserted structurally: there is exactly one side-effect
// channel and it sends mail.
func TestWatcherHasNoWriteSeam(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, fixed(review("mg-0001", after, "")))
	w.Check(t0)
	// The finding was produced...
	if len(rec.mails) != 1 {
		t.Fatalf("precondition: nothing was reported, so this proves nothing")
	}
	// ...and the source it was produced from is untouched. A Watcher holds a
	// SourceFunc, not a store handle, so there is nothing it could write through
	// even if a future edit wanted to.
	items, _ := w.source()
	if items[0].Reviews != "" {
		t.Error("the watcher wrote a `reviews:` value back through its source")
	}
}
