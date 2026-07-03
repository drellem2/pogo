package search

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/pkg/plugin"
)

// makeTestRepo creates a minimal project tree with one searchable file and
// returns its root (with trailing separator, as project roots are stored).
func makeTestRepo(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	content := fmt.Sprintf("// unique-%s-token\n", marker)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0644); err != nil {
		t.Fatalf("could not write test file: %v", err)
	}
	root, err := absolute(dir)
	if err != nil {
		t.Fatalf("could not resolve test repo root: %v", err)
	}
	return root
}

func waitForStatus(t *testing.T, g *BasicSearch, root string, want IndexingStatus) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := g.GetStatus(root); s != nil && s.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := g.GetStatus(root)
	t.Fatalf("project %s never reached status %q (last status: %+v)", root, want, s)
}

// TestWorkerPoolIndexesMultipleProjects drives several projects through the
// sharded build pool at once and verifies each ends Ready and searchable —
// builds no longer funnel through a single writer goroutine (gh #39).
func TestWorkerPoolIndexesMultipleProjects(t *testing.T) {
	g := createBasicSearch()

	const n = 5
	roots := make([]string, n)
	for i := 0; i < n; i++ {
		roots[i] = makeTestRepo(t, fmt.Sprintf("repo%d", i))
	}
	for _, root := range roots {
		req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
		go g.Index(&req)
	}
	for i, root := range roots {
		waitForStatus(t, g, root, StatusReady)
		res, err := g.Search(root, fmt.Sprintf("unique-repo%d-token", i), "10s")
		if err != nil {
			t.Fatalf("search failed for %s: %v", root, err)
		}
		if len(res.Files) != 1 {
			t.Errorf("expected 1 match in %s, got %d", root, len(res.Files))
		}
	}
}

// TestUpdaterShardRoutingIsStable verifies updates for the same project root
// always land on the same shard, which is what keeps per-project build
// ordering intact across the pool.
func TestUpdaterShardRoutingIsStable(t *testing.T) {
	g := createBasicSearch()
	u := g.updater
	if len(u.shards) != indexWorkerPoolSize {
		t.Fatalf("expected %d shards, got %d", indexWorkerPoolSize, len(u.shards))
	}
	for _, root := range []string{"/a/", "/b/", "/some/longer/path/"} {
		first := u.shardIndex(root)
		if first < 0 || first >= len(u.shards) {
			t.Fatalf("shard index %d for %s out of range", first, root)
		}
		for i := 0; i < 10; i++ {
			if got := u.shardIndex(root); got != first {
				t.Fatalf("shard routing for %s not stable: %d then %d", root, first, got)
			}
		}
	}
}
