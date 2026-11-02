package gitvcs

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox pins HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT under a throwaway
// root before any test runs (internal/testsandbox). This package writes only
// into t.TempDir() git fixtures, but git itself reads $HOME/.gitconfig, and a
// later test should inherit the envelope rather than have to remember it.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("gitvcs")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

// TestSandboxIsInForce is the positive control for the isolation above.
func TestSandboxIsInForce(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
