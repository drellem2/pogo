package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/driver"
)

// TestDiscoverNewReposSkipsProtectedHomeDir is the mg-5cd6 regression guard: a
// git repo living under a macOS TCC-protected home subtree (~/Documents, …) is
// NOT auto-registered by the index_roots scan — indexing it would fire a
// permission popup on every tick — while a repo under a normal dev path is.
func TestDiscoverNewReposSkipsProtectedHomeDir(t *testing.T) {
	ProjectFileName = "projects-protected-home-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	home := t.TempDir()
	t.Setenv("HOME", home)

	// A repo under ~/Documents (protected) and one under ~/dev (normal).
	protectedRepo := filepath.Join(home, "Documents", "secret-repo")
	normalRepo := filepath.Join(home, "dev", "work-repo")
	for _, r := range []string{protectedRepo, normalRepo} {
		if err := os.MkdirAll(filepath.Join(r, ".git"), 0755); err != nil {
			t.Fatalf("failed to create repo %s: %v", r, err)
		}
	}

	// scanRootForRepos has no ephemeral-path check (index_roots is an explicit
	// allowlist), so a temp-based $HOME does not interfere with this assertion.
	SetIndexRoots([]string{
		filepath.Join(home, "Documents"),
		filepath.Join(home, "dev"),
	})
	defer SetIndexRoots(nil)

	discoverNewRepos()

	if GetProjectByPath(addSlashToPath(protectedRepo)) != nil {
		t.Errorf("repo under ~/Documents must NOT be registered (would trigger macOS TCC popups): %s", protectedRepo)
	}
	if GetProjectByPath(addSlashToPath(normalRepo)) == nil {
		t.Errorf("repo under ~/dev should be registered normally: %s", normalRepo)
	}
}

// TestSearchAndCreateRefusesProtectedHomeDir verifies the auto-register path
// (searchAndCreate, reached via Visit / `pogo visit`) refuses a repo located in
// a protected home subtree (mg-5cd6).
func TestSearchAndCreateRefusesProtectedHomeDir(t *testing.T) {
	ProjectFileName = "projects-protected-visit-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Point TMPDIR at a sibling dir so ~/Desktop is not itself seen as an
	// ephemeral (temp) path — otherwise the ephemeral guard in searchAndCreate
	// would refuse the repo first and this test would pass for the wrong
	// reason, never exercising the protected-home guard.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	protectedRepo := filepath.Join(home, "Desktop", "notes")
	if err := os.MkdirAll(filepath.Join(protectedRepo, ".git"), 0755); err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}
	if isEphemeralPath(protectedRepo) {
		t.Skip("test environment reports the fake $HOME as ephemeral; cannot isolate the protected-home guard here")
	}

	numBefore := len(projects)
	proj, err := searchAndCreate(protectedRepo)
	if err != nil {
		t.Fatalf("searchAndCreate returned error: %v", err)
	}
	if proj != nil {
		t.Errorf("expected no project for a protected-home repo, got %#v", proj)
	}
	if len(projects) != numBefore {
		t.Errorf("protected-home repo must not be auto-registered; projects went %d -> %d", numBefore, len(projects))
	}
}
