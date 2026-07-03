package search

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/pkg/plugin"
)

// TestZoektBuildReusesCarriedContents proves the single-read path (gh #39):
// the zoekt build consumes the bytes the hash walk carried instead of
// re-reading the file. The file on disk is rewritten between walk and build;
// if the build re-read from disk it would index the disk token, not the
// carried one.
func TestZoektBuildReusesCarriedContents(t *testing.T) {
	g := createBasicSearch()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f.go")
	carried := []byte("// carried-token\n")
	if err := os.WriteFile(filePath, []byte("// disk-token\n"), 0644); err != nil {
		t.Fatalf("could not write test file: %v", err)
	}
	root, err := absolute(dir)
	if err != nil {
		t.Fatalf("could not resolve root: %v", err)
	}

	h := sha256.Sum256(carried)
	proj := &IndexedProject{
		Root:       root,
		Paths:      []string{"f.go"},
		FileHashes: map[string]string{"f.go": hex.EncodeToString(h[:])},
		FileMtimes: map[string]int64{"f.go": time.Now().UnixNano()},
		Status:     StatusIndexing,
	}
	if !g.reserveContent(len(carried)) {
		t.Fatal("could not reserve carried-content budget")
	}
	contents := map[string][]byte{"f.go": carried}

	prev := IndexedProject{}
	g.serializeProjectIndex(proj, &prev, false, contents)

	res, err := g.Search(root, "carried-token", "10s")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("expected carried content to be indexed, got %d file matches", len(res.Files))
	}
	res, err = g.Search(root, "disk-token", "10s")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(res.Files) != 0 {
		t.Errorf("expected disk content NOT to be indexed (file should not be re-read), got %d matches", len(res.Files))
	}
	if got := g.carriedBytes.Load(); got != 0 {
		t.Errorf("carried-content budget not fully released after build: %d bytes still held", got)
	}
}

// TestCarriedContentBudgetReleasedAfterIndex runs a full Index pass and
// verifies the carried-content reservation drains back to zero once the
// build completes.
func TestCarriedContentBudgetReleasedAfterIndex(t *testing.T) {
	g := createBasicSearch()
	root := makeTestRepo(t, "budget")

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	g.Index(&req)
	waitForStatus(t, g, root, StatusReady)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if g.carriedBytes.Load() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("carried-content budget not released after index: %d bytes still held", g.carriedBytes.Load())
}

// TestReserveContentEnforcesBudget verifies reservations past the budget are
// refused and refunds are exact.
func TestReserveContentEnforcesBudget(t *testing.T) {
	g := createBasicSearch()
	if !g.reserveContent(carriedContentBudget - 1) {
		t.Fatal("reservation within budget refused")
	}
	if g.reserveContent(2) {
		t.Fatal("reservation past budget accepted")
	}
	if g.carriedBytes.Load() != carriedContentBudget-1 {
		t.Fatalf("failed reservation leaked into the budget: %d", g.carriedBytes.Load())
	}
	g.releaseContents(map[string][]byte{"a": make([]byte, carriedContentBudget-1)})
	if g.carriedBytes.Load() != 0 {
		t.Fatalf("budget not zero after release: %d", g.carriedBytes.Load())
	}
}
