package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// The store is test-safe BY DEFAULT, and this file is the control (mg-da48).
//
// WHAT HAPPENED. `go test ./internal/agent/` wrote PHANTOM polecat records into
// the LIVE witness store — the test process's real pid under Go fixture names —
// and pogod's orphan detector read them back, could not find them in its
// registry (they were never polecats), and mailed the mayor an authoritative
// `kill <pid>`. Three such mails on 2026-07-17 in ten minutes, for pids
// 71259/99124/438, all already dead and recyclable by the time they were read.
//
// WHY THE EXISTING GUARD DID NOT CATCH IT, and why these tests are written the
// way they are. witnessPathOverride and sandboxWitness both already existed;
// witness_test.go calls sandboxWitness sixteen times. The two files that
// polluted the fleet called it zero times, because they spawn agents while
// testing NUDGES and ATTACH — the witness is not their subject and they had no
// reason to know it existed. So these tests deliberately do NOT call
// sandboxWitness: sandboxing them would make them model the file that already
// remembers, when the whole defect is the files that do not. Each one below
// stands where a naive sibling stands — override empty, POGO_HOME pointing at a
// store it has never heard of — and asserts that store stays untouched.
//
// A note on what "the live store" means here. These tests point POGO_HOME at a
// temp dir and treat it as the fleet's real store. That is not a weakening: the
// assertion is that WitnessPath refuses to resolve to the CONFIG-DERIVED path
// under test, whatever that path happens to be. Aiming the test at the operator's
// actual ~/.pogo would prove the same property by risking the very corruption it
// is testing for, which is not a trade any test should make.

// realStorePath is where the witness WOULD live if this process were pogod:
// the config-derived path. It is the thing that must not be touched.
func realStorePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(config.PogoHome(), witnessFileName)
}

// unsandboxed puts the package back in the state a fresh test file starts in —
// no override at all — and restores whatever was there afterwards. This models
// the naive sibling, which is the only thing worth modelling here.
func unsandboxed(t *testing.T) {
	t.Helper()
	prev := witnessPathOverride
	witnessPathOverride = ""
	t.Cleanup(func() { witnessPathOverride = prev })
}

// TestWitnessPathNeverResolvesToTheLiveStoreUnderTest is the property in its
// most direct form: with no override, running under `go test`, the path must not
// be the one pogod uses. Everything else in this file is a consequence.
func TestWitnessPathNeverResolvesToTheLiveStoreUnderTest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	unsandboxed(t)

	got := WitnessPath()
	live := realStorePath(t)

	if got == live {
		t.Fatalf("WitnessPath() = %s, the CONFIG-DERIVED store — a test with no override is pointed at "+
			"the live fleet's state. This is mg-da48: any test that spawns an agent writes a phantom "+
			"polecat here, and pogod mails the mayor a kill for its pid.", got)
	}
	if strings.HasPrefix(got, config.PogoHome()) {
		t.Errorf("WitnessPath() = %s, which is under PogoHome() (%s) — a different filename in the live "+
			"state dir is still the live state dir.", got, config.PogoHome())
	}
}

// TestWitnessPathIsStableAcrossCalls guards the memoisation. A path that
// differed per call would make the store write-only: loadWitness and saveWitness
// would address different files, and every read would answer "no record" —
// silently turning the suite green while measuring nothing.
func TestWitnessPathIsStableAcrossCalls(t *testing.T) {
	unsandboxed(t)
	if a, b := WitnessPath(), WitnessPath(); a != b {
		t.Errorf("WitnessPath() is not stable: %s then %s — the store would be unreadable by its own writer", a, b)
	}
}

// TestExplicitOverrideStillWins pins that the default did not eat the existing
// mechanism. witness_test.go's sixteen sandboxWitness calls give each test its
// own file, which is isolation from OTHER TESTS — a different question from
// isolation from the LIVE FLEET, and one the default deliberately does not
// answer. Both must keep working.
func TestExplicitOverrideStillWins(t *testing.T) {
	want := filepath.Join(t.TempDir(), "polecat-witness.json")
	prev := witnessPathOverride
	witnessPathOverride = want
	t.Cleanup(func() { witnessPathOverride = prev })

	if got := WitnessPath(); got != want {
		t.Errorf("WitnessPath() = %s, want the explicit override %s — the test-safe default must not "+
			"override a test that asked for a specific path", got, want)
	}
}

// TestSpawningAgentDoesNotTouchTheLiveStore is THE acceptance test for mg-da48,
// and it is today's incident reproduced.
//
// It does exactly what nudge_test.go and attach_regression_test.go do: it
// records a witness for a live process under a fixture name, with no sandbox and
// no awareness that this store exists. Before the fix, that wrote
// {"name":"ready-test","pid":<the go test process>} into the fleet's real store
// and earned the mayor a `kill`. The assertion is that the live store is not
// merely correct afterwards — it was never created at all.
func TestSpawningAgentDoesNotTouchTheLiveStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	unsandboxed(t)

	live := realStorePath(t)
	pid := liveProcess(t)

	// The naive sibling's incidental spawn, through the production path: Spawn
	// calls noteWitnessStart, which calls RecordPolecatWitness. A fixture name,
	// because that is what the fleet actually saw.
	noteWitnessStart(&Agent{Type: TypePolecat, Name: "ready-test", PID: pid, WorkItemID: ""})

	if _, err := os.Stat(live); !os.IsNotExist(err) {
		data, _ := os.ReadFile(live)
		t.Fatalf("a test that spawned an agent WROTE THE LIVE WITNESS STORE at %s (stat err: %v).\n"+
			"pogod's orphan detector reads this file, finds no registry entry for a Go fixture name, and "+
			"mails the mayor `kill %d` — for a pid that is dead and recyclable seconds later (mg-da48).\n"+
			"contents:\n%s", live, err, pid, string(data))
	}

	// And the record did land — in the sandbox. A default that made the store
	// silently unwritable would pass the assertion above while breaking the
	// witness for every test that legitimately uses it, so prove the write
	// happened somewhere real.
	if _, err := os.Stat(WitnessPath()); err != nil {
		t.Errorf("the witness was not written to the sandbox either (%v) — the default must REDIRECT the "+
			"store, not disable it: mg-13a3's tests depend on this write actually happening", err)
	}
}

// TestWitnessRoundTripsThroughTheDefaultSandbox proves the redirect is a real,
// working store and not a write-only hole. loadWitness/saveWitness/PolecatWitness
// must all agree on the default path, or the sixteen tests in witness_test.go are
// the only ones measuring anything and every naive sibling silently no-ops.
func TestWitnessRoundTripsThroughTheDefaultSandbox(t *testing.T) {
	unsandboxed(t)
	// Not this suite's other fixture names: the default store is shared by the
	// whole test binary, so a name collision here would be a flake.
	const name = "cat-default-roundtrip"
	pid := liveProcess(t)

	if err := RecordPolecatWitness(name, pid, "mg-da48"); err != nil {
		t.Fatalf("RecordPolecatWitness through the default sandbox: %v", err)
	}
	t.Cleanup(func() { noteWitnessExit(&Agent{Type: TypePolecat, Name: name}) })

	if got := PolecatWitness(name); got != WitnessAlive {
		t.Errorf("PolecatWitness(%s) = %s, want alive — the default path does not round-trip, so the "+
			"store is write-only under test and proves nothing", name, got)
	}
}
