package refinery

import "log"

// fastForwardSourceCheckout advances the source checkout's target branch
// after a successful merge has pushed origin/<target> forward. Without
// this, the local checkout the MR was submitted from goes stale — the
// user sees "merged" but their tree still shows pre-merge code, and the
// next polecat branches from stale local state (gh #30).
//
// Conservative by design — it fast-forwards only, and only when:
//   - repoPath is a non-bare working checkout
//   - HEAD is on the target branch (not detached, not another branch)
//   - no tracked file is modified or staged (untracked files don't block:
//     an ff-only merge never overwrites them — git aborts if it would)
//
// It never merges, rebases, or resets, and never touches a dirty tree.
// Every skip or failure is logged and swallowed: the merge has already
// landed on origin, so this is purely a convenience refresh.
func fastForwardSourceCheckout(repoPath, targetRef string) {
	// Must be a working checkout — bare repos (common as test origins)
	// have no tree to refresh. Silent skip: nothing to do, not an anomaly.
	if out, err := gitCmdOutput(repoPath, "rev-parse", "--is-inside-work-tree"); err != nil || out != "true" {
		return
	}

	// Must be on the target branch. symbolic-ref fails on detached HEAD.
	branch, err := gitCmdOutput(repoPath, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch != targetRef {
		log.Printf("refinery: not fast-forwarding local %s in %s: checked out on %q; run 'git pull' on %s to update",
			targetRef, repoPath, branch, targetRef)
		return
	}

	// Never touch a dirty tree: any staged or unstaged change to a
	// tracked file blocks the fast-forward.
	status, err := gitCmdOutput(repoPath, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		log.Printf("refinery: not fast-forwarding local %s in %s: status failed: %s", targetRef, repoPath, status)
		return
	}
	if status != "" {
		log.Printf("refinery: local %s in %s is behind origin but the working tree is dirty; run 'git pull' after committing or stashing",
			targetRef, repoPath)
		return
	}

	// Fetch the just-pushed target and fast-forward onto it. FETCH_HEAD
	// (not origin/<target>) so this works regardless of the checkout's
	// fetch refspec configuration.
	if out, err := gitCmdOutput(repoPath, "fetch", "origin", targetRef); err != nil {
		log.Printf("refinery: not fast-forwarding local %s in %s: fetch failed: %s", targetRef, repoPath, out)
		return
	}
	if out, err := gitCmdOutput(repoPath, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		log.Printf("refinery: local %s in %s could not be fast-forwarded (diverged from origin?); run 'git pull': %s",
			targetRef, repoPath, out)
		return
	}
	sha, _ := gitCmdOutput(repoPath, "rev-parse", "--short", "HEAD")
	log.Printf("refinery: fast-forwarded local %s in %s to %s", targetRef, repoPath, sha)
}
