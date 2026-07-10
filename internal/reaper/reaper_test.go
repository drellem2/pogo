package reaper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEnv is a controllable world for the reaper: a virtual clock, a virtual
// set of heartbeat files (path -> mtime), a kickstart that records calls and
// can be scripted to "restart" a job by freshening its heartbeat, and captured
// log/mail output. This lets every liveness decision be exercised
// deterministically without touching launchd or the real clock.
type fakeEnv struct {
	now        time.Time
	mtimes     map[string]time.Time // heartbeat path -> last touch; absent = missing
	kickstarts []string             // labels kickstarted, in order
	kickErr    map[string]error     // label -> error to return from kickstart
	nextPID    int
	logs       []string
	mails      []mail
	// onKickstart, if set, runs after a successful kickstart — used to
	// simulate a job that resumes ticking (freshens its heartbeat) vs. one
	// that FATALs on start (does nothing).
	onKickstart func(label string)
}

type mail struct{ to, from, subject, body string }

func newEnv(start time.Time) *fakeEnv {
	return &fakeEnv{
		now:     start,
		mtimes:  map[string]time.Time{},
		kickErr: map[string]error{},
		nextPID: 1000,
	}
}

func (e *fakeEnv) reaper(o Options) *Reaper {
	o.Now = func() time.Time { return e.now }
	o.Stat = func(path string) (time.Time, error) {
		mt, ok := e.mtimes[path]
		if !ok {
			return time.Time{}, os.ErrNotExist
		}
		return mt, nil
	}
	o.Kickstart = func(label string) (int, error) {
		e.kickstarts = append(e.kickstarts, label)
		if err := e.kickErr[label]; err != nil {
			return 0, err
		}
		e.nextPID++
		if e.onKickstart != nil {
			e.onKickstart(label)
		}
		return e.nextPID, nil
	}
	o.Mail = func(to, from, subject, body string) error {
		e.mails = append(e.mails, mail{to, from, subject, body})
		return nil
	}
	o.Logf = func(format string, args ...any) {
		e.logs = append(e.logs, fmt.Sprintf(format, args...))
	}
	return New(o)
}

func (e *fakeEnv) touch(path string)           { e.mtimes[path] = e.now }
func (e *fakeEnv) advance(d time.Duration)     { e.now = e.now.Add(d) }
func (e *fakeEnv) lastLog() string             { return e.logs[len(e.logs)-1] }
func (e *fakeEnv) logContains(sub string) bool { return anyContains(e.logs, sub) }
func (e *fakeEnv) mailSubjects() []string      { return mapField(e.mails) }

// --- positive control, direction 1: it FIRES on a stale heartbeat and stops
// once the job resumes ticking. ---
func TestReaperFiresThenRecovers(t *testing.T) {
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	e := newEnv(start)
	hb := "/hb/watchdog"
	e.touch(hb) // healthy to begin with

	// The kickstart simulates a healthy restart: the job comes back and
	// freshens its heartbeat at the current (post-kickstart) time.
	e.onKickstart = func(label string) { e.touch(hb) }

	r := e.reaper(Options{Jobs: []Job{{Label: "com.pogo.watchdog", Heartbeat: hb, Period: 5 * time.Minute}}})

	// Healthy: no kickstart.
	e.advance(time.Minute)
	r.Check(e.now)
	if len(e.kickstarts) != 0 {
		t.Fatalf("kickstarted a healthy job: %v", e.kickstarts)
	}

	// Heartbeat goes stale (10m > 5m period): reaper fires exactly once, and
	// because the restart freshened the heartbeat, it does not fire again.
	e.advance(10 * time.Minute)
	r.Check(e.now)
	if len(e.kickstarts) != 1 || e.kickstarts[0] != "com.pogo.watchdog" {
		t.Fatalf("expected one kickstart of watchdog, got %v", e.kickstarts)
	}
	if !e.logContains("kickstarted (attempt 1/3), new pid") {
		t.Fatalf("kickstart not logged loudly; logs: %v", e.logs)
	}

	// Subsequent sweeps see a fresh heartbeat -> no more kickstarts, and a
	// recovery line is emitted once.
	e.advance(time.Minute)
	r.Check(e.now)
	if len(e.kickstarts) != 1 {
		t.Fatalf("kickstarted again after recovery: %v", e.kickstarts)
	}
	if !e.logContains("RECOVERED") {
		t.Fatalf("recovery not logged; logs: %v", e.logs)
	}
}

// --- positive control, direction 2: it does NOT fire against a healthy job,
// tick after tick. ---
func TestReaperNeverFiresOnHealthyJob(t *testing.T) {
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	e := newEnv(start)
	hb := "/hb/gh-issues"
	r := e.reaper(Options{Jobs: []Job{{Label: "com.pogo.gh-issues", Heartbeat: hb, Period: 5 * time.Minute}}})

	// The job ticks its heartbeat every 2 minutes; the reaper sweeps every
	// minute. It must never fire.
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			e.touch(hb)
		}
		r.Check(e.now)
		e.advance(time.Minute)
	}
	if len(e.kickstarts) != 0 {
		t.Fatalf("reaper fired on a healthy job: %v", e.kickstarts)
	}
}

// --- the mg-1679 defense: a job that FATALs on every start (never freshens its
// heartbeat) is kickstarted at most MaxKickstarts times, then the reaper GIVES
// UP loudly and escalates exactly once — it does not loop forever. ---
func TestReaperBoundedGivesUpOnFatalingJob(t *testing.T) {
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	e := newEnv(start)
	hb := "/hb/bridget"
	e.touch(hb)
	// onKickstart does NOTHING: the job "restarts" but never ticks its
	// heartbeat — exactly the mg-1679 shape (launchctl reports a pid, the job
	// FATALs immediately).

	r := e.reaper(Options{Jobs: []Job{{Label: "com.pogo.bridget", Heartbeat: hb, Period: 5 * time.Minute}}})

	// Drive many sweeps well past the give-up point. Each iteration advances a
	// full period so the settle window always elapses and the next attempt is
	// eligible.
	for i := 0; i < 20; i++ {
		e.advance(6 * time.Minute)
		r.Check(e.now)
	}

	if len(e.kickstarts) != 3 {
		t.Fatalf("expected exactly 3 kickstarts (the cap), got %d: %v", len(e.kickstarts), e.kickstarts)
	}
	// The single most important log line.
	if !e.logContains("GIVING UP on com.pogo.bridget") || !e.logContains("kickstarted 3 times, heartbeat still stale") {
		t.Fatalf("give-up line missing or malformed; logs: %v", e.logs)
	}
	// Escalation goes to BOTH mayor and human, exactly once each.
	var toMayor, toHuman int
	for _, m := range e.mails {
		switch m.to {
		case "mayor":
			toMayor++
		case "human":
			toHuman++
		}
		if m.from != "pogod-reaper" {
			t.Fatalf("escalation mail from unexpected sender %q", m.from)
		}
	}
	if toMayor != 1 || toHuman != 1 {
		t.Fatalf("expected one mail each to mayor and human, got mayor=%d human=%d (subjects %v)", toMayor, toHuman, e.mailSubjects())
	}
}

// The settle/backoff window: right after a kickstart, a still-stale heartbeat
// must NOT trigger another immediate kickstart — the job needs its Period to
// produce a fresh beat.
func TestReaperBacksOffWithinSettleWindow(t *testing.T) {
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	e := newEnv(start)
	hb := "/hb/pa"
	e.touch(hb)
	r := e.reaper(Options{Jobs: []Job{{Label: "com.pogo.pa", Heartbeat: hb, Period: 5 * time.Minute}}})

	// Go stale, fire once.
	e.advance(6 * time.Minute)
	r.Check(e.now)
	if len(e.kickstarts) != 1 {
		t.Fatalf("expected first kickstart, got %v", e.kickstarts)
	}
	// Sweep again only 1 minute later (< 5m period). Heartbeat still stale, but
	// we are inside the settle window -> no second kickstart.
	e.advance(time.Minute)
	r.Check(e.now)
	if len(e.kickstarts) != 1 {
		t.Fatalf("kickstarted inside settle window: %v", e.kickstarts)
	}
	// Once the settle window elapses and it is STILL stale, the second attempt
	// fires.
	e.advance(5 * time.Minute)
	r.Check(e.now)
	if len(e.kickstarts) != 2 {
		t.Fatalf("expected second kickstart after settle window, got %v", e.kickstarts)
	}
}

// A missing heartbeat file (job never started) counts as stale.
func TestReaperMissingHeartbeatIsStale(t *testing.T) {
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	e := newEnv(start)
	r := e.reaper(Options{Jobs: []Job{{Label: "com.pogo.never", Heartbeat: "/hb/absent", Period: time.Minute}}})
	r.Check(e.now)
	if len(e.kickstarts) != 1 {
		t.Fatalf("missing heartbeat should be treated as stale and kickstarted; got %v", e.kickstarts)
	}
	if !e.logContains("heartbeat missing") {
		t.Fatalf("missing heartbeat not reported distinctly; logs: %v", e.logs)
	}
}

// A kickstart that itself errors is logged as a failed attempt and still counts
// toward the cap (so a job whose kickstart always errors also gives up).
func TestReaperKickstartErrorCountsAndLogs(t *testing.T) {
	start := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	e := newEnv(start)
	hb := "/hb/err"
	e.touch(hb)
	e.kickErr["com.pogo.err"] = os.ErrPermission
	r := e.reaper(Options{Jobs: []Job{{Label: "com.pogo.err", Heartbeat: hb, Period: time.Minute}}})

	for i := 0; i < 10; i++ {
		e.advance(2 * time.Minute)
		r.Check(e.now)
	}
	if len(e.kickstarts) != 3 {
		t.Fatalf("expected 3 attempts even on kickstart error, got %d", len(e.kickstarts))
	}
	if !e.logContains("kickstart attempt 1/3 FAILED") {
		t.Fatalf("kickstart failure not logged; logs: %v", e.logs)
	}
	if len(e.mails) != 2 {
		t.Fatalf("expected escalation after cap, got %d mails", len(e.mails))
	}
}

// WriteHeartbeat creates the file and its parent dir, and bumps mtime on
// subsequent calls — the mechanism pogod uses to publish its own heartbeat.
func TestWriteHeartbeat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "health", "pogod.heartbeat")
	if err := WriteHeartbeat(path); err != nil {
		t.Fatalf("WriteHeartbeat create: %v", err)
	}
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after create: %v", err)
	}
	// Force an older mtime, then rewrite and confirm it advanced.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := WriteHeartbeat(path); err != nil {
		t.Fatalf("WriteHeartbeat rewrite: %v", err)
	}
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi2.ModTime().After(fi1.ModTime().Add(-time.Minute)) || fi2.ModTime().Before(old.Add(time.Minute)) {
		t.Fatalf("mtime did not advance on rewrite: %v -> %v", fi1.ModTime(), fi2.ModTime())
	}
}

// --- tiny helpers to keep the fake env terse without extra imports at the top
// of every test. ---

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func mapField(ms []mail) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.subject
	}
	return out
}
