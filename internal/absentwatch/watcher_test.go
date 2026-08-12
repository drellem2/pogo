package absentwatch

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/events"
)

type sentMail struct{ to, from, subject, body string }

// recorder collects the watcher's only two side-effect channels.
type recorder struct {
	mu     sync.Mutex
	mails  []sentMail
	evs    []events.Event
	mailer func(to string) error
}

func (r *recorder) mail(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mails = append(r.mails, sentMail{to, from, subject, body})
	if r.mailer != nil {
		return r.mailer(to)
	}
	return nil
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
}

func (r *recorder) toList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.mails))
	for _, m := range r.mails {
		out = append(out, m.to)
	}
	return out
}

func (r *recorder) eventTypes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.evs))
	for _, e := range r.evs {
		out = append(out, e.EventType)
	}
	return out
}

func (r *recorder) eventsOfType(t string) []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.evs {
		if e.EventType == t {
			out = append(out, e)
		}
	}
	return out
}

func (r *recorder) lastBody() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.mails) == 0 {
		return ""
	}
	return r.mails[len(r.mails)-1].body
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// staticSource yields a fixed snapshot, so every test in this file is a pure
// function of its fixture — no registry, no prompt tree, no ~/.pogo.
func staticSource(absent ...Finding) SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		return Snapshot{
			Now:        now,
			Configured: len(absent) + 3,
			Present:    3,
			Absent:     absent,
		}, nil
	}
}

func supervised(name string) Finding {
	return Finding{Name: name, Identity: "crew-" + name, Class: ClassSupervised, RestartOnCrash: true}
}

// onDemand is the mg-7d20 shape: doctor's own frontmatter, both flags false.
func onDemand(name string) Finding {
	return Finding{Name: name, Identity: "crew-" + name, Class: ClassOnDemand, RestartOnCrash: false}
}

func newTestWatcher(t *testing.T, rec *recorder, src SourceFunc, mutate func(*Options)) *Watcher {
	t.Helper()
	opts := Options{
		Enabled:       true,
		Source:        src,
		Mail:          rec.mail,
		Emit:          rec.emit,
		Interval:      time.Minute,
		HoldDown:      15 * time.Minute,
		DormantAfter:  24 * time.Hour,
		RenotifyAfter: 12 * time.Hour,
		EscalateAfter: 48 * time.Hour,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return New(opts)
}

// TestSupervisedAbsenceAnnouncedAfterHoldDown is the fault an auto_start agent
// represents: pogod's own desired state says it should be running.
func TestSupervisedAbsenceAnnouncedAfterHoldDown(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(supervised("pm-pogo")), nil)

	t0 := time.Date(2026, 8, 10, 17, 14, 0, 0, time.UTC)
	w.Check(t0)
	if len(rec.mails) != 0 {
		t.Fatalf("must not announce inside the hold-down, mailed %d", len(rec.mails))
	}
	if !has(rec.eventTypes(), EventPending) {
		t.Errorf("entering the hold-down must be visible in the event log, got %v", rec.eventTypes())
	}

	w.Check(t0.Add(16 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("expected 1 announcement after the hold-down, got %d", len(rec.mails))
	}
	m := rec.mails[0]
	if m.to != DefaultNotifyTo || m.from != mailFrom {
		t.Errorf("routing = %s <- %s, want %s <- %s", m.to, m.from, DefaultNotifyTo, mailFrom)
	}
	if !strings.Contains(m.subject, "pm-pogo") {
		t.Errorf("the subject must name the agent, got %q", m.subject)
	}
	if !strings.Contains(m.body, "auto_start = true") {
		t.Errorf("the body must say what the frontmatter asked for, got:\n%s", m.body)
	}
}

// TestOnDemandAbsenceIsPatient is the anti-wolf rule and the reason this
// detector is usable at all: doctor being off for an afternoon is its ordinary
// state, and a detector that mails about it gets filtered.
func TestOnDemandAbsenceIsPatient(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(onDemand("doctor")), nil)

	t0 := time.Date(2026, 8, 10, 17, 14, 23, 0, time.UTC)
	for _, d := range []time.Duration{0, time.Hour, 6 * time.Hour, 23 * time.Hour} {
		w.Check(t0.Add(d))
		if len(rec.mails) != 0 {
			t.Fatalf("mailed about an on-demand agent after %s; it must wait out DormantAfter", d)
		}
	}

	// mg-7d20's timeline: down 2026-08-10T17:14:23Z, restarted by hand ~08-12
	// 14:00Z. A 24h threshold announces it on 08-11, 21 hours before anyone
	// noticed.
	w.Check(t0.Add(24*time.Hour + time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("expected an announcement past DormantAfter, got %d", len(rec.mails))
	}
	if !strings.Contains(rec.mails[0].body, "auto_start = false") {
		t.Errorf("the body must name the on-demand class, got:\n%s", rec.mails[0].body)
	}
	if !strings.Contains(rec.mails[0].body, "restart_on_crash = false") {
		t.Errorf("the body must warn that a start will not stick, got:\n%s", rec.mails[0].body)
	}
}

// TestUnclassifiableUsesTheShortHoldDown: a prompt that exists and cannot be
// read is an unknown, and an unknown must not buy the quieter answer.
func TestUnclassifiableUsesTheShortHoldDown(t *testing.T) {
	rec := &recorder{}
	f := Finding{Name: "garbled", Identity: "crew-garbled", Class: ClassUnclassifiable, Reason: "bad bool"}
	w := newTestWatcher(t, rec, staticSource(f), nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("an unclassifiable absence must use the SUPERVISED hold-down, mailed %d", len(rec.mails))
	}
	if !strings.Contains(rec.mails[0].body, "bad bool") {
		t.Errorf("the body must carry the parse error, got:\n%s", rec.mails[0].body)
	}
}

// TestAgentThatComesBackResetsTheClock: a flap must restart the hold-down rather
// than accumulate toward it.
func TestAgentThatComesBackResetsTheClock(t *testing.T) {
	rec := &recorder{}
	var absent []Finding
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 4, Present: 4 - len(absent), Absent: absent}, nil
	}
	w := newTestWatcher(t, rec, src, nil)

	t0 := time.Now().UTC()
	absent = []Finding{supervised("flappy")}
	w.Check(t0)
	absent = nil
	w.Check(t0.Add(10 * time.Minute))
	absent = []Finding{supervised("flappy")}
	w.Check(t0.Add(20 * time.Minute))
	if len(rec.mails) != 0 {
		t.Fatalf("the clock must restart after the agent came back, mailed %d", len(rec.mails))
	}
	w.Check(t0.Add(36 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("expected 1 announcement once the SECOND absence outlived the hold-down, got %d", len(rec.mails))
	}
}

// TestUnchangedRosterStaysQuiet: ages advance every tick, so a detector that
// fingerprinted them would mail every interval and get filtered.
func TestUnchangedRosterStaysQuiet(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(supervised("pm-pogo")), nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	w.Check(t0.Add(30 * time.Minute))
	w.Check(t0.Add(2 * time.Hour))
	if len(rec.mails) != 1 {
		t.Fatalf("an unchanged roster must stay quiet until RenotifyAfter, mailed %d", len(rec.mails))
	}
	w.Check(t0.Add(13 * time.Hour))
	if len(rec.mails) != 2 {
		t.Fatalf("expected a renotify past 12h, mailed %d", len(rec.mails))
	}
}

// TestChangedRosterMailsImmediately: a new name is news, whatever the renotify
// clock says.
func TestChangedRosterMailsImmediately(t *testing.T) {
	rec := &recorder{}
	absent := []Finding{supervised("pm-pogo")}
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 5, Present: 5 - len(absent), Absent: absent}, nil
	}
	w := newTestWatcher(t, rec, src, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	absent = []Finding{supervised("pm-pogo"), supervised("architect")}
	w.Check(t0.Add(17 * time.Minute)) // architect enters its hold-down
	w.Check(t0.Add(40 * time.Minute)) // architect confirmed -> roster changed
	if len(rec.mails) != 2 {
		t.Fatalf("a changed roster must mail immediately, mailed %d", len(rec.mails))
	}
	if !strings.Contains(rec.mails[1].subject, "architect") {
		t.Errorf("the second subject must name the new agent, got %q", rec.mails[1].subject)
	}
}

// TestEpisodeClearsWhenEveryoneIsBack pins the close: an all-clear mail plus the
// generic incident_episode_cleared event carrying the roster.
func TestEpisodeClearsWhenEveryoneIsBack(t *testing.T) {
	rec := &recorder{}
	absent := []Finding{supervised("pm-pogo")}
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 5, Present: 5 - len(absent), Parked: 0, Absent: absent}, nil
	}
	w := newTestWatcher(t, rec, src, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	absent = nil
	w.Check(t0.Add(30 * time.Minute))

	if len(rec.mails) != 2 {
		t.Fatalf("expected alarm + all-clear, got %d", len(rec.mails))
	}
	if !strings.Contains(rec.mails[1].subject, "roster complete again") {
		t.Errorf("clear subject = %q", rec.mails[1].subject)
	}
	cleared := rec.eventsOfType(IncidentEpisodeClearedEvent)
	if len(cleared) != 1 {
		t.Fatalf("expected 1 incident_episode_cleared, got %d (%v)", len(cleared), rec.eventTypes())
	}
	if got := cleared[0].Details["kind"]; got != EpisodeKind {
		t.Errorf("details.kind = %v, want %q", got, EpisodeKind)
	}

	// And a later recurrence is news again rather than a suppressed repeat.
	absent = []Finding{supervised("pm-pogo")}
	w.Check(t0.Add(40 * time.Minute))
	w.Check(t0.Add(60 * time.Minute))
	if len(rec.mails) != 3 {
		t.Fatalf("a recurrence must be announced afresh, mailed %d", len(rec.mails))
	}
}

// TestAbsentCoordinatorEscalatesImmediately is this detector's routing rule, and
// it is stronger than deafwatch's: the mayor is not merely unwakeable here, it
// is not running, so a mail to it has no reader at all.
func TestAbsentCoordinatorEscalatesImmediately(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(supervised("mayor")), nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))

	tos := rec.toList()
	if !has(tos, DefaultNotifyTo) || !has(tos, DefaultEscalateTo) {
		t.Fatalf("an absent coordinator must reach both mailboxes on the FIRST notice, got %v", tos)
	}
	if !strings.Contains(rec.lastBody(), "ESCALATED IMMEDIATELY") {
		t.Errorf("the escalation must say why, got:\n%s", rec.lastBody())
	}
	fired := rec.eventsOfType(EventFired)
	if len(fired) != 1 || fired[0].Details["coordinator"] != true {
		t.Errorf("the event must record the coordinator case, got %+v", fired)
	}
}

// TestAgedFindingEscalates: a finding the fleet has had two days to fix and has
// not reaches the human mailbox.
func TestAgedFindingEscalates(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(supervised("pm-pogo")), func(o *Options) {
		o.RenotifyAfter = time.Hour
	})

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	if has(rec.toList()[1:], DefaultEscalateTo) {
		t.Fatal("must not escalate on age before EscalateAfter")
	}
	w.Check(t0.Add(49 * time.Hour))
	if !has(rec.toList(), DefaultEscalateTo) {
		t.Fatalf("expected an age escalation past 48h, recipients %v", rec.toList())
	}
	if !strings.Contains(rec.lastBody(), "ESCALATED:") {
		t.Errorf("the escalation must say why, got:\n%s", rec.lastBody())
	}
}

// TestClearReachesEveryoneWhoWasAlarmed: an all-clear that goes to fewer
// mailboxes than the alarm leaves someone holding an open incident forever.
func TestClearReachesEveryoneWhoWasAlarmed(t *testing.T) {
	rec := &recorder{}
	absent := []Finding{supervised("mayor")}
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 5, Present: 5 - len(absent), Absent: absent}, nil
	}
	w := newTestWatcher(t, rec, src, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	absent = nil
	w.Check(t0.Add(30 * time.Minute))

	var clears []string
	for _, m := range rec.mails {
		if strings.Contains(m.subject, "roster complete again") {
			clears = append(clears, m.to)
		}
	}
	if !has(clears, DefaultNotifyTo) || !has(clears, DefaultEscalateTo) {
		t.Fatalf("the all-clear must reach everyone the alarm did, got %v", clears)
	}
}

// TestSourceErrorIsNotACleanRoster: a blind detector that renders as a quiet one
// is this lineage's founding bug.
func TestSourceErrorIsNotACleanRoster(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, func(now time.Time) (Snapshot, error) {
		return Snapshot{}, errors.New("prompt tree unreadable")
	}, nil)

	w.Check(time.Now().UTC())
	if len(rec.mails) != 0 {
		t.Errorf("a failed sample must not mail, mailed %d", len(rec.mails))
	}
	errs := rec.eventsOfType(EventError)
	if len(errs) != 1 {
		t.Fatalf("expected 1 absent_watch_error, got %v", rec.eventTypes())
	}
	if !strings.Contains(errs[0].Details["error"].(string), "prompt tree unreadable") {
		t.Errorf("the error must be carried, got %+v", errs[0].Details)
	}
}

// TestEmptyRosterIsAnErrorNotAnAllClear: zero configured agents means there was
// nothing to compare. It must not close an open episode.
func TestEmptyRosterIsAnErrorNotAnAllClear(t *testing.T) {
	rec := &recorder{}
	configured := 4
	absent := []Finding{supervised("pm-pogo")}
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: configured, Absent: absent}, nil
	}
	w := newTestWatcher(t, rec, src, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("setup: expected the alarm, got %d", len(rec.mails))
	}

	configured, absent = 0, nil
	w.Check(t0.Add(30 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("an empty prompt tree must not read as an all-clear, mailed %d", len(rec.mails))
	}
	if len(rec.eventsOfType(EventError)) != 1 {
		t.Errorf("an empty roster must be recorded as an error, got %v", rec.eventTypes())
	}
}

// TestThrottleHonoursInterval: Check is wired to every heartbeat tick, so all
// but the first tick of each interval must be a no-op.
func TestThrottleHonoursInterval(t *testing.T) {
	rec := &recorder{}
	var samples int
	w := newTestWatcher(t, rec, func(now time.Time) (Snapshot, error) {
		samples++
		return Snapshot{Now: now, Configured: 3, Present: 3}, nil
	}, func(o *Options) { o.Interval = 5 * time.Minute })

	t0 := time.Now().UTC()
	for i := 0; i < 10; i++ {
		w.Check(t0.Add(time.Duration(i) * time.Minute))
	}
	if samples != 2 {
		t.Fatalf("expected 2 samples across 10 minutes at a 5m interval, got %d", samples)
	}
}

// TestDisabledAndUnwiredWatchersAreInert.
func TestDisabledAndUnwiredWatchersAreInert(t *testing.T) {
	rec := &recorder{}
	cases := map[string]*Watcher{
		"disabled": New(Options{Enabled: false, Source: staticSource(supervised("x")), Mail: rec.mail, Emit: rec.emit}),
		"no source": New(Options{Enabled: true, Mail: rec.mail, Emit: rec.emit,
			HoldDown: -1}),
		"no mail": New(Options{Enabled: true, Source: staticSource(supervised("x")), Emit: rec.emit,
			HoldDown: -1}),
	}
	var nilW *Watcher
	cases["nil"] = nilW
	for name, w := range cases {
		w.Check(time.Now().UTC())
		if len(rec.mails) != 0 || len(rec.evs) != 0 {
			t.Fatalf("%s watcher must be inert, got %d mails / %d events", name, len(rec.mails), len(rec.evs))
		}
	}
}

// TestMailFailureIsRecorded: a fault that was detected and could not be reported
// is this ticket's own bug, one level up.
func TestMailFailureIsRecorded(t *testing.T) {
	rec := &recorder{mailer: func(to string) error { return errors.New("maildir full") }}
	w := newTestWatcher(t, rec, staticSource(supervised("pm-pogo")), nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))

	fired := rec.eventsOfType(EventFired)
	if len(fired) != 1 {
		t.Fatalf("expected 1 absent_watch_fired, got %v", rec.eventTypes())
	}
	if _, ok := fired[0].Details["mail_error_"+DefaultNotifyTo]; !ok {
		t.Errorf("a failed send must be recorded in the event, got %+v", fired[0].Details)
	}
}

// TestBodyNamesTheDenominatorAndTheReadSurface. A reader's first question after
// "who is missing" is "out of how many", and their second is "where do I look".
func TestBodyNamesTheDenominatorAndTheReadSurface(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 11, Present: 9, Parked: 1,
			Absent: []Finding{onDemand("doctor")}}, nil
	}, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(25 * time.Hour))
	body := rec.lastBody()
	for _, want := range []string{"11 configured", "9 running", "1 parked", "pogo agent roster", "REPORT-ONLY"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

// TestEpisodeKindMatchesContract pins the one string this package must not
// diverge on: the generic incident_episode_cleared event type that mg-e0f6's
// notifier matches (mg-55b2).
func TestEpisodeKindMatchesContract(t *testing.T) {
	if IncidentEpisodeClearedEvent != claude.IncidentEpisodeClearedEvent {
		t.Fatalf("event type drifted: %q vs %q", IncidentEpisodeClearedEvent, claude.IncidentEpisodeClearedEvent)
	}
}
