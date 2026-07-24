package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/driver"
)

// TestPruneRegistryRemovesStaleEntries verifies the mg-d205 registry GC:
// entries with nonexistent paths and entries under ephemeral roots are pruned,
// while a normal existing repo is kept.
func TestPruneRegistryRemovesStaleEntries(t *testing.T) {
	ProjectFileName = "projects-prune-test.json"
	Init()
	defer RemoveSaveFile()

	aServiceAbs, err := absolute(aService)
	if err != nil {
		t.Fatalf("could not resolve a-service: %v", err)
	}

	tempRepo := t.TempDir() // exists, but ephemeral (under os.TempDir())
	nonexistent := "/Users/pogo/definitely/not/here/repo-xyz/"

	projects = []Project{
		{Id: 1, Path: aServiceAbs},
		{Id: 2, Path: tempRepo + string(filepath.Separator)},
		{Id: 3, Path: nonexistent},
	}

	PruneRegistry()

	if len(projects) != 1 {
		t.Fatalf("expected 1 project kept after GC, got %d: %v", len(projects), projects)
	}
	if projects[0].Path != aServiceAbs {
		t.Errorf("expected %s to be kept, got %s", aServiceAbs, projects[0].Path)
	}
}

// TestPruneRegistryNoOpWhenClean verifies GC leaves a clean registry untouched.
func TestPruneRegistryNoOpWhenClean(t *testing.T) {
	ProjectFileName = "projects-prune-clean-test.json"
	Init()
	defer RemoveSaveFile()

	aServiceAbs, err := absolute(aService)
	if err != nil {
		t.Fatalf("could not resolve a-service: %v", err)
	}
	projects = []Project{{Id: 1, Path: aServiceAbs}}

	PruneRegistry()

	if len(projects) != 1 {
		t.Errorf("expected the clean registry to be untouched, got %d entries", len(projects))
	}
}

func TestIsEphemeralPath(t *testing.T) {
	tmp := t.TempDir()
	if !isEphemeralPath(tmp) {
		t.Errorf("OS temp dir %s should be ephemeral", tmp)
	}
	if !isEphemeralPath("/tmp/some/repo") {
		t.Errorf("/tmp path should be ephemeral")
	}
	if !isEphemeralPath("/private/tmp/some/repo") {
		t.Errorf("/private/tmp path should be ephemeral")
	}

	// A synthetic home, NOT os.UserHomeDir(). isEphemeralPath is pure string
	// work (plus gitgc.DefaultPolecatsDir, which derives from $HOME), so no
	// such directory needs to exist — and reading the machine's real home made
	// the assertions below depend on where that home happens to live. Once
	// TestMain re-pointed HOME at a throwaway tree under the OS temp dir
	// (mg-5336), every path built from it matched the tempRoots rule and the
	// "nested inside a worktree is NOT ephemeral" case failed for a reason
	// that has nothing to do with the polecats rule it exists to test.
	home := "/Users/pogo-test-home"
	t.Setenv("HOME", home)
	polecatRoot := filepath.Join(home, ".pogo", "polecats", "d999")
	if !isEphemeralPath(polecatRoot) {
		t.Errorf("polecat worktree root %s should be ephemeral", polecatRoot)
	}
	// A repo nested deeper inside a worktree is NOT a worktree root and must
	// not be flagged — this is what keeps test fixtures safe.
	nested := filepath.Join(polecatRoot, "_testdata", "x")
	if isEphemeralPath(nested) {
		t.Errorf("path nested inside a worktree %s should not be ephemeral", nested)
	}

	if isEphemeralPath("/Users/someone/dev/project") {
		t.Errorf("an ordinary dev path should not be ephemeral")
	}
}

func TestWithinIndexRoots(t *testing.T) {
	// No allowlist: everything is eligible (default zero-config behavior).
	SetIndexRoots(nil)
	if !withinIndexRoots("/anywhere/at/all") {
		t.Errorf("with no allowlist configured, every path must be eligible")
	}

	SetIndexRoots([]string{"/Users/me/dev", "/work"})
	defer SetIndexRoots(nil)

	if !withinIndexRoots("/Users/me/dev/proj/") {
		t.Errorf("a path under an index root should be eligible")
	}
	if !withinIndexRoots("/work") {
		t.Errorf("an index root itself should be eligible")
	}
	if withinIndexRoots("/Users/me/other/proj") {
		t.Errorf("a path outside the index roots should not be eligible")
	}
}

// TestVisitRefusesEphemeralRepo verifies that auto-registration via Visit
// refuses repos under ephemeral roots, preventing the registry from
// re-accumulating polecat-worktree / temp entries (mg-d205).
func TestVisitRefusesEphemeralRepo(t *testing.T) {
	ProjectFileName = "projects-visit-ephemeral-test.json"
	driver.Init()
	defer driver.Kill()
	Init()
	defer RemoveSaveFile()

	tempRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempRepo, ".git"), 0755); err != nil {
		t.Fatalf("could not create temp repo: %v", err)
	}

	numBefore := len(projects)
	resp, _ := Visit(VisitRequest{Path: tempRepo})
	if resp != nil {
		t.Errorf("expected no project for an ephemeral repo, got %#v", resp)
	}
	if len(projects) != numBefore {
		t.Errorf("ephemeral repo must not be auto-registered; projects went %d -> %d", numBefore, len(projects))
	}
}
