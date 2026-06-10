package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
)

// recorder captures nudges and emitted events for assertions.
type recorder struct {
	mu       sync.Mutex
	nudges   []nudge
	events   []events.Event
	nudgeErr error
}

type nudge struct {
	agent   string
	message string
}

func (r *recorder) nudge(agent, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nudges = append(r.nudges, nudge{agent, message})
	return r.nudgeErr
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) nudgeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.nudges)
}

func (r *recorder) eventCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// testEnv builds a watcher over temp work/mail roots with the given config and
// a recorder wired in.
func testEnv(t *testing.T, cfg config.StallWatchConfig) (*Watcher, *recorder, string, string) {
	t.Helper()
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	mailRoot := filepath.Join(root, "mail")
	for _, d := range []string{"available", "claimed", "done"} {
		if err := os.MkdirAll(filepath.Join(workRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	w := New(cfg, Options{
		WorkRoot: workRoot,
		MailRoot: mailRoot,
		Nudge:    rec.nudge,
		Emit:     rec.emit,
	})
	return w, rec, workRoot, mailRoot
}

// writeItem writes an available work item with the given assignee and sets its
// mtime to age-old relative to now.
func writeItem(t *testing.T, workRoot, id, assignee string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(workRoot, "available", id+".md")
	content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: %s\n---\n# %s\n", id, assignee, id)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// writeMail writes a message into the agent's new/ maildir with the given mtime.
func writeMail(t *testing.T, mailRoot, agent, name string, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(mailRoot, agent, "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("From: x\nSubject: y\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func baseConfig() config.StallWatchConfig {
	return config.StallWatchConfig{
		Enabled:                   true,
		Agent:                     "mayor",
		UnclaimedItemAgeThreshold: 10 * time.Minute,
		UnreadMailAgeThreshold:    10 * time.Minute,
		MaxUnreadMailCount:        5,
		NudgeCooldown:             5 * time.Minute,
	}
}

func TestUnclaimedItemFires(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	// An item assigned to mayor, 20 minutes old — over the 10m threshold.
	writeItem(t, workRoot, "mg-aaaa", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("expected 1 nudge, got %d", rec.nudgeCount())
	}
	if rec.nudges[0].agent != "mayor" {
		t.Errorf("nudge sent to %q, want mayor", rec.nudges[0].agent)
	}
	if rec.eventCount() != 1 {
		t.Fatalf("expected 1 event, got %d", rec.eventCount())
	}
	ev := rec.events[0]
	if ev.EventType != "stall_watch_fired" {
		t.Errorf("event type = %q, want stall_watch_fired", ev.EventType)
	}
	if ev.Details["category"] != categoryUnclaimedItems {
		t.Errorf("category = %v, want %s", ev.Details["category"], categoryUnclaimedItems)
	}
}

func TestUnassignedItemCountsAsMayors(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-bbbb", "", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("expected 1 nudge for unassigned available item, got %d", rec.nudgeCount())
	}
}

func TestFreshItemDoesNotFire(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	// Only 2 minutes old — under threshold.
	writeItem(t, workRoot, "mg-cccc", "mayor", now.Add(-2*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("expected no nudge for fresh item, got %d", rec.nudgeCount())
	}
}

func TestItemAssignedElsewhereIgnored(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-dddd", "alice", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("expected no nudge for item assigned to another agent, got %d", rec.nudgeCount())
	}
}

func TestUnreadMailAgeFires(t *testing.T) {
	w, rec, _, mailRoot := testEnv(t, baseConfig())
	now := time.Now()
	writeMail(t, mailRoot, "mayor", "msg1", now.Add(-15*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("expected 1 nudge for old unread mail, got %d", rec.nudgeCount())
	}
	if rec.events[0].Details["category"] != categoryUnreadMail {
		t.Errorf("category = %v, want %s", rec.events[0].Details["category"], categoryUnreadMail)
	}
	if rec.events[0].Details["over_age"] != true {
		t.Errorf("expected over_age=true, got %v", rec.events[0].Details["over_age"])
	}
}

func TestUnreadMailCountFires(t *testing.T) {
	w, rec, _, mailRoot := testEnv(t, baseConfig())
	now := time.Now()
	// 6 fresh messages (under the age threshold) but over the count of 5.
	for i := 0; i < 6; i++ {
		writeMail(t, mailRoot, "mayor", fmt.Sprintf("msg%d", i), now.Add(-1*time.Minute))
	}

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("expected 1 nudge for too many unread, got %d", rec.nudgeCount())
	}
	if rec.events[0].Details["over_count"] != true {
		t.Errorf("expected over_count=true, got %v", rec.events[0].Details["over_count"])
	}
}

func TestFewFreshMailDoesNotFire(t *testing.T) {
	w, rec, _, mailRoot := testEnv(t, baseConfig())
	now := time.Now()
	for i := 0; i < 3; i++ {
		writeMail(t, mailRoot, "mayor", fmt.Sprintf("msg%d", i), now.Add(-1*time.Minute))
	}

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("expected no nudge for a few fresh messages, got %d", rec.nudgeCount())
	}
}

func TestCooldownSuppressesRepeatNudge(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-eeee", "mayor", now.Add(-20*time.Minute))

	w.Check(now)
	// Second check 1 minute later — within the 5m cooldown.
	w.Check(now.Add(1 * time.Minute))

	if rec.nudgeCount() != 1 {
		t.Fatalf("expected cooldown to suppress the second nudge, got %d", rec.nudgeCount())
	}

	// A check past the cooldown fires again.
	w.Check(now.Add(6 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("expected a second nudge after cooldown, got %d", rec.nudgeCount())
	}
}

func TestCategoriesHaveIndependentCooldowns(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-ffff", "mayor", now.Add(-20*time.Minute))
	writeMail(t, mailRoot, "mayor", "msg1", now.Add(-15*time.Minute))

	w.Check(now)

	// Both categories should fire on the same tick — separate cooldowns.
	if rec.nudgeCount() != 2 {
		t.Fatalf("expected 2 nudges (one per category), got %d", rec.nudgeCount())
	}
}

func TestDisabledWatcherIsNoOp(t *testing.T) {
	cfg := baseConfig()
	cfg.Enabled = false
	w, rec, workRoot, mailRoot := testEnv(t, cfg)
	now := time.Now()
	writeItem(t, workRoot, "mg-gggg", "mayor", now.Add(-20*time.Minute))
	writeMail(t, mailRoot, "mayor", "msg1", now.Add(-15*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 || rec.eventCount() != 0 {
		t.Fatalf("disabled watcher should do nothing, got %d nudges %d events",
			rec.nudgeCount(), rec.eventCount())
	}
}

func TestNoMaildirIsBenign(t *testing.T) {
	// mail root has no mayor/new dir at all — must not panic or fire.
	w, rec, _, _ := testEnv(t, baseConfig())
	w.Check(time.Now())
	if rec.nudgeCount() != 0 {
		t.Fatalf("expected no nudge with no maildir, got %d", rec.nudgeCount())
	}
}

func TestNudgeErrorStillEmitsEvent(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	rec.nudgeErr = fmt.Errorf("agent offline")
	now := time.Now()
	writeItem(t, workRoot, "mg-hhhh", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.eventCount() != 1 {
		t.Fatalf("expected event even when nudge fails, got %d", rec.eventCount())
	}
	if rec.events[0].Details["nudge_error"] != "agent offline" {
		t.Errorf("expected nudge_error recorded, got %v", rec.events[0].Details["nudge_error"])
	}
}

func TestDefaultsAppliedFromZeroConfig(t *testing.T) {
	// A zero-value config (only Enabled set) should pick up package defaults.
	root := t.TempDir()
	rec := &recorder{}
	w := New(config.StallWatchConfig{Enabled: true}, Options{
		WorkRoot: filepath.Join(root, "work"),
		MailRoot: filepath.Join(root, "mail"),
		Nudge:    rec.nudge,
		Emit:     rec.emit,
	})
	if w.cfg.Agent != config.DefaultStallWatchAgent {
		t.Errorf("agent = %q, want default %q", w.cfg.Agent, config.DefaultStallWatchAgent)
	}
	if w.cfg.UnclaimedItemAgeThreshold != config.DefaultUnclaimedItemAgeThreshold {
		t.Errorf("item threshold = %v, want default", w.cfg.UnclaimedItemAgeThreshold)
	}
	if w.cfg.MaxUnreadMailCount != config.DefaultMaxUnreadMailCount {
		t.Errorf("max mail = %d, want default", w.cfg.MaxUnreadMailCount)
	}
	if w.cfg.NudgeCooldown != config.DefaultStallNudgeCooldown {
		t.Errorf("cooldown = %v, want default", w.cfg.NudgeCooldown)
	}
}
