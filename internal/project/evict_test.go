package project

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/search"
)

// makeGitRepo creates a temp git-marked repo with one file and returns its
// normalized project root.
func makeGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	drainSearch(t)
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatalf("failed to write fixture file: %v", err)
	}
	return addSlashToPath(repo)
}

// TestRemoveEvictsSearchState verifies removing a project also drops the
// search service's in-memory index state for it (gh #39).
func TestRemoveEvictsSearchState(t *testing.T) {
	ProjectFileName = "projects-remove-evict-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	root := makeGitRepo(t)
	Add(&Project{Id: 0, Path: root})
	if !waitForStatus(root, search.StatusReady, 15*time.Second) {
		t.Fatalf("initial index did not become ready")
	}

	if !Remove(root) {
		t.Fatalf("Remove reported project not found")
	}

	if s := search.SearchService.GetStatus(root); s != nil {
		t.Errorf("expected search state evicted after Remove, got %+v", s)
	}
}

// TestReindexTickEvictsUnregisteredProjects verifies the periodic indexer
// sweeps search-service state whose project is no longer in the registry —
// the catch-all for state re-inserted by an index pass that was in flight
// when its project was removed (gh #39).
func TestReindexTickEvictsUnregisteredProjects(t *testing.T) {
	ProjectFileName = "projects-tick-evict-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	root := makeGitRepo(t)
	Add(&Project{Id: 0, Path: root})
	if !waitForStatus(root, search.StatusReady, 15*time.Second) {
		t.Fatalf("initial index did not become ready")
	}

	// Drop the registry entry without going through Remove, simulating search
	// state that outlived its registration.
	projects = []Project{}

	s := newReindexScheduler(time.Minute)
	reindexTick(s, time.Now())

	if st := search.SearchService.GetStatus(root); st != nil {
		t.Errorf("expected tick to evict unregistered project, got %+v", st)
	}
}
