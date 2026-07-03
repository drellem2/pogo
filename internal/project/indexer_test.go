package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/driver"
	"github.com/drellem2/pogo/internal/search"
)

// waitForStatus polls the search service until projectRoot reaches want, or
// the timeout elapses.
func waitForStatus(root string, want search.IndexingStatus, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s := search.SearchService.GetStatus(root); s != nil && s.Status == want {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// indexedFileCount returns the number of files in projectRoot's index, or -1
// if the project is unknown to the search service.
func indexedFileCount(root string) int {
	s := search.SearchService.GetStatus(root)
	if s == nil {
		return -1
	}
	return s.FileCount
}

// TestPeriodicReindexPicksUpNewFile is the acceptance-bar-2 regression guard:
// a file created after the initial index must be reflected within one
// index_interval of the timer-driven re-indexer.
func TestPeriodicReindexPicksUpNewFile(t *testing.T) {
	ProjectFileName = "projects-indexer-reindex-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	repo := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "first.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatalf("failed to write fixture file: %v", err)
	}
	root := addSlashToPath(repo)

	// Register the repo and wait for the initial index to settle.
	Add(&Project{Id: 0, Path: root})
	if !waitForStatus(root, search.StatusReady, 15*time.Second) {
		t.Fatalf("initial index did not become ready")
	}
	before := indexedFileCount(root)
	if before < 1 {
		t.Fatalf("expected the initial index to contain at least the seed file, got %d", before)
	}

	// A new file appears after the project has been indexed.
	if err := os.WriteFile(filepath.Join(repo, "second.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatalf("failed to write new file: %v", err)
	}

	// Start the periodic indexer with a short interval so the test is fast.
	// Wait for the indexer goroutine to fully exit on teardown: a mid-tick
	// goroutine surviving into the next test races with its Init() /
	// SetIndexRoots() writes to package globals.
	ctx, cancel := context.WithCancel(context.Background())
	done := StartPeriodicIndexer(ctx, 100*time.Millisecond)
	defer func() {
		cancel()
		<-done
	}()

	// Within a few ticks the new file must be reflected in the index.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if indexedFileCount(root) > before {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := indexedFileCount(root); got <= before {
		t.Errorf("periodic re-index did not pick up the new file: file count stayed at %d", got)
	}
}

// TestDiscoverNewReposScansIndexRoots verifies the index_roots scan that
// replaces the watch-driven Scanner: a new git repo dropped under a configured
// index_root is auto-registered on the next tick.
func TestDiscoverNewReposScansIndexRoots(t *testing.T) {
	ProjectFileName = "projects-indexer-discover-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	parentDir := t.TempDir()
	SetIndexRoots([]string{parentDir})
	defer SetIndexRoots(nil)

	newRepo := filepath.Join(parentDir, "new-repo")
	if err := os.MkdirAll(filepath.Join(newRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create new repo: %v", err)
	}

	numBefore := len(projects)
	discoverNewRepos()

	if len(projects) != numBefore+1 {
		t.Fatalf("expected %d projects after discovery, got %d", numBefore+1, len(projects))
	}
	if GetProjectByPath(addSlashToPath(newRepo)) == nil {
		t.Errorf("discovered repo not registered: %s", newRepo)
	}

	// A second pass must not double-register the same repo.
	discoverNewRepos()
	if len(projects) != numBefore+1 {
		t.Errorf("discovery re-registered an already-known repo: %d projects", len(projects))
	}
}

// TestDiscoverNewReposRespectsPogoStop verifies a repo carrying a .pogo_stop
// marker is left unregistered by the index_roots scan.
func TestDiscoverNewReposRespectsPogoStop(t *testing.T) {
	ProjectFileName = "projects-indexer-pogostop-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	parentDir := t.TempDir()
	SetIndexRoots([]string{parentDir})
	defer SetIndexRoots(nil)

	repo := filepath.Join(parentDir, "opted-out-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".pogo_stop"), []byte{}, 0644); err != nil {
		t.Fatalf("failed to create .pogo_stop: %v", err)
	}

	numBefore := len(projects)
	discoverNewRepos()

	if len(projects) != numBefore {
		t.Errorf("a repo with .pogo_stop must not be registered; projects went %d -> %d", numBefore, len(projects))
	}
}

// TestDiscoverNewReposNoIndexRootsIsNoop verifies that with no index_roots
// configured — the zero-config default — discovery registers nothing.
func TestDiscoverNewReposNoIndexRootsIsNoop(t *testing.T) {
	ProjectFileName = "projects-indexer-noroots-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()
	SetIndexRoots(nil)

	// A git repo exists, but no index_root points at it.
	parentDir := t.TempDir()
	repo := filepath.Join(parentDir, "unconfigured-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}

	numBefore := len(projects)
	discoverNewRepos()

	if len(projects) != numBefore {
		t.Errorf("discovery must be a no-op with no index_roots; projects went %d -> %d", numBefore, len(projects))
	}
}
