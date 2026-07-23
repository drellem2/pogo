package synthwatch

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/claude"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/synthfail"
)

type mail struct{ to, from, subject, body string }

type recorder struct {
	mu     sync.Mutex
	mails  []mail
	events []events.Event
}

func (r *recorder) send(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mails = append(r.mails, mail{to, from, subject, body})
	return nil
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) eventTypes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.EventType)
	}
	return out
}

func (r *recorder) countType(t string) int {
	n := 0
	for _, e := range r.eventTypes() {
		if e == t {
			n++
		}
	}
	return n
}

// eventsOfType returns every captured event of the given type, in emit order.
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

func failing(reason synthfail.Reason) synthfail.Report {
	return synthfail.Report{
		State:  synthfail.StateFailing,
		Reason: reason,
		Count:  14,
		First:  time.Date(2026, 7, 21, 23, 10, 0, 0, time.UTC),
		Last:   time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		Detail: "Login expired · Please run /login",
	}
}

// scanByWorkdir returns a Scan func that looks the verdict up by the workdir
// the watcher passed to Globs — the only per-target value Scan can see.
func scanByWorkdir(verdicts map[string]synthfail.Report) (globs func(string) []string, scan func(string, []string, synthfail.Options) synthfail.Report) {
	globs = func(workdir string) []string { return []string{workdir} }
	scan = func(_ string, g []string, _ synthfail.Options) synthfail.Report {
		if len(g) == 0 {
			return synthfail.Report{}
		}
		return verdicts[g[0]]
	}
	return globs, scan
}

func build(rec *recorder, targets []Target, verdicts map[string]synthfail.Report) *Watcher {
	globs, scan := scanByWorkdir(verdicts)
	return New(Options{
		Targets:  func() []Target { return targets },
		Globs:    globs,
		Scan:     scan,
		Mail:     rec.send,
		Emit:     rec.emit,
		Interval: time.Nanosecond,
	})
}

// ---------------------------------------------------------------------------
// PAGE
// ---------------------------------------------------------------------------

func TestCheck_PagesHumanOnDetection(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "pm-pogo", Identity: "crew-pm-pogo", Workdir: "/w/pm-pogo"}}
	w := build(rec, targets, map[string]synthfail.Report{"/w/pm-pogo": failing(synthfail.ReasonAuthFailed)})

	w.Check(time.Now())

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails, want 1: a detector nobody hears about is the bug, not the fix", len(rec.mails))
	}
	m := rec.mails[0]
	if m.to != "human" {
		t.Errorf("paged %q, want human — the fleet-wide case takes the mayor down too", m.to)
	}
	// The page must lead a reader away from the restart instinct, because the
	// mayor's documented rule is to restart at 120 minutes.
	if !strings.Contains(m.body, "DO NOT RESTART") {
		t.Error("page body does not say DO NOT RESTART")
	}
	if !strings.Contains(m.body, "not a wedge") {
		t.Error("page body does not distinguish this from a wedge")
	}
	if !strings.Contains(m.body, "auth_failed") && !strings.Contains(m.subject, "auth_failed") {
		t.Error("page does not name the reason")
	}
	if rec.countType(EventDetected) != 1 {
		t.Errorf("emitted %d %s events, want 1", rec.countType(EventDetected), EventDetected)
	}
}

// TestCheck_CoalescesAnEpisode. This class is characteristically fleet-wide —
// one shared credential — so per-agent mail turns one fact into a notification
// storm at the moment a human most needs one clear thing.
func TestCheck_CoalescesAnEpisode(t *testing.T) {
	rec := &recorder{}
	var targets []Target
	verdicts := map[string]synthfail.Report{}
	for _, n := range []string{"mayor", "pm-pogo", "pm-dealdesk", "architect", "pa", "pm-onethird"} {
		targets = append(targets, Target{Name: n, Workdir: "/w/" + n})
		verdicts["/w/"+n] = failing(synthfail.ReasonAuthFailed)
	}
	w := build(rec, targets, verdicts)

	w.Check(time.Now())

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails for a 6-agent episode, want 1", len(rec.mails))
	}
	// Every agent still gets its own event: the mail is coalesced, the record is not.
	if got := rec.countType(EventDetected); got != 6 {
		t.Errorf("emitted %d %s events, want 6 — coalescing the PAGE must not coalesce the RECORD", got, EventDetected)
	}
}

// TestCheck_DoesNotRepageAStandingEpisode. mg-18d0 named 124 identical
// stall_watch fires as its own defect: a detector with no escalation path.
func TestCheck_DoesNotRepageAStandingEpisode(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "pm-pogo", Workdir: "/w/pm-pogo"}}
	w := build(rec, targets, map[string]synthfail.Report{"/w/pm-pogo": failing(synthfail.ReasonRateLimit)})

	for i := 0; i < 20; i++ {
		w.Check(time.Now())
	}

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails across 20 ticks of one standing episode, want 1", len(rec.mails))
	}
	if got := rec.countType(EventDetected); got != 1 {
		t.Errorf("emitted %d detection events for one continuous episode, want 1", got)
	}
}

func TestCheck_EpisodeCloseSendsOneClearMail(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "pm-pogo", Workdir: "/w/pm-pogo"}}
	verdicts := map[string]synthfail.Report{"/w/pm-pogo": failing(synthfail.ReasonAuthFailed)}
	w := build(rec, targets, verdicts)

	w.Check(time.Now())
	verdicts["/w/pm-pogo"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(time.Now())

	if len(rec.mails) != 2 {
		t.Fatalf("sent %d mails, want 2 (open + clear)", len(rec.mails))
	}
	clear := rec.mails[1]
	if !strings.Contains(clear.body, "pm-pogo") {
		t.Error("clear mail does not name the recovered agent")
	}
	// The window's nudges were consumed and destroyed, not queued. A human who
	// thinks the work is merely late will not re-run it.
	if !strings.Contains(clear.body, "destroyed, not queued") {
		t.Error("clear mail does not say the window's work was destroyed rather than deferred")
	}
	if rec.countType(EventCleared) != 1 {
		t.Errorf("emitted %d %s events, want 1", rec.countType(EventCleared), EventCleared)
	}
}

// TestCheck_UnavailableDoesNotCloseAnEpisode is the absence-as-evidence guard
// at the episode level. If the transcript becomes unreadable mid-episode — the
// harness upgraded, the path moved — that must NOT be read as recovery, or the
// human gets an all-clear for a fleet that is still dead.
func TestCheck_UnavailableDoesNotCloseAnEpisode(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "pm-pogo", Workdir: "/w/pm-pogo"}}
	verdicts := map[string]synthfail.Report{"/w/pm-pogo": failing(synthfail.ReasonAuthFailed)}
	w := build(rec, targets, verdicts)

	w.Check(time.Now())
	verdicts["/w/pm-pogo"] = synthfail.Report{Unavailable: "transcript path moved"}
	w.Check(time.Now())

	if len(rec.mails) != 1 {
		t.Fatalf("sent %d mails, want 1: 'we stopped being able to look' is not 'it recovered'", len(rec.mails))
	}
	if rec.countType(EventCleared) != 0 {
		t.Error("emitted a cleared event on an unreadable transcript")
	}
	if !w.SuppressRestart("pm-pogo", "crew-pm-pogo") {
		t.Error("suppression lifted when the transcript became unreadable; the fleet is still dead")
	}
}

// TestCheck_UnavailableNeverOpensAnEpisode — the other direction.
func TestCheck_UnavailableNeverOpensAnEpisode(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "codex-agent", Workdir: "/w/codex"}}
	w := build(rec, targets, map[string]synthfail.Report{
		"/w/codex": {Unavailable: "this harness declares no session transcript path"},
	})

	w.Check(time.Now())

	if len(rec.mails) != 0 {
		t.Fatalf("paged %d times for a harness with no transcript; every non-Claude agent would page forever", len(rec.mails))
	}
	if w.SuppressRestart("codex-agent", "codex-agent") {
		t.Error("suppressed restarts for an agent we cannot observe")
	}
}

// ---------------------------------------------------------------------------
// SUPPRESS — the other half of the fix, and the half that is easy to ship
// broken because nothing visibly happens when it works.
// ---------------------------------------------------------------------------

func TestSuppressRestart_OnlyForFailingAndAlwaysAudited(t *testing.T) {
	rec := &recorder{}
	targets := []Target{
		{Name: "failing", Identity: "crew-failing", Workdir: "/w/failing"},
		{Name: "quiet", Identity: "crew-quiet", Workdir: "/w/quiet"},
		{Name: "unknown", Identity: "crew-unknown", Workdir: "/w/unknown"},
	}
	w := build(rec, targets, map[string]synthfail.Report{
		"/w/failing": failing(synthfail.ReasonSpendLimit),
		"/w/quiet":   {State: synthfail.StateQuiet},
		"/w/unknown": {Unavailable: "no transcript"},
	})
	w.Check(time.Now())

	if !w.SuppressRestart("failing", "crew-failing") {
		t.Error("did not suppress a restart for an agent failing every turn")
	}
	if w.SuppressRestart("quiet", "crew-quiet") {
		t.Error("suppressed a restart for a QUIET agent — a wedged agent must stay restartable")
	}
	if w.SuppressRestart("unknown", "crew-unknown") {
		t.Error("suppressed a restart on no evidence")
	}
	if w.SuppressRestart("never-scanned", "crew-never-scanned") {
		t.Error("suppressed a restart for an agent that was never scanned")
	}

	// A suppression that happens silently is indistinguishable from one that
	// never happened. Exactly one audit event, for the one real suppression.
	if got := rec.countType(EventRestartSuppressed); got != 1 {
		t.Errorf("emitted %d %s events, want exactly 1", got, EventRestartSuppressed)
	}
}

func TestReapMissing_StoppedAgentClosesTheEpisode(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "pm-pogo", Workdir: "/w/pm-pogo"}}
	globs, scan := scanByWorkdir(map[string]synthfail.Report{"/w/pm-pogo": failing(synthfail.ReasonAuthFailed)})
	w := New(Options{
		Targets:  func() []Target { return targets },
		Globs:    globs,
		Scan:     scan,
		Mail:     rec.send,
		Emit:     rec.emit,
		Interval: time.Nanosecond,
	})
	w.Check(time.Now())

	targets = nil // agent stopped and left the registry
	w.Check(time.Now())

	if w.SuppressRestart("pm-pogo", "crew-pm-pogo") {
		t.Error("an agent pogod no longer runs is still suppressed; the episode would never close")
	}
	if len(rec.mails) != 2 {
		t.Fatalf("sent %d mails, want 2 (open + clear on departure)", len(rec.mails))
	}
}

func TestCheck_IsInertWithoutDependencies(t *testing.T) {
	rec := &recorder{}
	w := New(Options{Emit: rec.emit, Mail: rec.send})

	w.Check(time.Now()) // must not panic

	if len(rec.mails) != 0 || len(rec.events) != 0 {
		t.Error("an unwired watcher did something")
	}
}

func TestCheck_ThrottlesPerAgent(t *testing.T) {
	rec := &recorder{}
	scans := 0
	targets := []Target{{Name: "a", Workdir: "/w/a"}}
	w := New(Options{
		Targets: func() []Target { return targets },
		Globs:   func(string) []string { return []string{"g"} },
		Scan: func(string, []string, synthfail.Options) synthfail.Report {
			scans++
			return synthfail.Report{State: synthfail.StateQuiet}
		},
		Emit:     rec.emit,
		Interval: time.Hour,
	})

	now := time.Now()
	for i := 0; i < 10; i++ {
		w.Check(now.Add(time.Duration(i) * time.Minute))
	}

	if scans != 1 {
		t.Errorf("scanned %d times across 10 ticks inside one interval, want 1 — this reads files on every heartbeat otherwise", scans)
	}
}

// ---------------------------------------------------------------------------
// INCIDENT EPISODE CLEARED — the founding-case emit (mg-b8c8).
//
// synthwatch is the AUTH source of the generic incident_episode_cleared contract
// (mg-55b2). At every auth-episode close it must emit ONE structured event whose
// details shape is byte-identical to usagelimit.go's — {kind, episode_id, roster,
// opened_at, closed_at} — changing only kind to "auth". That event is what the
// pogo-reminders notifier (mg-e0f6) coalesces the fleet's auth self-reports on;
// without it, the 2026-07-22 founding swarm repeats.
// ---------------------------------------------------------------------------

// reader's load_episodes() rejects a boundary record unless episode_id, opened_at,
// closed_at all parse non-empty AND roster is non-empty. This mirrors that gate so
// a shape regression fails HERE, not silently at the notifier.
func requireReaderConsumable(t *testing.T, ev events.Event) {
	t.Helper()
	if ev.EventType != claude.IncidentEpisodeClearedEvent {
		t.Fatalf("event_type = %q, want %q (reuse the const, do not re-mint)", ev.EventType, claude.IncidentEpisodeClearedEvent)
	}
	if ev.Agent != "pogod" {
		t.Errorf("agent = %q, want pogod (coordinator-level event, mirrors usagelimit)", ev.Agent)
	}
	// The reader requires exactly these details keys; usagelimit.go emits exactly
	// these. A drift in either direction is the cross-repo divergence mg-55b2 killed.
	wantKeys := []string{"closed_at", "episode_id", "kind", "opened_at", "roster"}
	var gotKeys []string
	for k := range ev.Details {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("details keys = %v, want %v (byte-shape must match usagelimit.go)", gotKeys, wantKeys)
	}
	if got := ev.Details["kind"]; got != AuthEpisodeKind {
		t.Errorf("details.kind = %v, want %q", got, AuthEpisodeKind)
	}
	if eid, _ := ev.Details["episode_id"].(string); strings.TrimSpace(eid) == "" {
		t.Error("details.episode_id empty — reader drops the record and reports swarm")
	}
	roster, ok := ev.Details["roster"].([]string)
	if !ok || len(roster) == 0 {
		t.Fatalf("details.roster = %#v, want a non-empty []string", ev.Details["roster"])
	}
	if !sort.StringsAreSorted(roster) {
		t.Errorf("details.roster = %v, want sorted (deterministic on-disk record)", roster)
	}
	for _, key := range []string{"opened_at", "closed_at"} {
		s, _ := ev.Details[key].(string)
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			t.Errorf("details.%s = %q not RFC3339Nano: %v", key, s, err)
		}
	}
}

// TestIncident_EmittedOnceAtAuthEpisodeClose is the core acceptance: a multi-agent
// auth episode opens, and at its single close ONE incident_episode_cleared{kind:auth}
// fires carrying the whole roster and the true [open, close] window.
func TestIncident_EmittedOnceAtAuthEpisodeClose(t *testing.T) {
	rec := &recorder{}
	targets := []Target{
		{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"},
		{Name: "pm-pogo", Identity: "crew-pm-pogo", Workdir: "/w/pm-pogo"},
		{Name: "8cdb", Identity: "cat-8cdb", Workdir: "/w/8cdb"},
	}
	verdicts := map[string]synthfail.Report{}
	for _, tg := range targets {
		verdicts[tg.Workdir] = failing(synthfail.ReasonAuthFailed)
	}
	w := build(rec, targets, verdicts)

	opened := time.Date(2026, 7, 22, 22, 30, 0, 0, time.UTC)
	w.Check(opened)

	// No incident event while the episode is OPEN — it is a CLOSE-boundary record.
	if n := rec.countType(claude.IncidentEpisodeClearedEvent); n != 0 {
		t.Fatalf("emitted %d incident events while the episode was still open, want 0", n)
	}

	// All three recover at the same later instant → the episode closes once.
	for _, tg := range targets {
		verdicts[tg.Workdir] = synthfail.Report{State: synthfail.StateQuiet}
	}
	closed := time.Date(2026, 7, 22, 22, 47, 0, 0, time.UTC)
	w.Check(closed)

	inc := rec.eventsOfType(claude.IncidentEpisodeClearedEvent)
	if len(inc) != 1 {
		t.Fatalf("emitted %d incident_episode_cleared events for one episode, want exactly 1", len(inc))
	}
	ev := inc[0]
	requireReaderConsumable(t, ev)

	// The roster is the full fleet, as EVENT-LOG IDENTITIES (what the notifier
	// matches mail senders against), sorted — mirroring usagelimit's a.EventAgent().
	roster := ev.Details["roster"].([]string)
	wantRoster := []string{"cat-8cdb", "crew-mayor", "crew-pm-pogo"}
	if strings.Join(roster, ",") != strings.Join(wantRoster, ",") {
		t.Errorf("roster = %v, want %v", roster, wantRoster)
	}

	// The window is the TRUE [open, close] — not [close, close]. mg-e0f6's grace
	// trap: reports land AFTER close, and the reader admits them via close+GRACE.
	// If opened_at were stamped at close, the whole grace window would be empty.
	if got := ev.Details["opened_at"]; got != opened.Format(time.RFC3339Nano) {
		t.Errorf("opened_at = %v, want the episode OPEN time %v", got, opened.Format(time.RFC3339Nano))
	}
	if got := ev.Details["closed_at"]; got != closed.Format(time.RFC3339Nano) {
		t.Errorf("closed_at = %v, want the episode CLOSE time %v", got, closed.Format(time.RFC3339Nano))
	}
	if ev.Timestamp != closed.Format(time.RFC3339Nano) {
		t.Errorf("timestamp = %v, want closed_at %v", ev.Timestamp, closed.Format(time.RFC3339Nano))
	}

	// episode_id is derived from the opening agent + open time, exactly as usagelimit.
	wantEID := fmt.Sprintf("ep-%d-%s", opened.UTC().UnixNano(), "crew-mayor")
	if got := ev.Details["episode_id"]; got != wantEID {
		t.Errorf("episode_id = %v, want %v", got, wantEID)
	}

	// Emit the exact on-disk JSON line events.Emit would write, so the cross-repo
	// founding-case replay (against pogo-reminders' poll-mail.sh) can consume
	// synthwatch's OWN bytes rather than a hand-authored approximation.
	line, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal emitted event: %v", err)
	}
	t.Logf("EVENTS_LOG_LINE %s", line)
}

// TestIncident_NoEmitWithoutEmit is the negative/positive-control seam the ticket
// names: no incident event unless an episode actually closes. A standing episode
// (nobody recovers) emits ZERO — the notifier then has nothing to coalesce on and
// the reports swarm, which is the pre-fix behaviour the emit exists to end.
func TestIncident_NoEmitWhileEpisodeStandsOpen(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonAuthFailed)}
	w := build(rec, targets, verdicts)

	for i := 0; i < 5; i++ {
		w.Check(time.Now())
	}
	if n := rec.countType(claude.IncidentEpisodeClearedEvent); n != 0 {
		t.Fatalf("a standing (never-closing) episode emitted %d incident events, want 0", n)
	}
}

// TestIncident_SequentialEpisodesGetDistinctIDs guards the reader's grouping key:
// two separate auth outages must not collapse into one incident. A flap that opens
// and closes, then a real outage, are DIFFERENT episodes with DIFFERENT ids.
func TestIncident_SequentialEpisodesGetDistinctIDs(t *testing.T) {
	rec := &recorder{}
	targets := []Target{{Name: "mayor", Identity: "crew-mayor", Workdir: "/w/mayor"}}
	verdicts := map[string]synthfail.Report{"/w/mayor": failing(synthfail.ReasonAuthFailed)}
	w := build(rec, targets, verdicts)

	w.Check(time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC))
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(time.Date(2026, 7, 22, 1, 0, 5, 0, time.UTC)) // episode 1 closes

	verdicts["/w/mayor"] = failing(synthfail.ReasonAuthFailed)
	w.Check(time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)) // episode 2 opens
	verdicts["/w/mayor"] = synthfail.Report{State: synthfail.StateQuiet}
	w.Check(time.Date(2026, 7, 22, 5, 30, 0, 0, time.UTC)) // episode 2 closes

	inc := rec.eventsOfType(claude.IncidentEpisodeClearedEvent)
	if len(inc) != 2 {
		t.Fatalf("emitted %d incident events across two episodes, want 2", len(inc))
	}
	if inc[0].Details["episode_id"] == inc[1].Details["episode_id"] {
		t.Errorf("two distinct episodes shared episode_id %v — the notifier would merge them", inc[0].Details["episode_id"])
	}
}
