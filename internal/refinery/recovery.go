package refinery

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// resolveRecovered resolves an in-flight (processing) MR loaded from the
// state file after a pogod crash or restart. Called from Start, before the
// first processNext, so callbacks are already wired.
//
// The dangerous crash window is after `git push` landed the merge but before
// the history append recorded it. Blindly re-running the item would re-run
// gates on an already-merged branch (and fail the rebase); blindly dropping
// it loses the MR. Instead, probe whether the branch tip is an ancestor of
// origin/<target>:
//
//   - ancestor       ⇒ the merge landed: record as merged and fire OnMerged
//     so the notifications that died with the daemon still happen. Gates are
//     NOT re-run.
//   - not ancestor   ⇒ nothing landed: clean the private clone (a crash
//     mid-rebase leaves an in-progress rebase behind) and re-queue at head
//     for a fresh attempt.
//   - probe fails    ⇒ branch deleted or remote unreachable: move the ID to
//     the lost list and emit an event so the author can resubmit.
func (r *Refinery) resolveRecovered() {
	r.mu.Lock()
	mr := r.recovered
	r.mu.Unlock()
	if mr == nil {
		return
	}

	merged, sha, probeErr := r.probeInFlight(mr)

	r.mu.Lock()
	r.recovered = nil
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
		log.Printf("refinery: recovery re-queued in-flight MR %s at head (branch=%s not merged)", mr.ID, mr.Branch)
	}
	r.saveStateLocked()
	r.mu.Unlock()

	if probeErr != nil {
		emitRecoveryLost(mr, probeErr)
	} else if merged {
		emitMerged(mr, 0, sha, 0)
		if fire != nil {
			fire(mr)
		}
	}
}

// probeInFlight reports whether the in-flight MR's branch already landed on
// the target ref. It cleans the refinery's private clone first: a crash
// mid-rebase leaves an in-progress rebase that would break every subsequent
// git operation (ensureWorktree only checks that .git exists).
//
// Returns (merged, branchSHA, error). A non-nil error means the probe itself
// could not answer (branch deleted, remote unreachable) — the caller moves
// the MR to the lost list.
func (r *Refinery) probeInFlight(mr *MergeRequest) (bool, string, error) {
	wtDir, err := r.ensureWorktree(mr.RepoPath)
	if err != nil {
		return false, "", fmt.Errorf("worktree setup: %w", err)
	}

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
				return false, "", fmt.Errorf("clean %s state: %w", d, rmErr)
			}
		}
	}
	gitCmdOutput(wtDir, "reset", "--hard")

	if out, gerr := gitCmdOutput(wtDir, "fetch", "origin"); gerr != nil {
		return false, "", fmt.Errorf("fetch origin: %s: %w", out, gerr)
	}

	sha, gerr := gitCmdOutput(wtDir, "rev-parse", "--verify", "refs/remotes/origin/"+mr.Branch)
	if gerr != nil {
		return false, "", fmt.Errorf("branch %q not found on origin: %s: %w", mr.Branch, sha, gerr)
	}

	merged, gerr := isAncestor(wtDir, sha, "refs/remotes/origin/"+mr.TargetRef)
	if gerr != nil {
		return false, "", fmt.Errorf("ancestor probe %s vs origin/%s: %w", mr.Branch, mr.TargetRef, gerr)
	}
	return merged, sha, nil
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
