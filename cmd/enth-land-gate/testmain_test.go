package main

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox pins HOME, XDG_CONFIG_HOME, POGO_HOME and MG_ROOT under a throwaway
// root before any test runs (internal/testsandbox). The tool reads none of
// them, but the enth it drives would read a saved login under HOME. Test-only:
// the binary itself imports nothing from pogo.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("enth-land-gate")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

// TestSandboxIsInForce is the positive control for the isolation above.
func TestSandboxIsInForce(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
