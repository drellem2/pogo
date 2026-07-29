package project

import (
	"os"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/search"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// This package WRITES. Init() (project.go) resolves projectFile as
// config.PogoHome()/ProjectFileName, then MkdirAll's that home and Create's the
// file; Add/Visit/SaveProjects WriteFile it and RemoveSaveFile deletes it. Every
// unisolated caller — setUp in project_test.go, and the Init() calls in
// evict_test.go, indexer_test.go and registry_test.go — therefore created
// directories and files inside the real ~/.pogo of whoever ran `go test`. The
// only reason the operator's projects.json survived is that those tests each
// assign ProjectFileName a "projects-…-test.json" name first; a test that calls
// Init() before setting it inherits the "projects.json" default and overwrites
// the live registry. That is silent state corruption, not a red test. mg-5336.
//
// mg-5336 fixed it by hand, and correctly, with os.Unsetenv("POGO_HOME") plus
// os.Setenv("HOME", os.MkdirTemp(...)). What it could not fix is that the
// isolation was still built at the call site: nothing checked that the override
// took, that the value did not resolve back onto the real tree, or that the
// other two variables were pinned at all. mg-0941 moves the envelope behind
// testsandbox so the next test written here inherits it.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("project")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestStateRootIsSandboxed is the positive control for the isolation above: it
// asserts that the state root the package actually writes through resolves
// under the throwaway tree, not under the machine's real home. Without it the
// isolation is an unverified claim — a later edit could drop it and every other
// test in the package would still pass, quietly writing to ~/.pogo again.
func TestStateRootIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	if got := config.PogoHome(); !sandbox.Contains(got) {
		t.Errorf("PogoHome() = %s, want a path under the sandbox root %s; the package is "+
			"resolving state through the real home", got, sandbox.Root)
	}

	origName := ProjectFileName
	t.Cleanup(func() { ProjectFileName = origName })
	ProjectFileName = "projects-sandbox-guard-test.json"
	Init()
	defer RemoveSaveFile()

	if !sandbox.Contains(projectFile) {
		t.Errorf("projectFile = %s, want a path under the sandbox root %s; Init() would "+
			"write the registry into the real ~/.pogo", projectFile, sandbox.Root)
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
