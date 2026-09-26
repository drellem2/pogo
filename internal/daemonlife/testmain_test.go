package daemonlife

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope (see internal/testsandbox).
// Every test here passes an explicit t.TempDir() path, but Path(pogoHome) is one
// argument away from the live pogod.lifecycle.json, and a test that stamped a
// shutdown onto the running daemon's record would make its next boot report a
// clean exit that never happened.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("daemonlife")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsPinned is the positive control for the isolation above.
func TestSandboxIsPinned(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
