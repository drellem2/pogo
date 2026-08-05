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
	to     []string
	err    error
}

func (m *mailRecorder) send(to, from, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if from != mailFrom {
		panic("unexpected mail sender: " + from)
	}
	m.sent = append(m.sent, subject)
	m.bodies = append(m.bodies, body)
	m.to = append(m.to, to)
	return m.err
}

func (m *mailRecorder) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// recipients reports every mailbox mailed so far, so a test can assert who was
// NOT reached as easily as who was.
func (m *mailRecorder) recipients() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.to...)
}

func (m *mailRecorder) mailed(box string) bool {
	for _, to := range m.recipients() {
		if to == box {
			return true
		}
	}
	return false
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
// mail, naming the carrier and the issue.
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

// ROUTING POSITIVE CONTROL (mg-b586). With NotifyTo unset — the configuration
// every deployment gets by default — a miss must reach the FLEET mailbox and
// must NOT reach `human`. A default that has only ever been observed with an
// explicit override in place has not been tested, so this test deliberately
// sets no routing options at all.
func TestDefaultRoutingGoesToTheFleetNotHuman(t *testing.T) {
	mail := &mailRecorder{}
	w := New(Options{
		Enabled: true,
		Source:  func() ([]Carrier, error) { return []Carrier{carrier07ba()}, nil },
		Lookup:  openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})

	w.Check(time.Now())

	if mail.count() != 1 {
		t.Fatalf("want exactly 1 mail, got %d to %v", mail.count(), mail.recipients())
	}
	// A bare literal, not DefaultNotifyTo: comparing against the constant would
	// make this test FOLLOW a future flip back to `human` instead of catching it.
	if got := mail.recipients()[0]; got != "pm-pogo" {
		t.Errorf("default recipient is %q, want the fleet mailbox %q", got, "pm-pogo")
	}
	// The load-bearing half: a fleet workflow miss is not a human's decision.
	if mail.mailed("human") {
		t.Error("the default routing mailed `human` — a teardown miss is a fleet " +
			"workflow failure, and unbatched mail a human cannot action gets the sender filtered")
	}
}

func TestNotifyToOverrideIsHonoured(t *testing.T) {
	mail := &mailRecorder{}
	w := New(Options{
		Enabled: true, NotifyTo: "mayor",
		Source: func() ([]Carrier, error) { return []Carrier{carrier07ba()}, nil },
		Lookup: openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})
	w.Check(time.Now())
	if got := mail.recipients(); len(got) != 1 || got[0] != "mayor" {
		t.Errorf("recipients = %v, want [mayor]", got)
	}
}

// A finding the fleet does not clear is a different fact from the finding
// itself: at that point "the fleet is not handling this" IS a human's to know.
func TestAStalledFindingEscalatesToHuman(t *testing.T) {
	mail := &mailRecorder{}
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: 24 * time.Hour,
		EscalateAfter: 72 * time.Hour,
		Source:        func() ([]Carrier, error) { return []Carrier{carrier07ba()}, nil },
		Lookup:        openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	w.Check(start) // first sighting: fleet only
	if mail.mailed("human") {
		t.Fatal("escalated on the first sighting — the fleet has not failed at anything yet")
	}

	w.Check(start.Add(25 * time.Hour)) // still inside the window
	if mail.mailed("human") {
		t.Fatal("escalated after 25h with escalate_after=72h")
	}

	w.Check(start.Add(73 * time.Hour))
	if !mail.mailed("human") {
		t.Errorf("a finding unresolved for 73h never reached `human`: recipients %v", mail.recipients())
	}
	// Escalation COPIES the human; it does not silently redirect away from the
	// fleet, which still owns the remedy.
	if !mail.mailed("pm-pogo") {
		t.Error("escalation dropped the fleet mailbox")
	}
	last := mail.bodies[len(mail.bodies)-1]
	if !strings.Contains(last, "ESCALATED") {
		t.Error("the escalated notice does not say why it reached a human")
	}
}

// A run that saw NOTHING must not reset the escalation clock on a miss it
// could not see (mg-dd22).
//
// This is the masking failure in its most expensive form. The escalation clock
// is what guarantees a finding the fleet ignores eventually reaches a human;
// trackAges rebuilds its map from the CURRENT findings, so a blind run would
// drop every clock and hand each miss a fresh 72 hours on its return. On a box
// whose network blips every few days (mg-0ffc), that is not a corner case — it
// is a standing mechanism for keeping a forgotten finding forgotten.
//
// Two carriers, because it takes two to tell a broken instrument from a broken
// carrier. One is a real miss; the middle run goes blind on both.
func TestABlindRunDoesNotResetAnEscalationClock(t *testing.T) {
	mail := &mailRecorder{}
	clean := Carrier{ID: "mg-clean", Status: "done", Repo: "drellem2/pogo", Number: 93}
	var blind bool
	var mu sync.Mutex
	lookup := func(_ string, number int) (IssueState, error) {
		mu.Lock()
		defer mu.Unlock()
		if blind {
			return StateUnknown, errors.New("error connecting to api.github.com")
		}
		if number == 89 {
			return StateOpen, nil // the real miss
		}
		return StateClosed, nil
	}
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: 24 * time.Hour,
		EscalateAfter: 72 * time.Hour,
		Source:        func() ([]Carrier, error) { return []Carrier{carrier07ba(), clean}, nil },
		Lookup:        lookup, Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	w.Check(start) // the miss is first seen here; this is the clock that must survive
	if !mail.mailed("pm-pogo") {
		t.Fatal("the miss did not reach the fleet on first sighting")
	}

	mu.Lock()
	blind = true
	mu.Unlock()
	w.Check(start.Add(1 * time.Hour)) // the blip: no verdict for either carrier
	last := mail.bodies[len(mail.bodies)-1]
	if !strings.Contains(last, "SUSPECTED INSTRUMENT FAILURE") {
		t.Errorf("a run that measured nothing must say so, not report a count:\n%s", last)
	}
	if mail.mailed("human") {
		t.Fatal("a one-hour-old blip escalated to a human")
	}

	mu.Lock()
	blind = false
	mu.Unlock()
	w.Check(start.Add(73 * time.Hour))
	if !mail.mailed("human") {
		t.Errorf("A BLIP RESET THE ESCALATION CLOCK: the miss has been outstanding 73h and never "+
			"reached `human`; recipients %v", mail.recipients())
	}
}

// Escalation ages each FINDING, not the finding-set. A new miss arriving beside
// an old one changes the set fingerprint, and if that reset the clock the
// stalest finding — the exact one escalation exists for — would never age.
func TestANewFindingDoesNotResetAnOldOnesEscalationClock(t *testing.T) {
	mail := &mailRecorder{}
	carriers := []Carrier{carrier07ba()}
	var mu sync.Mutex
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: 24 * time.Hour,
		EscalateAfter: 72 * time.Hour,
		Source: func() ([]Carrier, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]Carrier(nil), carriers...), nil
		},
		Lookup: openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	w.Check(start)

	// A second miss shows up two days in — long after the first started aging.
	mu.Lock()
	carriers = append(carriers, Carrier{ID: "mg-9999", Status: "done", Repo: "drellem2/pogo", Number: 91})
	mu.Unlock()
	w.Check(start.Add(48 * time.Hour))
	if mail.mailed("human") {
		t.Fatal("escalated at 48h")
	}

	w.Check(start.Add(73 * time.Hour))
	if !mail.mailed("human") {
		t.Error("the newer finding reset the older one's clock — the stalest finding never escalated")
	}
}

// A cleared finding that later recurs starts its stall clock fresh: the fleet
// DID act the first time, so it has not failed to act on the new one.
func TestResolvedFindingClearsItsEscalationClock(t *testing.T) {
	mail := &mailRecorder{}
	state := StateOpen
	var mu sync.Mutex
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: 24 * time.Hour,
		EscalateAfter: 72 * time.Hour,
		Source:        func() ([]Carrier, error) { return []Carrier{carrier07ba()}, nil },
		Lookup: func(string, int) (IssueState, error) {
			mu.Lock()
			defer mu.Unlock()
			return state, nil
		},
		Mail: mail.send, Emit: func(events.Event) {},
	})

	start := time.Now()
	w.Check(start)

	mu.Lock()
	state = StateClosed
	mu.Unlock()
	w.Check(start.Add(2 * time.Minute)) // resolved

	mu.Lock()
	state = StateOpen
	mu.Unlock()
	w.Check(start.Add(4 * time.Minute)) // recurred, clock restarts here
	w.Check(start.Add(71 * time.Hour))  // 71h since recurrence, not 71h since start

	if mail.mailed("human") {
		t.Errorf("a recurrence escalated on the ORIGINAL sighting's clock: %v", mail.recipients())
	}
}

// Escalation must have an off switch distinguishable from an unset field, or a
// config omitting the key would silently disable it.
func TestNegativeEscalateAfterDisablesEscalation(t *testing.T) {
	mail := &mailRecorder{}
	w := New(Options{
		Enabled: true, Interval: time.Minute, RenotifyAfter: time.Hour,
		EscalateAfter: -1,
		Source:        func() ([]Carrier, error) { return []Carrier{carrier07ba()}, nil },
		Lookup:        openLookup, Mail: mail.send, Emit: func(events.Event) {},
	})
	start := time.Now()
	w.Check(start)
	w.Check(start.Add(30 * 24 * time.Hour))
	if mail.mailed("human") {
		t.Error("escalation disabled, but `human` was mailed anyway")
	}
	if mail.count() == 0 {
		t.Error("disabling escalation also silenced the fleet notice")
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

// A no-verdict finding alone must still page: a detector that cannot see is
// itself the finding. An expired credential is instrument-class (mg-03ea's
// class), so since mg-dd22 the notice must say the carrier was NOT CHECKED
// rather than presenting the non-answer as a determination.
func TestANoVerdictFindingAlonePages(t *testing.T) {
	mail := &mailRecorder{}
	w := testWatcher(t, []Carrier{carrier07ba()}, func(string, int) (IssueState, error) {
		return StateUnknown, errors.New("gh auth login required: no GH_TOKEN")
	}, mail)
	w.Check(time.Now())
	if mail.count() != 1 {
		t.Fatalf("a no-verdict finding did not page: %d mails", mail.count())
	}
	if !strings.Contains(mail.bodies[0], "NOT CHECKED") {
		t.Errorf("notice must say the carrier was never checked:\n%s", mail.bodies[0])
	}
}

// A genuine indeterminate — the instrument worked, the answer is unusable —
// must still page AND must still be described as a determination that is not
// clean. This is the arm that keeps the mg-dd22 split from quietly turning into
// "everything is an instrument failure now".
func TestGenuineIndeterminateAlonePages(t *testing.T) {
	mail := &mailRecorder{}
	w := testWatcher(t, []Carrier{carrier07ba()}, func(string, int) (IssueState, error) {
		return StateUnknown, errors.New("GraphQL: Could not resolve to an Issue with the number of 89")
	}, mail)
	w.Check(time.Now())
	if mail.count() != 1 {
		t.Fatalf("a genuine indeterminate did not page: %d mails", mail.count())
	}
	body := mail.bodies[0]
	if !strings.Contains(body, "NOT clean") {
		t.Errorf("notice must say indeterminate is not clean:\n%s", body)
	}
	if strings.Contains(body, "NOT CHECKED") {
		t.Errorf("a working lookup must not be reported as an instrument failure:\n%s", body)
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
