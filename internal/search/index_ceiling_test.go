package search

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/drellem2/pogo/pkg/plugin"
)

// TestMaxFilesPerTreeCeiling verifies the mg-d205 per-tree file-count ceiling:
// a tree over the ceiling is marked StatusSkippedTooLarge and its index is
// bounded at the ceiling.
func TestMaxFilesPerTreeCeiling(t *testing.T) {
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	const nFiles = 200
	for i := 0; i < nFiles; i++ {
		p := filepath.Join(dir, "f"+strconv.Itoa(i)+".go")
		if err := os.WriteFile(p, []byte("package x\n"), 0644); err != nil {
			t.Fatalf("could not write fixture file: %v", err)
		}
	}
	root := dir + string(os.PathSeparator)

	bs := createBasicSearch()
	// Registered before the drain so it runs after it (LIFO): removing .pogo
	// while the write shard is still creating it is the same race.
	t.Cleanup(func() { cleanPogoFolder(t, dir) })
	quiesceOnCleanup(t, bs)

	const ceiling = 50
	bs.SetMaxFilesPerTree(ceiling)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)

	deadline := time.Now().Add(15 * time.Second)
	var status IndexingStatus
	for time.Now().Before(deadline) {
		bs.mu.RLock()
		p, ok := bs.projects[root]
		status = p.Status
		bs.mu.RUnlock()
		if ok && (status == StatusSkippedTooLarge || status == StatusReady) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if status != StatusSkippedTooLarge {
		t.Errorf("expected status %q for an over-ceiling tree, got %q", StatusSkippedTooLarge, status)
	}

	bs.mu.RLock()
	indexed := len(bs.projects[root].Paths)
	bs.mu.RUnlock()
	if indexed > ceiling {
		t.Errorf("expected indexing to stop at the %d-file ceiling, indexed %d", ceiling, indexed)
	}
	if indexed == 0 {
		t.Errorf("expected a partial index up to the ceiling, indexed 0")
	}
}

// TestUnderCeilingIndexedNormally is a control: a tree under the ceiling is
// fully indexed as usual.
func TestUnderCeilingIndexedNormally(t *testing.T) {
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	const nFiles = 10
	for i := 0; i < nFiles; i++ {
		p := filepath.Join(dir, "f"+strconv.Itoa(i)+".go")
		if err := os.WriteFile(p, []byte("package x\n"), 0644); err != nil {
			t.Fatalf("could not write fixture file: %v", err)
		}
	}
	root := dir + string(os.PathSeparator)

	bs := createBasicSearch()
	// Registered before the drain so it runs after it (LIFO): removing .pogo
	// while the write shard is still creating it is the same race.
	t.Cleanup(func() { cleanPogoFolder(t, dir) })
	quiesceOnCleanup(t, bs)
	bs.SetMaxFilesPerTree(1000)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)

	deadline := time.Now().Add(15 * time.Second)
	var status IndexingStatus
	for time.Now().Before(deadline) {
		bs.mu.RLock()
		status = bs.projects[root].Status
		bs.mu.RUnlock()
		if status == StatusReady || status == StatusSkippedTooLarge {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if status != StatusReady {
		t.Errorf("expected status %q for an under-ceiling tree, got %q", StatusReady, status)
	}
	bs.mu.RLock()
	indexed := len(bs.projects[root].Paths)
	bs.mu.RUnlock()
	if indexed != nFiles {
		t.Errorf("expected all %d files indexed, got %d", nFiles, indexed)
	}
}
