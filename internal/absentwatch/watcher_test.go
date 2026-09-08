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

// ---------------------------------------------------------------------------
// mg-c86d: DELIBERATELY ABSENT. `auto_start = false` is a declaration, not a
// symptom, and the detector had no way to say so — it escalated `doctor` and
// `representative` to the mayor for 132 unbroken hours over a state both agents
// were configured into on purpose. The tests below pin the partition: a fault
// still runs the full episode machinery, and a declared absence is said once.
// ---------------------------------------------------------------------------

// declaredMails returns the one-time notices, which are identifiable from the
// outside by their subject — that is the property the fix is FOR, since a
// subject line is the part a reader filters and forwards on.
func declaredMails(rec *recorder) []sentMail {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []sentMail
	for _, m := range rec.mails {
		if strings.Contains(m.subject, "DELIBERATELY ABSENT") {
			out = append(out, m)
		}
	}
	return out
}

// TestDeclaredAbsenceIsReportedExactlyOnce is the ticket in one test: two
// on-demand agents, absent for a week, and the mailbox sees one notice.
func TestDeclaredAbsenceIsReportedExactlyOnce(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(onDemand("doctor"), onDemand("representative")), nil)

	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	w.Check(t0)
	if len(rec.mails) != 0 {
		t.Fatalf("must wait out DormantAfter, mailed %d", len(rec.mails))
	}
	w.Check(t0.Add(25 * time.Hour))
	if len(rec.mails) != 1 {
		t.Fatalf("expected exactly 1 notice past DormantAfter, got %d", len(rec.mails))
	}

	// mg-c86d's own timeline: the live finding had run 132h when the ticket was
	// raised, and 156h when the agents were last measured absent. Not one more
	// byte of mail in any of it.
	for _, d := range []time.Duration{26 * time.Hour, 37 * time.Hour, 96 * time.Hour,
		132 * time.Hour, 156 * time.Hour, 21 * 24 * time.Hour} {
		w.Check(t0.Add(d))
	}
	if len(rec.mails) != 1 {
		t.Fatalf("a declared absence must be said ONCE; after 21 days it had been said %d times:\n%v",
			len(rec.mails), rec.toList())
	}
}

// TestDeclaredAbsenceNeverEscalatesOnAge: `human` must never be copied on an
// agent that is off because its config says to be off. This is the escalation
// that ran for 132 hours and could not clear.
func TestDeclaredAbsenceNeverEscalatesOnAge(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(onDemand("doctor"), onDemand("representative")),
		func(o *Options) { o.RenotifyAfter = time.Hour; o.EscalateAfter = time.Hour })

	t0 := time.Now().UTC()
	w.Check(t0)
	for h := 25; h <= 200; h += 5 {
		w.Check(t0.Add(time.Duration(h) * time.Hour))
	}
	if has(rec.toList(), DefaultEscalateTo) {
		t.Fatalf("a declared absence must never age into an escalation, recipients %v", rec.toList())
	}
	for _, m := range rec.mails {
		if strings.Contains(m.body, "ESCALATED:") {
			t.Fatalf("the age-escalation preamble reached a declared absence:\n%s", m.body)
		}
	}
	// Positive control on the same instrument: a SUPERVISED absence at the same
	// settings does escalate, so the negative above is the partition working and
	// not the escalation being switched off wholesale.
	ctl := &recorder{}
	cw := newTestWatcher(t, ctl, staticSource(supervised("pm-pogo")),
		func(o *Options) { o.RenotifyAfter = time.Hour; o.EscalateAfter = time.Hour })
	cw.Check(t0)
	cw.Check(t0.Add(16 * time.Minute))
	cw.Check(t0.Add(3 * time.Hour))
	if !has(ctl.toList(), DefaultEscalateTo) {
		t.Fatalf("positive control failed: a supervised absence must still escalate, recipients %v", ctl.toList())
	}
}

// TestDeclaredAbsenceOpensNoEpisode. An episode whose only members can never
// return on their own is an incident nobody can close — and closing it is the
// only thing that makes the NEXT alarm legible.
func TestDeclaredAbsenceOpensNoEpisode(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(onDemand("doctor")), nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(25 * time.Hour))
	w.Check(t0.Add(50 * time.Hour))

	if got := len(rec.eventsOfType(EventFired)); got != 0 {
		t.Errorf("a declared absence must emit no %s, got %d", EventFired, got)
	}
	if got := len(rec.eventsOfType(EventDeclared)); got != 1 {
		t.Fatalf("expected exactly 1 %s, got %d (%v)", EventDeclared, got, rec.eventTypes())
	}
	for _, m := range rec.mails {
		if strings.Contains(m.subject, "roster complete again") {
			t.Errorf("no episode was opened, so nothing may claim to close one: %q", m.subject)
		}
	}
	d := rec.eventsOfType(EventDeclared)[0]
	if d.Details["escalated"] != false {
		t.Errorf("declared event must record escalated=false, got %+v", d.Details)
	}
}

// TestDeclaredAbsenceIsNewsAgainAfterItComesBack is the mg-f341 guard. Quieting
// a declared absence must not MUTE it: the ledger clears when the agent returns,
// so the next absence is announced afresh.
func TestDeclaredAbsenceIsNewsAgainAfterItComesBack(t *testing.T) {
	rec := &recorder{}
	var absent []Finding
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 4, Present: 4 - len(absent), Absent: absent}, nil
	}
	w := newTestWatcher(t, rec, src, nil)

	t0 := time.Now().UTC()
	absent = []Finding{onDemand("doctor")}
	w.Check(t0)
	w.Check(t0.Add(25 * time.Hour))
	if len(declaredMails(rec)) != 1 {
		t.Fatalf("expected the first notice, got %d", len(declaredMails(rec)))
	}
	absent = nil
	w.Check(t0.Add(26 * time.Hour))
	absent = []Finding{onDemand("doctor")}
	w.Check(t0.Add(27 * time.Hour))
	w.Check(t0.Add(30 * time.Hour))
	if len(declaredMails(rec)) != 1 {
		t.Fatalf("the second absence must serve its own DormantAfter, mailed %d", len(declaredMails(rec)))
	}
	w.Check(t0.Add(52 * time.Hour))
	if len(declaredMails(rec)) != 2 {
		t.Fatalf("a declared absence that returned and went away again is news, mailed %d", len(declaredMails(rec)))
	}
}

// TestDeclaredNoticeSaysWhichItIs. The ticket's second half: the mail must
// distinguish "nothing will bring it back" as a fault from the same fact as a
// design property, since one is alarming and the other is descriptive.
func TestDeclaredNoticeSaysWhichItIs(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 11, Present: 9, Parked: 1,
			Absent: []Finding{onDemand("doctor")}}, nil
	}, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(25 * time.Hour))

	m := rec.mails[0]
	for _, want := range []string{"DELIBERATELY ABSENT", "no action owed"} {
		if !strings.Contains(m.subject, want) {
			t.Errorf("subject missing %q: %q", want, m.subject)
		}
	}
	for _, unwanted := range []string{"NOT RUNNING", "nothing else reports"} {
		if strings.Contains(m.subject, unwanted) {
			t.Errorf("a declared notice must not borrow the fault subject's vocabulary (%q): %q", unwanted, m.subject)
		}
	}
	for _, want := range []string{
		"auto_start = false", "DECLARATION",
		"said ONCE per absence", "will NOT escalate",
		// mg-f341: quieted, never hidden. The denominator and the read surface
		// keep it findable.
		"11 configured", "9 running", "1 parked", "1 absent",
		"pogo agent roster", "REPORT-ONLY",
	} {
		if !strings.Contains(m.body, want) {
			t.Errorf("declared body missing %q:\n%s", want, m.body)
		}
	}
	if strings.Contains(m.body, "ESCALATED") {
		t.Errorf("declared body must carry no escalation preamble:\n%s", m.body)
	}
}

// TestFaultMailCarriesDeclaredAbsencesAsContext: the quiet half must still
// appear where the loud half is read, or the fault mail's own denominator counts
// absences its body never names.
func TestFaultMailCarriesDeclaredAbsencesAsContext(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 8, Present: 5, Parked: 0,
			Absent: []Finding{onDemand("doctor"), supervised("pm-pogo"), onDemand("representative")}}, nil
	}, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute)) // pm-pogo confirmed; the on-demand pair is still dormant

	fired := rec.eventsOfType(EventFired)
	if len(fired) != 1 {
		t.Fatalf("expected the supervised fault to fire, got %d (%v)", len(fired), rec.eventTypes())
	}
	if got := fired[0].Details["count"]; got != 1 {
		t.Errorf("the fault count must not include declared absences, got %v", got)
	}
	if !strings.Contains(rec.lastBody(), "3 absent") {
		t.Errorf("the denominator must count every absence, not just the faults:\n%s", rec.lastBody())
	}

	// Once the pair crosses DormantAfter they appear as context in the fault
	// mail — named, not merely counted — and the subject stays about the fault.
	w.Check(t0.Add(25 * time.Hour))
	var faultBody string
	for _, m := range rec.mails {
		if strings.Contains(m.subject, "pm-pogo") {
			faultBody = m.body
		}
	}
	if !strings.Contains(faultBody, "ALSO ABSENT, BY DECLARATION") {
		t.Errorf("the fault mail must carry the declared set as context:\n%s", faultBody)
	}
	for _, want := range []string{"doctor", "representative"} {
		if !strings.Contains(faultBody, want) {
			t.Errorf("context section missing %q:\n%s", want, faultBody)
		}
	}
	for _, m := range rec.mails {
		if strings.Contains(m.subject, "doctor") && strings.Contains(m.subject, "NOT RUNNING") {
			t.Errorf("a declared absence must never reach a fault subject: %q", m.subject)
		}
	}
}

// TestFaultFingerprintIgnoresDeclaredChurn: a declared absence appearing or
// clearing must not re-mail a fault roster that did not change. Feeding it into
// the fingerprint would put the un-clearable set back on the mailing clock by a
// different route — the same defect, one level over.
func TestFaultFingerprintIgnoresDeclaredChurn(t *testing.T) {
	rec := &recorder{}
	absent := []Finding{supervised("pm-pogo")}
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 6, Present: 6 - len(absent), Absent: absent}, nil
	}
	// RenotifyAfter is pushed past the whole window on purpose: this test is
	// about the FINGERPRINT, and a 12h renotify firing at the 25h mark would
	// account for a second mail all by itself and hide what is being measured.
	w := newTestWatcher(t, rec, src, func(o *Options) { o.RenotifyAfter = 30 * 24 * time.Hour })

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("expected the fault announcement, got %d", len(rec.mails))
	}
	absent = []Finding{supervised("pm-pogo"), onDemand("doctor")}
	w.Check(t0.Add(20 * time.Minute))
	w.Check(t0.Add(25 * time.Hour)) // doctor crosses DormantAfter: one declared notice
	if len(declaredMails(rec)) != 1 {
		t.Fatalf("expected 1 declared notice, got %d", len(declaredMails(rec)))
	}
	absent = []Finding{supervised("pm-pogo")}
	w.Check(t0.Add(26 * time.Hour))
	w.Check(t0.Add(27 * time.Hour))

	var faultMails int
	for _, m := range rec.mails {
		if strings.Contains(m.subject, "NOT RUNNING") {
			faultMails++
		}
	}
	if faultMails != 1 {
		t.Fatalf("the fault roster never changed, so it must have mailed once; mailed %d\n%v",
			faultMails, rec.toList())
	}
}

// TestEpisodeClosesOverRemainingDeclaredAbsences: the fault cleared, so the
// episode closes — but "roster complete again" over a machine with two on-demand
// agents still off is the alarm's own wrong sentence, reassuring instead of
// alarming. The clear must say what is still absent and why that is fine.
func TestEpisodeClosesOverRemainingDeclaredAbsences(t *testing.T) {
	rec := &recorder{}
	absent := []Finding{supervised("pm-pogo"), onDemand("doctor")}
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 6, Present: 6 - len(absent), Absent: absent}, nil
	}
	w := newTestWatcher(t, rec, src, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(25 * time.Hour)) // pm-pogo confirmed; doctor crosses DormantAfter
	absent = []Finding{onDemand("doctor")}
	w.Check(t0.Add(26 * time.Hour))

	cleared := rec.eventsOfType(IncidentEpisodeClearedEvent)
	if len(cleared) != 1 {
		t.Fatalf("the fault cleared, so the episode must close: got %d (%v)", len(cleared), rec.eventTypes())
	}
	var clear string
	for _, m := range rec.mails {
		if strings.Contains(m.subject, "roster complete again") {
			clear = m.body
		}
	}
	if clear == "" {
		t.Fatal("expected an all-clear mail")
	}
	if !strings.Contains(clear, "ALSO ABSENT, BY DECLARATION") || !strings.Contains(clear, "doctor") {
		t.Errorf("the all-clear must name what is still absent by declaration:\n%s", clear)
	}
	if !strings.Contains(clear, "1 absent") {
		t.Errorf("the all-clear must not claim 0 absent while doctor is off:\n%s", clear)
	}
}

// TestAbsentOnDemandCoordinatorStillEscalates is the one rule that survives the
// partition. If the mailbox this notice goes to is ITSELF the absent on-demand
// agent, the notice has no reader — and that is not a matter of patience, so it
// copies EscalateTo on its single firing.
func TestAbsentOnDemandCoordinatorStillEscalates(t *testing.T) {
	rec := &recorder{}
	w := newTestWatcher(t, rec, staticSource(onDemand("mayor")), nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(25 * time.Hour))

	tos := rec.toList()
	if !has(tos, DefaultNotifyTo) || !has(tos, DefaultEscalateTo) {
		t.Fatalf("an absent coordinator must reach both mailboxes even when declared, got %v", tos)
	}
	if !strings.Contains(rec.lastBody(), "ESCALATED IMMEDIATELY") {
		t.Errorf("the escalation must say why:\n%s", rec.lastBody())
	}
	d := rec.eventsOfType(EventDeclared)
	if len(d) != 1 || d[0].Details["coordinator"] != true {
		t.Fatalf("the declared event must record the coordinator case, got %+v", d)
	}
	// And it is still said once: the coordinator rule changes the RECIPIENTS,
	// not the cadence.
	w.Check(t0.Add(80 * time.Hour))
	if len(declaredMails(rec)) != 2 {
		t.Fatalf("expected 2 mails (one notice to each mailbox) and no repeats, got %d",
			len(declaredMails(rec)))
	}
}

// TestUnclassifiableIsNotDeclared: a prompt we could not read declared nothing.
// Folding it in with on-demand would buy silence with an unknown, which is the
// bug this whole lineage exists to stop.
func TestUnclassifiableIsNotDeclared(t *testing.T) {
	f := Finding{Name: "garbled", Class: ClassUnclassifiable, Reason: "bad bool"}
	if f.Declared() {
		t.Fatal("an unreadable prompt declared nothing and must not be treated as a declaration")
	}
	if !onDemand("doctor").Declared() {
		t.Fatal("auto_start = false IS the declaration")
	}
	if supervised("pm-pogo").Declared() {
		t.Fatal("auto_start = true is a desired state, not a declared absence")
	}
	faults, declared := partition([]Finding{onDemand("doctor"), f, supervised("pm-pogo")})
	if len(faults) != 2 || len(declared) != 1 || declared[0].Name != "doctor" {
		t.Fatalf("partition = faults %v, declared %v", names(faults), names(declared))
	}
}

// TestEpisodeClosedByReclassificationDoesNotClaimRestoration. Editing an absent
// agent's frontmatter to `auto_start = false` is a plausible response to this
// very alarm, and it closes the episode without anything starting. Calling that
// "Restored" would put the un-clearable sentence back in the mail with its sign
// flipped — a reassurance about an agent that is still off.
func TestEpisodeClosedByReclassificationDoesNotClaimRestoration(t *testing.T) {
	rec := &recorder{}
	absent := []Finding{supervised("doctor")}
	src := func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Configured: 6, Present: 6 - len(absent), Absent: absent}, nil
	}
	w := newTestWatcher(t, rec, src, nil)

	t0 := time.Now().UTC()
	w.Check(t0)
	w.Check(t0.Add(16 * time.Minute))
	if len(rec.mails) != 1 {
		t.Fatalf("expected the supervised alarm, got %d", len(rec.mails))
	}

	// The operator flips auto_start to false. doctor is still absent — and only
	// 20 minutes in, so it is nowhere near DormantAfter and is confirmed in
	// NEITHER set. This is the window a confirmed-set-only check would miss.
	absent = []Finding{onDemand("doctor")}
	w.Check(t0.Add(20 * time.Minute))

	clear := rec.mails[len(rec.mails)-1]
	if strings.Contains(clear.subject, "roster complete again") {
		t.Errorf("nothing was restored, so nothing may claim a complete roster: %q", clear.subject)
	}
	if !strings.Contains(clear.subject, "closed by DECLARATION") {
		t.Errorf("the close must say how it closed: %q", clear.subject)
	}
	if strings.Contains(clear.body, "Restored:") {
		t.Errorf("doctor never came back:\n%s", clear.body)
	}
	if !strings.Contains(clear.body, "STILL ABSENT") {
		t.Errorf("the close must say the agent is still off:\n%s", clear.body)
	}
	if strings.Contains(clear.body, "pogod should be running this") {
		t.Errorf("the reclassified member must render in its CURRENT class:\n%s", clear.body)
	}
	if !strings.Contains(clear.body, "1 absent") {
		t.Errorf("the denominator must still count it:\n%s", clear.body)
	}
	// The episode is closed, so the un-clearable state is gone: no further mail
	// however long doctor stays off.
	before := len(rec.mails)
	for _, d := range []time.Duration{25 * time.Hour, 60 * time.Hour, 200 * time.Hour} {
		w.Check(t0.Add(d))
	}
	// Exactly one more: doctor's own one-time declared notice past DormantAfter.
	if len(rec.mails) != before+1 {
		t.Fatalf("expected exactly one further mail (the declared notice), got %d",
			len(rec.mails)-before)
	}
	if len(declaredMails(rec)) != 1 {
		t.Fatalf("expected 1 declared notice, got %d", len(declaredMails(rec)))
	}
}
