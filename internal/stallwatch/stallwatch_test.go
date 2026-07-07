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
	writeItemIn(t, workRoot, "available", id, assignee, "", modTime)
}

// writePriorityItem writes an available work item carrying a priority value.
func writePriorityItem(t *testing.T, workRoot, id, assignee, priority string, modTime time.Time) {
	t.Helper()
	writeItemIn(t, workRoot, "available", id, assignee, priority, modTime)
}

// writeItemIn writes a work item into an arbitrary status directory (statusDir)
// with an optional priority, so tests can model blocked (pending/) and claimed
// (claimed/) items as well as available ones. The directory is created if it
// does not already exist so tests can exercise pending/, which testEnv does not
// pre-create.
func writeItemIn(t *testing.T, workRoot, statusDir, id, assignee, priority string, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(workRoot, statusDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".md")
	content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: %s\n", id, assignee)
	if priority != "" {
		content += fmt.Sprintf("priority: %s\n", priority)
	}
	content += fmt.Sprintf("---\n# %s\n", id)
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
		PriorityWakeEnabled:       true,
		HighPriorityWakeDelay:     30 * time.Second,
		HighPriorityWakeCooldown:  3 * time.Minute,
		FastPriorities:            []string{"high"},
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

func TestNonAvailableItemsDoNotFire(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	// Stale, mayor-assigned items in claimed/ and done/ — outside the
	// watcher's scope. Only available/ is scanned (gh #37), so these must
	// neither fire nor even be parsed.
	for _, dir := range []string{"claimed", "done"} {
		id := "mg-" + dir
		path := filepath.Join(workRoot, dir, id+".md")
		content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: mayor\n---\n# %s\n", id, id)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		old := now.Add(-20 * time.Minute)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("expected 0 nudges for claimed/done items, got %d", rec.nudgeCount())
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

// --- Priority wake (gh drellem2/pogo #61) ---------------------------------

// TestPriorityWakeBypassesTenMinuteGate: a high-priority item that has aged past
// the short HighPriorityWakeDelay but is nowhere near the 10-min
// UnclaimedItemAgeThreshold fires a priority_wake nudge — the core latency win.
func TestPriorityWakeBypassesTenMinuteGate(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	// 1 minute old: over the 30s wake delay, far under the 10m gate.
	writePriorityItem(t, workRoot, "mg-hi01", "mayor", "high", now.Add(-1*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("expected 1 priority-wake nudge, got %d", rec.nudgeCount())
	}
	if rec.eventCount() != 1 {
		t.Fatalf("expected 1 event, got %d", rec.eventCount())
	}
	if rec.events[0].Details["category"] != categoryPriorityWake {
		t.Errorf("category = %v, want %s", rec.events[0].Details["category"], categoryPriorityWake)
	}
	if !strings.Contains(rec.nudges[0].message, "priority-wake") {
		t.Errorf("nudge message = %q, want it to mention priority-wake", rec.nudges[0].message)
	}
	if !strings.Contains(rec.nudges[0].message, "mg-hi01") {
		t.Errorf("nudge message = %q, want it to name the item", rec.nudges[0].message)
	}
}

// TestPriorityWakeRespectsWakeDelay: a high-priority item younger than the wake
// delay must not fire yet — the delay lets a burst of enqueues settle so a batch
// is one nudge, not one per item.
func TestPriorityWakeRespectsWakeDelay(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	// 10 seconds old: under the 30s wake delay.
	writePriorityItem(t, workRoot, "mg-hi02", "mayor", "high", now.Add(-10*time.Second))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("expected no wake for an item under the wake delay, got %d", rec.nudgeCount())
	}
}

// TestPriorityWakeCooldownPreventsLoopNudge covers architect review point (b):
// a high-priority item that stays available (e.g. the coordinator can't dispatch
// it yet) must NOT re-nudge every tick. The dedicated priority cooldown caps it
// to one nudge per cooldown window.
func TestPriorityWakeCooldownPreventsLoopNudge(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi03", "mayor", "high", now.Add(-1*time.Minute))

	// Simulate many rapid heartbeat ticks (30s apart) with the item still sitting
	// available the whole time.
	for i := 0; i < 6; i++ {
		w.Check(now.Add(time.Duration(i) * 30 * time.Second))
	}
	if rec.nudgeCount() != 1 {
		t.Fatalf("cooldown should hold a stuck item to 1 nudge, got %d", rec.nudgeCount())
	}

	// Past the 3m cooldown it may fire once more.
	w.Check(now.Add(4 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("expected a second wake after the cooldown, got %d", rec.nudgeCount())
	}
}

// TestBlockedOrClaimedHighPriorityDoesNotWake covers architect review point (b):
// a high-priority item that is blocked (unmet deps → sits in pending/) or already
// claimed (in claimed/) must never trigger a wake. Only available/ is scanned,
// so such items are never even seen — they cannot loop-nudge.
func TestBlockedOrClaimedHighPriorityDoesNotWake(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	old := now.Add(-1 * time.Minute)
	// Blocked: high-priority, assigned to mayor, but gated in pending/.
	writeItemIn(t, workRoot, "pending", "mg-blk1", "mayor", "high", old)
	// Already claimed: high-priority, assigned to mayor, in claimed/.
	writeItemIn(t, workRoot, "claimed", "mg-clm1", "mayor", "high", old)

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("blocked/claimed high-priority items must not wake, got %d nudges", rec.nudgeCount())
	}
}

// TestTenMinuteGateStillAppliesToNonHighPriority covers architect review point
// (c): a non-high-priority item keeps the original 10-min gate — the short wake
// delay must not apply to it.
func TestTenMinuteGateStillAppliesToNonHighPriority(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	// medium priority, 1 minute old: over the wake delay but under the 10m gate.
	writePriorityItem(t, workRoot, "mg-md01", "mayor", "medium", now.Add(-1*time.Minute))

	w.Check(now)
	if rec.nudgeCount() != 0 {
		t.Fatalf("non-high item under the 10m gate must not fire, got %d", rec.nudgeCount())
	}

	// Age it past the 10m gate: now the standard unclaimed-items nudge fires.
	writePriorityItem(t, workRoot, "mg-md01", "mayor", "medium", now.Add(-11*time.Minute))
	w.Check(now)
	if rec.nudgeCount() != 1 {
		t.Fatalf("expected the 10m gate to fire for a non-high item, got %d", rec.nudgeCount())
	}
	if rec.events[0].Details["category"] != categoryUnclaimedItems {
		t.Errorf("category = %v, want %s", rec.events[0].Details["category"], categoryUnclaimedItems)
	}
}

// TestPriorityWakeDisabledFallsBackToStandardGate: with the wake disabled, a
// high-priority item must NOT get the fast path but must STILL get the standard
// 10-min nudge — disabling the feature never silences a high-priority item.
func TestPriorityWakeDisabledFallsBackToStandardGate(t *testing.T) {
	cfg := baseConfig()
	cfg.PriorityWakeEnabled = false
	w, rec, workRoot, _ := testEnv(t, cfg)
	now := time.Now()

	// 1 minute old, high priority: no fast wake because the feature is off.
	writePriorityItem(t, workRoot, "mg-hi04", "mayor", "high", now.Add(-1*time.Minute))
	w.Check(now)
	if rec.nudgeCount() != 0 {
		t.Fatalf("wake disabled: no nudge expected under the 10m gate, got %d", rec.nudgeCount())
	}

	// Aged past the 10m gate: the standard path fires (not silenced).
	writePriorityItem(t, workRoot, "mg-hi04", "mayor", "high", now.Add(-11*time.Minute))
	w.Check(now)
	if rec.nudgeCount() != 1 {
		t.Fatalf("wake disabled: high-priority item must still get the 10m nudge, got %d", rec.nudgeCount())
	}
	if rec.events[0].Details["category"] != categoryUnclaimedItems {
		t.Errorf("category = %v, want %s", rec.events[0].Details["category"], categoryUnclaimedItems)
	}
}

// TestPriorityWakeAndStandardHaveIndependentCooldowns: a high-priority (fast)
// item and a stale non-high item fire on the same tick under separate cooldowns.
func TestPriorityWakeAndStandardHaveIndependentCooldowns(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi05", "mayor", "high", now.Add(-1*time.Minute))
	writeItem(t, workRoot, "mg-old5", "mayor", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 2 {
		t.Fatalf("expected 2 nudges (priority wake + standard stall), got %d", rec.nudgeCount())
	}
	cats := map[any]bool{}
	for _, ev := range rec.events {
		cats[ev.Details["category"]] = true
	}
	if !cats[categoryPriorityWake] || !cats[categoryUnclaimedItems] {
		t.Errorf("expected both categories to fire, got %v", cats)
	}
}

// TestPriorityWakeFastPollInvokedOnFire: the FastPoll hook is called exactly once
// when a wake fires, and NOT called when a subsequent within-cooldown check is
// suppressed — the property that keeps FastPoll from storming the heartbeat.
func TestPriorityWakeFastPollInvokedOnFire(t *testing.T) {
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	mailRoot := filepath.Join(root, "mail")
	for _, d := range []string{"available", "claimed", "done"} {
		if err := os.MkdirAll(filepath.Join(workRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	var fastPolls int
	w := New(baseConfig(), Options{
		WorkRoot: workRoot,
		MailRoot: mailRoot,
		Nudge:    rec.nudge,
		Emit:     rec.emit,
		FastPoll: func() { fastPolls++ },
	})
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi06", "mayor", "high", now.Add(-1*time.Minute))

	w.Check(now)
	if fastPolls != 1 {
		t.Fatalf("expected FastPoll called once on fire, got %d", fastPolls)
	}
	// A second check inside the cooldown is suppressed and must not FastPoll.
	w.Check(now.Add(30 * time.Second))
	if fastPolls != 1 {
		t.Fatalf("FastPoll must not fire on a cooled-down check, got %d", fastPolls)
	}
}

// TestPriorityWakeIgnoresItemsAssignedElsewhere: a high-priority item assigned to
// another agent is not the watched agent's concern and must not wake it.
func TestPriorityWakeIgnoresItemsAssignedElsewhere(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi07", "alice", "high", now.Add(-1*time.Minute))

	w.Check(now)
	if rec.nudgeCount() != 0 {
		t.Fatalf("high-priority item assigned elsewhere must not wake, got %d", rec.nudgeCount())
	}
}

// TestPriorityWakeCaseInsensitivePriority: a "High" frontmatter value still
// triggers the wake — priority matching is case-insensitive and trimmed.
func TestPriorityWakeCaseInsensitivePriority(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi08", "mayor", "High", now.Add(-1*time.Minute))

	w.Check(now)
	if rec.nudgeCount() != 1 {
		t.Fatalf("expected wake for case-variant 'High', got %d", rec.nudgeCount())
	}
}

func TestPriorityWakeDefaultsAppliedFromZeroConfig(t *testing.T) {
	root := t.TempDir()
	rec := &recorder{}
	w := New(config.StallWatchConfig{Enabled: true, PriorityWakeEnabled: true}, Options{
		WorkRoot: filepath.Join(root, "work"),
		MailRoot: filepath.Join(root, "mail"),
		Nudge:    rec.nudge,
		Emit:     rec.emit,
	})
	if w.cfg.HighPriorityWakeDelay != config.DefaultHighPriorityWakeDelay {
		t.Errorf("wake delay = %v, want default", w.cfg.HighPriorityWakeDelay)
	}
	if w.cfg.HighPriorityWakeCooldown != config.DefaultHighPriorityWakeCooldown {
		t.Errorf("wake cooldown = %v, want default", w.cfg.HighPriorityWakeCooldown)
	}
	if len(w.cfg.FastPriorities) != 1 || w.cfg.FastPriorities[0] != "high" {
		t.Errorf("fast priorities = %v, want default [high]", w.cfg.FastPriorities)
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
