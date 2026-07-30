package proctable

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox.
//
// This package reads the HOST — /proc and `ps` — and touches no pogo state at
// all. The isolation is adopted anyway, for the reason internal/hostload gives:
// "this suite does not need it" is the claim every unisolated suite could make
// until one of them turned out to be wrong.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("proctable")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsInForce is the positive control for the isolation above.
func TestSandboxIsInForce(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
