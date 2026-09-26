package agent

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// The merge-queue credit (mg-976f). Measured 2026-09-07: three pogo polecats,
// every one submitted and waiting on a serial merge queue, held the whole cap
// while the fleet used 0.40 of 10 cores — and a ready high-priority item was
// refused for capacity nobody was using.

// queueHolding is a merge-queue probe for goRepo holding mrs, and a busy
// refinery there (the reserve applies, as it did in the measurement).
func queueHolding(reg *Registry, mrs ...QueuedMerge) {
	reg.SetRefineryActivity(RefineryActivityFunc(func(repo string) (bool, bool) {
		return config.SameRepo(repo, goRepo), true
	}))
	reg.SetMergeQueue(MergeQueueReaderFunc(func(repo string) ([]QueuedMerge, bool) {
		if !config.SameRepo(repo, goRepo) {
			return nil, true
		}
		return mrs, true
	}))
}

// TestAllWorkersWaitingOnTheQueueFreesASlot is the measured state: three of
// three live, all three submitted, the reserve making the cap 2. Before the
// credit this refused.
func TestAllWorkersWaitingOnTheQueueFreesASlot(t *testing.T) {
	reg := capRegistry(t, 3) // a-cat, b-cat, c-cat on mg-a-cat, ...
	// The positive control: the same state with no queue probe is refused, so
	// a pass below is the credit's doing and not a fixture that never filled.
	queueHolding(reg)
	reg.SetMergeQueue(nil)
	if occ := reg.RepoOccupancyFor(goRepo); !occ.WouldRefuse {
		t.Fatalf("control: 3 live workers against cap %d were not refused", occ.Cap)
	}

	// All three spellings a submitter uses: the work item id (the protocol),
	// the bare agent name, and a branch whose author says neither.
	queueHolding(reg,
		QueuedMerge{Author: "mg-a-cat", Branch: "polecat-a-cat"},
		QueuedMerge{Author: "b-cat", Branch: "something-else"},
		QueuedMerge{Author: "someone", Branch: "polecat-c-cat"},
	)
	occ := reg.RepoOccupancyFor(goRepo)
	if occ.Cap != 2 {
		t.Fatalf("cap = %d, want 2 — the fixture no longer reproduces the reserve", occ.Cap)
	}
	if occ.WouldRefuse {
		t.Fatalf("refused with every worker only waiting on the merge queue: count %d, cap %d, excused %v",
			occ.Count, occ.Cap, occ.MergeQueued)
	}
	if got := len(occ.MergeQueued); got != config.DefaultMergeQueuedCredit {
		t.Errorf("excused %d (%v), want the credit %d", got, occ.MergeQueued, config.DefaultMergeQueuedCredit)
	}
	if occ.Count+len(occ.MergeQueued) != 3 {
		t.Errorf("count %d + excused %d != 3 live — a worker vanished from the report",
			occ.Count, len(occ.MergeQueued))
	}
	if len(occ.MergeQueuedOverCredit) != 1 {
		t.Errorf("over-credit = %v, want the one waiting worker the credit did not cover", occ.MergeQueuedOverCredit)
	}
	for _, n := range occ.MergeQueued {
		for _, p := range occ.Polecats {
			if n == p {
				t.Errorf("%s is both excused and counted", n)
			}
		}
	}
	if rr := spawnIntoRepo(t, reg, "cat-next", goRepo); rr.Code == 503 {
		t.Errorf("the spawn path refused what RepoOccupancyFor admitted: %s", rr.Body.String())
	}
}

// TestTheCreditIsBounded is the reason it is a credit and not "release the
// slot on submit": a failed gate sends a waiting worker back to building, so
// what the credit admitted is overshoot, and the overshoot must stay bounded.
func TestTheCreditIsBounded(t *testing.T) {
	reg := capRegistry(t, 4) // a..d-cat, one more than the configured cap
	queueHolding(reg,
		QueuedMerge{Author: "mg-a-cat"}, QueuedMerge{Author: "mg-b-cat"},
		QueuedMerge{Author: "mg-c-cat"}, QueuedMerge{Author: "mg-d-cat"},
	)
	occ := reg.RepoOccupancyFor(goRepo)
	if occ.Count != 2 || !occ.WouldRefuse {
		t.Errorf("4 waiting workers, credit 2, cap 2: count = %d refuse = %v, want 2 and refused",
			occ.Count, occ.WouldRefuse)
	}
	if !strings.Contains(refusalFor(t, reg, goRepo), "credit for waiting workers is spent") {
		t.Error("the refusal does not say the credit is spent")
	}
}

// TestBuildingWorkersStillCount: only a worker the queue names is excused.
func TestBuildingWorkersStillCount(t *testing.T) {
	reg := capRegistry(t, 3)
	queueHolding(reg, QueuedMerge{Author: "mg-a-cat", Branch: "polecat-a-cat"})
	occ := reg.RepoOccupancyFor(goRepo)
	if occ.Count != 2 || !occ.WouldRefuse {
		t.Errorf("two building + one waiting against cap 2: count = %d refuse = %v, want 2 and refused",
			occ.Count, occ.WouldRefuse)
	}
	if !strings.Contains(refusalFor(t, reg, goRepo), "NOT counted, because their branch is in the merge queue") {
		t.Error("the refusal does not name the worker it excused")
	}
}

// TestAnotherReposQueueExcusesNobody: the probe is asked about THIS repo, and
// a worker's MR in a different one says nothing about its CPU here.
func TestAnotherReposQueueExcusesNobody(t *testing.T) {
	reg := capRegistry(t, 3)
	queueHolding(reg)
	reg.SetMergeQueue(MergeQueueReaderFunc(func(repo string) ([]QueuedMerge, bool) {
		if config.SameRepo(repo, otherRepo) {
			return []QueuedMerge{{Author: "mg-a-cat"}, {Author: "mg-b-cat"}}, true
		}
		return nil, true
	}))
	if occ := reg.RepoOccupancyFor(goRepo); len(occ.MergeQueued) != 0 || occ.Count != 3 {
		t.Errorf("another repo's queue excused %v here", occ.MergeQueued)
	}
}

// TestUnaskableQueueAndZeroCreditExcuseNobody: both fail closed — to exactly
// the behaviour before mg-976f.
func TestUnaskableQueueAndZeroCreditExcuseNobody(t *testing.T) {
	reg := capRegistry(t, 3)
	queueHolding(reg)
	reg.SetMergeQueue(MergeQueueReaderFunc(func(string) ([]QueuedMerge, bool) {
		return []QueuedMerge{{Author: "mg-a-cat"}}, false
	}))
	if occ := reg.RepoOccupancyFor(goRepo); occ.Count != 3 {
		t.Errorf("a queue that could not be read excused %v", occ.MergeQueued)
	}

	queueHolding(reg, QueuedMerge{Author: "mg-a-cat"}, QueuedMerge{Author: "mg-b-cat"})
	cfg := config.DefaultDispatchCapConfig()
	cfg.MergeQueuedCredit = 0
	reg.SetDispatchCap(cfg)
	if occ := reg.RepoOccupancyFor(goRepo); occ.Count != 3 {
		t.Errorf("merge_queued_credit = 0 still excused %v", occ.MergeQueued)
	}
}
