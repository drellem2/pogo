package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// Tests for the unread-mail alarm (gh drellem2/pogo#190).
//
// The defect: the check had a flat 5-minute cooldown with no repeat backoff,
// it counted its own notices (pogod mails them into the maildir it reads when
// the recipient is not running), and it kept firing while pogod was
// index-only. One stopped mayor drew 156 notices in 13h, the subject climbing
// on nothing but the notices before it.

// writeMailFrom writes a message into the agent's new/ maildir with an explicit
// From: header, in the shape `mg mail send` writes it.
func writeMailFrom(t *testing.T, mailRoot, agent, name, from string, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(mailRoot, agent, "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	body := fmt.Sprintf("Message-Id: %s\nFrom: %s\nSubject: s\nDate: %s\n\nbody\n",
		name, from, modTime.UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// readMail models the recipient reading a message: maildir moves it out of
// new/ into cur/.
func readMail(t *testing.T, mailRoot, agent, name string) {
	t.Helper()
	cur := filepath.Join(mailRoot, agent, "cur")
	if err := os.MkdirAll(cur, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(mailRoot, agent, "new", name), filepath.Join(cur, name)); err != nil {
		t.Fatal(err)
	}
}

// offlineMailbox is a Nudger that behaves like pogod's offline road: every
// notice is written, From: stall-watch, into the recipient's own new/ maildir —
// the one the unread-mail check reads. This is what made the alarm feed itself.
type offlineMailbox struct {
	t        *testing.T
	mailRoot string
	mu       sync.Mutex
	now      time.Time
	subjects []string
}

func (o *offlineMailbox) nudge(agent string, n Notice) (Delivery, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.subjects = append(o.subjects, n.Subject)
	writeMailFrom(o.t, o.mailRoot, agent, fmt.Sprintf("notice-%04d", len(o.subjects)), Sender, o.now)
	return Delivery{Channel: DeliveryMail}, nil
}

// simulate190 replays the reported incident: one real unread message lands at
// start and is never read, the recipient is stopped (every notice goes to its
// maildir), and pogod's heartbeat samples every 30s for 13h30m. It returns the
// subjects of every notice sent, and the unread_count each fire's event stamped.
func simulate190(t *testing.T, cfg config.StallWatchConfig) ([]string, []int) {
	t.Helper()
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	mailRoot := filepath.Join(root, "mail")
	if err := os.MkdirAll(filepath.Join(workRoot, "available"), 0o755); err != nil {
		t.Fatal(err)
	}
	box := &offlineMailbox{t: t, mailRoot: mailRoot}
	rec := &recorder{}
	w := New(cfg, Options{WorkRoot: workRoot, MailRoot: mailRoot, Nudge: box.nudge, Emit: rec.emit})

	start := time.Date(2026, 9, 25, 2, 0, 0, 0, time.UTC)
	writeMailFrom(t, mailRoot, cfg.Agent, "real-0001", "pm-pogo", start)
	window := 13*time.Hour + 30*time.Minute
	for elapsed := time.Duration(0); elapsed <= window; elapsed += 30 * time.Second {
		now := start.Add(elapsed)
		box.mu.Lock()
		box.now = now
		box.mu.Unlock()
		w.Check(now)
	}
	var counts []int
	for _, e := range rec.events {
		if e.Details["category"] == categoryUnreadMail {
			n, _ := e.Details["unread_count"].(int)
			counts = append(counts, n)
		}
	}
	return box.subjects, counts
}

// TestUnreadMailSelfFeedingRepeatsAreBounded is the regression test for #190,
// with its numbers stated rather than eyeballed.
//
// The bound: the oldest message crosses the 10m age threshold at 10m, then the
// backoff doubles from the 5m base to the 4h cap — notices at 10m, 15m, 25m,
// 45m, 85m, 165m, 325m, 565m and 805m. Nine over 13h30m.
//
// Its positive control is TestUnreadMail190ControlRunsThePreFixCode, which
// runs this same simulation against the stallwatch package as it stood before
// the fix.
func TestUnreadMailSelfFeedingRepeatsAreBounded(t *testing.T) {
	cfg := baseConfig()
	cfg.RepeatBackoffCap = 4 * time.Hour

	got, counts := simulate190(t, cfg)
	const want = 9
	if len(got) != want {
		t.Fatalf("stopped recipient drew %d unread-mail notices over 13h30m; want %d (5m doubling to a 4h cap)\nsubjects: %q",
			len(got), want, got)
	}
	// Its own notices are not the backlog: every fire counts the single real
	// message, never the notices that piled up beside it. (This read the
	// subject until mg-09d9 took the count out of it; the event is where the
	// count lives now.)
	if len(counts) != len(got) {
		t.Fatalf("%d notices but %d unread_mail events", len(got), len(counts))
	}
	for i, n := range counts {
		if n != 1 {
			t.Errorf("notice %d counted %d unread; want 1, the one real message", i+1, n)
		}
	}
	// And the nine are one alarm, so they share one subject — the property
	// `mg mail reclaim`'s exact-Subject grouping needs to coalesce them
	// (mg-09d9). Before, each carried its own count and age.
	for i, s := range got {
		if s != "stall-watch: "+unreadMailSubjectHead {
			t.Errorf("notice %d subject %q; want the constant unread-mail subject", i+1, s)
		}
	}
}

// TestUnreadMailOnlyOwnNoticesNeverFires closes the self-feeding loop: a
// mailbox holding ONLY stall-watch's own notices is not a backlog, however
// many and however old. The positive control adds one message from anyone
// else, and the same instrument fires.
func TestUnreadMailOnlyOwnNoticesNeverFires(t *testing.T) {
	w, rec, _, mailRoot := testEnv(t, baseConfig())
	now := time.Now()
	for i := 0; i < 20; i++ {
		writeMailFrom(t, mailRoot, "mayor", fmt.Sprintf("notice-%02d", i), Sender, now.Add(-time.Duration(i+1)*time.Hour))
	}

	w.Check(now)
	if rec.nudgeCount() != 0 {
		t.Fatalf("a mailbox of only stall-watch notices fired %d times; want 0", rec.nudgeCount())
	}

	writeMailFrom(t, mailRoot, "mayor", "real", "pm-pogo", now.Add(-15*time.Minute))
	w.Check(now.Add(time.Second))
	if rec.nudgeCount() != 1 {
		t.Fatalf("positive control: one real old message beside them fired %d times; want 1", rec.nudgeCount())
	}
	d := rec.events[0].Details
	if d["unread_count"] != 1 {
		t.Errorf("unread_count = %v, want 1 (own notices excluded)", d["unread_count"])
	}
	if d["self_notices_excluded"] != 20 {
		t.Errorf("self_notices_excluded = %v, want 20", d["self_notices_excluded"])
	}
}

// TestUnreadMailBackoffResetRule pins the reset rule in both directions: a new
// unread message arriving does NOT reset the escalation, and reading the
// oldest message DOES.
func TestUnreadMailBackoffResetRule(t *testing.T) {
	cfg := baseConfig()
	cfg.RepeatBackoffCap = 4 * time.Hour
	w, rec, _, mailRoot := testEnv(t, cfg)
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	writeMailFrom(t, mailRoot, "mayor", "a-oldest", "pm-pogo", t0.Add(-15*time.Minute))

	// Walk the backoff: notices at t0, +5m, +15m; the next gap is 20m.
	for _, at := range []time.Duration{0, 5 * time.Minute, 15 * time.Minute} {
		w.Check(t0.Add(at))
	}
	if rec.nudgeCount() != 3 {
		t.Fatalf("setup: want 3 notices walking the backoff, got %d", rec.nudgeCount())
	}

	// A new arrival does NOT reset. At +20m a flat or arrival-reset cooldown
	// would fire (5m since the last notice); the escalated 20m gap must hold
	// until +35m.
	writeMailFrom(t, mailRoot, "mayor", "b-new", "pm-pogo", t0.Add(16*time.Minute))
	for _, at := range []time.Duration{20 * time.Minute, 30 * time.Minute} {
		w.Check(t0.Add(at))
	}
	if rec.nudgeCount() != 3 {
		t.Fatalf("a new arrival reset the backoff: %d notices, want still 3", rec.nudgeCount())
	}
	w.Check(t0.Add(35 * time.Minute))
	if rec.nudgeCount() != 4 {
		t.Fatalf("want the 4th notice once the 20m gap elapsed, got %d", rec.nudgeCount())
	}
	if n := rec.events[3].Details["notice_count"]; n != 4 {
		t.Fatalf("notice_count = %v, want 4 — the arrival must not have reset the count", n)
	}

	// Reading the oldest DOES reset: the count returns to zero, so the next
	// notice comes one base cooldown after the last fire (+40m), not after the
	// escalated 40m gap (+75m).
	readMail(t, mailRoot, "mayor", "a-oldest")
	w.Check(t0.Add(36 * time.Minute)) // b-new is 20m old: over threshold, but inside the base floor
	if rec.nudgeCount() != 4 {
		t.Fatalf("a read must not fire inside the base cooldown floor: %d notices", rec.nudgeCount())
	}
	w.Check(t0.Add(40 * time.Minute))
	if rec.nudgeCount() != 5 {
		t.Fatalf("reading the oldest did not reset the backoff: %d notices, want 5", rec.nudgeCount())
	}
	if n := rec.events[4].Details["notice_count"]; n != 1 {
		t.Errorf("notice_count after a read = %v, want 1 (a fresh backlog)", n)
	}
	if strings.Contains(rec.nudges[4].message, "[repeat]") {
		t.Errorf("a notice after the reset must not read as a repeat: %s", rec.nudges[4].message)
	}
	if !strings.Contains(rec.nudges[3].message, "[repeat] notice #4") {
		t.Errorf("an escalated notice must say it is a repeat: %s", rec.nudges[3].message)
	}
}

// TestPausedWatcherSendsNothingAndResumesFresh pins (c): while pogod is
// index-only the watcher sends nothing — no mail notice, no item notice, no
// entry notice — and the first tick after resume fires fresh rather than from
// inside a backoff built before the pause.
func TestPausedWatcherSendsNothingAndResumesFresh(t *testing.T) {
	cfg := baseConfig()
	cfg.RepeatBackoffCap = 4 * time.Hour
	var paused bool
	var mu sync.Mutex
	isPaused := func() bool { mu.Lock(); defer mu.Unlock(); return paused }
	setPaused := func(v bool) { mu.Lock(); paused = v; mu.Unlock() }

	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	mailRoot := filepath.Join(root, "mail")
	if err := os.MkdirAll(filepath.Join(workRoot, "available"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	w := New(cfg, Options{WorkRoot: workRoot, MailRoot: mailRoot, Nudge: rec.nudge, Emit: rec.emit, Paused: isPaused})

	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	writeMailFrom(t, mailRoot, "mayor", "real", "pm-pogo", t0.Add(-15*time.Minute))

	// Two notices before the pause: the next would be due at +15m (10m gap).
	w.Check(t0)
	w.Check(t0.Add(5 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("setup: want 2 notices before the pause, got %d", rec.nudgeCount())
	}

	setPaused(true)
	writeItem(t, workRoot, "mg-held", "mayor", t0.Add(-time.Hour))
	for at := 6 * time.Minute; at <= 3*time.Hour; at += 30 * time.Second {
		w.Check(t0.Add(at))
	}
	if rec.nudgeCount() != 2 || rec.eventCount() != 2 {
		t.Fatalf("paused watcher sent %d notices / %d events; want nothing past the 2 before the pause",
			rec.nudgeCount(), rec.eventCount())
	}

	// Resume inside what WOULD have been a backoff had the pause never
	// happened: both the mail backlog and the item fire at once, as first
	// notices.
	setPaused(false)
	w.Check(t0.Add(3*time.Hour + 30*time.Second))
	if rec.nudgeCount() != 4 {
		t.Fatalf("first tick after resume sent %d notices in total; want 4 (mail + item, fresh)", rec.nudgeCount())
	}
	for _, e := range rec.events[2:] {
		if e.Details["category"] == categoryUnreadMail && e.Details["notice_count"] != 1 {
			t.Errorf("resumed mail notice_count = %v, want 1 (fresh)", e.Details["notice_count"])
		}
	}
}

func TestHeaderFromIs(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"Message-Id: 1\nFrom: stall-watch\nSubject: x\n\nbody", true},
		{"From:stall-watch\r\n\r\n", true},
		{"from: stall-watch\n\n", true},
		{"From: pm-pogo\nSubject: x\n\nFrom: stall-watch\n", false},
		{"Subject: x\n\nFrom: stall-watch\n", false}, // body, not header
		{"From: stall-watcher\n\n", false},
		{"", false},
	}
	for _, c := range cases {
		if got := headerFromIs(c.msg, Sender); got != c.want {
			t.Errorf("headerFromIs(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}
