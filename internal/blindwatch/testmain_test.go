package blindwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope. The default events.Emit writes to
// the real event log, and a Watcher built without an Emit would put
// blind_watch_* fixture events into the live spine, where they are
// indistinguishable from findings about the real fleet.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("blindwatch")
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
