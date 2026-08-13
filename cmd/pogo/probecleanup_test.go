package main

// The check-* probes must not abandon their store (mg-60eb).
//
// Both `pogo check-orphans --probe` and `pogo check-turns --probe` build a
// throwaway macguffin-shaped store in a temp directory and had a
// `defer os.RemoveAll(dir)` sitting over it. That defer is correct and it did
// not run: deferred functions do not run on os.Exit, and every verdict arm past
// it — instrument failure, blind, fail — exited. So the store survived exactly
// when the probe did NOT pass, which is the only occasion anyone runs one twice.
//
// It is the same defect that filled this host's volume to 100% on 2026-08-13 and
// failed every merge gate on it with Errno 28: a cleanup that runs only when
// nothing went wrong. The remedy is structural rather than another defer — the
// function that OWNS the directory now returns an exit code and its caller does
// the exiting, so there is no arm on which the defer can be skipped.
//
// The load-bearing case here is TestProbeVerdictsReturnRatherThanExit. It is a
// positive control in the strongest available sense: against the pre-fix code it
// does not fail, it takes the whole test binary down with it, because the
// function under test really did call os.Exit on the arm it forces.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unwritableTempDir returns a directory that os.MkdirTemp cannot create inside,
// or skips. Skipping rather than failing because a suite running as root can
// write to a 0500 directory and the case is then unconstructable, which is a
// fact about the runner and not about the tree.
func unwritableTempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sealed")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if probe, err := os.MkdirTemp(dir, "writable"); err == nil {
		os.RemoveAll(probe)
		t.Skip("this runner can write into a 0500 directory (root?); the instrument-failure arm cannot be constructed")
	}
	return dir
}

// TestProbeVerdictsReturnRatherThanExit forces the instrument-failure arm and
// requires the code to come back as a VALUE.
//
// A returned code is what makes every other arm's cleanup reachable, so this is
// the assertion the leak fix actually rests on. Against the pre-fix code the arm
// is an os.Exit(3) and this process ends there — no failure message, no other
// test in the package reported. That is louder than a FAIL line, and it is the
// honest rendering of what the old shape did.
func TestProbeVerdictsReturnRatherThanExit(t *testing.T) {
	sealed := unwritableTempDir(t)

	t.Run("check-orphans", func(t *testing.T) {
		t.Setenv("TMPDIR", sealed)
		if got := orphanProbeVerdict(true); got != exitInstrumentFailure {
			t.Errorf("orphanProbeVerdict = %d, want %d (instrument failure)", got, exitInstrumentFailure)
		}
	})

	t.Run("check-turns", func(t *testing.T) {
		t.Setenv("TMPDIR", sealed)
		if got := turnProbeVerdict(true); got != exitInstrumentFailure {
			t.Errorf("turnProbeVerdict = %d, want %d (instrument failure)", got, exitInstrumentFailure)
		}
	})
}

// TestProbesLeaveNothingInTMPDIR is the measurement, and it is deliberately
// indifferent to the verdict: this host's load decides whether the orphan probe
// passes or comes back blind, and NEITHER outcome licenses leaving a store
// behind. Asserting on the verdict here would make the file load-sensitive for
// no gain — the arms that a verdict distinguishes are the ones the test above
// already covers structurally.
func TestProbesLeaveNothingInTMPDIR(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
		run    func(bool) int
	}{
		{"check-orphans", "orphanprobe", orphanProbeVerdict},
		{"check-turns", "turnprobe", turnProbeVerdict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A private $TMPDIR, so the count is about this call and not about
			// whatever else is running on the box — which, on this host, is
			// several gates and polecats at once.
			pinned := t.TempDir()
			t.Setenv("TMPDIR", pinned)

			tc.run(true)

			entries, err := os.ReadDir(pinned)
			if err != nil {
				t.Fatalf("ReadDir %s: %v", pinned, err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), tc.prefix) {
					t.Errorf("%s abandoned its store %s in $TMPDIR; one per invocation is how a"+
						" volume reaches 100%% (mg-60eb)", tc.name, e.Name())
				}
			}
		})
	}
}
