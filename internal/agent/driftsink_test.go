package agent

import (
	"sync"
	"testing"
)

// The exported test seam (mg-54f8). StubDriftSinkForTesting and
// DriftSinkIsProductionForTesting exist so that a test binary in ANY package —
// internal/cursor and internal/claude drive the real trust-dialog hook loop, and
// this package drives the real prompt-ready gate — cannot reach
// defaultDriftAlert, whose loud half mails the fleet coordinator.
//
// Three packages now assert their isolation by calling the predicate. That makes
// the predicate load-bearing: one that always answered "not production" would
// leave all three controls green and every suite exposed. So the tests below
// prove it can detect the production sink, not just that it currently says no.

// TestDriftSinkPredicateDetectsTheProductionSink is the mutation control. It
// installs defaultDriftAlert deliberately — the only place in the test tree that
// does — and requires the predicate to say so. Recording nothing while it is
// installed is what keeps this safe: the sink is reachable for the length of two
// comparisons and is never invoked.
func TestDriftSinkPredicateDetectsTheProductionSink(t *testing.T) {
	if DriftSinkIsProductionForTesting() {
		t.Fatal("production sink installed before this test touched anything — " +
			"TestMain's StubDriftSinkForTesting call is missing")
	}

	restore := readyDrift.setAlert(defaultDriftAlert)
	if !DriftSinkIsProductionForTesting() {
		restore()
		t.Fatal("predicate did not recognise defaultDriftAlert: every package " +
			"control built on it is vacuous, and the suites are exposed while " +
			"reporting that they are not")
	}
	restore()

	if DriftSinkIsProductionForTesting() {
		t.Fatal("restore() did not take the production sink back out")
	}
}

// TestStubRestoreDoesNotReinstateProduction pins the shape of restore. It puts
// back the sink that was there, NOT the default — which is the same distinction
// that leaked test events into the developer's live log when an event-log
// override restored "" instead of the package sandbox (mg-c33e). Nested stubs are
// the normal case: TestMain installs one for the whole binary and a single test
// may want its own on top.
func TestStubRestoreDoesNotReinstateProduction(t *testing.T) {
	restore := StubDriftSinkForTesting()
	restore()

	if DriftSinkIsProductionForTesting() {
		t.Fatal("restoring a nested stub reinstated the production sink instead " +
			"of TestMain's stub — the rest of this binary would then be able to " +
			"mail the coordinator")
	}
}

// TestRecordRoutesToTheInstalledSink proves the swap is on the path record
// actually takes. Without this, the stub could be installed correctly and still
// be bypassed. A unique key keeps the samples out of the real initial-nudge and
// trust-dialog windows.
func TestRecordRoutesToTheInstalledSink(t *testing.T) {
	var fired []DriftAlert
	restore := readyDrift.setAlert(func(a DriftAlert) { fired = append(fired, a) })
	defer restore()

	for i := 0; i < driftMinSamples; i++ {
		readyDrift.record("seam-test/gate", true, driftMeta{
			Provider: "seam-test", Gate: "gate", Sentinel: "s",
		})
	}
	if len(fired) != 1 {
		t.Fatalf("installed sink received %d alerts, want 1 — record is not "+
			"reading the sink the seam writes", len(fired))
	}
}

// TestDriftSinkSwapIsRaceFreeAgainstRecord is the control mg-d578 earns. That
// ticket's defect was a test-only global written by one test while a watcher
// goroutine leaked from a previous test still read it — a genuine data race in
// the fixtures, which failed the package 2 runs in 3 and trained everyone to
// re-run. A drift-sink seam is exactly that shape: a process-global function
// value, written by tests, read by whatever hook loops are still winding down.
//
// So the sink is written under the detector's mutex AND read under it, and this
// test is what makes that claim falsifiable: drop either lock in setAlert or in
// record and `go test -race` reports on this test.
func TestDriftSinkSwapIsRaceFreeAgainstRecord(t *testing.T) {
	outer := StubDriftSinkForTesting()
	defer outer()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				readyDrift.record("race-test/gate", i%2 == 0, driftMeta{
					Provider: "race-test", Gate: "gate", Sentinel: "s",
				})
			}
		}(g)
	}
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// Every sink in flight is a swallowing stub, so a threshold
				// crossing mid-swap still cannot reach the coordinator.
				StubDriftSinkForTesting()
			}
		}()
	}
	wg.Wait()

	// outer() runs last and writes unconditionally, so the intermediate stubs
	// above cannot leave a stray sink installed for the rest of the binary.
}
