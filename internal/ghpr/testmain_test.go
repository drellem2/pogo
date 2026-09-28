package ghpr

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope (see internal/testsandbox).
// Every test here runs a stub `gh` first on PATH, but a real gh reads its auth
// and config from HOME, and a stub that failed to install would otherwise reach
// the operator's live GitHub login.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("ghpr")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestPackageIsolationIsEstablished is the positive control for the isolation
// above.
func TestPackageIsolationIsEstablished(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
