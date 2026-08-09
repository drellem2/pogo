package verdictwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established before a
// single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT are pinned under a throwaway root, read back out of the process,
// and refused if any of them resolves onto the developer's live tree.
//
// MG_ROOT is the one that matters here, twice over. Every test builds a store
// under t.TempDir() and hands Scan the path explicitly — but DefaultRoot() reads
// MG_ROOT, and the LIVE PROBE shells out to the real `mg`, which reads it too. A
// probe that leaked onto ~/.macguffin would file work items and send mail into
// the fleet's real store, which is the one failure mode this package must not
// have: it is the only file here that WRITES anything.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("verdictwatch")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestStoreRootIsSandboxed is the positive control for the isolation above.
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
