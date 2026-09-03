package logliveness

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
// POGO_HOME-derived — the LIVE pogod's lockfile on this machine — and then runs
// lsof against whatever pid it finds. Without this envelope the package whose
// whole subject is "you are reading the wrong process's record" would be
// reading the running fleet's daemon in its own tests.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("logliveness")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsInForce is the positive control: without it the isolation above
// is an unverified claim, and every other test here would stay green while the
// envelope silently stopped taking.
func TestSandboxIsInForce(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
