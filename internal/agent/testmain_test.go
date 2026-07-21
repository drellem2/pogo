package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// TestMain clears any ambient POGO_HOME (e.g. exported by the developer's
// shell) before the package's tests run. Since mg-3dc3 every pogo state path
// derives from $POGO_HOME (falling back to $HOME/.pogo), and most tests
// isolate themselves by re-pointing HOME at a temp dir — an inherited
// POGO_HOME would defeat that isolation and leak test writes into the real
// state dir. Tests that exercise POGO_HOME semantics set it explicitly via
// t.Setenv.
//
// It also re-points the event log at a throwaway file. Spawning agents emits
// real events, and events.Emit resolves its path once, before any test can
// re-point HOME — so without this the package writes agent_spawned and
// agent_attach_rebound records into the developer's live ~/.pogo/events.log.
// agent_attach_rebound is an operator alarm for a production fault (mg-d216);
// a test run must not manufacture one.
// sandboxEventLog is the package-wide throwaway event log TestMain installs.
// Per-test redirects (useTempEventLog) must restore THIS path on cleanup, not
// "" — see the note in useTempEventLog for why restoring "" leaked test events
// into the developer's live log (mg-c33e).
var sandboxEventLog string

func TestMain(m *testing.M) {
	os.Unsetenv("POGO_HOME")

	logDir, err := os.MkdirTemp("", "pogo-agent-events")
	if err != nil {
		panic("create temp event log dir: " + err.Error())
	}
	sandboxEventLog = filepath.Join(logDir, "events.log")
	events.SetLogPathForTesting(sandboxEventLog)

	code := m.Run()

	events.SetLogPathForTesting("")
	os.RemoveAll(logDir)
	os.Exit(code)
}
