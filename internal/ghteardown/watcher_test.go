package ghteardown

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

type mailRecorder struct {
	mu     sync.Mutex
	sent   []string
	bodies []string
	err    error
}

func (m *mailRecorder) send(to, from, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if to != mailTo || from != mailFrom {
		panic("unexpected mail routing: " + to + "/" + from)
	}
	m.sent = append(m.sent, subject)
	m.bodies = append(m.bodies, body)
	return m.err
}

func (m *mailRecorder) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func testWatcher(t *testing.T, carriers []Carrier, lookup LookupFunc, mail *mailRecorder) *Watcher {
	t.Helper()
	return New(Options{
		Enabled: true,
		Source:  func() ([]Carrier, error) { return carriers, nil },
		Lookup:  lookup,
		Mail:    mail.send,
		Emit:    func(events.Event) {},
	})
}

func openLookup(string, int) (IssueState, error)   { return StateOpen, nil }
func closedLookup(string, int) (IssueState, error) { return StateClosed, nil }

// The runner's positive control: a done carrier with an open issue must produce
// mail to `human`, naming the carrier and the issue.
func TestWatcherMailsOnATeardownMiss(t *testing.T) {
	mail := &mailRecorder{}
	w := testWatcher(t, []Carrier{carrier07ba()}, openLookup, mail)

	w.Check(time.Now())

	if mail.count() != 1 {
		t.Fatalf("want 1 mail, got %d", mail.count())
	}
	if !strings.Contains(mail.sent[0], "drellem2/pogo#89") {
		t.Errorf("subject %q does not name the issue", mail.sent[0])
	}
	if !strings.Contains(mail.bodies[0], "mg-07ba") {
		t.Error("body does not name the carrier")
	}
	// The notice must say it did not act, so nobody assumes the issue is handled.
	if !strings.Contains(mail.bodies[0], "REPORT-ONLY") {
		t.Error("notice must state that pogod did not close or comment")
	}
}

func TestWatcherSilentWhenClean(t *testing.T) {
	mail := &mailRecorder{}
	w := testWatcher(t, []Carrier{carrier07ba()}, closedLookup, mail)
	w.Check(time.Now())
	if mail.count() != 0 {
		t.Fatalf("clean scan mailed %d time(s): %v", mail.count(), mail.sent)
	}
}

func TestWatcherDisabledIsANoOp(t *testing.T) {
	mail := &mailRecorder{}
	w := New(Options{
		Enabled: false,
		Source:  func() ([]Carrier, error) { t.Fatal("disabled watcher read the store"); return nil, nil },
		Mail:    mail.send,
		Emit:    func(events.Event) {},
	})
	w.Check(time.Now())
	if mail.count() != 0 {
		t.Error("disabled watcher mailed")
	}
}

// The coarse throttle: the heartbeat ticks every ~30s, and the runner must
// sample at most once per interval no matter how often it is called.
func TestWatcherThrottlesToItsInterval(t *testing.T) {
	mail := &mailRecorder{}
	var samples int
	var mu sync.Mutex
	w := New(Options{
		Enabled:  true,
		Interval: time.Hour,
		Source: func() ([]Carrier, error) {
			mu.Lock()
			samples++
			mu.Unlock()
			return []Carrier{carrier07ba()}, nil
		},
		Lookup: openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	for i := 0; i < 120; i++ { // an hour of 30s ticks
		w.Check(start.Add(time.Duration(i) * 30 * time.Second))
	}
	if samples != 1 {
		t.Errorf("sampled %d times in one interval, want 1 — each sample costs a network call per carrier", samples)
	}

	w.Check(start.Add(2 * time.Hour))
	if samples != 2 {
		t.Errorf("after the interval elapsed, samples = %d, want 2", samples)
	}
}

// An unchanged finding set must not mail on every interval — a detector that
// repeats itself hourly gets filtered, and a filtered detector is worse than
// none because it also manufactures the feeling of coverage.
func TestUnchangedFindingsDoNotRemailImmediately(t *testing.T) {
	mail := &mailRecorder{}
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: 24 * time.Hour,
		Source: func() ([]Carrier, error) { return []Carrier{carrier07ba()}, nil },
		Lookup: openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	for i := 0; i < 10; i++ {
		w.Check(start.Add(time.Duration(i) * 2 * time.Minute))
	}
	if mail.count() != 1 {
		t.Errorf("unchanged findings mailed %d times, want 1", mail.count())
	}
}

// ...but an unresolved miss must NOT go permanently silent. Falling quiet is
// how #89 sat open for four days; the miss has to keep costing someone something.
func TestUnchangedFindingsRemailAfterRenotifyWindow(t *testing.T) {
	mail := &mailRecorder{}
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: 24 * time.Hour,
		Source: func() ([]Carrier, error) { return []Carrier{carrier07ba()}, nil },
		Lookup: openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	w.Check(start)
	w.Check(start.Add(25 * time.Hour))
	if mail.count() != 2 {
		t.Errorf("an unresolved miss went silent: mailed %d times over 25h, want 2", mail.count())
	}
}

// A NEW miss appearing alongside an existing one is news and must mail at once,
// rather than waiting out the renotify window.
func TestNewFindingMailsImmediately(t *testing.T) {
	mail := &mailRecorder{}
	carriers := []Carrier{carrier07ba()}
	var mu sync.Mutex
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: 24 * time.Hour,
		Source: func() ([]Carrier, error) {
			mu.Lock()
			defer mu.Unlock()
			return carriers, nil
		},
		Lookup: openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	w.Check(start)

	mu.Lock()
	carriers = append(carriers, Carrier{ID: "mg-9999", Status: "done", Repo: "drellem2/pogo", Number: 91})
	mu.Unlock()

	w.Check(start.Add(2 * time.Minute))
	if mail.count() != 2 {
		t.Errorf("a new miss did not page immediately: mailed %d times, want 2", mail.count())
	}
}

// A miss that is resolved and later recurs must be news again, not suppressed
// as "the same fingerprint we already mailed".
func TestResolvedThenRecurringMissMailsAgain(t *testing.T) {
	mail := &mailRecorder{}
	state := StateOpen
	var mu sync.Mutex
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: 24 * time.Hour,
		Source: func() ([]Carrier, error) { return []Carrier{carrier07ba()}, nil },
		Lookup: func(string, int) (IssueState, error) {
			mu.Lock()
			defer mu.Unlock()
			return state, nil
		},
		Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	w.Check(start) // miss -> mail

	mu.Lock()
	state = StateClosed
	mu.Unlock()
	w.Check(start.Add(2 * time.Minute)) // resolved -> silent

	mu.Lock()
	state = StateOpen
	mu.Unlock()
	w.Check(start.Add(4 * time.Minute)) // recurred -> must mail again

	if mail.count() != 2 {
		t.Errorf("a recurring miss was suppressed: mailed %d times, want 2", mail.count())
	}
}

// An unreadable store must emit an error event rather than passing as a clean
// scan — the detector going blind has to be visible.
func TestUnreadableStoreIsVisible(t *testing.T) {
	mail := &mailRecorder{}
	var got []events.Event
	var mu sync.Mutex
	w := New(Options{
		Enabled: true,
		Source:  func() ([]Carrier, error) { return nil, errors.New("mg: store unreadable") },
		Lookup:  openLookup, Mail: mail.send,
		Emit: func(e events.Event) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		},
	})
	w.Check(time.Now())

	if mail.count() != 0 {
		t.Error("an unreadable store must not produce a findings mail")
	}
	if len(got) != 1 || got[0].EventType != "gh_teardown_watch_error" {
		t.Fatalf("want a gh_teardown_watch_error event, got %+v", got)
	}
}

// Indeterminate findings alone must still page: a detector that cannot see is
// itself the finding.
func TestIndeterminateAlonePages(t *testing.T) {
	mail := &mailRecorder{}
	w := testWatcher(t, []Carrier{carrier07ba()}, func(string, int) (IssueState, error) {
		return StateUnknown, errors.New("gh auth expired")
	}, mail)
	w.Check(time.Now())
	if mail.count() != 1 {
		t.Fatalf("indeterminate findings did not page: %d mails", mail.count())
	}
	if !strings.Contains(mail.bodies[0], "NOT clean") {
		t.Error("notice must say indeterminate is not clean")
	}
}

// Declared-open carriers must never page on their own — that is the entire
// point of the annotation.
func TestDeclaredOpenAloneDoesNotPage(t *testing.T) {
	mail := &mailRecorder{}
	c := carrier07ba()
	c.DeclaredOpenReason = "open on purpose, waiting on the reporter"
	w := testWatcher(t, []Carrier{c}, openLookup, mail)
	w.Check(time.Now())
	if mail.count() != 0 {
		t.Errorf("a declared-open carrier paged: %v", mail.sent)
	}
}
