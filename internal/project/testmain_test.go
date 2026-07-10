package project

import (
	"os"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/search"
)

// TestMain clears any ambient POGO_HOME (e.g. exported by the developer's
// shell) before the package's tests run. Since mg-3dc3 every pogo state path
// derives from $POGO_HOME (falling back to $HOME/.pogo), and most tests
// isolate themselves by re-pointing HOME at a temp dir — an inherited
// POGO_HOME would defeat that isolation and leak test writes into the real
// state dir. Tests that exercise POGO_HOME semantics set it explicitly via
// t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("POGO_HOME")
	os.Exit(m.Run())
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
