package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// TestStarvedWalksDoNotBlindTheCPUTerm is drellem2/pogo#156's property: the
// worktree walks and the worker-CPU read have SEPARATE budgets, so walks that
// exhaust theirs cannot hand the CPU read an expired context. Before #156 one
// context served both, the walks ran first, and the CPU term read "context
// deadline exceeded" for 11 hours.
func TestStarvedWalksDoNotBlindTheCPUTerm(t *testing.T) {
	live := []agent.WorkerProgress{
		{Name: "p1", PID: os.Getpid(), Age: time.Hour, WorktreeDir: t.TempDir()},
		{Name: "p2", PID: os.Getpid(), Age: time.Hour, WorktreeDir: t.TempDir()},
	}

	// A walk budget that is spent before the first walk starts.
	snap := sampleWorkers(time.Now(), live, 30*time.Second, time.Nanosecond, nil)

	if strings.Contains(snap.CoresError, "deadline") {
		t.Errorf("the CPU term went blind on the WALKS' budget: %q", snap.CoresError)
	}
	if !snap.CoresKnown && snap.CoresError == "" {
		t.Errorf("CPU term neither measured nor explained: %+v", snap)
	}
	if len(snap.Workers) != 2 {
		t.Fatalf("got %d workers, want 2", len(snap.Workers))
	}
	for _, w := range snap.Workers {
		if w.WritesKnown {
			t.Errorf("%s: a walk with no budget left reported a write state: %+v", w.Name, w)
		}
		if !strings.Contains(w.WritesError, "share of the sample budget") {
			t.Errorf("%s: writes error = %q, want it attributed to the walk budget", w.Name, w.WritesError)
		}
	}

	// GREEN control: with a real walk budget the same trees measure.
	snap = sampleWorkers(time.Now(), live, 30*time.Second, 30*time.Second, nil)
	for _, w := range snap.Workers {
		if !w.WritesKnown {
			t.Errorf("control: %s did not measure with a live budget: %q", w.Name, w.WritesError)
		}
	}
}

// TestWalkShareIsDerivedFromWhatIsLeft: each worktree gets the remaining walk
// budget divided by the worktrees still to walk — no per-tree constant, which
// would be tuned to one host's per-entry cost and wrong ~50x on another.
func TestWalkShareIsDerivedFromWhatIsLeft(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	four := walkShare(ctx, 4)
	if four > 10*time.Second || four < 9*time.Second {
		t.Errorf("share of 40s over 4 trees = %s, want ~10s", four)
	}
	if one := walkShare(ctx, 1); one < 39*time.Second {
		t.Errorf("the last tree gets %s, want everything left (~40s)", one)
	}

	spent, cancel2 := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel2()
	<-spent.Done()
	if got := walkShare(spent, 3); got != 0 {
		t.Errorf("share of a spent budget = %s, want 0", got)
	}
}

// TestDetectorWalkSkipsTheDependencyStore: with skip_dirs the detector reads
// the agent's own writes, not an install's — and without it, the same tree
// reads the store (the positive control that makes the first answer a skip).
func TestDetectorWalkSkipsTheDependencyStore(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	for _, rel := range []string{"src/main.ts", "node_modules/left-pad/index.js", ".pnpm/x@1/index.js"} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"src/main.ts", "src", "node_modules", ".pnpm", "."} {
		if err := os.Chtimes(filepath.Join(dir, rel), old, old); err != nil {
			t.Fatal(err)
		}
	}

	w := agent.WorkerProgress{Name: "p1", Age: 3 * time.Hour, WorktreeDir: dir}
	now := time.Now()

	skipped := workerReading(context.Background(), w, now, []string{"node_modules", ".pnpm"})
	if !skipped.WritesKnown || skipped.WriteIdle < 119*time.Minute {
		t.Errorf("with skip_dirs: write_idle = %s (%+v), want ~2h — the store's fresh files must not count",
			skipped.WriteIdle, skipped)
	}

	unskipped := workerReading(context.Background(), w, now, nil)
	if !unskipped.WritesKnown || unskipped.WriteIdle > time.Minute {
		t.Errorf("control without skip_dirs: write_idle = %s, want fresh — the fixture's store is not fresh, "+
			"so the first assertion measures nothing", unskipped.WriteIdle)
	}
}
