package hookarm

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox.
//
// Every test here works in a t.TempDir, so nothing in this suite is aimed at
// the developer's live ~/.pogo today. The envelope is established anyway: this
// package's whole subject is a control that was correct in the tree and absent
// from the running system, and a suite exempted because it "happens not to need
// it yet" is the same bet.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("hookarm")
	sandbox = sb

	code := m.Run()

	down()
	os.Exit(code)
}

// TestSuiteIsSandboxed is the positive control for the isolation above.
func TestSuiteIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)
}
