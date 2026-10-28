package gitgc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// jsWorktree lays out the shape drellem2/pogo#156 and #168 measured: a little
// source, and a dependency store orders of magnitude larger — node_modules AND
// pnpm's .pnpm store as a repo-root sibling, both of which sort AHEAD of src/
// in WalkDir's lexical order. It returns the tree, the number of source
// entries (root included), and the mtimes it set.
func jsWorktree(t *testing.T) (dir string, sourceEntries int, srcTime, depTime time.Time) {
	t.Helper()
	dir = t.TempDir()
	srcTime = time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	// The dependency store was written MORE recently than any source: an
	// install ran after the agent's last edit.
	depTime = time.Now().Add(-1 * time.Minute).Truncate(time.Second)

	mk := func(rel string, when time.Time) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 400; i++ {
		mk(fmt.Sprintf("node_modules/pkg%03d/index.js", i), depTime)
		mk(fmt.Sprintf(".pnpm/pkg%03d@1.0.0/node_modules/pkg%03d/index.js", i, i), depTime)
	}
	mk("src/main.ts", srcTime)
	mk("src/lib/util.ts", srcTime)
	mk("package.json", srcTime)
	// .git is a pointer FILE in a linked worktree; it is skipped regardless.
	mk(".git", depTime)

	// Directory mtimes moved as files were created; pin every one to the
	// source time so the only fresh things are the dependency store's FILES.
	var dirs []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range dirs {
		if err := os.Chtimes(p, srcTime, srcTime); err != nil {
			t.Fatal(err)
		}
	}
	// root, src, src/lib, src/main.ts, src/lib/util.ts, package.json
	return dir, 6, srcTime, depTime
}

// TestSkipDirsVisitsSourceNotTheDependencyStore is drellem2/pogo#178's
// acceptance criterion, MEASURED as entries visited rather than asserted: with
// the detector's opt-in list the walk is O(source), and the store it skips is
// the bulk of the tree.
func TestSkipDirsVisitsSourceNotTheDependencyStore(t *testing.T) {
	dir, sourceEntries, srcTime, _ := jsWorktree(t)

	newest, visited, err := newestWriteWalk(context.Background(), dir, 0, []string{"node_modules", ".pnpm"})
	if err != nil {
		t.Fatal(err)
	}
	// +2: the node_modules and .pnpm entries themselves are visited, then
	// pruned. .git is visited and not counted toward the age.
	if want := sourceEntries + 3; visited != want {
		t.Errorf("visited %d entries with the skip list, want %d (source + the two pruned dirs + .git)",
			visited, want)
	}
	if !newest.Equal(srcTime) {
		t.Errorf("newest = %s, want the source's %s — the skipped store must not raise the maximum",
			newest, srcTime)
	}

	// Positive control: the same tree WITHOUT the list visits the store, which
	// is what makes the count above a saving rather than a broken walk.
	_, all, err := newestWriteWalk(context.Background(), dir, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if all < 10*visited {
		t.Errorf("unskipped walk visited %d entries, skipped %d: the fixture is not dependency-heavy, "+
			"so this test measures nothing", all, visited)
	}
	t.Logf("entries visited: %d with skip_dirs, %d without", visited, all)
}

// TestOperatorAgeReportIgnoresTheSkipList: the opt-in list belongs to the
// detector only. The age report — the `untouched N` a human clears a pin on —
// still measures every entry but .git, so a fresh write inside node_modules
// still reads as recent there (drellem2/pogo#156, the two-caller question).
func TestOperatorAgeReportIgnoresTheSkipList(t *testing.T) {
	dir, _, srcTime, depTime := jsWorktree(t)

	report, err := newestWriteWithin(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Equal(depTime) {
		t.Errorf("age report newest = %s, want the dependency store's %s — the operator report "+
			"must not inherit the detector's skip list", report, depTime)
	}
	plain, err := newestWrite(dir)
	if err != nil || !plain.Equal(report) {
		t.Errorf("newestWrite = %s, %v; want %s — the two operator paths must agree", plain, err, report)
	}

	// And NewestWrite with nil is the same .git-only measurement.
	exported, err := NewestWrite(context.Background(), dir, nil)
	if err != nil || !exported.Equal(depTime) {
		t.Errorf("NewestWrite(nil skip) = %s, %v; want %s", exported, err, depTime)
	}
	if detector, _ := NewestWrite(context.Background(), dir, []string{"node_modules", ".pnpm"}); !detector.Equal(srcTime) {
		t.Errorf("NewestWrite(skip) = %s, want %s", detector, srcTime)
	}
}

// TestCancelledWalkReturnsNoPartialMaximum: ctx bounds the walk, and an
// abandoned walk reports no age — the rule ErrWalkBudget already follows.
func TestCancelledWalkReturnsNoPartialMaximum(t *testing.T) {
	dir, _, _, _ := jsWorktree(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	when, err := NewestWrite(ctx, dir, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context must abandon the walk with context.Canceled, got %v", err)
	}
	if !when.IsZero() {
		t.Errorf("an abandoned walk returned a time (%v): a partial maximum is a confident wrong answer", when)
	}

	expired, cancel2 := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel2()
	<-expired.Done()
	if _, err := NewestWrite(expired, dir, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an expired context must abandon the walk with DeadlineExceeded, got %v", err)
	}

	// GREEN control: a live context measures the same tree.
	if _, err := NewestWrite(context.Background(), dir, nil); err != nil {
		t.Errorf("control: a live context must walk the tree, got %v", err)
	}
}
