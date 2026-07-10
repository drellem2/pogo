package search

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/pkg/plugin"
)

// quiesceOnCleanup drains g's background index work when the test ends.
//
// Call it immediately after the t.TempDir() whose tree g will index. Cleanups
// run LIFO, so a drain registered after the temp dir runs before that dir's
// RemoveAll — which is the whole point: the index pass creates
// <root>/.pogo/search from a goroutine that outlives the assertions, and
// RemoveAll racing it is the "directory not empty" flake (mg-36d9).
func quiesceOnCleanup(t *testing.T, g *BasicSearch) {
	t.Helper()
	t.Cleanup(func() {
		if !g.Quiesce(30 * time.Second) {
			t.Error("search service still had index work in flight 30s after " +
				"the test ended; its writes will race t.TempDir cleanup")
		}
	})
}

// TestQuiesceWaitsForIndexWritesToLand is the mg-36d9 regression guard for the
// barrier itself. ProcessProject indexes asynchronously and the write shard
// creates .pogo/search *after* the project's status flips to Ready, so status
// alone is not a safe point to tear a temp tree down. Quiesce must not return
// until those writes have landed.
func TestQuiesceWaitsForIndexWritesToLand(t *testing.T) {
	g := createBasicSearch()
	dir := t.TempDir()
	quiesceOnCleanup(t, g)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	// Enough files that the walk cannot plausibly finish before Quiesce is
	// entered — a race window this test would otherwise close by luck.
	for i := 0; i < 400; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%03d.go", i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("package x // %d\n", i)), 0644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	root, err := absolute(dir)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	if err := g.ProcessProject(&req); err != nil {
		t.Fatalf("ProcessProject: %v", err)
	}

	if !g.Quiesce(60 * time.Second) {
		t.Fatal("Quiesce timed out waiting for the index pass to finish")
	}

	// Post-condition: nothing is in flight, and the pass really did write.
	if n := g.inflight.Load(); n != 0 {
		t.Errorf("inflight = %d after Quiesce, want 0", n)
	}
	saveFile := filepath.Join(root, pogoDir, searchDir, saveFileName)
	if _, err := os.Stat(saveFile); err != nil {
		t.Errorf("Quiesce returned before the index save file was written: %v", err)
	}
	if s := g.GetStatus(root); s == nil || s.Status != StatusReady {
		t.Errorf("project status after Quiesce = %+v, want Ready", s)
	}
}

// TestQuiesceReportsTimeout pins the negative case: a caller that will not
// wait long enough gets false rather than a false sense of an idle service.
func TestQuiesceReportsTimeout(t *testing.T) {
	g := createBasicSearch()
	g.inflight.Add(1)
	defer g.inflight.Add(-1)
	if g.Quiesce(20 * time.Millisecond) {
		t.Error("Quiesce reported idle while index work was still in flight")
	}
}
