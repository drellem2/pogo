package stallwatch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// Tests for the per-item repeat suppressor (mg-1693).
//
// The defect these pin: the cooldown was keyed on the CATEGORY, so it
// suppressed repeats of a kind of alert rather than repeats about a given item.
// A deliberately-held item re-notified every cooldown forever (mg-61f4 drew 22
// notices in one night), and — the same bug from the other side — a genuinely
// new item arriving mid-cooldown was swallowed by the held item's timer.
//
// Detection was correct in every one of those fires. Nothing here makes
// detection stricter; every test below keeps the item detectable and asserts
// only on how often the watcher repeats ITSELF.

// countingConfig is baseConfig with an explicit backoff cap, so escalation
// arithmetic in these tests does not depend on the production default.
func backoffConfig(capDur time.Duration) config.StallWatchConfig {
	cfg := baseConfig()
	cfg.RepeatBackoffCap = capDur
	return cfg
}

// TestHeldItemStopsRenotifyingForever is the regression test for the reported
// defect, in the shape it was measured: one high-priority item the coordinator
// holds on purpose, sampled every 30s (pogod's heartbeat) across the same
// 4h20m window the mg-1693 measurement covered.
//
// Before the fix this item drew one notice per 3m priority cooldown for the
// whole window — ~86 ticks producing ~86 nudges, of which the live log caught
// 22. After it, the backoff walks 3m → 6m → 12m → ... → the 4h cap, which over
// this window is a single-digit number of notices.
func TestHeldItemStopsRenotifyingForever(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	start := time.Now()
	// Held on purpose: ready, high-priority, dispatchable, and never claimed.
	writePriorityItem(t, workRoot, "mg-61f4", "pm-pogo", "high", start.Add(-time.Minute))

	window := 4*time.Hour + 20*time.Minute
	for elapsed := time.Duration(0); elapsed <= window; elapsed += 30 * time.Second {
		w.Check(start.Add(elapsed))
	}

	got := rec.nudgeCount()
	// 3m base doubling to a 4h cap: notices at ~0, 3m, 9m, 21m, 45m, 93m, 189m,
	// and the cap has not elapsed again by 260m. Seven, not eighty-six.
	if got != 7 {
		t.Errorf("held item drew %d notices over %s; want 7 (backoff 3m→4h cap)", got, window)
	}
	// The bug's signature was one notice per cooldown for the whole window.
	if perCooldown := int(window / (3 * time.Minute)); got >= perCooldown {
		t.Errorf("still notifying once per cooldown (%d notices vs %d cooldowns) — "+
			"the cooldown is not actually keyed per item", got, perCooldown)
	}
}

// TestNewItemNotSwallowedByHeldItemsCooldown pins the inverse half of the same
// defect, and it is the half that was never in the noise complaint: with the
// cooldown keyed per category, a held item's timer suppressed the nudge about a
// DIFFERENT, brand-new item. Quieting the channel is a nuisance; losing a new
// item's notice to an old item's cooldown is a miss.
func TestNewItemNotSwallowedByHeldItemsCooldown(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-held", "pm-pogo", "high", now.Add(-time.Minute))

	w.Check(now)
	if rec.nudgeCount() != 1 {
		t.Fatalf("setup: want 1 nudge for the held item, got %d", rec.nudgeCount())
	}

	// A new high-priority item lands 30s later — deep inside the held item's 3m
	// cooldown, which under the old per-category key would have swallowed it.
	writePriorityItem(t, workRoot, "mg-fresh", "pm-pogo", "high", now.Add(-time.Minute))
	w.Check(now.Add(30 * time.Second))

	if rec.nudgeCount() != 2 {
		t.Fatalf("new item during another item's cooldown must still fire, got %d nudges", rec.nudgeCount())
	}
	msg := rec.nudges[1].message
	if !strings.Contains(msg, "mg-fresh") {
		t.Errorf("second nudge should name the new item, got: %s", msg)
	}
	// And it must name ONLY the due item — re-listing the held item is how the
	// per-item count stayed high even when the fire count fell.
	if strings.Contains(msg, "mg-held") {
		t.Errorf("second nudge re-listed the still-cooling item, got: %s", msg)
	}
}

// TestNoNudgeWhenEveryItemIsCoolingDown: the payoff. A backlog the coordinator
// is holding produces NO wake traffic at all once each item has backed off —
// not a nudge listing zero new things.
func TestNoNudgeWhenEveryItemIsCoolingDown(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()
	for _, id := range []string{"mg-61f4", "mg-0e24", "mg-7c95"} {
		writePriorityItem(t, workRoot, id, "pm-pogo", "high", now.Add(-time.Minute))
	}

	w.Check(now)
	if rec.nudgeCount() != 1 {
		t.Fatalf("setup: want 1 batched nudge, got %d", rec.nudgeCount())
	}

	// Every 30s tick for the next 2 minutes: all three items are inside their
	// own backoff, so nothing should fire.
	for elapsed := 30 * time.Second; elapsed < 3*time.Minute; elapsed += 30 * time.Second {
		w.Check(now.Add(elapsed))
	}
	if rec.nudgeCount() != 1 {
		t.Errorf("want silence while every item is cooling down, got %d nudges", rec.nudgeCount())
	}
	if rec.eventCount() != 1 {
		t.Errorf("a suppressed tick must not emit an event either, got %d", rec.eventCount())
	}
}

// TestBackoffEscalates walks the doubling explicitly: each repeat about the same
// item waits twice as long as the last. The second notice stays prompt (one base
// cooldown) so a stall the coordinator genuinely missed is re-raised quickly —
// only persistent holding walks out to the cap.
func TestBackoffEscalates(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-0e24", "pm-pogo", "high", now.Add(-time.Minute))

	// Notice 1 at t=0. Then the gaps are 3m, 6m, 12m, 24m.
	w.Check(now)
	elapsed := time.Duration(0)
	for i, gap := range []time.Duration{3 * time.Minute, 6 * time.Minute, 12 * time.Minute, 24 * time.Minute} {
		want := i + 2 // notices so far after this step

		// One tick just before the gap elapses: still suppressed.
		w.Check(now.Add(elapsed + gap - time.Second))
		if got := rec.nudgeCount(); got != want-1 {
			t.Fatalf("notice %d fired early at gap %s: %d nudges, want %d", want, gap, got, want-1)
		}
		// One tick just after: fires.
		elapsed += gap
		w.Check(now.Add(elapsed + time.Second))
		if got := rec.nudgeCount(); got != want {
			t.Fatalf("notice %d did not fire after %s: %d nudges, want %d", want, gap, got, want)
		}
	}
}

// TestBackoffHonorsCap: the doubling stops at RepeatBackoffCap rather than
// running away to never-again. The safety net is the point — a genuinely
// forgotten item must still resurface.
func TestBackoffHonorsCap(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(10*time.Minute))
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-7c95", "pm-pogo", "high", now.Add(-time.Minute))

	// 3m base, 10m cap: gaps go 3m, 6m, then 10m forever (not 12m).
	w.Check(now)
	w.Check(now.Add(3*time.Minute + time.Second))
	w.Check(now.Add(9*time.Minute + 2*time.Second))
	if rec.nudgeCount() != 3 {
		t.Fatalf("setup: want 3 notices, got %d", rec.nudgeCount())
	}

	// Fourth gap would be 12m uncapped; at a 10m cap it fires at +10m.
	base := now.Add(9*time.Minute + 2*time.Second)
	w.Check(base.Add(10*time.Minute - time.Second))
	if rec.nudgeCount() != 3 {
		t.Errorf("fired before the cap elapsed, got %d notices", rec.nudgeCount())
	}
	w.Check(base.Add(10*time.Minute + time.Second))
	if rec.nudgeCount() != 4 {
		t.Errorf("capped backoff must still re-notify, got %d notices", rec.nudgeCount())
	}
}

// TestCapAtOrBelowBaseDisablesEscalation documents the escape hatch: a cap at or
// under the base cooldown gives a flat per-item cooldown. That is still a fix
// for the per-category keying — it just declines the backoff half.
func TestCapAtOrBelowBaseDisablesEscalation(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(time.Minute)) // below the 3m base
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-flat", "pm-pogo", "high", now.Add(-time.Minute))

	// Every gap should stay at the 3m base, never doubling.
	w.Check(now)
	for i := 1; i <= 4; i++ {
		at := now.Add(time.Duration(i) * 3 * time.Minute).Add(time.Second)
		w.Check(at)
		if got := rec.nudgeCount(); got != i+1 {
			t.Fatalf("flat cooldown: after %d gaps want %d notices, got %d", i, i+1, got)
		}
	}
}

// TestItemLeavingAvailableResetsItsBackoff: an item that gets claimed and later
// returns to available/ is a fresh event, not a continuation. It must notify
// immediately rather than inherit the backoff it accumulated last time.
func TestItemLeavingAvailableResetsItsBackoff(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-cycle", "pm-pogo", "high", now.Add(-time.Minute))

	// Two notices, so the item is sitting on a 6m backoff.
	w.Check(now)
	w.Check(now.Add(3*time.Minute + time.Second))
	if rec.nudgeCount() != 2 {
		t.Fatalf("setup: want 2 notices, got %d", rec.nudgeCount())
	}

	// Claimed: it leaves available/ entirely.
	movePriorityItem(t, workRoot, "available", "claimed", "mg-cycle", "pm-pogo", "high", now.Add(-time.Minute))
	w.Check(now.Add(4 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("a claimed item must not fire, got %d notices", rec.nudgeCount())
	}

	// Released back to available/ one minute later — well inside the 6m backoff
	// it had accumulated. It is new information now, so it fires.
	movePriorityItem(t, workRoot, "claimed", "available", "mg-cycle", "pm-pogo", "high", now.Add(-time.Minute))
	w.Check(now.Add(5 * time.Minute))
	if rec.nudgeCount() != 3 {
		t.Errorf("a released item must notify fresh, got %d notices", rec.nudgeCount())
	}
}

// TestPruneBoundsTheCooldownMap: the per-item keying introduces a map keyed by
// item id, and a coordinator churns through thousands of items over a long
// pogod run. Keys for items no longer in the candidate set must be dropped.
func TestPruneBoundsTheCooldownMap(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()

	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("mg-p%03d", i)
		writePriorityItem(t, workRoot, id, "pm-pogo", "high", now.Add(-time.Minute))
		w.Check(now.Add(time.Duration(i) * time.Second))
		removeItem(t, workRoot, "available", id)
	}
	if rec.nudgeCount() == 0 {
		t.Fatal("setup: expected the items to have fired")
	}

	// One final tick with an empty queue prunes the last survivor.
	w.Check(now.Add(time.Hour))

	w.mu.Lock()
	n := len(w.lastNudge)
	w.mu.Unlock()
	if n != 0 {
		t.Errorf("cooldown map retained %d keys for items no longer available; want 0", n)
	}
}

// TestRepeatIsVisibleToTheReader: a repeat must not be textually identical to a
// first notice. Reading the same sentence twenty-two times is what teaches a
// coordinator that the channel means nothing.
func TestRepeatIsVisibleToTheReader(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-61f4", "pm-pogo", "high", now.Add(-time.Minute))

	w.Check(now)
	if strings.Contains(rec.nudges[0].message, "[repeat]") {
		t.Errorf("first notice must not be marked a repeat: %s", rec.nudges[0].message)
	}

	w.Check(now.Add(3*time.Minute + time.Second))
	if rec.nudgeCount() != 2 {
		t.Fatalf("want 2 notices, got %d", rec.nudgeCount())
	}
	msg := rec.nudges[1].message
	for _, want := range []string{"[repeat]", "mg-61f4", "notice #2", "backing off"} {
		if !strings.Contains(msg, want) {
			t.Errorf("repeat notice missing %q\ngot: %s", want, msg)
		}
	}
}

// TestEventCountsRepeatsPerItem: the mg-1693 defect survived a night because
// events.log recorded fires but never counted them per item, so a correct
// detector with a broken repeat-suppressor looked exactly like an over-firing
// detector. The fix has to be measurable the same way the defect was, without
// hand-correlating item ids across fires.
func TestEventCountsRepeatsPerItem(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-61f4", "pm-pogo", "high", now.Add(-time.Minute))
	writePriorityItem(t, workRoot, "mg-0e24", "pm-pogo", "high", now.Add(-time.Minute))

	w.Check(now)
	// A third item lands during the first two's backoff: this fire has one due
	// item and two suppressed.
	writePriorityItem(t, workRoot, "mg-new1", "pm-pogo", "high", now.Add(-time.Minute))
	w.Check(now.Add(time.Minute))

	if rec.eventCount() != 2 {
		t.Fatalf("want 2 events, got %d", rec.eventCount())
	}
	d := rec.events[1].Details
	suppressed, ok := d["backoff_suppressed_ids"].([]string)
	if !ok || len(suppressed) != 2 {
		t.Fatalf("want 2 backoff_suppressed_ids, got %v (ok=%v)", d["backoff_suppressed_ids"], ok)
	}
	if d["backoff_suppressed_count"] != 2 {
		t.Errorf("backoff_suppressed_count = %v, want 2", d["backoff_suppressed_count"])
	}
	if ids, _ := d["item_ids"].([]string); len(ids) != 1 || ids[0] != "mg-new1" {
		t.Errorf("item_ids = %v, want only the due item [mg-new1]", d["item_ids"])
	}

	// Now let mg-61f4 come due and check its repeat count is stamped.
	w.Check(now.Add(3*time.Minute + time.Second))
	last := rec.events[len(rec.events)-1].Details
	counts, ok := last["repeat_counts"].(map[string]int)
	if !ok {
		t.Fatalf("want repeat_counts on a repeat fire, got %v", last["repeat_counts"])
	}
	if counts["mg-61f4"] != 2 {
		t.Errorf("repeat_counts[mg-61f4] = %d, want 2", counts["mg-61f4"])
	}
	if _, present := counts["mg-new1"]; present {
		t.Errorf("mg-new1 is on its first notice and must not appear in repeat_counts: %v", counts)
	}
}

// TestStandardStallCategoryIsAlsoPerItem: the priority wake is where the
// measurement landed, but the same broken key gated the 10-minute unclaimed
// check. Both categories get the per-item treatment.
func TestStandardStallCategoryIsAlsoPerItem(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()
	writeItem(t, workRoot, "mg-slow1", "pm-pogo", now.Add(-20*time.Minute))

	w.Check(now)
	if rec.nudgeCount() != 1 {
		t.Fatalf("setup: want 1 nudge, got %d", rec.nudgeCount())
	}

	// A second stale item appears inside the first's 5m cooldown — must fire.
	writeItem(t, workRoot, "mg-slow2", "pm-pogo", now.Add(-20*time.Minute))
	w.Check(now.Add(time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("new stale item must fire during another item's cooldown, got %d", rec.nudgeCount())
	}
	if msg := rec.nudges[1].message; strings.Contains(msg, "mg-slow1") {
		t.Errorf("second nudge re-listed the cooling item: %s", msg)
	}

	// And the held first item backs off rather than repeating every 5m: at
	// t=6m it is due (5m base elapsed), at t=7m it is not (next gap is 10m).
	before := rec.nudgeCount()
	w.Check(now.Add(6*time.Minute + time.Second))
	if rec.nudgeCount() != before+1 {
		t.Fatalf("want the 2nd notice at one base cooldown, got %d", rec.nudgeCount())
	}
	w.Check(now.Add(12 * time.Minute))
	if rec.nudgeCount() != before+1 {
		t.Errorf("3rd notice must wait 10m (doubled), fired early: %d", rec.nudgeCount())
	}
}

// TestMailCategoryStaysPerCategory: unread mail is a single aggregate condition
// with no per-item identity to key on, so it keeps the flat category cooldown.
// This is scope discipline, recorded as a test so the asymmetry is deliberate
// rather than an oversight.
func TestMailCategoryStaysPerCategory(t *testing.T) {
	w, rec, workRoot, mailRoot := testEnv(t, backoffConfig(4*time.Hour))
	now := time.Now()
	// No work items at all, so only the mail category can fire.
	_ = workRoot
	writeMail(t, mailRoot, "mayor", "msg1", now.Add(-15*time.Minute))

	w.Check(now)
	w.Check(now.Add(time.Minute))
	if rec.nudgeCount() != 1 {
		t.Fatalf("mail cooldown should suppress the repeat, got %d", rec.nudgeCount())
	}
	// Flat, not escalating: the second notice comes one NudgeCooldown later and
	// so does the third.
	w.Check(now.Add(5*time.Minute + time.Second))
	if rec.nudgeCount() != 2 {
		t.Fatalf("want 2 mail notices after one cooldown, got %d", rec.nudgeCount())
	}
	w.Check(now.Add(10*time.Minute + 2*time.Second))
	if rec.nudgeCount() != 3 {
		t.Errorf("mail category must stay flat (no backoff), got %d notices", rec.nudgeCount())
	}
}

func TestRepeatCooldownArithmetic(t *testing.T) {
	base, capDur := 3*time.Minute, 4*time.Hour
	tests := []struct {
		count int
		want  time.Duration
	}{
		{0, 3 * time.Minute},
		{1, 3 * time.Minute},
		{2, 6 * time.Minute},
		{3, 12 * time.Minute},
		{4, 24 * time.Minute},
		{5, 48 * time.Minute},
		{6, 96 * time.Minute},
		{7, 192 * time.Minute},
		{8, capDur},
		{9, capDur},
		// A count far past the cap must terminate at the cap, not overflow.
		{1000, capDur},
	}
	for _, tt := range tests {
		if got := repeatCooldown(base, capDur, tt.count); got != tt.want {
			t.Errorf("repeatCooldown(3m, 4h, %d) = %s, want %s", tt.count, got, tt.want)
		}
	}
	if got := repeatCooldown(base, time.Minute, 5); got != base {
		t.Errorf("cap below base should pin to base, got %s", got)
	}
	if got := repeatCooldown(0, capDur, 3); got != capDur {
		t.Errorf("zero base should fall back to the cap, got %s", got)
	}
}

func TestFireKeyCannotCollide(t *testing.T) {
	// The separator must not be producible from a category or an id, or two
	// different pairs could share a cooldown — the exact class of bug being
	// fixed here.
	if fireKey(categoryPriorityWake, "mg-1") == fireKey(categoryUnclaimedItems, "mg-1") {
		t.Error("same item in two categories must not share a key")
	}
	if fireKey(categoryPriorityWake, "") != categoryPriorityWake {
		t.Error("an empty item must yield the bare category key")
	}
	if strings.ContainsAny(categoryPriorityWake+categoryUnclaimedItems+categoryUnreadMail, "\x00") {
		t.Error("a category constant contains the key separator")
	}
}

func TestRepeatBackoffCapDefaultsFromZeroConfig(t *testing.T) {
	cfg := baseConfig()
	cfg.RepeatBackoffCap = 0
	w, _, _, _ := testEnv(t, cfg)
	if w.cfg.RepeatBackoffCap != config.DefaultStallRepeatBackoffCap {
		t.Errorf("RepeatBackoffCap = %s, want default %s",
			w.cfg.RepeatBackoffCap, config.DefaultStallRepeatBackoffCap)
	}
}
