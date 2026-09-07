package heartwatch

import (
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is this package's CHECKED envelope. HOME, XDG_CONFIG_HOME, POGO_HOME
// and MG_ROOT are pinned under a throwaway root before any test runs.
//
// It matters more here than in most packages: Dir() resolves through
// config.PogoHome(), and this machine's live tree carries three sweep.log files
// whose agents have not existed for months. A test that fell through to the
// real tree would read them and pass or fail on the developer's fleet — the
// mg-6092 / mg-e8e7 / mg-5336 shape. The default events.Emit is the other half:
// a Watcher built without an Emit would write heart_watch_* fixture events into
// the live spine, where they are indistinguishable from findings about the real
// fleet.
var sandbox *testsandbox.Sandbox

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("heartwatch")
	sandbox = sb
	code := m.Run()
	down()
	os.Exit(code)
}

// TestPackageStateIsSandboxed is the positive control for that envelope.
func TestPackageStateIsSandboxed(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !sandbox.Contains(home) {
		t.Errorf("HOME = %s, want a path under the sandbox root %s", home, sandbox.Root)
	}
	if !sandbox.Contains(Dir()) {
		t.Errorf("Dir() = %s, want a path under the sandbox root %s; this package's scan "+
			"would otherwise read the developer's live ~/.pogo/agents tree", Dir(), sandbox.Root)
	}
}
