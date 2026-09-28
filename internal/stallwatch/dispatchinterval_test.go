package stallwatch

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// The tests in this file pin mg-b12da: the two dispatch categories
// (priority_wake, unclaimed_items) re-announce a held item at most once per
// its per-item cooldown, and send at most one notice per DispatchNoticeInterval
// — without ever dropping an item that came due inside that interval.

// quietConfig is baseConfig with the mg-b12da production defaults applied.
func quietConfig() config.StallWatchConfig {
	cfg := baseConfig()
	cfg.RepeatBackoffCap = config.DefaultStallRepeatBackoffCap
	cfg.HighPriorityWakeCooldown = config.DefaultHighPriorityWakeCooldown
	cfg.UnclaimedItemCooldown = config.DefaultUnclaimedItemCooldown
	cfg.DispatchNoticeInterval = config.DefaultDispatchNoticeInterval
	return cfg
}

// TestDispatchIntervalHoldsButNeverDropsANewItem is the property that
// separates this interval from the per-category cooldown mg-1693 removed: a
// new item arriving inside the interval is DELAYED to the next slot, and is
// then named as a first notice — not swallowed, and not read as a repeat.
func TestDispatchIntervalHoldsButNeverDropsANewItem(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, quietConfig())
	t0 := time.Now()
	writePriorityItem(t, workRoot, "mg-a001", "", "high", t0.Add(-time.Minute))
	w.Check(t0)
	if rec.nudgeCount() != 1 {
		t.Fatalf("first item: expected 1 nudge, got %d", rec.nudgeCount())
	}

	// A second high-priority item becomes ready ten minutes later.
	writePriorityItem(t, workRoot, "mg-b002", "", "high", t0.Add(9*time.Minute))
	for m := 10; m < 60; m += 5 {
		w.Check(t0.Add(time.Duration(m) * time.Minute))
	}
	if rec.nudgeCount() != 1 {
		t.Fatalf("inside the 1h interval: expected no further nudge, got %d total", rec.nudgeCount())
	}

	w.Check(t0.Add(time.Hour))
	if rec.nudgeCount() != 2 {
		t.Fatalf("at the next slot: expected the held item's notice, got %d total", rec.nudgeCount())
	}
	got := rec.nudges[1]
	if !strings.Contains(got.subject, "mg-b002") {
		t.Errorf("second notice subject %q does not name the held item", got.subject)
	}
	// mg-a001 is inside its own 4h per-item cooldown, so it is not re-named...
	if strings.Contains(got.subject, "mg-a001") {
		t.Errorf("second notice re-named mg-a001 inside its 4h cooldown: %q", got.subject)
	}
	// ...and mg-b002 was never told, so it must not read as a repeat.
	if strings.Contains(got.message, "[repeat]") {
		t.Errorf("held item was reported as a repeat: %q", got.message)
	}
	if v := rec.events[1].Details["notice_interval"]; v != "1h0m0s" {
		t.Errorf("notice_interval detail = %v, want 1h0m0s", v)
	}
}

// TestHeldPriorityItemRenotifiedAtMostEvery4h: the #211 shape — one ready
// high-priority item the coordinator is holding — measured over a day of 30s
// ticks. Under the old 3m-doubling default it drew ~13 notices in 24h; now it
// draws one plus one per 4h.
func TestHeldPriorityItemRenotifiedAtMostEvery4h(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, quietConfig())
	t0 := time.Now()
	writePriorityItem(t, workRoot, "mg-c003", "", "high", t0.Add(-time.Minute))
	for d := time.Duration(0); d < 24*time.Hour; d += 30 * time.Second {
		w.Check(t0.Add(d))
	}
	if n := rec.nudgeCount(); n != 6 {
		t.Fatalf("held item over 24h: got %d notices, want 6 (t0, +4h, ... +20h)", n)
	}

	// Positive control: the same item under the pre-mg-b12da defaults.
	old := baseConfig()
	old.RepeatBackoffCap = 4 * time.Hour
	wo, reco, workRootO, _ := testEnv(t, old)
	writePriorityItem(t, workRootO, "mg-c003", "", "high", t0.Add(-time.Minute))
	for d := time.Duration(0); d < 24*time.Hour; d += 30 * time.Second {
		wo.Check(t0.Add(d))
	}
	if n := reco.nudgeCount(); n <= 6 {
		t.Fatalf("control: old defaults drew only %d notices in 24h — the test no longer distinguishes the policies", n)
	}
}

// TestDispatchIntervalIsPerCategory: a priority-wake notice must not hold back
// the standard unclaimed-items notice, or the other way round — they are
// different findings about different items.
func TestDispatchIntervalIsPerCategory(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, quietConfig())
	now := time.Now()
	writePriorityItem(t, workRoot, "mg-d004", "", "high", now.Add(-time.Minute))
	writeItem(t, workRoot, "mg-e005", "", now.Add(-20*time.Minute))
	w.Check(now)

	cats := map[any]bool{}
	for _, e := range rec.events {
		cats[e.Details["category"]] = true
	}
	if !cats[categoryPriorityWake] || !cats[categoryUnclaimedItems] {
		t.Fatalf("expected one notice of each dispatch category on the same tick, got %v", cats)
	}
}

// TestDispatchIntervalOpensOnlyOnASend: a tick on which every candidate is
// still cooling down sends nothing, so it must not restart the interval —
// otherwise a held item could push a new item's notice out indefinitely.
func TestDispatchIntervalOpensOnlyOnASend(t *testing.T) {
	cfg := quietConfig()
	w, rec, workRoot, _ := testEnv(t, cfg)
	t0 := time.Now()
	writePriorityItem(t, workRoot, "mg-f006", "", "high", t0.Add(-time.Minute))
	w.Check(t0)
	// Ticks well past the interval while mg-f006 is still inside its 4h.
	w.Check(t0.Add(90 * time.Minute))
	w.Check(t0.Add(100 * time.Minute))
	writePriorityItem(t, workRoot, "mg-g007", "", "high", t0.Add(100*time.Minute))
	w.Check(t0.Add(101 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("new item after a quiet hour should go out on its first due tick; got %d notices", rec.nudgeCount())
	}
}

// TestItemLeavingDuringIntervalIsForgotten: the interval skips selection but
// not pruning, so an item claimed and released inside it is a fresh event.
func TestItemLeavingDuringIntervalIsForgotten(t *testing.T) {
	w, rec, workRoot, _ := testEnv(t, quietConfig())
	t0 := time.Now()
	writePriorityItem(t, workRoot, "mg-h008", "", "high", t0.Add(-time.Minute))
	w.Check(t0)
	movePriorityItem(t, workRoot, "available", "claimed", "mg-h008", "", "high", t0)
	w.Check(t0.Add(10 * time.Minute)) // inside the interval; must still prune
	movePriorityItem(t, workRoot, "claimed", "available", "mg-h008", "", "high", t0.Add(11*time.Minute))
	w.Check(t0.Add(61 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("released item should be re-announced as new at the next slot; got %d notices", rec.nudgeCount())
	}
	if strings.Contains(rec.nudges[1].message, "[repeat]") {
		t.Errorf("released-and-returned item read as a repeat: %q", rec.nudges[1].message)
	}
}

// TestZeroDispatchIntervalIsOff: a config that predates the knob (zero) keeps
// the old behaviour — a new item goes out on the tick it becomes due.
func TestZeroDispatchIntervalIsOff(t *testing.T) {
	cfg := quietConfig()
	cfg.DispatchNoticeInterval = 0
	w, rec, workRoot, _ := testEnv(t, cfg)
	t0 := time.Now()
	writePriorityItem(t, workRoot, "mg-i009", "", "high", t0.Add(-time.Minute))
	w.Check(t0)
	writePriorityItem(t, workRoot, "mg-j010", "", "high", t0.Add(time.Minute))
	w.Check(t0.Add(2 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("interval off: expected the second item immediately, got %d notices", rec.nudgeCount())
	}
	if _, ok := rec.events[1].Details["notice_interval"]; ok {
		t.Error("notice_interval stamped while the interval is off")
	}
}

// TestUnclaimedItemCooldownKnob: the standard category uses its own per-item
// base when set, and falls back to NudgeCooldown when a hand-built config
// leaves it zero.
func TestUnclaimedItemCooldownKnob(t *testing.T) {
	for _, tc := range []struct {
		name     string
		knob     time.Duration
		repeatAt time.Duration // earliest tick that may re-name the item
	}{
		{"set", 4 * time.Hour, 4 * time.Hour},
		{"zero falls back to nudge_cooldown", 0, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.RepeatBackoffCap = 4 * time.Hour
			cfg.UnclaimedItemCooldown = tc.knob
			w, rec, workRoot, _ := testEnv(t, cfg)
			t0 := time.Now()
			writeItem(t, workRoot, "mg-k011", "", t0.Add(-20*time.Minute))
			w.Check(t0)
			w.Check(t0.Add(tc.repeatAt - time.Second))
			if rec.nudgeCount() != 1 {
				t.Fatalf("re-named before its cooldown: %d notices", rec.nudgeCount())
			}
			w.Check(t0.Add(tc.repeatAt))
			if rec.nudgeCount() != 2 {
				t.Fatalf("not re-named at its cooldown: %d notices", rec.nudgeCount())
			}
		})
	}
}

// TestPauseResetsDispatchInterval: a resume restarts the coordinator, so the
// first tick after it reports afresh rather than from inside an interval the
// new process never saw — the same rule as the per-item backoffs.
func TestPauseResetsDispatchInterval(t *testing.T) {
	paused := false
	cfg := quietConfig()
	w, rec, workRoot, _ := testEnv(t, cfg)
	w.paused = func() bool { return paused }
	t0 := time.Now()
	writePriorityItem(t, workRoot, "mg-l012", "", "high", t0.Add(-time.Minute))
	w.Check(t0)
	paused = true
	w.Check(t0.Add(time.Minute))
	paused = false
	w.Check(t0.Add(2 * time.Minute))
	if rec.nudgeCount() != 2 {
		t.Fatalf("after resume: expected a fresh notice, got %d total", rec.nudgeCount())
	}
}
