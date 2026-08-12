package reviewdecl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established before a
// single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT are pinned under a throwaway root, read back out of the process,
// and refused if any of them resolves onto the developer's live tree.
//
// MG_ROOT is the one that matters here. resolveRoot reads it first, exactly as
// mg does, so an unpinned MG_ROOT would point a full scan at the fleet's real
// work items. This package only reads — but a suite whose verdicts depend on
// what the fleet happened to be doing that hour is precisely the kind of result
// this whole detector family exists to distrust, and the detector's own report
// would be about tickets no test wrote.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("reviewdecl")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestStoreRootIsSandboxed is the positive control for the isolation above.
// Without it the envelope is an unverified claim: dropping the TestMain would
// leave every other test in this package green while the suite went back to
// scanning the operator's live macguffin store.
func TestStoreRootIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	got := (Source{}).resolveRoot()
	if got == "" {
		t.Fatal("resolveRoot() = \"\" with the sandbox's MG_ROOT set; the envelope is not pinning MG_ROOT")
	}
	if !sandbox.Contains(got) {
		t.Errorf("resolveRoot() = %s, want a path under the sandbox root %s; a scan would read the "+
			"live macguffin store", got, sandbox.Root)
	}
	if want := filepath.Join(os.Getenv("MG_ROOT"), "work"); got != want {
		t.Errorf("resolveRoot() = %s, want %s — the scan must read the WORK subdirectory of the "+
			"resolved store, not the store root", got, want)
	}

	// With MG_ROOT cleared the test-binary branch is the floor UNDER the
	// envelope: it must refuse rather than fall through to $HOME/.macguffin.
	t.Setenv("MG_ROOT", "")
	if got := (Source{}).resolveRoot(); got != "" {
		t.Errorf("resolveRoot() with MG_ROOT cleared = %q, want \"\" under a test binary", got)
	}
	if _, err := (Source{}).Items(); err == nil {
		t.Error("a scan with no resolvable root returned no error — an unresolvable store must " +
			"never render as a clean, empty scan")
	}
}

// TestExplicitRootWinsOverMGRoot. Every other test in this package passes an
// explicit Root, so the precedence has to hold or they would all silently read
// the sandbox store instead of their own fixture and agree with each other for
// the wrong reason.
func TestExplicitRootWinsOverMGRoot(t *testing.T) {
	t.Setenv("MG_ROOT", "/nonexistent/elsewhere")
	dir := t.TempDir()
	if got := (Source{Root: dir}).resolveRoot(); got != dir {
		t.Errorf("resolveRoot() = %s, want the explicit root %s", got, dir)
	}
}
