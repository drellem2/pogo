package synthwatch

import (
	"strings"
	"sync"
	"testing"
	"time"

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
