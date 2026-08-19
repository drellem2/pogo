package main

import (
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// refineryMergedWork answers, for the merged-work dispatch gate (mg-9d4e),
// whether a work item's branch has already landed on the target.
//
// # Why the refinery and not git
//
// The question the gate asks is "has this item's work merged", and the refinery
// holds that fact EXACTLY: it recorded the merge itself, keyed by the `--author`
// the submitter passed, which for a polecat is the work-item id. The git-side
// answer is a heuristic — attribute a branch to an item by its name or by a
// trailing "(mg-xxxx)" in a commit subject, then ask whether its commits are
// present upstream by patch id — and the stranded-work gate documents both of
// those routes as incomplete. A gate whose refusal is wrong gets overridden on
// reflex, so this one refuses only on a record the refinery wrote.
//
// # Why the queue is reached through a thunk
//
// Same reason as refineryRepoActivity: an orchestration restart constructs a NEW
// *refinery.Refinery and reassigns the package variable, and a closure over the
// old pointer would answer from an instance nobody is using.
//
// # nil is "cannot tell", never "nothing merged"
//
// A nil queue means the refinery is disabled by config or not yet constructed.
// That answers false — dispatch proceeds — and it is the right direction: a
// daemon with no refinery performs no merges, so there is nothing for this gate
// to have missed. The gate's own doc comment states the failure direction it
// shares with every other gate at that chokepoint.
func refineryMergedWork(queue func() *refinery.Refinery) agent.MergedWorkGate {
	return agent.MergedWorkGateFunc(func(workItemID string) (agent.MergedWork, bool) {
		if queue == nil {
			return agent.MergedWork{}, false
		}
		q := queue()
		if q == nil {
			return agent.MergedWork{}, false
		}
		return mergedWorkFor(q.History(), workItemID)
	})
}

// mergedWorkFor picks the most recent LANDED merge authored by workItemID out of
// a history slice. Split out from the closure above so the matching rules can be
// tested against fabricated history — a merged MR is not a state a test can
// produce by driving a real refinery.
//
// History() is oldest-first, so the scan runs backwards and returns the newest
// match. Which one it returns matters for the message and not for the verdict:
// an item that merged twice has certainly merged.
//
// THREE THINGS ARE REQUIRED, and each exclusion is a case where refusing would
// be wrong rather than merely noisy:
//
//   - The author must MATCH, case-folded and trimmed. `--author` is a free
//     string and arrives as a work-item id from a polecat and as an agent name
//     from a crew agent; only the first can equal the id being dispatched.
//   - The status must be StatusMerged. A queued, processing, failed or cancelled
//     MR is not landed work, and a failed one is precisely the item that SHOULD
//     be dispatched again.
//   - The merge must NOT be PRFlow. A PR-flow merge lands on an integration
//     branch and the deliverable — a pull request against the default branch —
//     does not exist yet (mg-7746). The item is legitimately unfinished there,
//     and a gate that refused it would block the dispatch that finishes it.
//     Everything else that lands on the repo default is refused, --defer-done
//     included: the code is on the target either way, and re-deriving it is the
//     harm this gate exists to stop.
func mergedWorkFor(history []refinery.MergeRequest, workItemID string) (agent.MergedWork, bool) {
	want := strings.ToLower(strings.TrimSpace(workItemID))
	if want == "" {
		return agent.MergedWork{}, false
	}
	for i := len(history) - 1; i >= 0; i-- {
		mr := history[i]
		if mr.Status != refinery.StatusMerged || mr.PRFlow {
			continue
		}
		if strings.ToLower(strings.TrimSpace(mr.Author)) != want {
			continue
		}
		return agent.MergedWork{
			MR:        mr.ID,
			Branch:    mr.Branch,
			Target:    mr.TargetRef,
			MergedSHA: mr.MergedSHA,
			MergedAt:  mr.DoneTime,
		}, true
	}
	return agent.MergedWork{}, false
}
