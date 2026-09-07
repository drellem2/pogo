package carrierdrift

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// sentMail records one delivery for assertions.
type sentMail struct{ to, from, subject, body string }

// recorder collects the mail and events one or more samples produced.
type recorder struct {
	mu     sync.Mutex
	mails  []sentMail
	events []events.Event
	fail   error
}

func (r *recorder) send(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mails = append(r.mails, sentMail{to, from, subject, body})
	return r.fail
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.EventType)
	}
	return out
}

// driftedCarriers is one unacknowledged carrier — enough to make a pass
// actionable without dragging every kind into a mail-policy test.
func driftedCarriers() []Carrier {
	return []Carrier{carrier("mg-0802", "drellem2/pogo#159", "gated", 4*24*time.Hour)}
}

func driftedSnapshot(string, int) (Snapshot, error) {
	return openSnap(16*24*time.Hour, false, 0), nil
}

func cleanSnapshot(string, int) (Snapshot, error) {
	return openSnap(16*24*time.Hour, true, 2), nil
}

func newTestWatcher(rec *recorder, carriers []Carrier, snap SnapshotFunc, opts Options) *Watcher {
	opts.Enabled = true
	opts.Mail = rec.send
	opts.Emit = rec.emit
	opts.Snapshot = snap
	opts.Source = func() ([]Carrier, int, error) { return carriers, len(carriers), nil }
	return New(opts)
}

// TestWatcherMailsTheCoordinatorOnADrift is the basic path: a finding reaches
// the one agent that can act on it, with the report body a reader can reproduce.
func TestWatcherMailsTheCoordinatorOnADrift(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, driftedCarriers(), driftedSnapshot, Options{})
	w.Check(now)

	if len(rec.mails) != 1 {
		t.Fatalf("mails = %d, want 1", len(rec.mails))
	}
	m := rec.mails[0]
	if m.to != DefaultNotifyTo || m.from != mailFrom {
		t.Fatalf("routed to %q from %q", m.to, m.from)
	}
	if !strings.Contains(m.subject, "NO acknowledgement") {
		t.Errorf("subject = %q", m.subject)
	}
	// The body must carry the reproduce-it-yourself line, or a recipient can only
	// take the notice's word for it.
	if !strings.Contains(m.body, "pogo check-carriers") {
		t.Errorf("body does not say how to re-derive it:\n%s", m.body)
	}
	// And it must say it did not act, because a detector that might have acted is
	// one a coordinator has to check behind.
	if !strings.Contains(m.body, "REPORT-ONLY") {
		t.Errorf("body does not state that it took no action:\n%s", m.body)
	}
	if got := rec.types(); len(got) != 1 || got[0] != "carrier_drift_watch_fired" {
		t.Fatalf("events = %v", got)
	}
}

// TestWatcherThrottlesToItsInterval: it rides pogod's heartbeat, which fires far
// more often than an hour, and a sample per tick would be one `gh` call per live
// carrier per tick against somebody else's tracker.
func TestWatcherThrottlesToItsInterval(t *testing.T) {
	rec := &recorder{}
	calls := 0
	w := New(Options{
		Enabled: true, Mail: rec.send, Emit: rec.emit,
		Snapshot: driftedSnapshot, Interval: time.Hour,
		Source: func() ([]Carrier, int, error) {
			calls++
			return driftedCarriers(), 1, nil
		},
	})
	w.Check(now)
	w.Check(now.Add(time.Minute))
	w.Check(now.Add(30 * time.Minute))
	if calls != 1 {
		t.Fatalf("sampled %d times inside one interval, want 1", calls)
	}
	w.Check(now.Add(61 * time.Minute))
	if calls != 2 {
		t.Fatalf("did not sample after the interval elapsed: %d", calls)
	}
}

// TestWatcherMailsOnChangeAndThenOnlyDaily. An unchanged finding costs one mail
// a day, not one an hour — a detector that repeats itself every sample is one
// whose sender gets filtered.
func TestWatcherMailsOnChangeAndThenOnlyDaily(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, driftedCarriers(), driftedSnapshot,
		Options{Interval: time.Minute, RenotifyAfter: 24 * time.Hour})

	w.Check(now)
	w.Check(now.Add(2 * time.Minute))
	w.Check(now.Add(4 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("unchanged findings mailed %d times, want 1", len(rec.mails))
	}
	w.Check(now.Add(25 * time.Hour))
	if len(rec.mails) != 2 {
		t.Fatalf("renotify did not fire after 24h: %d mails", len(rec.mails))
	}
}

// TestWatcherClearsItsMemoryWhenAPassIsClean is the property that keeps this
// watcher from rebuilding the defect it detects: nothing it remembers may
// outlive the condition. A carrier that drifts again after being cleared is
// NEWS, not a suppressed duplicate.
func TestWatcherClearsItsMemoryWhenAPassIsClean(t *testing.T) {
	rec := &recorder{}
	carriers := driftedCarriers()
	drifted := true
	snap := func(repo string, number int) (Snapshot, error) {
		if drifted {
			return driftedSnapshot(repo, number)
		}
		return cleanSnapshot(repo, number)
	}
	w := newTestWatcher(rec, carriers, snap, Options{Interval: time.Minute, RenotifyAfter: 24 * time.Hour})

	w.Check(now)
	if len(rec.mails) != 1 {
		t.Fatalf("first drift did not mail")
	}
	drifted = false
	w.Check(now.Add(2 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("a clean pass mailed: %d", len(rec.mails))
	}
	drifted = true
	w.Check(now.Add(4 * time.Minute))
	if len(rec.mails) != 2 {
		t.Fatalf("a drift that RECURRED after clearing was suppressed as unchanged: %d mails",
			len(rec.mails))
	}
	if got := rec.types(); len(got) != 3 || got[1] != "carrier_drift_watch_clean" {
		t.Fatalf("events = %v, want the clean pass recorded", got)
	}
}

// TestWatcherEscalatesPerFindingAndNotPerSet. A new finding arriving alongside
// an old one must not reset the old one's clock — that is exactly the bug that
// lets the forgotten case stay forgotten.
func TestWatcherEscalatesPerFindingAndNotPerSet(t *testing.T) {
	rec := &recorder{}
	old := carrier("mg-old", "drellem2/pogo#159", "gated", 30*24*time.Hour)
	fresh := carrier("mg-new", "drellem2/pogo#160", "gated", 30*24*time.Hour)
	carriers := []Carrier{old}
	w := New(Options{
		Enabled: true, Mail: rec.send, Emit: rec.emit,
		Snapshot: driftedSnapshot, Interval: time.Minute,
		RenotifyAfter: 24 * time.Hour, EscalateAfter: 72 * time.Hour,
		Source: func() ([]Carrier, int, error) { return carriers, len(carriers), nil },
	})

	w.Check(now)
	if len(rec.mails) != 1 || rec.mails[0].to != DefaultNotifyTo {
		t.Fatalf("first mail: %+v", rec.mails)
	}

	// A second finding appears two days in. It must not reset mg-old's clock.
	carriers = []Carrier{old, fresh}
	w.Check(now.Add(48 * time.Hour))

	// Four days after mg-old was first seen, it escalates — even though the SET
	// has only existed for two.
	before := len(rec.mails)
	w.Check(now.Add(96 * time.Hour))
	escalated := rec.mails[before:]
	if len(escalated) != 2 {
		t.Fatalf("escalation sent %d mails, want 2 (coordinator + human)", len(escalated))
	}
	var sawHuman bool
	for _, m := range escalated {
		if m.to == DefaultEscalateTo {
			sawHuman = true
			if !strings.Contains(m.body, "ESCALATED") {
				t.Errorf("escalated body does not say so:\n%s", m.body)
			}
		}
	}
	if !sawHuman {
		t.Fatalf("nothing reached %s: %+v", DefaultEscalateTo, escalated)
	}
}

// TestWatcherRecordsAStoreReadFailureInsteadOfReportingClean. Zero carriers and
// an unreadable store both render as "nothing to report", and letting them
// collapse is this package's own subject matter one level up.
func TestWatcherRecordsAStoreReadFailureInsteadOfReportingClean(t *testing.T) {
	rec := &recorder{}
	w := New(Options{
		Enabled: true, Mail: rec.send, Emit: rec.emit, Snapshot: driftedSnapshot,
		Source: func() ([]Carrier, int, error) { return nil, 0, errors.New("mg list: store unreadable") },
	})
	w.Check(now)

	if len(rec.mails) != 0 {
		t.Fatalf("a failed store read mailed a report: %+v", rec.mails)
	}
	got := rec.types()
	if len(got) != 1 || got[0] != "carrier_drift_watch_error" {
		t.Fatalf("events = %v, want the failure recorded rather than a clean pass", got)
	}
}

// TestWatcherRecordsAMailFailure. A notice that reaches nobody is this package's
// own failure mode one level up, so it must not be silent.
func TestWatcherRecordsAMailFailure(t *testing.T) {
	rec := &recorder{fail: errors.New("no such mailbox")}
	w := newTestWatcher(rec, driftedCarriers(), driftedSnapshot, Options{})
	w.Check(now)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 1 {
		t.Fatalf("events = %d", len(rec.events))
	}
	if _, ok := rec.events[0].Details["mail_error_"+DefaultNotifyTo]; !ok {
		t.Fatalf("a failed delivery left no trace: %v", rec.events[0].Details)
	}
}

// TestWatcherNeedsAllThreeSeams: a runner missing any of its dependencies must
// be inert rather than half-working. In particular a nil Snapshot is a runner
// that cannot re-read, which is the defect and not the detector.
func TestWatcherNeedsAllThreeSeams(t *testing.T) {
	rec := &recorder{}
	for name, opts := range map[string]Options{
		"no source":   {Enabled: true, Mail: rec.send, Emit: rec.emit, Snapshot: driftedSnapshot},
		"no snapshot": {Enabled: true, Mail: rec.send, Emit: rec.emit, Source: func() ([]Carrier, int, error) { return driftedCarriers(), 1, nil }},
		"no mail":     {Enabled: true, Emit: rec.emit, Snapshot: driftedSnapshot, Source: func() ([]Carrier, int, error) { return driftedCarriers(), 1, nil }},
		"disabled":    {Mail: rec.send, Emit: rec.emit, Snapshot: driftedSnapshot, Source: func() ([]Carrier, int, error) { return driftedCarriers(), 1, nil }},
	} {
		rec.mails = nil
		rec.events = nil
		New(opts).Check(now)
		if len(rec.mails) != 0 || len(rec.events) != 0 {
			t.Errorf("%s: watcher acted anyway (%d mails, %d events)", name, len(rec.mails), len(rec.events))
		}
	}
	// A nil watcher is the shape pogod holds when the detector is not armed.
	var nilWatcher *Watcher
	nilWatcher.Check(now)
}

// TestFingerprintIgnoresAgeButNotComposition. A finding whose age ticks up by an
// hour is the same finding; folding the age in would make every sample "changed"
// and turn the renotify policy into no policy at all.
func TestFingerprintIgnoresAgeButNotComposition(t *testing.T) {
	carriers := driftedCarriers()
	snaps := table(t, map[string]Snapshot{"drellem2/pogo#159": openSnap(16*24*time.Hour, false, 0)})

	a := Detect(carriers, snaps, now, Windows{})
	b := Detect(carriers, snaps, now.Add(3*time.Hour), Windows{})
	if a.fingerprint(false) != b.fingerprint(false) {
		t.Fatal("three hours of ageing changed the fingerprint")
	}
	if a.fingerprint(false) == a.fingerprint(true) {
		t.Fatal("crossing the escalation threshold did not change the fingerprint — " +
			"a slow renotify window would postpone escalation by a day")
	}

	more := append(carriers, carrier("mg-new", "drellem2/pogo#160", "gated", 4*24*time.Hour))
	snaps2 := table(t, map[string]Snapshot{
		"drellem2/pogo#159": openSnap(16*24*time.Hour, false, 0),
		"drellem2/pogo#160": openSnap(16*24*time.Hour, false, 0),
	})
	if a.fingerprint(false) == Detect(more, snaps2, now, Windows{}).fingerprint(false) {
		t.Fatal("a new finding did not change the fingerprint")
	}
}

// TestWatcherMailStatesItsOwnCoverage. The mailed body carries the same coverage
// line the CLI prints, and it must NAME the statuses that were scanned. A body
// reading "in status []" would be the report asserting it examined nothing —
// this detector making, about itself, exactly the claim it exists to catch.
func TestWatcherMailStatesItsOwnCoverage(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(rec, driftedCarriers(), driftedSnapshot,
		Options{Statuses: []string{"available", "claimed", "pending"}})
	w.Check(now)

	if len(rec.mails) != 1 {
		t.Fatalf("mails = %d", len(rec.mails))
	}
	body := rec.mails[0].body
	if !strings.Contains(body, "in status [available claimed pending]") {
		t.Fatalf("mail body does not name the scanned statuses:\n%s", body)
	}
	if strings.Contains(body, "in status []") {
		t.Fatalf("mail body reports empty coverage:\n%s", body)
	}
}
