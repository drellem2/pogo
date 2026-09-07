package refusalstreak

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope. Scan resolves transcript globs
// under a caller-supplied home, and Locate would happily walk the developer's
// real ~/.claude if a test ever passed "" or forgot the override — which is the
// shape of mg-6092, mg-e8e7 and mg-5336, three separate tickets for tests that
// read the live fleet.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("refusalstreak")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

func TestPackageStateIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !sandbox.Contains(home) {
		t.Errorf("HOME = %s, want a path under the sandbox root %s", home, sandbox.Root)
	}
}
