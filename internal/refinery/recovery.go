package refinery

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveRecovered resolves the in-flight MRs loaded from the state file after
// a pogod crash or restart. Called from Start, before the first dispatch, so
// callbacks are already wired.
//
// There can be more than one since per-repo lanes (mg-37ad). They are resolved
// one at a time, on this goroutine, before any lane starts — each probe wants
// exclusive use of its repo's clone (it force-removes rebase debris), and
// serialising the probes is free next to what they protect.
//
// The dangerous crash window is after `git push` landed the merge but before
// the history append recorded it. Blindly re-running the item would re-run
// gates on an already-merged branch (and fail the rebase); blindly dropping
// it loses the MR. Instead, probe whether the branch tip is an ancestor of
// origin/<target>, and then whether THIS MR (or a prior merged one) is what put
// it there — ancestry alone never proves landing (mg-c184d):
//
//   - ancestor, and landed ⇒ the merge landed: record as merged and fire
//     OnMerged so the notifications that died with the daemon still happen.
//     Gates are NOT re-run. "Landed" means a prior merged MR on record covers
//     the head, or this MR's TargetAtStart shows the head was AHEAD of the
//     target when processing began (see landedInCrashWindow).
//   - otherwise       ⇒ nothing this MR did landed: clean the private clone (a
//     crash mid-rebase leaves an in-progress rebase behind) and re-queue at
//     head for a fresh attempt. A head that is an ancestor with nothing on
//     record carries no commits, and the re-run refuses it as a defect.
//   - probe fails    ⇒ branch deleted or remote unreachable: move the ID to
//     the lost list and emit an event so the author can resubmit.
func (r *Refinery) resolveRecovered() {
	r.mu.Lock()
	pending := r.recovered
	r.mu.Unlock()

	// Resolve newest-first so the re-queue-at-head path below leaves the
	// oldest in-flight merge at the very front of the queue: a merge
	// interrupted by a restart should not lose its place to one that started
	// after it.
	for i := len(pending) - 1; i >= 0; i-- {
		r.resolveRecoveredOne(pending[i])
	}

	r.mu.Lock()
	r.recovered = nil
	r.saveStateLocked()
	r.mu.Unlock()
	r.flushState()
}

// resolveRecoveredOne resolves a single recovered in-flight merge request.
func (r *Refinery) resolveRecoveredOne(mr *MergeRequest) {
	if mr == nil {
		return
	}

	probe, probeErr := r.probeAlreadyMerged(mr)
	merged := probeErr == nil && probe.HeadInTarget && r.landedInCrashWindow(mr, probe)
	sha := probe.Head

	r.mu.Lock()
	// Drop it from the recovered set before persisting: saveStateLocked writes
	// r.recovered as in-flight, and an item that has just been resolved into
	// history must not also be persisted as still running.
	for i, p := range r.recovered {
		if p == mr {
			r.recovered = append(r.recovered[:i], r.recovered[i+1:]...)
			break
		}
	}
	var fire OnMerged
	switch {
	case probeErr != nil:
		delete(r.byID, mr.ID)
		r.lost = append(r.lost, LostEntry{
			ID:        mr.ID,
			Branch:    mr.Branch,
			Author:    mr.Author,
			RepoPath:  mr.RepoPath,
			TargetRef: mr.TargetRef,
			Reason:    probeErr.Error(),
			LostTime:  r.nowFunc(),
		})
		log.Printf("refinery: recovery could not resolve in-flight MR %s (branch=%s): %v — marked lost", mr.ID, mr.Branch, probeErr)
	case merged:
		mr.Status = StatusMerged
		mr.DoneTime = r.nowFunc()
		if mr.Author != "" {
			delete(r.failureCounts, mr.Author)
		}
		r.history = append(r.history, mr)
		fire = r.onMerged
		log.Printf("refinery: recovery found in-flight MR %s already merged (branch=%s ancestor of origin/%s)", mr.ID, mr.Branch, mr.TargetRef)
	default:
		mr.Status = StatusQueued
		mr.Error = ""
		mr.GateOutput = ""
		r.queue = append([]*MergeRequest{mr}, r.queue...)
		if probe.HeadInTarget {
			log.Printf("refinery: recovery re-queued in-flight MR %s at head (branch=%s head %s is in origin/%s, but neither a prior merged MR nor this MR's own start record shows it landing there — the re-run decides, and refuses a branch that carries no commits)",
				mr.ID, mr.Branch, shortSHA(probe.Head), mr.TargetRef)
		} else {
			log.Printf("refinery: recovery re-queued in-flight MR %s at head (branch=%s not merged)", mr.ID, mr.Branch)
		}
	}
	r.saveStateLocked()
	r.mu.Unlock()
	// Write-through, with r.mu released: recovery's whole job is to make the
	// state file agree with reality before the callback below fires (mg-538e).
	r.flushState()

	if probeErr != nil {
		emitRecoveryLost(mr, probeErr)
	} else if merged {
		emitMerged(mr, 0, sha, 0, false)
		if fire != nil {
			fire(mr)
		}
	}
}

// landingProbe is what probeAlreadyMerged observed after a fresh fetch.
type landingProbe struct {
	// WtDir is the refinery's clone the probe ran in.
	WtDir string
	// Head is the branch tip on origin.
	Head string
	// Target is origin/<target> at the same moment.
	Target string
	// HeadInTarget reports whether Head is an ancestor of (or equal to)
	// Target. It is NOT a verdict that the branch landed: a branch with no
	// commits of its own answers true here exactly as a fast-forwarded one
	// does (mg-c184d). See priorMergeCovering and landedInCrashWindow for the
	// questions that decide it.
	HeadInTarget bool
}

// probeAlreadyMerged reports whether the MR's branch tip is contained in the
// target ref (an ancestor of origin/<target> after a fresh fetch). It cleans the
// refinery's private clone first: a crash mid-rebase leaves an in-progress
// rebase that would break every subsequent git operation (ensureWorktree only
// checks that .git exists).
//
// Containment is evidence, not a verdict. The refinery REBASES before merging,
// so a branch whose work really landed is normally NOT an ancestor of the target
// (its landed copies are new commits). Containment is therefore true in two
// cases: the merge was a fast-forward and the branch tip IS the merged commit,
// or the branch has no commits of its own. Callers decide between them from the
// refinery's own record, never from the ancestry alone (mg-c184d).
//
// Two callers, and both hold the repo's clone exclusively when they run:
// resolveRecovered (crash-window recovery, above) runs before any lane starts,
// and processMerge (already-merged guard against double-submits, gh #34) runs
// inside a lane, which is per-repo and therefore per-clone. Concurrency across
// repos does not reach this: the destructive step below force-removes rebase
// state, and it must never do that under another merge's feet.
//
// A non-nil error means the probe itself could not answer (branch deleted,
// remote unreachable) — recovery moves the MR to the lost list; processMerge
// falls through to the normal pipeline, which surfaces the real error.
//
// The worktree comes from ensureGateWorktree, so a gate the previous pogod left
// running in it is reaped BEFORE the reset below, not under it (mg-58f3).
func (r *Refinery) probeAlreadyMerged(mr *MergeRequest) (landingProbe, error) {
	wtDir, err := r.ensureGateWorktree(mr)
	if err != nil {
		return landingProbe{}, fmt.Errorf("worktree setup: %w", err)
	}
	probe := landingProbe{WtDir: wtDir}

	// Clean any crash debris. Both commands are no-ops on a clean clone;
	// errors are ignored (rebase --abort fails when no rebase is running).
	gitCmdOutput(wtDir, "rebase", "--abort")
	// A crash mid-write of the rebase state itself can leave a rebase dir
	// that even `rebase --abort` refuses to touch. The clone is private to
	// the refinery, so force-remove the leftovers.
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		stateDir := filepath.Join(wtDir, ".git", d)
		if _, statErr := os.Stat(stateDir); statErr == nil {
			log.Printf("refinery: recovery force-removing leftover %s state in %s", d, wtDir)
			if rmErr := os.RemoveAll(stateDir); rmErr != nil {
				return probe, fmt.Errorf("clean %s state: %w", d, rmErr)
			}
		}
	}
	gitCmdOutput(wtDir, "reset", "--hard")

	if out, gerr := gitCmdOutput(wtDir, "fetch", "origin"); gerr != nil {
		return probe, fmt.Errorf("fetch origin: %s: %w", out, gerr)
	}

	sha, gerr := gitCmdOutput(wtDir, "rev-parse", "--verify", "refs/remotes/origin/"+mr.Branch)
	if gerr != nil {
		return probe, fmt.Errorf("branch %q not found on origin: %s: %w", mr.Branch, sha, gerr)
	}
	probe.Head = strings.TrimSpace(sha)

	target, gerr := gitCmdOutput(wtDir, "rev-parse", "--verify", "refs/remotes/origin/"+mr.TargetRef)
	if gerr != nil {
		return probe, fmt.Errorf("target %q not found on origin: %s: %w", mr.TargetRef, target, gerr)
	}
	probe.Target = strings.TrimSpace(target)

	in, gerr := isAncestor(wtDir, probe.Head, probe.Target)
	if gerr != nil {
		return probe, fmt.Errorf("ancestor probe %s vs origin/%s: %w", mr.Branch, mr.TargetRef, gerr)
	}
	probe.HeadInTarget = in
	return probe, nil
}

// isAncestor reports whether sha is an ancestor of ref in the given repo.
// `git merge-base --is-ancestor` answers via exit code: 0 = ancestor,
// 1 = not an ancestor, anything else = the probe itself failed.
func isAncestor(dir, sha, ref string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", sha, ref)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("merge-base --is-ancestor: %s: %w", string(out), err)
}
