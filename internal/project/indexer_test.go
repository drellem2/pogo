package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRepoDiscoveryTickRegistersNewRepo is the end-to-end guard for the
// timer-driven loop: a git repo created under a configured index_root after
// StartRepoDiscovery is running is registered within a few ticks.
func TestRepoDiscoveryTickRegistersNewRepo(t *testing.T) {
	ProjectFileName = "projects-indexer-tick-test.json"
	Init()
	defer RemoveSaveFile()

	parentDir := t.TempDir()
	SetIndexRoots([]string{parentDir})
	defer SetIndexRoots(nil)

	// Wait for the goroutine to fully exit on teardown: a mid-tick goroutine
	// surviving into the next test races with its Init() / SetIndexRoots()
	// writes to package globals.
	ctx, cancel := context.WithCancel(context.Background())
	done := StartRepoDiscovery(ctx, 50*time.Millisecond)
	defer func() {
		cancel()
		<-done
	}()

	newRepo := filepath.Join(parentDir, "late-repo")
	if err := os.MkdirAll(filepath.Join(newRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if GetProjectByPath(addSlashToPath(newRepo)) != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("repo created under index_root was not registered by the discovery tick: %s", newRepo)
}

// TestDiscoverNewReposScansIndexRoots verifies the index_roots scan that
// replaces the watch-driven Scanner: a new git repo dropped under a configured
// index_root is auto-registered on the next tick.
func TestDiscoverNewReposScansIndexRoots(t *testing.T) {
	ProjectFileName = "projects-indexer-discover-test.json"
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
