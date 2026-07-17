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
	// nudgeDelivery is the Delivery the fake nudger reports. The zero value
	// leaves Channel empty; tests that care about channel stamping set it.
	nudgeDelivery Delivery
}

type nudge struct {
	agent   string
	message string
}

func (r *recorder) nudge(agent, message string) (Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nudges = append(r.nudges, nudge{agent, message})
	return r.nudgeDelivery, r.nudgeErr
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

// TestUnclaimedItemFiresOnPMAssignedItem is mg-4bd4's acceptance bar for the
// standard 10-min detector: it must fire on a PM-ASSIGNED item, which is the
// population that was invisible (13 of 14 available items on 2026-07-17).
//
// The bar is deliberately NOT "prove the detector can fire" — the old predicate
// already passed that, firing 9 times on 2026-07-17 on the unassigned items it
// could see. Only an owner-assigned item exercises the fix.
func TestUnclaimedItemFiresOnPMAssignedItem(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-dddd", "pm-pogo", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("pm-assigned item past the threshold must fire: got %d nudges, want 1", rec.nudgeCount())
	}
	ids, ok := rec.events[0].Details["item_ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "mg-dddd" {
		t.Errorf("item_ids = %v, want [mg-dddd]", rec.events[0].Details["item_ids"])
	}
}

// TestItemGatedToHumanIgnored: `--assignee=human` is an execution GATE, not an
// assignment — mayor.md files manual-QA items that way precisely so no worker is
// dispatched at them. It is the one assignee both detectors must still skip.
func TestItemGatedToHumanIgnored(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-eeee", "human", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("human-gated item must not fire, got %d nudges", rec.nudgeCount())
	}
}

// TestUnknownAssigneeIsWatched pins the failure DIRECTION: an assignee nobody
// has seen before is watched, not skipped. This is what keeps the fix from
// re-introducing the bug the day a new PM joins — a roster allowlist would have
// to be edited to keep seeing pm-newhire's work, and until someone noticed, that
// work would be invisible exactly as pm-pogo's was.
func TestUnknownAssigneeIsWatched(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-ffff", "pm-newhire", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("item owned by an unrecognized agent must still be watched: got %d nudges, want 1", rec.nudgeCount())
	}
}

// TestDispatchGateIsCaseInsensitive: a "Human" frontmatter value gates too —
// otherwise the gate is bypassed by capitalization and a manual-QA item draws
// nudges forever.
func TestDispatchGateIsCaseInsensitive(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writeItem(t, workRoot, "mg-9999", " Human ", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("' Human ' must gate like 'human', got %d nudges", rec.nudgeCount())
	}
}

// TestNonDispatchableAssigneesConfigurable proves the gate vocabulary is a
// config value, not a fact compiled into the binary: an operator who invents a
// second gate adds a config line rather than editing this package.
func TestNonDispatchableAssigneesConfigurable(t *testing.T) {
	cfg := baseConfig()
	cfg.NonDispatchableAssignees = []string{"human", "legal-review"}
	w, rec, workRoot, _ := testEnv(t, cfg)
	now := time.Now()
	writeItem(t, workRoot, "mg-8888", "legal-review", now.Add(-20*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 0 {
		t.Fatalf("configured gate 'legal-review' must not fire, got %d nudges", rec.nudgeCount())
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
// TestPriorityWakeFiresOnPMAssignedItem is mg-4bd4's acceptance bar for the
// second detector: the fast wake must fire on a PM-ASSIGNED high-priority item.
//
// This is the matched-control experiment the mayor observed on 2026-07-17 at
// 12:56Z, when three high-priority available items differed only by assignee and
// the wake fired on the unassigned one while staying silent on both pm-pogo
// ones. Under the fixed predicate the silent two are the ones under test.
func TestPriorityWakeFiresOnPMAssignedItem(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi07", "pm-pogo", "high", now.Add(-1*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 1 {
		t.Fatalf("pm-assigned high-priority item must wake: got %d nudges, want 1", rec.nudgeCount())
	}
	if rec.events[0].Details["category"] != categoryPriorityWake {
		t.Errorf("category = %v, want %s", rec.events[0].Details["category"], categoryPriorityWake)
	}
}

// TestPriorityWakeIgnoresHumanGatedItem: the gate outranks priority. A
// high-priority manual-QA item is still not the coordinator's to dispatch.
func TestPriorityWakeIgnoresHumanGatedItem(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-hi08", "human", "high", now.Add(-1*time.Minute))

	w.Check(now)
	if rec.nudgeCount() != 0 {
		t.Fatalf("human-gated high-priority item must not wake, got %d", rec.nudgeCount())
	}
}

// TestBothDetectorsSeeMixedQueue models 2026-07-17's real queue shape — mostly
// PM-owned, one human-gated — and asserts both detectors report exactly the
// dispatchable items and exclude the gate. The old predicate saw 0 of these.
func TestBothDetectorsSeeMixedQueue(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, baseConfig())
	now := time.Now()
	// Stale, owner-assigned → standard detector.
	writeItem(t, workRoot, "mg-own1", "pm-pogo", now.Add(-20*time.Minute))
	writeItem(t, workRoot, "mg-own2", "pm-other", now.Add(-20*time.Minute))
	// Gated → neither detector.
	writeItem(t, workRoot, "mg-gate", "human", now.Add(-20*time.Minute))
	writePriorityItem(t, workRoot, "mg-gatp", "human", "high", now.Add(-20*time.Minute))
	// Fresh high-priority, owner-assigned → priority wake.
	writePriorityItem(t, workRoot, "mg-fast", "pm-pogo", "high", now.Add(-1*time.Minute))

	w.Check(now)

	if rec.nudgeCount() != 2 {
		t.Fatalf("want 2 nudges (one per work-item detector), got %d", rec.nudgeCount())
	}
	got := map[string][]string{}
	for _, ev := range rec.events {
		cat, _ := ev.Details["category"].(string)
		ids, _ := ev.Details["item_ids"].([]string)
		got[cat] = ids
	}
	want := map[string][]string{
		categoryUnclaimedItems: {"mg-own1", "mg-own2"},
		categoryPriorityWake:   {"mg-fast"},
	}
	for cat, wantIDs := range want {
		if strings.Join(got[cat], ",") != strings.Join(wantIDs, ",") {
			t.Errorf("%s item_ids = %v, want %v", cat, got[cat], wantIDs)
		}
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
	// A zero-value config must still gate "human": New() defaulting this is what
	// keeps a bare config from dispatch-nudging manual-QA items.
	if !w.isDispatchGated("human") {
		t.Errorf("zero config must default the dispatch gate to %v, got %v",
			config.DefaultNonDispatchableAssignees, w.cfg.NonDispatchableAssignees)
	}
	if !w.watchedForDispatch("pm-pogo") {
		t.Error("zero config must watch pm-assigned items")
	}
}

// TestFireStampsDeliveryChannel (mg-79dc): the emitted event must record WHICH
// channel carried the nudge, not merely whether something errored. Before this,
// a fire delivered over the PTY and a fire that fell back to durable mail were
// indistinguishable in the log — and a fire that reached NOBODY was
// distinguishable only by a free-text `nudge_error` string. Channel stamping is
// what makes the PTY channel's failure rate countable instead of grep-able.
func TestFireStampsDeliveryChannel(t *testing.T) {
	tests := []struct {
		name           string
		delivery       Delivery
		nudgeErr       error
		wantChannel    any
		wantReason     any
		wantErrPresent bool
	}{
		{
			name:        "pty delivery stamps the channel and no fallback reason",
			delivery:    Delivery{Channel: DeliveryPTY},
			wantChannel: DeliveryPTY,
			wantReason:  nil,
		},
		{
			// The mg-79dc case: the busy mayor. Delivery SUCCEEDED (no error)
			// but took the durable road, and the event says so.
			name:        "busy-agent mail fallback stamps channel and reason",
			delivery:    Delivery{Channel: DeliveryMailFallback, FallbackReason: "still producing output after 30s"},
			wantChannel: DeliveryMailFallback,
			wantReason:  "still producing output after 30s",
		},
		{
			// Both channels down. Now — and only now — does nudge_error mean
			// "the notice reached nobody".
			name:           "hard failure records the error and claims no channel",
			delivery:       Delivery{},
			nudgeErr:       fmt.Errorf("everything is down"),
			wantChannel:    nil,
			wantReason:     nil,
			wantErrPresent: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, rec, workRoot, _ := testEnv(t, baseConfig())
			rec.nudgeDelivery = tc.delivery
			rec.nudgeErr = tc.nudgeErr

			now := time.Now()
			writePriorityItem(t, workRoot, "mg-ch01", "mayor", "high", now.Add(-1*time.Minute))
			w.Check(now)

			if rec.eventCount() != 1 {
				t.Fatalf("expected 1 event, got %d", rec.eventCount())
			}
			d := rec.events[0].Details
			if got := d["nudge_delivery"]; got != tc.wantChannel {
				t.Errorf("nudge_delivery = %v, want %v", got, tc.wantChannel)
			}
			if got := d["nudge_fallback_reason"]; got != tc.wantReason {
				t.Errorf("nudge_fallback_reason = %v, want %v", got, tc.wantReason)
			}
			if _, ok := d["nudge_error"]; ok != tc.wantErrPresent {
				t.Errorf("nudge_error present = %v, want %v", ok, tc.wantErrPresent)
			}
		})
	}
}
