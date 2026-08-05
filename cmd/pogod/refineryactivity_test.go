package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/refinery"
)

const capRepo = "/Users/daniel/dev/pogo"

// TestQueuedMergeCountsAsRefineryWork is the claim that makes the reserved slot
// worth having. On 2026-08-05 three merges sat QUEUED for 24+ minutes without
// one beginning, because the host's CPU had already gone to workers whose
// branches were already submitted. A reserve that only recognised the in-flight
// merge would have reserved nothing at the moment it mattered.
func TestQueuedMergeCountsAsRefineryWork(t *testing.T) {
	queued := []refinery.MergeRequest{
		{ID: "mr-1", RepoPath: capRepo, Status: refinery.StatusQueued},
	}
	if !refineryHasWorkIn(queued, capRepo) {
		t.Error("a queued merge did not count as refinery work")
	}
}

func TestInFlightMergeCountsAsRefineryWork(t *testing.T) {
	inFlight := []refinery.MergeRequest{
		{ID: "mr-1", RepoPath: capRepo, Status: refinery.StatusProcessing},
	}
	if !refineryHasWorkIn(inFlight, capRepo) {
		t.Error("an in-flight merge did not count as refinery work")
	}
}

// TestAnotherReposMergeDoesNotReserve. The reserve is per-repo for the same
// reason the cap is: a merge in one repository does not contend with workers
// building a different one.
func TestAnotherReposMergeDoesNotReserve(t *testing.T) {
	elsewhere := []refinery.MergeRequest{
		{ID: "mr-1", RepoPath: "/Users/daniel/dev/other", Status: refinery.StatusProcessing},
	}
	if refineryHasWorkIn(elsewhere, capRepo) {
		t.Error("a merge in another repo reserved a slot here")
	}
}

func TestRepoSpellingStillMatchesTheQueue(t *testing.T) {
	mrs := []refinery.MergeRequest{{ID: "mr-1", RepoPath: capRepo + "/", Status: refinery.StatusQueued}}
	if !refineryHasWorkIn(mrs, capRepo) {
		t.Error("a trailing slash on the submitted repo path hid the merge from the reserve")
	}
}

func TestEmptyQueueReservesNothing(t *testing.T) {
	if refineryHasWorkIn(nil, capRepo) {
		t.Error("an empty queue reserved a slot")
	}
}

// TestNilRefineryIsNotAsked, never "idle". Holding a slot for a refinery that
// is disabled or not yet constructed costs one worker per repo for a merge
// that is never coming.
func TestNilRefineryIsNotAsked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		thunk func() *refinery.Refinery
	}{
		{"nil thunk", nil},
		{"thunk returns nil", func() *refinery.Refinery { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			has, known := refineryRepoActivity(tc.thunk).HasWorkIn(capRepo)
			if known {
				t.Error("known = true — a refinery that does not exist was reported as asked")
			}
			if has {
				t.Error("has = true for a refinery that does not exist")
			}
		})
	}
}
