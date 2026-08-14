package ghintake

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

type mailRecorder struct {
	mu       sync.Mutex
	subjects []string
	bodies   []string
	to       []string
	err      error
}

func (m *mailRecorder) send(to, from, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if from != mailFrom {
		panic("unexpected mail sender: " + from)
	}
	m.subjects = append(m.subjects, subject)
	m.bodies = append(m.bodies, body)
	m.to = append(m.to, to)
	return m.err
}

func (m *mailRecorder) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subjects)
}

// recipients reports every mailbox mailed so far, so a test can assert who was
// NOT reached as easily as who was.
func (m *mailRecorder) recipients() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.to...)
}

type eventRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (e *eventRecorder) emit(ev events.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func (e *eventRecorder) types() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		out = append(out, ev.EventType)
	}
	return out
}

func (e *eventRecorder) last() events.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.events) == 0 {
		return events.Event{}
	}
	return e.events[len(e.events)-1]
}

// uncarriedInv is an inventory in which #99 is open with no carrier.
func uncarriedInv(age time.Duration) Inventory {
	return inv([]Issue{issue("drellem2/pogo", 99, age)}, nil)
}

// carriedInv is the same inventory with the carrier filed.
func carriedInv(age time.Duration) Inventory {
	return inv([]Issue{issue("drellem2/pogo", 99, age)},
		[]CarrierRef{carrier("mg-d764", "drellem2/pogo#99")})
}

func newWatcher(t *testing.T, src SourceFunc, mail *mailRecorder, ev *eventRecorder, tweak func(*Options)) *Watcher {
	t.Helper()
	opts := Options{Enabled: true, Source: src, Mail: mail.send, Emit: ev.emit}
	if tweak != nil {
		tweak(&opts)
	}
	return New(opts)
}

// The transition into the uncarried state mails the COORDINATOR — the only agent
// that can file a carrier — and nobody else.
func TestFiresToTheCoordinatorOnTransition(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) { return uncarriedInv(2 * time.Hour), nil }, mail, ev, nil)

	w.Check(scanTime)

	if mail.count() != 1 {
		t.Fatalf("want exactly 1 mail on transition, got %d", mail.count())
	}
	if got := mail.recipients(); len(got) != 1 || got[0] != DefaultNotifyTo {
		t.Fatalf("mailed %v, want only %q", got, DefaultNotifyTo)
	}
	if DefaultNotifyTo != "mayor" {
		t.Fatalf("the coordinator is the actor for an uncarried issue; NotifyTo default is %q", DefaultNotifyTo)
	}
	if !strings.Contains(mail.subjects[0], "drellem2/pogo#99") {
		t.Errorf("subject = %q, want the issue named", mail.subjects[0])
	}
	if !strings.Contains(mail.bodies[0], "pogo check-intake") {
		t.Errorf("body must tell the reader how to re-derive the report; got:\n%s", mail.bodies[0])
	}
	if !strings.Contains(mail.bodies[0], "REPORT-ONLY") {
		t.Errorf("body must say nothing was filed on anyone's behalf; got:\n%s", mail.bodies[0])
	}
	if got := ev.types(); len(got) != 1 || got[0] != "gh_intake_watch_fired" {
		t.Errorf("events = %v, want one gh_intake_watch_fired", got)
	}
}

// Rate-limit by CONDITION, not by message: an unchanged finding set stays quiet
// until RenotifyAfter, so an issue uncarried for a week does not produce a mail
// per sample.
func TestUnchangedFindingsStayQuietUntilRenotify(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) { return uncarriedInv(2 * time.Hour), nil }, mail, ev,
		func(o *Options) {
			o.Interval = time.Minute
			o.RenotifyAfter = 24 * time.Hour
			o.EscalateAfter = -1 // escalation off, so this test measures the renotify policy alone
		})

	w.Check(scanTime)
	if mail.count() != 1 {
		t.Fatalf("first sample must mail, got %d", mail.count())
	}
	// A whole day of samples at one-minute intervals: 1440 opportunities.
	for i := 1; i <= 1439; i++ {
		w.Check(scanTime.Add(time.Duration(i) * time.Minute))
	}
	if mail.count() != 1 {
		t.Fatalf("unchanged findings must stay quiet: %d mails across 1440 samples", mail.count())
	}
	// Past the renotify interval it speaks again — a miss nobody cleared must keep
	// costing someone something.
	w.Check(scanTime.Add(24*time.Hour + time.Minute))
	if mail.count() != 2 {
		t.Fatalf("want a renotify past the interval, got %d mails", mail.count())
	}
}

// A NEW uncarried issue is news even inside the quiet window.
func TestANewFindingMailsImmediately(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	state := uncarriedInv(2 * time.Hour)
	w := newWatcher(t, func() (Inventory, error) { return state, nil }, mail, ev,
		func(o *Options) { o.Interval = time.Minute; o.EscalateAfter = -1 })

	w.Check(scanTime)
	if mail.count() != 1 {
		t.Fatalf("setup: want 1 mail, got %d", mail.count())
	}
	state = inv([]Issue{
		issue("drellem2/pogo", 99, 2*time.Hour),
		issue("drellem2/pogo", 100, 90*time.Minute),
	}, nil)
	w.Check(scanTime.Add(2 * time.Minute))
	if mail.count() != 2 {
		t.Fatalf("a newly uncarried issue must mail at once, got %d mails", mail.count())
	}
	if !strings.Contains(mail.subjects[1], "drellem2/pogo#100") {
		t.Errorf("second subject = %q, want the new issue named", mail.subjects[1])
	}
}

// Filing the carrier clears the state, and a LATER uncarried issue is news again
// rather than being suppressed as "unchanged".
func TestClearingResetsTheQuietWindow(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	state := uncarriedInv(2 * time.Hour)
	w := newWatcher(t, func() (Inventory, error) { return state, nil }, mail, ev,
		func(o *Options) { o.Interval = time.Minute; o.EscalateAfter = -1 })

	w.Check(scanTime)
	if mail.count() != 1 {
		t.Fatalf("setup: %d mails", mail.count())
	}

	// mayor files the carrier.
	state = carriedInv(2 * time.Hour)
	w.Check(scanTime.Add(2 * time.Minute))
	if mail.count() != 1 {
		t.Fatalf("a clean scan must not mail, got %d", mail.count())
	}
	if got := ev.types(); got[len(got)-1] != "gh_intake_watch_clean" {
		t.Errorf("a clean scan must still emit an event, got %v", got)
	}

	// It regresses.
	state = uncarriedInv(2 * time.Hour)
	w.Check(scanTime.Add(4 * time.Minute))
	if mail.count() != 2 {
		t.Fatalf("a recurrence must mail again, got %d", mail.count())
	}
}

// Escalation: once ONE uncarried issue has persisted past EscalateAfter, `human`
// is copied — because at that point "the coordinator is not handling this" is the
// news. This is the answer to "what happens if mayor is down".
func TestEscalatesToHumanWhenTheCoordinatorDoesNotClearIt(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) { return uncarriedInv(2 * time.Hour), nil }, mail, ev,
		func(o *Options) {
			o.Interval = 15 * time.Minute
			o.RenotifyAfter = 24 * time.Hour // deliberately longer than the escalation window
			o.EscalateAfter = 4 * time.Hour
		})

	w.Check(scanTime)
	if got := mail.recipients(); len(got) != 1 || got[0] != "mayor" {
		t.Fatalf("first notice must go to the coordinator alone, got %v", got)
	}

	// Just short of the threshold: still quiet, still coordinator-only.
	w.Check(scanTime.Add(3*time.Hour + 45*time.Minute))
	if mail.count() != 1 {
		t.Fatalf("inside the escalation window the notice must stay quiet, got %d mails", mail.count())
	}

	// Past it: mails at once despite the 24h renotify interval, and copies human.
	w.Check(scanTime.Add(4*time.Hour + time.Minute))
	got := mail.recipients()
	if len(got) != 3 {
		t.Fatalf("want 3 deliveries after escalation (1 + mayor + human), got %v", got)
	}
	if got[1] != "mayor" || got[2] != DefaultEscalateTo {
		t.Fatalf("escalated recipients = %v, want [mayor %s]", got[1:], DefaultEscalateTo)
	}
	if !strings.Contains(mail.bodies[2], "ESCALATED") {
		t.Errorf("the escalated body must say so; got:\n%s", mail.bodies[2])
	}
	if ev.last().Details["escalated"] != true {
		t.Errorf("the event must record the escalation, got %+v", ev.last().Details)
	}
}

// Escalation is per ISSUE, not per finding-set: a new uncarried issue arriving
// alongside an old one must not reset the old one's clock. That reset is exactly
// the bug that would let the forgotten case stay forgotten.
func TestANewFindingDoesNotResetAnOldOnesEscalationClock(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	state := uncarriedInv(2 * time.Hour)
	w := newWatcher(t, func() (Inventory, error) { return state, nil }, mail, ev,
		func(o *Options) { o.Interval = time.Minute; o.EscalateAfter = 4 * time.Hour })

	w.Check(scanTime)

	// Halfway through #99's escalation window, a second issue appears.
	state = inv([]Issue{
		issue("drellem2/pogo", 99, 2*time.Hour),
		issue("drellem2/pogo", 100, 90*time.Minute),
	}, nil)
	w.Check(scanTime.Add(2 * time.Hour))
	if mail.count() != 2 {
		t.Fatalf("setup: the new issue should have mailed, got %d", mail.count())
	}

	// #99 crosses 4h from ITS first sighting. If the set change had reset the
	// clock, nothing would escalate until 6h.
	w.Check(scanTime.Add(4*time.Hour + time.Minute))
	if got := mail.recipients(); got[len(got)-1] != DefaultEscalateTo {
		t.Fatalf("the OLD finding's clock must survive a set change; recipients = %v", got)
	}
}

// A negative EscalateAfter is the documented off switch, and must be
// distinguishable from an unset field.
func TestNegativeEscalateAfterDisablesEscalation(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) { return uncarriedInv(2 * time.Hour), nil }, mail, ev,
		func(o *Options) { o.Interval = time.Minute; o.RenotifyAfter = time.Hour; o.EscalateAfter = -1 })

	for i := 0; i < 10; i++ {
		w.Check(scanTime.Add(time.Duration(i) * time.Hour))
	}
	for _, to := range mail.recipients() {
		if to == DefaultEscalateTo {
			t.Fatalf("escalation must be off; recipients = %v", mail.recipients())
		}
	}
}

// An issue inside the grace window must not mail at all — otherwise the detector
// fires on every new issue and gets muted before the run that matters.
func TestFreshIssuesDoNotMail(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) { return uncarriedInv(46 * time.Second), nil }, mail, ev, nil)
	w.Check(scanTime)
	if mail.count() != 0 {
		t.Fatalf("a fresh issue must not mail, got %d: %v", mail.count(), mail.subjects)
	}
	if got := ev.types(); len(got) != 1 || got[0] != "gh_intake_watch_clean" {
		t.Errorf("events = %v, want one gh_intake_watch_clean", got)
	}
}

// A repo we could not read is reported to the coordinator, not swallowed. This is
// the arm that fails toward noticing: every issue we DID see was carried.
func TestUnreadableRepoMails(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) {
		in := carriedInv(48 * time.Hour)
		in.RepoErrors = []RepoError{{Repo: "drellem2/macguffin", Detail: "gh: HTTP 401"}}
		return in, nil
	}, mail, ev, nil)

	w.Check(scanTime)
	if mail.count() != 1 {
		t.Fatalf("an unreadable repo must mail, got %d", mail.count())
	}
	if !strings.Contains(mail.bodies[0], "drellem2/macguffin") {
		t.Errorf("body must name the unreadable repo; got:\n%s", mail.bodies[0])
	}
}

// A credential that goes MISSING between samples is news, even though the set of
// unreadable repos is byte-identical (mg-fb29). The same two repo names now mean
// a different fault with a different remedy — `gh auth login` rather than "check
// the network" — and without the credential state in the fingerprint the 24-hour
// renotify window would sit on that transition for a day.
func TestACredentialTransitionMailsImmediately(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	cred := CredentialPresent
	w := newWatcher(t, func() (Inventory, error) {
		in := carriedInv(48 * time.Hour)
		in.RepoErrors = []RepoError{
			{Repo: "drellem2/macguffin", Detail: "gh: request failed"},
			{Repo: "drellem2/pogo", Detail: "gh: request failed"},
		}
		in.Credential, in.CredentialSource = cred, "shell"
		return in, nil
	}, mail, ev, func(o *Options) { o.Interval = time.Minute })

	w.Check(scanTime)
	if mail.count() != 1 {
		t.Fatalf("setup: want the first sample to mail, got %d", mail.count())
	}

	// Identical findings, identical credential: quiet, as before.
	w.Check(scanTime.Add(2 * time.Minute))
	if mail.count() != 1 {
		t.Fatalf("an unchanged sample must stay quiet, got %d mails", mail.count())
	}

	// Identical findings, credential gone: news.
	cred = CredentialMissing
	w.Check(scanTime.Add(4 * time.Minute))
	if mail.count() != 2 {
		t.Fatalf("the credential going missing must mail at once even though the repo set "+
			"is unchanged, got %d mails", mail.count())
	}
	if !strings.Contains(mail.subjects[1], "NO GitHub credential") {
		t.Errorf("the second subject does not name the new fault: %q", mail.subjects[1])
	}
	if mail.subjects[0] == mail.subjects[1] {
		t.Error("both subjects read alike, so the transition is invisible to the reader " +
			"it was mailed to")
	}
}

// A blind carrier scan mails too — a detector that cannot see is itself the
// finding — and it must NOT arrive as a wall of per-issue findings.
func TestBlindStoreMailsAsBlindnessNotAsFindings(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) {
		in := inv([]Issue{
			issue("drellem2/pogo", 88, 72*time.Hour),
			issue("drellem2/pogo", 99, 10*time.Hour),
		}, nil)
		in.ItemsScanned = 0
		return in, nil
	}, mail, ev, nil)

	w.Check(scanTime)
	if mail.count() != 1 {
		t.Fatalf("a blind scan must mail, got %d", mail.count())
	}
	if !strings.Contains(mail.subjects[0], "BLIND SCAN") {
		t.Errorf("subject = %q, want it to announce blindness", mail.subjects[0])
	}
	if strings.Contains(mail.bodies[0], "UNCARRIED") {
		t.Errorf("a blind scan must not report per-issue findings; got:\n%s", mail.bodies[0])
	}
	if ev.last().Details["blind_store"] != true {
		t.Errorf("the event must record the blindness, got %+v", ev.last().Details)
	}
}

// A carrier-scan error is not a clean scan: it emits an error event and mails
// nobody, because a report built on a failed scan would be fiction.
func TestSourceErrorEmitsAndDoesNotMail(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) { return Inventory{}, errors.New("mg: no such store") }, mail, ev, nil)

	w.Check(scanTime)
	if mail.count() != 0 {
		t.Fatalf("a failed scan must not mail a report, got %v", mail.subjects)
	}
	if got := ev.types(); len(got) != 1 || got[0] != "gh_intake_watch_error" {
		t.Fatalf("events = %v, want one gh_intake_watch_error", got)
	}
}

// A notice that reaches nobody is this package's own failure mode one level up,
// so the delivery failure is recorded rather than dropped.
func TestMailFailureIsRecordedInTheEvent(t *testing.T) {
	mail, ev := &mailRecorder{err: errors.New("maildir full")}, &eventRecorder{}
	w := newWatcher(t, func() (Inventory, error) { return uncarriedInv(2 * time.Hour), nil }, mail, ev, nil)

	w.Check(scanTime)
	if _, ok := ev.last().Details["mail_error_mayor"]; !ok {
		t.Fatalf("a failed delivery must be recorded, got %+v", ev.last().Details)
	}
}

// The coarse interval means one sample per interval, never one per heartbeat tick.
func TestIntervalThrottlesSamples(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	var samples int
	w := newWatcher(t, func() (Inventory, error) {
		samples++
		return carriedInv(2 * time.Hour), nil
	}, mail, ev, func(o *Options) { o.Interval = 15 * time.Minute })

	// Heartbeat ticks every 30 seconds for an hour.
	for i := 0; i < 120; i++ {
		w.Check(scanTime.Add(time.Duration(i) * 30 * time.Second))
	}
	if samples != 4 {
		t.Errorf("samples = %d, want 4 (one per 15m across an hour)", samples)
	}
}

// A slow or failing sample still consumes its slot — the interval is recorded
// before the sample runs, so a wedged source cannot turn into a tight loop.
func TestAFailingSampleStillConsumesItsSlot(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	var samples int
	w := newWatcher(t, func() (Inventory, error) {
		samples++
		return Inventory{}, errors.New("boom")
	}, mail, ev, func(o *Options) { o.Interval = 15 * time.Minute })

	for i := 0; i < 10; i++ {
		w.Check(scanTime.Add(time.Duration(i) * time.Minute))
	}
	if samples != 1 {
		t.Errorf("samples = %d, want 1 — a failing sample must not retry every tick", samples)
	}
}

// A disarmed or half-built watcher must be inert rather than panicking on a nil
// seam, because it rides pogod's heartbeat.
func TestDisarmedWatcherIsInert(t *testing.T) {
	mail, ev := &mailRecorder{}, &eventRecorder{}
	src := func() (Inventory, error) { return uncarriedInv(2 * time.Hour), nil }

	var nilW *Watcher
	nilW.Check(scanTime) // must not panic

	for _, w := range []*Watcher{
		New(Options{Enabled: false, Source: src, Mail: mail.send, Emit: ev.emit}),
		New(Options{Enabled: true, Mail: mail.send, Emit: ev.emit}),
		New(Options{Enabled: true, Source: src, Emit: ev.emit}),
	} {
		w.Check(scanTime)
	}
	if mail.count() != 0 || len(ev.types()) != 0 {
		t.Errorf("a disarmed watcher must do nothing; mails=%d events=%v", mail.count(), ev.types())
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	w := New(Options{Enabled: true})
	if w.interval != DefaultInterval || w.grace != DefaultGrace ||
		w.renotifyAfter != DefaultRenotifyAfter || w.escalateAfter != DefaultEscalateAfter {
		t.Errorf("defaults not applied: %+v", w)
	}
	if w.notifyTo != DefaultNotifyTo || w.escalateTo != DefaultEscalateTo {
		t.Errorf("routing defaults not applied: notifyTo=%q escalateTo=%q", w.notifyTo, w.escalateTo)
	}
	if w.Grace() != DefaultGrace {
		t.Errorf("Grace() = %s, want %s", w.Grace(), DefaultGrace)
	}
}
