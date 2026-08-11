package turnwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope. HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT are pinned under a throwaway root before any test runs.
//
// The Watcher's own dependencies are injected — every test here supplies its
// own Scan and Mail — so nothing in this package reaches the live tree on
// purpose. That is exactly the situation the isolation exists for: the default
// events.Emit writes to the real event log, and a future test that builds a
// Watcher without an Emit would put turn_watch_* fixture events into the live
// spine, where they are indistinguishable from findings about the real fleet.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("turnwatch")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

// TestWatcherStateIsSandboxed is the positive control for that envelope.
func TestWatcherStateIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !sandbox.Contains(home) {
		t.Errorf("HOME = %s, want a path under the sandbox root %s; a Watcher built "+
			"without an Emit would write turn_watch_* events into the live event log",
			home, sandbox.Root)
	}
}
