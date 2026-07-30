package auditwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// MG_ROOT is the one that matters here. Every test in this package builds a
// store under t.TempDir() and hands Scan the path explicitly, so none of them
// depends on the ambient value — but DefaultRoot() reads it, and the whole
// subject of this package is a scan over the macguffin store. A suite whose
// results could vary with the contents of one machine's ~/.macguffin is exactly
// the kind of result this package exists to distrust.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("auditwatch")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestStoreRootIsSandboxed is the positive control for the isolation above. Two
// things are asserted and neither implies the other:
//
//   - With MG_ROOT set (the sandbox's own value), DefaultRoot() returns it and
//     it resolves under the throwaway root — not the developer's ~/.macguffin.
//   - With MG_ROOT cleared, DefaultRoot() returns "" under a test binary rather
//     than falling back to $HOME/.macguffin.
//
// Without this the isolation is an unverified claim: dropping it would leave
// every other test in the package green while a future test that called
// DefaultRoot() quietly scanned the live store.
func TestStoreRootIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	got := DefaultRoot()
	if got == "" {
		t.Fatal("DefaultRoot() = \"\" with the sandbox's MG_ROOT set; the envelope is not pinning MG_ROOT")
	}
	if !sandbox.Contains(got) {
		t.Errorf("DefaultRoot() = %s, want a path under the sandbox root %s; a scan would read the live macguffin store",
			got, sandbox.Root)
	}

	t.Setenv("MG_ROOT", "")
	if got := DefaultRoot(); got != "" {
		t.Errorf("DefaultRoot() with MG_ROOT cleared = %q, want \"\" under a test binary", got)
	}
}
