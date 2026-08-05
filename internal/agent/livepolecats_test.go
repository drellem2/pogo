package agent

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// LivePolecatSet is now the ONE definition of "who may a git-GC sweep not
// touch", shared by pogod's periodic sweep and by `pogo gc` (mg-1403). Before
// that it existed only in cmd/pogod, and the CLI's copy of the question — built
// from the registry alone — carried mg-0130's defect one caller over. A shared
// answer only helps if the answer itself is pinned, so the reap policy is
// asserted here rather than at either call site.

// TestLivePolecatSet_UnionsRegistryWithWitness is the property both callers
// depend on: a polecat the caller's registry has never heard of is still live if
// the witness says its process is ours and running. That is the post-restart
// case — the registry is empty permanently, and the witness is the only evidence
// that survived.
func TestLivePolecatSet_UnionsRegistryWithWitness(t *testing.T) {
	sandboxWitness(t)
	pid := liveProcess(t)
	if err := RecordPolecatWitness("survivor", pid, "mg-0130", ""); err != nil {
		t.Fatalf("RecordPolecatWitness: %v", err)
	}

	live, err := LivePolecatSet([]string{"registered"})
	if err != nil {
		t.Fatalf("LivePolecatSet: %v", err)
	}
	if !live["registered"] {
		t.Errorf("registry-known polecat dropped from the live set: %v", live)
	}
	if !live["survivor"] {
		t.Errorf("witnessed-alive polecat missing from the live set: %v — a restart empties the registry, "+
			"and without the witness union the survivor's SOLE worktree guard is gone (mg-0130)", live)
	}
}

// TestLivePolecatSet_KeepsUnreadableAndDropsDead pins the asymmetry. Worktree
// removal has no merge gate, so "cannot prove it is ours" must fall on the side
// of keeping the tree, while "provably dead" is exactly the population the sweep
// exists to reclaim.
func TestLivePolecatSet_KeepsUnreadableAndDropsDead(t *testing.T) {
	t.Run("unreadable identity counts as live", func(t *testing.T) {
		sandboxWitness(t)
		pid := liveProcess(t)
		if err := RecordPolecatWitness("murky", pid, "mg-13a3", ""); err != nil {
			t.Fatalf("RecordPolecatWitness: %v", err)
		}
		// The pid answers signals but its start time cannot be read — alive, but
		// we cannot say it is ours.
		prev := procStartFn
		procStartFn = func(int) (time.Time, bool) { return time.Time{}, false }
		t.Cleanup(func() { procStartFn = prev })

		live, err := LivePolecatSet(nil)
		if err != nil {
			t.Fatalf("LivePolecatSet: %v", err)
		}
		if !live["murky"] {
			t.Errorf("an unreadable witness verdict was treated as dead: %v — that reaps the work of a "+
				"live-but-unmeasurable polecat, which is the one outcome this store exists to prevent (mg-13a3)", live)
		}
	})

	t.Run("provably dead is not live", func(t *testing.T) {
		sandboxWitness(t)
		// Witnessed while alive — the only way the record can be written, since
		// a pid with no readable start time has no identity to record — then
		// killed and REAPED, so it stops answering signal 0.
		cmd := exec.Command("sleep", "600")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		if err := RecordPolecatWitness("gone", cmd.Process.Pid, "mg-0d0e", ""); err != nil {
			t.Fatalf("RecordPolecatWitness: %v", err)
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()

		live, err := LivePolecatSet(nil)
		if err != nil {
			t.Fatalf("LivePolecatSet: %v", err)
		}
		if live["gone"] {
			t.Errorf("a provably-dead polecat was held live: %v — the symmetric defect is never reaping "+
				"anything, and this set is the sweep's only eligibility filter", live)
		}
	})
}

// TestLivePolecatSet_PropagatesReadError is the guard on the guard: an
// unreadable store must reach the caller as an ERROR, never as a set that
// happens to be missing everyone. Both callers decline to sweep on it — pogod
// skips the pass, `pogo gc` exits — and neither can do that if the error is
// swallowed here.
func TestLivePolecatSet_PropagatesReadError(t *testing.T) {
	sandboxWitness(t)
	if err := os.WriteFile(WitnessPath(), []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("write corrupt witness: %v", err)
	}
	live, err := LivePolecatSet([]string{"registered"})
	if err == nil {
		t.Fatalf("an unreadable witness store returned no error (live=%v) — a caller cannot tell it from "+
			"an empty fleet, and sweeping against it deletes running polecats' work", live)
	}
	if live != nil {
		t.Errorf("want a nil set alongside the error, got %v — a partially-populated set invites a caller "+
			"to sweep with it anyway", live)
	}
}
