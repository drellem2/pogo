package supervision

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope: HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT pinned under a throwaway root, read back out of the process, and
// refused if any of them resolves onto the developer's live tree.
//
// It is not optional here. Observe reads config.LockfilePath(), which is
// POGO_HOME-derived — the LIVE pogod's lockfile on this machine. A test that
// exercised Observe without this envelope would read the running fleet's
// daemon, and its verdict would depend on whether a deploy happened to be
// bouncing pogod at that instant.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("supervision")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsInForce is the positive control: without it the isolation above
// is an unverified claim, and every other test in the package would stay green
// while the envelope silently stopped taking.
func TestSandboxIsInForce(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
