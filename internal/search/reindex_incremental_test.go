package search

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/pkg/plugin"
)

type indexEvent struct {
	root    string
	changed bool
}

// newTestProject creates a throwaway project directory containing the given
// files and returns a BasicSearch wired to report index completions on the
// returned channel, plus the project root key.
func newTestProject(t *testing.T, files map[string]string) (*BasicSearch, string, chan indexEvent) {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	root, err := absolute(dir)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}
	bs := createBasicSearch()
	events := make(chan indexEvent, 16)
	bs.SetOnIndexed(func(r string, changed bool) {
		events <- indexEvent{root: r, changed: changed}
	})
	return bs, root, events
}

// waitIndexed blocks until an index pass for root completes and returns
// whether it reported changed content.
func waitIndexed(t *testing.T, events chan indexEvent, root string) bool {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.root == root {
				return ev.changed
			}
		case <-deadline:
			t.Fatalf("timed out waiting for index completion of %s", root)
		}
	}
}

// TestReIndexRebuildsZoektOnContentChange is the regression guard for the
// stale-search bug fixed in mg-1236: serializeProjectIndex used to read its
// "previous" state from the projects map after write() had already stored the
// new state there, so the content compare always reported "unchanged" and the
// zoekt rebuild was skipped forever — new content never became searchable.
// It also pins the onIndexed changed-signal sequence the backoff scheduler
// relies on: initial index → changed, no-op re-index → unchanged, new file →
// changed.
func TestReIndexRebuildsZoektOnContentChange(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{
		"a.txt": "alpha tokenone\n",
	})

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	if changed := waitIndexed(t, events, root); !changed {
		t.Errorf("initial index must report changed content")
	}
	results, err := bs.Search(root, "tokenone", "5s")
	if err != nil {
		t.Fatalf("search after initial index: %v", err)
	}
	if len(results.Files) == 0 {
		t.Fatalf("initial content not searchable")
	}

	// A re-index with nothing changed must report unchanged — this is the
	// signal that lets the periodic indexer back off idle projects.
	bs.ReIndex(root)
	if changed := waitIndexed(t, events, root); changed {
		t.Errorf("no-op re-index must report unchanged content")
	}

	// New content appears; the re-index must rebuild the zoekt index so the
	// new token is searchable.
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("beta tokentwo\n"), 0644); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	bs.ReIndex(root)
	if changed := waitIndexed(t, events, root); !changed {
		t.Errorf("re-index after content change must report changed content")
	}
	results, err = bs.Search(root, "tokentwo", "5s")
	if err != nil {
		t.Fatalf("search after re-index: %v", err)
	}
	if len(results.Files) == 0 {
		t.Errorf("content added after the initial index is not searchable: zoekt rebuild was skipped")
	}
}

// TestReIndexReusesHashOnUnchangedMtime guards the incremental-walk fix of
// mg-1236: ReIndex used to delete the entire hash/mtime cache before walking
// a project root, which turned every periodic tick into a full read+hash of
// every file. With the cache preserved, a file whose mtime is unchanged must
// keep its cached hash — even if its bytes differ — proving the walk did not
// re-read it.
func TestReIndexReusesHashOnUnchangedMtime(t *testing.T) {
	bs, root, events := newTestProject(t, map[string]string{
		"f.txt": "original content\n",
	})

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	bs.mu.RLock()
	originalHash := bs.projects[root].FileHashes["f.txt"]
	bs.mu.RUnlock()
	if originalHash == "" {
		t.Fatalf("initial index recorded no hash for f.txt")
	}

	// Rewrite the file with different bytes, then restore its original mtime
	// so the incremental walk sees it as unchanged.
	filePath := filepath.Join(root, "f.txt")
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("modified content\n"), 0644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chtimes(filePath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	bs.ReIndex(root)
	if changed := waitIndexed(t, events, root); changed {
		t.Errorf("mtime-unchanged re-index must report unchanged content")
	}

	bs.mu.RLock()
	newHash := bs.projects[root].FileHashes["f.txt"]
	bs.mu.RUnlock()
	if newHash != originalHash {
		t.Errorf("hash was recomputed despite unchanged mtime: the pre-walk cache was not reused (old %s, new %s)",
			originalHash, newHash)
	}
}
