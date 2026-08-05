package main

import (
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/refinery"
)

// refineryRepoActivity answers, for the per-repo dispatch cap (mg-3977),
// whether the refinery holds a merge request for a repository — in flight OR
// queued.
//
// # Why QUEUED counts and not just in-flight
//
// The cap reserves a slot for the refinery so the workers verifying their
// branches cannot starve the process that merges them. Reserving only while a
// gate is RUNNING would be close to useless: the starvation is built by workers
// dispatched BEFORE the gate starts, and once they are building nothing can
// take them back. On 2026-08-05 three merges sat queued 24+ minutes without one
// beginning, precisely because the CPU had already gone to workers whose branches
// were already submitted. The reservation has to precede the gate to prevent that.
//
// # Why the queue is reached through a thunk
//
// An orchestration restart constructs a NEW *refinery.Refinery and reassigns
// the package variable (SetRefineryStarter). A closure over the pointer would
// go on answering from the old, stopped instance — reserving against a queue
// nobody is filling, or missing one that is.
//
// # nil is "not asked", never "idle"
//
// A nil queue means the refinery is disabled by config or has not been
// constructed yet. That returns known=false, which reserves NOTHING: holding a
// slot for a refinery that does not exist would cost a worker on every repo for
// a merge that is never coming. The cap's other failure directions are the same
// shape — see RepoOccupancyFor.
func refineryRepoActivity(queue func() *refinery.Refinery) agent.RefineryActivity {
	return agent.RefineryActivityFunc(func(repo string) (bool, bool) {
		if queue == nil {
			return false, false
		}
		q := queue()
		if q == nil {
			return false, false
		}
		return refineryHasWorkIn(q.QueueWithProcessing(), repo), true
	})
}

// refineryHasWorkIn reports whether any of these merge requests targets repo.
// Split out from the closure above so the repo-matching half can be tested
// against fabricated queue contents — an in-flight merge and a queue eight
// deep are both states a test cannot produce by driving a real refinery, since
// Submit blocks until the merge completes.
func refineryHasWorkIn(mrs []refinery.MergeRequest, repo string) bool {
	for _, mr := range mrs {
		if config.SameRepo(mr.RepoPath, repo) {
			return true
		}
	}
	return false
}
