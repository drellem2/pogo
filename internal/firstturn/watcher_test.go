package firstturn

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

type sentMail struct{ to, from, subject, body string }

type recorder struct {
	mails  []sentMail
	evs    []events.Event
	mailFn func(to, from, subject, body string) error
}

func (r *recorder) mail(to, from, subject, body string) error {
	if r.mailFn != nil {
		if err := r.mailFn(to, from, subject, body); err != nil {
			return err
		}
	}
	r.mails = append(r.mails, sentMail{to, from, subject, body})
	return nil
}

func (r *recorder) emit(e events.Event) { r.evs = append(r.evs, e) }

func (r *recorder) toed(to string) []sentMail {
	var out []sentMail
	for _, m := range r.mails {
		if m.to == to {
			out = append(out, m)
		}
	}
	return out
}

func (r *recorder) eventsOf(kind string) []events.Event {
	var out []events.Event
	for _, e := range r.evs {
		if e.EventType == kind {
			out = append(out, e)
		}
	}
	return out
}

// darkFleet builds a source that reports five crew agents spawned at `spawn`
// and never completing anything.
func darkFleet(spawn time.Time) SourceFunc {
	return func(now time.Time) Snapshot {
		var as []Agent
		for _, n := range []string{"architect", "mayor", "pa", "pm-onethird", "pm-pogo"} {
			as = append(as, Agent{Name: n, Identity: "crew-" + n, StartedAt: spawn, Delivered: 12})
		}
		return Snapshot{Now: now, Agents: as, Scanned: 5, Lookback: DefaultLookback}
	}
}

func newTestWatcher(r *recorder, src SourceFunc, opts Options) *Watcher {
	opts.Enabled = true
	opts.Source = src
	opts.Mail = r.mail
	opts.Emit = r.emit
	if opts.Interval == 0 {
		opts.Interval = time.Minute
	}
	return New(opts)
}

// TestWatcher_FleetCaseEscalatesOutsideTheFleetOnItsFirstSample.
//
// This is mg-e2a4's lesson applied before it can be relearned: at 16:12:59
// mid-outage ackwatch's fleet arm mailed exactly one recipient — `mayor` — an
// agent carrying the same 27 unacked fires as everybody else. A detector inside
// the job cannot report the job not running, and patience does not fix a
// recipient that is inside the failure.
func TestWatcher_FleetCaseEscalatesOutsideTheFleetOnItsFirstSample(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	r := &recorder{}
	w := newTestWatcher(r, darkFleet(spawn), Options{EscalateTo: "operator-pager"})

	w.Check(spawn.Add(DefaultGrace))

	if got := len(r.toed("operator-pager")); got != 1 {
		t.Fatalf("escalation mails = %d, want 1 on the FIRST sample; mails=%v", got, r.mails)
	}
	fired := r.eventsOf(EventDark)
	if len(fired) != 1 {
		t.Fatalf("%s events = %d, want 1", EventDark, len(fired))
	}
	if fired[0].Details["escalated"] != true {
		t.Error("escalated = false on a fleet-wide finding")
	}
	if fired[0].Details["notify_to_dark"] != true {
		t.Error("notify_to_dark = false, but the mayor is one of the five dark agents — the body claims it and the event must agree")
	}
	body := r.toed("operator-pager")[0].body
	if !strings.Contains(body, "ESCALATED IMMEDIATELY, NOT ON A TIMER") {
		t.Error("body does not say the escalation was structural rather than a timeout")
	}
	if !strings.Contains(body, "A SPAWN IS NOT A SUCCESS") {
		t.Error("body does not lead with the claim the ticket is about")
	}
}

// TestWatcher_SubjectCarriesTheGrowingDuration.
//
// The failure mode being avoided is measured, not theoretical: through the same
// 22h outage ackwatch's blackout arm sent 33 notices whose subject line was
// byte-identical ("90 fires delivered in the last 3h0m0s, NONE completed"). The
// 33rd carried no more information than the 1st and none of them ever said how
// long this had been going on. Each notice here must be distinguishable from its
// predecessor by the one number that grows.
func TestWatcher_SubjectCarriesTheGrowingDuration(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	r := &recorder{}
	w := newTestWatcher(r, darkFleet(spawn), Options{EscalateTo: "operator-pager", Interval: time.Minute})

	start := spawn.Add(DefaultGrace)
	// Walk 17 hours at 10-minute steps: the real duration of the inert window.
	for d := time.Duration(0); d <= 17*time.Hour; d += 10 * time.Minute {
		w.Check(start.Add(d))
	}

	pages := r.toed("operator-pager")
	if len(pages) < 4 {
		t.Fatalf("escalation mails = %d over 17h, want at least 4 rungs of the ladder", len(pages))
	}
	if len(pages) > 8 {
		t.Fatalf("escalation mails = %d over 17h; the ladder is meant to escalate, not to storm", len(pages))
	}
	seen := map[string]bool{}
	for _, m := range pages {
		t.Logf("rung: %s", m.subject)
		if seen[m.subject] {
			t.Errorf("duplicate subject %q — this is exactly the 33-identical-notices failure", m.subject)
		}
		seen[m.subject] = true
		if !strings.Contains(m.subject, "NO CREW AGENT HAS COMPLETED A TURN SINCE SPAWN") {
			t.Errorf("subject %q does not state the condition", m.subject)
		}
	}
	// The durations in the subjects must be strictly increasing, and the last
	// one must be deep into the outage rather than stuck at the opening value.
	var durs []time.Duration
	for _, m := range pages {
		durs = append(durs, subjectDuration(t, m.subject))
	}
	for i := 1; i < len(durs); i++ {
		if durs[i] <= durs[i-1] {
			t.Errorf("subject duration went %s -> %s; it must only grow", durs[i-1], durs[i])
		}
	}
	if durs[len(durs)-1] < 10*time.Hour {
		t.Errorf("final subject duration = %s after a 17h walk; the number that grows is the actionable one", durs[len(durs)-1])
	}
}

// subjectDuration pulls the "— <d> and counting" span back out of a subject
// line, which is the field a person actually skims.
func subjectDuration(t *testing.T, subject string) time.Duration {
	t.Helper()
	_, rest, ok := strings.Cut(subject, "— ")
	if !ok {
		t.Fatalf("subject %q has no duration field", subject)
	}
	span, _, ok := strings.Cut(rest, " and counting")
	if !ok {
		t.Fatalf("subject %q has no duration field", subject)
	}
	d, err := time.ParseDuration(span)
	if err != nil {
		t.Fatalf("subject %q: unparseable duration %q: %v", subject, span, err)
	}
	return d
}

// TestWatcher_ClearsAndSaysSoToEveryoneItAlarmed. An all-clear narrower than the
// alarm leaves someone holding an open incident forever.
func TestWatcher_ClearsAndSaysSoToEveryoneItAlarmed(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	r := &recorder{}
	acked := false
	src := func(now time.Time) Snapshot {
		s := darkFleet(spawn)(now)
		if acked {
			for i := range s.Agents {
				s.Agents[i].FirstCompletion = now.Add(-time.Minute)
			}
		}
		return s
	}
	w := newTestWatcher(r, src, Options{EscalateTo: "operator-pager", Interval: time.Minute})

	w.Check(spawn.Add(DefaultGrace))
	acked = true
	w.Check(spawn.Add(DefaultGrace + 10*time.Minute))

	clears := r.eventsOf(IncidentEpisodeClearedEvent)
	if len(clears) != 1 {
		t.Fatalf("%s events = %d, want 1", IncidentEpisodeClearedEvent, len(clears))
	}
	if clears[0].Details["kind"] != EpisodeKind {
		t.Errorf("kind = %v, want %q — the notifier coalesces on it (mg-55b2)", clears[0].Details["kind"], EpisodeKind)
	}
	if got := len(r.toed("operator-pager")); got != 2 {
		t.Errorf("escalation mailbox got %d mails, want the alarm AND the all-clear", got)
	}
	if got := len(r.toed(DefaultNotifyTo)); got != 2 {
		t.Errorf("%s got %d mails, want the alarm AND the all-clear", DefaultNotifyTo, got)
	}
}

// TestWatcher_EmitsOnTheQuietPath. A silent correct outcome and a control that
// is not running are the same observation — which is the whole reason this
// package exists one level up.
func TestWatcher_EmitsOnTheQuietPath(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	r := &recorder{}
	src := func(now time.Time) Snapshot {
		s := darkFleet(spawn)(now)
		for i := range s.Agents {
			s.Agents[i].FirstCompletion = spawn.Add(5 * time.Minute)
		}
		return s
	}
	w := newTestWatcher(r, src, Options{})
	w.Check(spawn.Add(DefaultGrace))

	clear := r.eventsOf(EventClear)
	if len(clear) != 1 {
		t.Fatalf("%s events = %d, want 1 — a quiet sample must be legible as a decision", EventClear, len(clear))
	}
	if len(r.mails) != 0 {
		t.Errorf("mailed %d times on a healthy fleet", len(r.mails))
	}
	if _, ok := clear[0].Details["judged"]; !ok {
		t.Error("clear event carries no judged roster; a clean scan must state its own denominator (mg-7a20)")
	}
}

// TestWatcher_BlindSampleIsNeverACleanFleet.
func TestWatcher_BlindSampleIsNeverACleanFleet(t *testing.T) {
	r := &recorder{}
	src := func(now time.Time) Snapshot { return Snapshot{Now: now, Err: "events log unreadable: boom"} }
	w := newTestWatcher(r, src, Options{})
	w.Check(at("2026-08-11T12:00:00Z"))

	if got := len(r.eventsOf(EventBlind)); got != 1 {
		t.Fatalf("%s events = %d, want 1", EventBlind, got)
	}
	if got := len(r.eventsOf(EventClear)); got != 0 {
		t.Errorf("%s events = %d; a blind sample must not be recorded as a clean one", EventClear, got)
	}
}

// TestWatcher_SuppressesForOneGraceAfterItsOwnStart. pogod's restart spawns the
// whole crew at once and none of them can have acked yet. Without this the
// nightly redeploy is a fleet alarm every single night, and an alarm that cries
// wolf nightly is strictly worse than the silence it replaced.
func TestWatcher_SuppressesForOneGraceAfterItsOwnStart(t *testing.T) {
	// Agents older than the grace, pogod itself only just up — the shape a
	// restart that re-attaches to a running fleet produces.
	spawn := at("2026-08-11T02:01:33Z")
	pogodStart := spawn.Add(2 * time.Hour)
	r := &recorder{}
	w := newTestWatcher(r, darkFleet(spawn), Options{StartedAt: pogodStart, Interval: time.Minute})

	w.Check(pogodStart.Add(DefaultGrace - time.Minute))
	if len(r.mails) != 0 {
		t.Fatalf("mailed inside pogod's own settle window: %v", r.mails)
	}
	suppressed := r.eventsOf(EventClear)
	if len(suppressed) != 1 || suppressed[0].Details["suppressed"] != true {
		t.Fatalf("suppression not recorded; a held finding that leaves no trace is indistinguishable from no finding")
	}

	// One grace later the same condition is still true, and it is reported.
	w.Check(pogodStart.Add(DefaultGrace + time.Minute))
	if len(r.mails) == 0 {
		t.Error("still silent one grace after pogod's start; the suppression is a settle window, not an amnesty")
	}
}

// TestWatcher_ANoticeThatReachedNobodyIsItsOwnEvent. The one state worse than
// the bug this arm fixes.
func TestWatcher_ANoticeThatReachedNobodyIsItsOwnEvent(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	r := &recorder{mailFn: func(to, from, subject, body string) error {
		return errors.New("no such mailbox")
	}}
	w := newTestWatcher(r, darkFleet(spawn), Options{EscalateTo: "operator-pager"})
	w.Check(spawn.Add(DefaultGrace))

	if got := len(r.eventsOf(EventUnreported)); got != 1 {
		t.Fatalf("%s events = %d, want 1", EventUnreported, got)
	}
}

// TestWatcher_ARosterThatGrowsDoesNotResetTheLadder. A fleet coming apart one
// agent at a time must not be able to hold its own escalation clock at rung one.
func TestWatcher_ARosterThatGrowsDoesNotResetTheLadder(t *testing.T) {
	spawn := at("2026-08-11T02:01:33Z")
	r := &recorder{}
	n := 1
	src := func(now time.Time) Snapshot {
		names := []string{"architect", "mayor", "pa", "pm-onethird", "pm-pogo"}
		var as []Agent
		for _, nm := range names[:n] {
			as = append(as, Agent{Name: nm, Identity: "crew-" + nm, StartedAt: spawn, Delivered: 12})
		}
		return Snapshot{Now: now, Agents: as, Scanned: 5, Lookback: DefaultLookback}
	}
	w := newTestWatcher(r, src, Options{EscalateTo: "operator-pager", Interval: time.Minute})

	start := spawn.Add(DefaultGrace)
	for i := 0; i < 5; i++ {
		n = i + 1
		w.Check(start.Add(time.Duration(i) * time.Minute))
	}
	// Five roster changes in five minutes: five mails, because each is news.
	// The ladder rung must still be the first one, not five doublings in.
	if w.repageAfter != DefaultFirstRepage {
		t.Errorf("repageAfter = %s after five roster changes, want %s — roster growth is news, not a rung",
			w.repageAfter, DefaultFirstRepage)
	}
}
