package hostload

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox.
//
// This package reads the HOST, not the developer's ~/.pogo — it execs `ps` and
// `sysctl` and touches no pogo state at all. The isolation is adopted anyway,
// because the ratchet in internal/testsandbox is a ratchet: "this suite does
// not need it" is exactly the claim every unisolated suite could make until one
// of them turned out to be wrong. Adopting costs nothing here and means the
// next test written in this package inherits the envelope rather than having to
// notice it is missing.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("hostload")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsInForce is the positive control for the isolation above.
func TestSandboxIsInForce(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
