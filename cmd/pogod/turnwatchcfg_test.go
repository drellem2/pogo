package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/turnlog"
	"github.com/drellem2/pogo/internal/turnwatch"
)

// Acceptance 1 (pogo#296): a config that does not mention max_age / hold_down
// runs with exactly the windows pogod had before the keys existed. The literals
// are deliberate — this pins the behaviour, not the constants' current values.
func TestTurnWatchWindowsDefaultTo3hAnd30m(t *testing.T) {
	win := resolveTurnWatchWindows(config.TurnWatchConfig{Enabled: true})
	if win.MaxAge != 3*time.Hour || win.HoldDown != 30*time.Minute {
		t.Errorf("defaults = max_age %s hold_down %s, want 3h / 30m", win.MaxAge, win.HoldDown)
	}
	if len(win.Rejected) != 0 {
		t.Errorf("absent keys reported as rejected: %v", win.Rejected)
	}
}

func TestTurnWatchWindowsConfiguredAndRejected(t *testing.T) {
	win := resolveTurnWatchWindows(config.TurnWatchConfig{MaxAge: 20 * time.Minute, HoldDown: 5 * time.Minute})
	if win.MaxAge != 20*time.Minute || win.HoldDown != 5*time.Minute {
		t.Errorf("configured windows not applied: %+v", win)
	}

	win = resolveTurnWatchWindows(config.TurnWatchConfig{MaxAgeInvalid: "-1h", HoldDownInvalid: "soon"})
	if win.MaxAge != 3*time.Hour || win.HoldDown != 30*time.Minute {
		t.Errorf("a rejected key displaced the default: %+v", win)
	}
	if len(win.Rejected) != 2 {
		t.Fatalf("want 2 rejection lines, got %v", win.Rejected)
	}
	for i, key := range []string{"max_age", "hold_down"} {
		line := win.Rejected[i]
		if !strings.Contains(line, "[turn_watch] "+key) || !strings.Contains(line, "CONFIG ERROR") {
			t.Errorf("rejection line does not name the key %s: %q", key, line)
		}
	}
}

type twMail struct {
	mu sync.Mutex
	to []string
}

func (m *twMail) send(to, from, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.to = append(m.to, to)
	return nil
}

func (m *twMail) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.to)
}

// coordinatorWatcher builds turn-watch through the same turnWatchOptions main
// uses, over a real turnlog tree in which the coordinator last completed a
// turn at lastTurn.
func coordinatorWatcher(t *testing.T, win turnWatchWindows, lastTurn time.Time) (*turnwatch.Watcher, *twMail) {
	t.Helper()
	root := t.TempDir()
	if err := turnlog.AppendIn(root, "mayor", "completed turn", lastTurn); err != nil {
		t.Fatal(err)
	}
	pop := func() ([]turnlog.Present, error) {
		// Started long ago: outside the grace window, so only the windows
		// under test decide.
		return []turnlog.Present{{Name: "mayor", Type: "crew", StartedAt: lastTurn.Add(-24 * time.Hour)}}, nil
	}
	m := &twMail{}
	opts := turnWatchOptions(true, win, root, pop, m.send, "mayor", "human", lastTurn.Add(-24*time.Hour))
	opts.Interval = time.Nanosecond // sample on every Check; the throttle is not under test
	opts.Emit = func(events.Event) {}
	return turnwatch.New(opts), m
}

// Acceptance 2 (pogo#296): a configured shorter max_age / hold_down escalates
// a wedged coordinator sooner — and, the positive control, not before the
// configured bound. The default watcher sampled at the same instants stays
// silent throughout, which is what "sooner" means.
func TestTurnWatchConfiguredWindowsEscalateSooner(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	short := resolveTurnWatchWindows(config.TurnWatchConfig{MaxAge: 20 * time.Minute, HoldDown: 5 * time.Minute})
	def := resolveTurnWatchWindows(config.TurnWatchConfig{})

	ws, ms := coordinatorWatcher(t, short, t0)
	wd, md := coordinatorWatcher(t, def, t0)

	// Samples: still live at 19m; first red at 21m (hold-down starts); still
	// inside the hold-down at 25m; past it at 27m.
	steps := []struct {
		at        time.Duration
		wantShort int
	}{
		{19 * time.Minute, 0},
		{21 * time.Minute, 0},
		{25 * time.Minute, 0}, // positive control: red, but hold_down not yet elapsed
		{27 * time.Minute, 1},
	}
	for _, s := range steps {
		now := t0.Add(s.at)
		ws.Check(now)
		wd.Check(now)
		if got := ms.count(); got != s.wantShort {
			t.Fatalf("configured 20m/5m at +%s: %d notice(s), want %d", s.at, got, s.wantShort)
		}
		if got := md.count(); got != 0 {
			t.Fatalf("default 3h/30m announced at +%s, long before its bound", s.at)
		}
	}
	if ms.to[0] != "human" {
		t.Errorf("coordinator finding mailed to %q, want the escalation box", ms.to[0])
	}
}

// The default windows themselves, end to end: red after 3h, announced after a
// further 30m, and not before. Guards against the configurable path silently
// changing what an unconfigured pogod does.
func TestTurnWatchDefaultWindowsEscalateAtThreeAndAHalfHours(t *testing.T) {
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	w, m := coordinatorWatcher(t, resolveTurnWatchWindows(config.TurnWatchConfig{}), t0)

	for _, s := range []struct {
		at   time.Duration
		want int
	}{
		{2*time.Hour + 59*time.Minute, 0},
		{3*time.Hour + time.Minute, 0},
		{3*time.Hour + 30*time.Minute, 0},
		{3*time.Hour + 32*time.Minute, 1},
	} {
		w.Check(t0.Add(s.at))
		if got := m.count(); got != s.want {
			t.Fatalf("default windows at +%s: %d notice(s), want %d", s.at, got, s.want)
		}
	}
}
