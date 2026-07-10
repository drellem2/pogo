package search

import (
	"testing"

	"github.com/drellem2/pogo/pkg/plugin"
)

// TestEvictDropsProjectState verifies Evict releases a project's in-memory
// index state (gh #39): after eviction the project is unknown to status and
// file lookups, and evicting an unknown root is a no-op.
func TestEvictDropsProjectState(t *testing.T) {
	g := createBasicSearch()
	root := makeTestRepo(t, g, "evict")

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	g.Index(&req)
	waitForStatus(t, g, root, StatusReady)

	g.Evict(root)

	if s := g.GetStatus(root); s != nil {
		t.Errorf("expected no status after eviction, got %+v", s)
	}
	if _, err := g.GetFiles(root); err == nil {
		t.Error("expected GetFiles to fail after eviction")
	}

	// Evicting a root that is not resident must not panic or error.
	g.Evict("/never/registered/")
}
