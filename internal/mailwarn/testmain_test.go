package mailwarn

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox.
//
// This package is pure — it parses a string and compares names against a
// RosterReport the caller supplies, so it reads no live state today. The
// envelope is still established, because the ledger is a ratchet and "this
// suite happens not to need it yet" is exactly the state every unisolated suite
// was in before it grew the call that made it need it.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("mailwarn")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSuiteIsSandboxed is the positive control for the isolation above.
func TestSuiteIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
