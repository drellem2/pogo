package deafwatch

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

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// staticSource yields a fixed snapshot, so every test in this file is a pure
// function of its fixture — no scheduler, no registry, no ~/.pogo.
func staticSource(missing ...Finding) SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		return Snapshot{Now: now, Scanned: len(missing) + 2, Judged: len(missing) + 2, Missing: missing}, nil
	}
}

func deaf(name string) Finding {
	return Finding{Name: name, Identity: "crew-" + name, Type: "crew", Alive: true}
}

// TestWatcher_AnnouncesFromOutsideTheAgentThatFailed is mg-032b's acceptance:
// nobody ran diagnose, nobody typed a name, and the fleet is told WHICH agent
// cannot be reached.
func TestWatcher_AnnouncesFromOutsideTheAgentThatFailed(t *testing.T) {
	rec := &recorder{}
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	w := New(Options{
		Enabled: true, Source: staticSource(deaf("pm-pogo")),
		Mail: rec.mail, Emit: rec.emit,
		Interval: time.Minute, HoldDown: 15 * time.Minute,
	})

	// First observation: the fault is real but young. The hold-down exists
	// because spawn and schedule registration are not simultaneous — a restart
	// would otherwise announce the whole fleet.
	w.Check(start)
	if got := rec.toList(); len(got) != 0 {
		t.Fatalf("mailed %v inside the hold-down window; a sub-window flap must page nobody (mg-4904)", got)
	}
	if !has(rec.eventTypes(), EventPending) {
		t.Errorf("events = %v, want a %s so the log distinguishes \"saw it and waited\" from \"never saw it\"",
			rec.eventTypes(), EventPending)
	}

	// Still deaf a hold-down later: announce.
	w.Check(start.Add(16 * time.Minute))
	mails := rec.mails
	if len(mails) != 1 {
		t.Fatalf("sent %d mails, want 1 after the hold-down elapsed", len(mails))
	}
	if mails[0].to != DefaultNotifyTo {
		t.Errorf("to = %q, want %q", mails[0].to, DefaultNotifyTo)
	}
	if !strings.Contains(mails[0].subject, "pm-pogo") {
		t.Errorf("subject = %q; it must NAME the agent — not knowing which name to type is the fault", mails[0].subject)
	}
	if !strings.Contains(mails[0].body, "pogo agent diagnose pm-pogo") {
		t.Errorf("body must hand over the diagnose invocation the operator could not construct:\n%s", mails[0].body)
	}
	if !has(rec.eventTypes(), EventFired) {
		t.Errorf("events = %v, want %s", rec.eventTypes(), EventFired)
	}
}

// TestWatcher_HoldDownSurvivesAFlap pins that a loop which comes back RESETS the
// clock rather than accumulating toward an announcement. Registration races are
// exactly this shape, and an alert that fires on one is an alert that gets
// filtered.
func TestWatcher_HoldDownSurvivesAFlap(t *testing.T) {
	rec := &recorder{}
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)

	var missing []Finding
	w := New(Options{
		Enabled: true, Emit: rec.emit, Mail: rec.mail,
		Interval: time.Minute, HoldDown: 15 * time.Minute,
		Source: func(now time.Time) (Snapshot, error) {
			return Snapshot{Now: now, Scanned: 3, Judged: 3, Missing: missing}, nil
		},
	})

	missing = []Finding{deaf("doctor")}
	w.Check(start)
	missing = nil // the loop registered — the race, not a fault
	w.Check(start.Add(2 * time.Minute))
	missing = []Finding{deaf("doctor")} // and gone again, freshly
	w.Check(start.Add(4 * time.Minute))
	w.Check(start.Add(17 * time.Minute)) // 13m into the SECOND run, not 17m into a total

	if got := rec.toList(); len(got) != 0 {
		t.Fatalf("mailed %v; the hold-down must restart after a repair, not accumulate across one", got)
	}

	// Push past the hold-down measured from the second observation and it does fire.
	w.Check(start.Add(20 * time.Minute))
	if got := rec.toList(); len(got) != 1 {
		t.Fatalf("mailed %v, want exactly one announcement once the SECOND run outlasted the hold-down", got)
	}
}

// TestWatcher_DeafCoordinatorEscalatesImmediately covers the routing rule unique
// to this detector. Every sibling watcher escalates on AGE. Here there is a case
// patience cannot help: the default recipient is the mayor, and mailing an agent
// with no mail loop about its own missing mail loop is not a weaker alert, it is
// no alert at all.
func TestWatcher_DeafCoordinatorEscalatesImmediately(t *testing.T) {
	rec := &recorder{}
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	w := New(Options{
		Enabled: true, Source: staticSource(deaf("mayor")),
		Mail: rec.mail, Emit: rec.emit,
		Interval: time.Minute, HoldDown: time.Minute,
		// Age-based escalation is a full day away, and disabled outright would
		// be the same: this path must not depend on it.
		EscalateAfter: 24 * time.Hour,
	})

	w.Check(start)
	w.Check(start.Add(2 * time.Minute))

	to := rec.toList()
	if !has(to, DefaultEscalateTo) {
		t.Fatalf("notified %v, want %q on the FIRST announcement — the mayor cannot read mail it will never be woken for (mg-d385)",
			to, DefaultEscalateTo)
	}
	fired := rec.eventsOfType(EventFired)
	if len(fired) != 1 {
		t.Fatalf("got %d %s events, want 1", len(fired), EventFired)
	}
	if fired[0].Details["coordinator"] != true {
		t.Errorf("details.coordinator = %v, want true", fired[0].Details["coordinator"])
	}
	body := rec.mails[0].body
	if !strings.Contains(body, "ESCALATED IMMEDIATELY") {
		t.Errorf("escalated body must say why it went straight to a human:\n%s", body)
	}
}

// TestWatcher_AgeEscalationStillApplies is the control for the test above: a
// finding that does NOT name the notify mailbox escalates on age, like every
// sibling detector.
func TestWatcher_AgeEscalationStillApplies(t *testing.T) {
	rec := &recorder{}
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	w := New(Options{
		Enabled: true, Source: staticSource(deaf("doctor")),
		Mail: rec.mail, Emit: rec.emit,
		Interval: time.Minute, HoldDown: time.Minute,
		RenotifyAfter: time.Hour, EscalateAfter: 6 * time.Hour,
	})

	w.Check(start)
	w.Check(start.Add(2 * time.Minute))
	if to := rec.toList(); has(to, DefaultEscalateTo) {
		t.Fatalf("notified %v; a fresh finding that is not the coordinator must not escalate yet", to)
	}

	w.Check(start.Add(7 * time.Hour))
	if to := rec.toList(); !has(to, DefaultEscalateTo) {
		t.Fatalf("notified %v, want %q once the finding outlived EscalateAfter", to, DefaultEscalateTo)
	}
}

// TestWatcher_UnchangedRosterStaysQuiet pins the notification policy: a roster
// is fingerprinted by IDENTITY, not by age. Fingerprinting the ages would mail
// every interval and get the sender filtered — which is how a detector becomes
// an alert nobody consumes.
func TestWatcher_UnchangedRosterStaysQuiet(t *testing.T) {
	rec := &recorder{}
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	missing := []Finding{deaf("doctor")}
	w := New(Options{
		Enabled: true, Emit: rec.emit, Mail: rec.mail,
		Interval: time.Minute, HoldDown: time.Minute, RenotifyAfter: 6 * time.Hour,
		Source: func(now time.Time) (Snapshot, error) {
			return Snapshot{Now: now, Scanned: 3, Judged: 3, Missing: missing}, nil
		},
	})

	w.Check(start)
	w.Check(start.Add(2 * time.Minute))
	for i := 3; i < 20; i++ {
		w.Check(start.Add(time.Duration(i) * time.Minute))
	}
	if n := len(rec.toList()); n != 1 {
		t.Fatalf("sent %d mails for one unchanged finding, want 1", n)
	}

	// A CHANGED roster is news immediately, without waiting out the renotify.
	missing = []Finding{deaf("doctor"), deaf("pm-lineara")}
	w.Check(start.Add(30 * time.Minute))
	w.Check(start.Add(32 * time.Minute))
	if n := len(rec.toList()); n != 2 {
		t.Fatalf("sent %d mails, want 2 — a new agent joining the roster is news", n)
	}
	if !strings.Contains(rec.mails[1].subject, "pm-lineara") {
		t.Errorf("subject = %q, want the new agent named", rec.mails[1].subject)
	}
}

// TestWatcher_EpisodeClearEmitsTheGenericIncidentEvent pins the mg-55b2
// contract. Sharing incident_episode_cleared is what lets the notifier coalesce
// a fleet-wide clear into ONE notification instead of a swarm; a bespoke event
// type here would quietly opt out of that.
func TestWatcher_EpisodeClearEmitsTheGenericIncidentEvent(t *testing.T) {
	rec := &recorder{}
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	missing := []Finding{deaf("doctor")}
	w := New(Options{
		Enabled: true, Emit: rec.emit, Mail: rec.mail,
		Interval: time.Minute, HoldDown: time.Minute,
		Source: func(now time.Time) (Snapshot, error) {
			return Snapshot{Now: now, Scanned: 3, Judged: 3, Missing: missing}, nil
		},
	})

	w.Check(start)
	w.Check(start.Add(2 * time.Minute))

	missing = nil
	closedAt := start.Add(30 * time.Minute)
	w.Check(closedAt)

	cleared := rec.eventsOfType(IncidentEpisodeClearedEvent)
	if len(cleared) != 1 {
		t.Fatalf("got %d %s events, want 1", len(cleared), IncidentEpisodeClearedEvent)
	}
	ev := cleared[0]
	if ev.Details["kind"] != EpisodeKind {
		t.Errorf("details.kind = %v, want %q", ev.Details["kind"], EpisodeKind)
	}
	if ev.Agent != "pogod" {
		t.Errorf("agent = %q, want pogod — same shape as the usage-limit and auth emitters", ev.Agent)
	}
	roster, _ := ev.Details["roster"].([]string)
	if len(roster) != 1 || roster[0] != "crew-doctor" {
		t.Errorf("details.roster = %v, want [crew-doctor] — event-log identities, not bare names", ev.Details["roster"])
	}
	if ev.Details["opened_at"] == nil || ev.Details["closed_at"] == nil {
		t.Errorf("details missing the episode window: %v", ev.Details)
	}

	// And the fleet is told it cleared, so nobody is left holding an open
	// incident.
	last := rec.mails[len(rec.mails)-1]
	if !strings.Contains(last.subject, "restored") || !strings.Contains(last.body, "doctor") {
		t.Errorf("clear mail = %q / %q, want an all-clear naming the agent", last.subject, last.body)
	}

	// A second quiet sample must NOT re-clear an already-closed episode.
	w.Check(start.Add(45 * time.Minute))
	if got := rec.eventsOfType(IncidentEpisodeClearedEvent); len(got) != 1 {
		t.Errorf("got %d clear events after a second quiet sample, want 1", len(got))
	}
}

// TestWatcher_ClearReachesEveryoneWhoWasAlarmed: an all-clear that goes to fewer
// mailboxes than the alarm leaves someone holding an open incident forever.
func TestWatcher_ClearReachesEveryoneWhoWasAlarmed(t *testing.T) {
	rec := &recorder{}
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	missing := []Finding{deaf("mayor")} // escalates immediately
	w := New(Options{
		Enabled: true, Emit: rec.emit, Mail: rec.mail,
		Interval: time.Minute, HoldDown: time.Minute,
		Source: func(now time.Time) (Snapshot, error) {
			return Snapshot{Now: now, Scanned: 3, Judged: 3, Missing: missing}, nil
		},
	})
	w.Check(start)
	w.Check(start.Add(2 * time.Minute))

	missing = nil
	w.Check(start.Add(10 * time.Minute))

	var clearTo []string
	for _, m := range rec.mails {
		if strings.Contains(m.subject, "restored") {
			clearTo = append(clearTo, m.to)
		}
	}
	if !has(clearTo, DefaultNotifyTo) || !has(clearTo, DefaultEscalateTo) {
		t.Errorf("clear mail went to %v, want both %q and %q — everyone alarmed must be told it cleared",
			clearTo, DefaultNotifyTo, DefaultEscalateTo)
	}
}

// TestWatcher_UnreadableSourceIsNotACleanFleet is the founding bug of this
// lineage, one level up: a detector that cannot look must not render as one that
// looked and found nothing.
func TestWatcher_UnreadableSourceIsNotACleanFleet(t *testing.T) {
	rec := &recorder{}
	w := New(Options{
		Enabled: true, Emit: rec.emit, Mail: rec.mail, Interval: time.Minute,
		Source: func(now time.Time) (Snapshot, error) {
			return Snapshot{}, errors.New("no mail-check provider installed")
		},
	})
	w.Check(time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC))

	errs := rec.eventsOfType(EventError)
	if len(errs) != 1 {
		t.Fatalf("got %d %s events, want 1", len(errs), EventError)
	}
	if len(rec.toList()) != 0 {
		t.Errorf("mailed %v on an unreadable source; there is nothing to announce", rec.toList())
	}
}

// TestWatcher_ThrottleAndDisarm pins the two ways the runner does nothing: it is
// a no-op on all but the first tick of each interval, and inert when disabled or
// half-wired.
func TestWatcher_ThrottleAndDisarm(t *testing.T) {
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)

	t.Run("throttled", func(t *testing.T) {
		rec := &recorder{}
		var samples int
		w := New(Options{
			Enabled: true, Emit: rec.emit, Mail: rec.mail, Interval: 30 * time.Minute,
			HoldDown: -1,
			Source: func(now time.Time) (Snapshot, error) {
				samples++
				return Snapshot{Now: now, Scanned: 1, Judged: 1}, nil
			},
		})
		for i := 0; i < 10; i++ {
			w.Check(start.Add(time.Duration(i) * time.Minute))
		}
		if samples != 1 {
			t.Errorf("sampled %d times in 10 minutes at a 30m interval, want 1", samples)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		rec := &recorder{}
		var samples int
		w := New(Options{
			Enabled: false, Emit: rec.emit, Mail: rec.mail,
			Source: func(now time.Time) (Snapshot, error) { samples++; return Snapshot{}, nil },
		})
		w.Check(start)
		if samples != 0 {
			t.Errorf("sampled %d times while disabled, want 0", samples)
		}
	})

	t.Run("no mailer", func(t *testing.T) {
		rec := &recorder{}
		var samples int
		w := New(Options{
			Enabled: true, Emit: rec.emit,
			Source: func(now time.Time) (Snapshot, error) { samples++; return Snapshot{}, nil },
		})
		w.Check(start)
		if samples != 0 {
			t.Errorf("sampled %d times with no way to report, want 0", samples)
		}
	})

	t.Run("nil watcher", func(t *testing.T) {
		var w *Watcher
		w.Check(start) // must not panic
	})
}

// TestWatcher_MailFailureIsRecorded: a fault that was detected and could not be
// reported is this ticket's bug one level up, so it must not vanish.
func TestWatcher_MailFailureIsRecorded(t *testing.T) {
	rec := &recorder{mailer: func(to string) error { return errors.New("mg mail send: exit 1") }}
	start := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	w := New(Options{
		Enabled: true, Source: staticSource(deaf("doctor")),
		Mail: rec.mail, Emit: rec.emit, Interval: time.Minute, HoldDown: time.Minute,
	})
	w.Check(start)
	w.Check(start.Add(2 * time.Minute))

	fired := rec.eventsOfType(EventFired)
	if len(fired) != 1 {
		t.Fatalf("got %d %s events, want 1", len(fired), EventFired)
	}
	if _, ok := fired[0].Details["mail_error_"+DefaultNotifyTo]; !ok {
		t.Errorf("details = %v, want a mail_error_%s key", fired[0].Details, DefaultNotifyTo)
	}
}

// TestEpisodeKindMatchesContract pins the two strings this package shares with
// the rest of the incident arc. The event TYPE must equal the one the
// usage-limit and auth emitters use, or the notifier's coalescing silently
// excludes this detector; the KIND must stay distinct from theirs, or a reader
// filtering by kind cannot tell the incidents apart.
func TestEpisodeKindMatchesContract(t *testing.T) {
	if IncidentEpisodeClearedEvent != claude.IncidentEpisodeClearedEvent {
		t.Errorf("event type = %q, want %q (mg-55b2 contract)",
			IncidentEpisodeClearedEvent, claude.IncidentEpisodeClearedEvent)
	}
	if EpisodeKind == claude.UsageLimitEpisodeKind {
		t.Errorf("kind = %q collides with the usage-limit kind", EpisodeKind)
	}
}
