package progresswatch

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
// Every test here builds its Snapshot by hand and injects its own emitter, so
// none is SUPPOSED to touch live state — which is precisely the reasoning under
// which mg-6092, mg-e8e7, mg-5336 and mg-3412 each shipped a suite that reached
// the real ~/.pogo anyway. A watcher constructed with a nil Emit falls back to
// events.Emit, one line of test setup away from appending to the operator's
// live events.log.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("progresswatch")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsPinned is the positive control for the isolation above: without
// it, the envelope is an unverified claim and dropping it would leave every
// other test in the package green.
func TestSandboxIsPinned(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
