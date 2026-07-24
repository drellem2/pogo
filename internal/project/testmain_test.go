package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/search"
)

// sandboxHome is the throwaway $HOME TestMain installs for the whole package.
// Tests that need to assert on hermeticity compare against it; tests that want
// a tree of their own still call t.Setenv("HOME", t.TempDir()) on top.
var sandboxHome string

// TestMain clears any ambient POGO_HOME (e.g. exported by the developer's
// shell) before the package's tests run. Since mg-3dc3 every pogo state path
// derives from $POGO_HOME (falling back to $HOME/.pogo), and most tests
// isolate themselves by re-pointing HOME at a temp dir — an inherited
// POGO_HOME would defeat that isolation and leak test writes into the real
// state dir. Tests that exercise POGO_HOME semantics set it explicitly via
// t.Setenv.
//
// Clearing POGO_HOME alone was not enough, and that gap is what mg-5336
// closes. With POGO_HOME unset the state root becomes $HOME/.pogo, so a test
// that never re-points HOME does not just READ the developer's live state dir
// — this package WRITES to it. Init() (project.go) resolves
// projectFile = config.PogoHome()/ProjectFileName, then os.MkdirAll's that
// home and os.Create's the file; Add/Visit/SaveProjects os.WriteFile it and
// RemoveSaveFile deletes it. Every unisolated caller — setUp in
// project_test.go, and the Init() calls in evict_test.go, indexer_test.go and
// registry_test.go — therefore created directories and files inside the real
// ~/.pogo of whoever ran `go test`. The only reason the operator's real
// projects.json survived is that those tests each assign ProjectFileName a
// "projects-…-test.json" name first; a future test that calls Init() before
// setting it inherits the "projects.json" default (Init's own fallback) and
// overwrites the live registry. That is a silent state corruption, not a red
// test, so TestMain now re-points HOME at a throwaway tree, making
// hermeticity the package default rather than something each new test has to
// remember. Same structural fix as mg-6092 and mg-e8e7 in internal/agent.
func TestMain(m *testing.M) {
	os.Unsetenv("POGO_HOME")

	home, err := os.MkdirTemp("", "pogo-project-home")
	if err != nil {
		panic("create temp home dir: " + err.Error())
	}
	sandboxHome = home
	os.Setenv("HOME", home)

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
}

// TestStateRootIsSandboxed is the positive control for the isolation above: it
// asserts that the state root the package actually writes through resolves
// under the throwaway tree, not under the machine's real home. Without it the
// isolation is an unverified claim — a later edit could drop the Setenv and
// every other test in the package would still pass, quietly writing to
// ~/.pogo again.
func TestStateRootIsSandboxed(t *testing.T) {
	under := func(path string) bool {
		return path == sandboxHome ||
			strings.HasPrefix(filepath.Clean(path), sandboxHome+string(filepath.Separator))
	}

	if got := config.PogoHome(); !under(got) {
		t.Errorf("PogoHome() = %s, want a path under the sandbox home %s; the "+
			"package is resolving state through the real home", got, sandboxHome)
	}

	origName := ProjectFileName
	t.Cleanup(func() { ProjectFileName = origName })
	ProjectFileName = "projects-sandbox-guard-test.json"
	Init()
	defer RemoveSaveFile()

	if !under(projectFile) {
		t.Errorf("projectFile = %s, want a path under the sandbox home %s; "+
			"Init() would write the registry into the real ~/.pogo",
			projectFile, sandboxHome)
	}
}

// drainSearch waits, at test teardown, for the search plugin to finish the
// index passes this test kicked off.
//
// Registering a project — via Add, Visit or discoverNewRepos — hands it to the
// search plugin, whose ProcessProject spawns `go Index`. That goroutine
// creates <repo>/.pogo/search and writes the index there, long after the
// registering call returned. For a repo under t.TempDir() the test's RemoveAll
// then races those writes and fails with "TempDir RemoveAll cleanup: directory
// not empty" (mg-36d9, seen on main under full-suite load).
//
// Call this after the last t.TempDir() in the test: cleanups run LIFO, so a
// drain registered later runs before the removals.
func drainSearch(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if !search.SearchService.Quiesce(30 * time.Second) {
			t.Error("search plugin still had index work in flight 30s after the " +
				"test ended; its writes race t.TempDir cleanup")
		}
	})
}
