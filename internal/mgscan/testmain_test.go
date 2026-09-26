package mgscan

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope (see internal/testsandbox).
// Nothing here forks mg or reads a store — the pool and cache are pure — but the
// callers of this package scan the live work-item store, and a test added here
// later that reached for a real `mg` would otherwise do so against ~/.macguffin.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("mgscan")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSandboxIsPinned is the positive control for the isolation above.
func TestSandboxIsPinned(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
