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
// Clearing POGO_HOME alone was not enough, and that gap is what mg-e8e7
// closes. With POGO_HOME unset the state root becomes $HOME/.pogo, so a test
// that never re-points HOME reads the DEVELOPER'S LIVE state dir. Every park
// check runs through it: ParkFilePath is PromptDir()/<name>/.parked, and
// Registry.Respawn, Registry.Stop and Agent.ShouldRespawn all consult it. A
// sweep of the whole tree (see isolateParkState) found seven such tests in
// this package — they answered differently depending on whether the agent
// names they invent happened to carry a park flag on the machine running
// them. So TestMain now re-points HOME at a throwaway tree as well, making
// hermeticity the package default rather than something each new test has to
// remember. Tests that assert on park behaviour still call isolateParkState
// for a per-test tree; this is the backstop under them, and under every test
// written next.
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

	sandboxHome, err := os.MkdirTemp("", "pogo-agent-home")
	if err != nil {
		panic("create temp home dir: " + err.Error())
	}
	os.Setenv("HOME", sandboxHome)

	logDir, err := os.MkdirTemp("", "pogo-agent-events")
	if err != nil {
		panic("create temp event log dir: " + err.Error())
	}
	sandboxEventLog = filepath.Join(logDir, "events.log")
	events.SetLogPathForTesting(sandboxEventLog)

	code := m.Run()

	events.SetLogPathForTesting("")
	os.RemoveAll(logDir)
	os.RemoveAll(sandboxHome)
	os.Exit(code)
}

// isolateParkState re-points HOME (and POGO_HOME under it) at a throwaway tree
// so a test's park lookups read a clean state dir of its own.
//
// Park state lives on disk and is addressed by AGENT NAME:
// ParkFilePath(name) is PromptDir()/<name>/.parked, PromptDir() is
// config.PogoHome()/agents, and PogoHome() is $POGO_HOME falling back to
// $HOME/.pogo. A test that names an agent and does not isolate HOME is
// therefore asking the developer's machine whether that agent is parked, and
// the answer changes as crew agents are parked and woken during the day.
//
// mg-6092 found this the loud way: a genuine pm-dealdesk spin-down wrote a
// real park flag and turned TestShouldRespawnAgent_WedgedAgentStillRespawns
// red on an unchanged tree, with a message that blamed the synthfail detector
// for a verdict the scanner had never even been asked for. The quiet direction
// is the dangerous one — a suppression test whose agent happens to be parked
// passes for the wrong reason, and a broken restart gate ships green.
//
// mg-e8e7 swept the tree for the rest of the class by running the whole suite
// twice under synthetic HOMEs identical but for planted .parked flags — one
// for every agent-name literal in every _test.go — and diffing the per-test
// outcomes. Seven tests in this package changed answer; every other package
// was already clean (cmd/pogod's sandboxPogoHome does the same thing this
// does). Those seven now call this, and TestMain re-points HOME underneath
// them so the next test to forget cannot reopen the hole.
func isolateParkState(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
}
