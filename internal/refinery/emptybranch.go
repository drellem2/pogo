package refinery

import (
	"fmt"
	"log"
	"path/filepath"
	"time"
)

// A branch with no commits of its own is a FAILED merge, not a landed one
// (mg-c184d).
//
// # What happened
//
// On 2026-10-01 a polecat pushed its branch at main's tip with nothing ahead —
// ~240 lines of its work were uncommitted in its worktree — and submitted it.
// The already-merged guard (gh #34) saw a branch head that was an ancestor of
// origin/main and resolved the MR as merged in three seconds, naming the
// PREVIOUS item's commit as "Merged as". pogod then closed the item done, mailed
// COMPLETED to the filer and reaped the polecat. Nothing had been delivered.
//
// # Why ancestry cannot decide it
//
// The refinery rebases before merging, so a branch whose work really landed is
// normally NOT an ancestor of the target: the landed copies are new commits.
// Containment is true in exactly two cases — the merge was a fast-forward and
// the tip IS the merged commit, or the branch carries nothing — and the probe
// sees them identically. Only the refinery's own record separates them.
//
// # The rule
//
//   - "Already landed" is returned only when the refinery's history holds a
//     prior MERGED MR (not itself an already-merged no-op) for the same branch
//     or work item, in the same repo, whose MergedSHA the head is reachable
//     from. That is the resubmit-after-losing-track case gh #34 exists for.
//   - Otherwise a contained head fails with class=defect at stage
//     "empty-branch": "branch carries no commits ahead of <target>". No MERGED
//     event, no MergedSHA, no already_merged — and so OnFailed, not OnMerged,
//     which is what keeps pogod from closing the item or reaping the author.
//
// FAILURE DIRECTION: a legitimate resubmit whose prior MR has been pruned from
// history (100 entries / 7 days) is refused too. That is the safe side — the
// author stays alive and the message says how to check and close by hand —
// where the opposite side is the silent false-done this exists to end.

// stageEmptyBranch is the pipeline stage an empty-branch refusal is recorded
// at. It is a verdict stage (see verdictStages): the tree was inspected and
// answered.
const stageEmptyBranch = "empty-branch"

// priorMergeCovering returns the prior merged MR whose landed commit contains
// head, or nil when the refinery has no such record.
//
// A prior AlreadyMerged entry never qualifies on its own: before mg-c184d that
// flag was set on ancestry alone, so history can hold false ones, and a record
// that only vouches for itself is how one false-done would launder the next.
func (r *Refinery) priorMergeCovering(mr *MergeRequest, wtDir, head string) *MergeRequest {
	r.mu.Lock()
	var candidates []MergeRequest
	for i := len(r.history) - 1; i >= 0; i-- {
		h := r.history[i]
		if h == nil || h == mr || h.ID == mr.ID {
			continue
		}
		if h.Status != StatusMerged || h.AlreadyMerged || h.MergedSHA == "" {
			continue
		}
		if filepath.Clean(h.RepoPath) != filepath.Clean(mr.RepoPath) {
			continue
		}
		sameBranch := h.Branch == mr.Branch
		sameItem := mr.Author != "" && h.Author == mr.Author
		if !sameBranch && !sameItem {
			continue
		}
		candidates = append(candidates, *h)
	}
	r.mu.Unlock()

	for i := range candidates {
		c := &candidates[i]
		covered, err := isAncestor(wtDir, head, c.MergedSHA)
		if err != nil {
			// The landed commit is unknown to this clone (rewritten target, a
			// different repo under the same path). Not evidence either way, so
			// it does not vouch for the head.
			log.Printf("refinery: MR %s prior-merge check against %s (%s) inconclusive: %v", mr.ID, c.ID, shortSHA(c.MergedSHA), err)
			continue
		}
		if covered {
			return c
		}
	}
	return nil
}

// landedInCrashWindow decides, for a recovered in-flight MR whose head is
// contained in the target, whether a merge actually put it there. Either a
// prior merged MR on record covers it, or this MR's own TargetAtStart — written
// before anything was pushed — shows the head was ahead of the target when
// processing began, so the only thing that can have moved it in is this MR's
// push. With neither, the branch was contained from the start and carried
// nothing; recovery re-queues it and the re-run refuses it.
func (r *Refinery) landedInCrashWindow(mr *MergeRequest, probe landingProbe) bool {
	if prior := r.priorMergeCovering(mr, probe.WtDir, probe.Head); prior != nil {
		return true
	}
	if mr.TargetAtStart == "" {
		return false
	}
	wasIn, err := isAncestor(probe.WtDir, probe.Head, mr.TargetAtStart)
	if err != nil {
		log.Printf("refinery: recovery could not compare MR %s head %s against its start target %s: %v", mr.ID, shortSHA(probe.Head), shortSHA(mr.TargetAtStart), err)
		return false
	}
	return !wasIn
}

// recordTargetAtStart persists the target tip the branch was found AHEAD of,
// before any push. See MergeRequest.TargetAtStart.
func (r *Refinery) recordTargetAtStart(mr *MergeRequest, target string) {
	if target == "" {
		return
	}
	r.mu.Lock()
	mr.TargetAtStart = target
	r.saveStateLocked()
	r.mu.Unlock()
	r.flushState()
}

// emptyBranchError is the terminal error for a branch that carries no commits
// ahead of its target.
type emptyBranchError struct {
	branch, target, head string
}

func (e *emptyBranchError) Error() string {
	return fmt.Sprintf("branch carries no commits ahead of %s: %s is at %s, which origin/%s already contains, "+
		"and the refinery has no prior merged MR for this branch or work item that landed it — NOTHING WAS DELIVERED. "+
		"If the work exists it is uncommitted or unpushed in the author's worktree (`git status --porcelain`, "+
		"`git rev-list --count origin/%s..HEAD`): commit it, push, and resubmit. If this branch's work really did land "+
		"under an earlier MR the refinery no longer has on record, confirm with `git log origin/%s` and close the item by hand",
		e.target, e.branch, shortSHA(e.head), e.target, e.target, e.target)
}

// refuseEmptyBranch fails mr as a defect without running gates or pushing. It
// goes through the same record, event and log path as any other terminal
// attempt failure, so `refinery show`, the history log and the MERGE FAILED
// mail all carry it — and it emits no refinery_merged event.
func (r *Refinery) refuseEmptyBranch(mr *MergeRequest, probe landingProbe) (mergeResult, error) {
	err := &emptyBranchError{branch: mr.Branch, target: mr.TargetRef, head: probe.Head}
	gateOutput := fmt.Sprintf("(branch carries no commits ahead of origin/%s — nothing to merge; quality gates, push and deploy not run)", mr.TargetRef)
	log.Printf("refinery: MR %s REFUSED branch=%s head=%s is contained in origin/%s with no prior merged MR on record — the branch carries no commits (mg-c184d)",
		mr.ID, mr.Branch, shortSHA(probe.Head), mr.TargetRef)

	emitMergeAttempted(mr, 1)
	fail, disp := r.describeAttemptFailure(probe.WtDir, 1, stageEmptyBranch, err)
	fail.Time = time.Now()
	fail.NotRetriedReason = "not retryable: " + disp.Reason
	r.recordAttemptFailure(mr, fail)
	emitMergeFailed(mr, 1, stageEmptyBranch, err, true, gateOutput, fail)
	log.Printf("refinery: MR %s %s", mr.ID, fail.Line())
	return mergeResult{GateOutput: gateOutput}, err
}
