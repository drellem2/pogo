package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// sandbox is the package's private, CHECKED envelope, established by TestMain
// before a single test runs. See internal/testsandbox: HOME, XDG_CONFIG_HOME,
// POGO_HOME and MG_ROOT are pinned under a throwaway root, read back out of the
// process, and refused if any of them resolves onto the developer's live tree.
//
// This package is where the class was first found. Park state is addressed by
// AGENT NAME on disk — ParkFilePath(name) is PromptDir()/<name>/.parked,
// PromptDir() is config.PogoHome()/agents, PogoHome() is $POGO_HOME falling
// back to $HOME/.pogo — so a test that names an agent and does not isolate is
// asking the developer's machine whether that agent happens to be parked.
// mg-6092 found it the loud way: a genuine pm-dealdesk spin-down wrote a real
// park flag and turned TestShouldRespawnAgent_WedgedAgentStillRespawns red on
// an unchanged tree. mg-e8e7 swept the package and found seven more.
//
// Both were fixed by hand, and correctly. What they could not fix is that the
// isolation was still built at the call site: nothing checked that the override
// took, that the other three variables were pinned alongside it, or that the
// value did not resolve back onto the real ~/.pogo. mg-0941 moves the whole
// envelope behind testsandbox, so the next test written here inherits it
// instead of remembering it.
//
// MG_ROOT is new to this package and it is load-bearing here specifically:
// claimrelease.go reads $MG_ROOT directly and falls back to the home directory,
// so a claim/release test under a pinned HOME alone could still reach the live
// ~/.macguffin store.
var sandbox *testsandbox.Sandbox

// sandboxEventLog is the package-wide throwaway event log TestMain installs.
// Per-test redirects (useTempEventLog) must restore THIS path on cleanup, not
// "" — see the note in useTempEventLog for why restoring "" leaked test events
// into the developer's live log (mg-c33e).
var sandboxEventLog string

func TestMain(m *testing.M) {
	sb, down := testsandbox.Main("agent")
	sandbox = sb

	// The event log is redirected separately because it is not addressed by an
	// environment variable: events.Emit resolves its path ONCE, before any test
	// can re-point HOME. Without this the package writes agent_spawned and
	// agent_attach_rebound records into the developer's live ~/.pogo/events.log,
	// and agent_attach_rebound is an operator alarm for a production fault
	// (mg-d216) — a test run must not manufacture one.
	sandboxEventLog = filepath.Join(sb.Root, "events.log")
	events.SetLogPathForTesting(sandboxEventLog)

	// The sentinel-drift alert sink is redirected for the same reason and is
	// NOT covered by either of the above (mg-54f8). nudgereadylog_test.go drives
	// the real prompt-ready gate all the way to its deadline arm, which records a
	// genuine MISS on the initial-nudge key of the process-global detector. Three
	// such fixtures exist and driftMinSamples is 4, so this package is one test —
	// or one `-count=2`, which accumulates in the same process — from crossing the
	// threshold and running defaultDriftAlert, whose loud half MAILS THE FLEET
	// COORDINATOR. The event half would land in sandboxEventLog and the mail in
	// the sandbox MG_ROOT, so the envelope above contains the damage; what it does
	// not do is stop a unit test from manufacturing a fleet drift alarm at all.
	restoreDriftSink := StubDriftSinkForTesting()

	code := m.Run()

	restoreDriftSink()
	events.SetLogPathForTesting("")
	down()
	os.Exit(code)
}

// TestPackageIsolationIsEstablished is the positive control for the isolation
// above, and the assertion this package went two tickets without: it re-proves
// the sandbox from inside a test, so dropping TestMain's call cannot leave the
// rest of the package green while the suite goes back to reading the machine's
// real park state.
func TestPackageIsolationIsEstablished(t *testing.T) {
	testsandbox.Verify(t, sandbox)

	// And the package-specific half: the path the park lookups actually run
	// through has to land inside the sandbox, not merely near it.
	if got := ParkFilePath("pm-dealdesk"); !sandbox.Contains(got) {
		t.Errorf("ParkFilePath(pm-dealdesk) = %s, want a path under the sandbox root %s; "+
			"the package is resolving park state through the real home", got, sandbox.Root)
	}

	// And the half no environment variable can pin: the drift sink is an
	// in-process function value, so nothing in the sandbox envelope prevents a
	// ready-gate miss run from reaching the coordinator's real inbox. Asserted by
	// asking which sink is installed rather than by tripping the threshold — a
	// control of that shape would send the mail it exists to prevent.
	if DriftSinkIsProductionForTesting() {
		t.Error("the process-global sentinel-drift sink is still the production " +
			"one: this package drives the real prompt-ready gate to its deadline " +
			"arm, so a run of misses can cross the drift threshold and mail the " +
			"fleet coordinator from a unit test. TestMain must call " +
			"StubDriftSinkForTesting.")
	}
}
