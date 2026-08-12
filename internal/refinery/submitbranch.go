package refinery

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// validateSubmitBranch checks, at submit time, that the branch a merge request
// names exists where the merge worker will go looking for it.
//
// This was the one argument Submit did not check (mg-586d). It validated
// RepoPath, TargetRef, PostMergeTag and Verdict, and asked of Branch — the
// subject of the whole operation — only that it was non-empty. A branch that
// existed nowhere was therefore ACCEPTED, returned an MR id, and failed later
// in the merge worker at `git checkout -B <branch> origin/<branch>`
// (merge.go:593, exit 128). So the submitter got a success-shaped answer for an
// operation that could not succeed, and the correction arrived in a component
// they never ran, read by whoever monitors the refinery rather than by the
// person who could fix it in one push.
//
// # Where "origin" is
//
// The predicate mirrors what the merge worker actually resolves. ensureWorktree
// clones repoPath and then fixRemoteURL repoints the clone's origin at
// repoPath's OWN origin remote — or, when repoPath is a bare repo with no
// remote (the test shape), leaves origin pointing at repoPath. So asking
// `git -C repoPath ls-remote --heads origin <branch>`, and falling back to
// repoPath's local refs/heads when repoPath has no origin, resolves the branch
// through the same two cases the merge worker does.
//
// # Why origin/<branch> specifically, and not "any origin ref"
//
// gh#134 / mg-1539 establishes that DURABILITY is "does any origin ref hold
// this head" — a reviewer's commits are safe when the builder's ref holds them,
// whatever the local branch is called. That predicate answers "would stopping
// this worker lose work". Submittability is a different question: it asks
// whether the merge worker's checkout can NAME the branch, and that checkout is
// `origin/<branch>` by name. Measured in a scratch repo: with origin/other
// holding the exact head of local branch b, and no origin/b, `git checkout -B b
// origin/b` still exits 128 with "'origin/b' is not a commit". Accepting such a
// branch here would re-file the very defect this closes, so the durability
// predicate is deliberately not adopted as the verdict.
//
// It is adopted for the DIAGNOSTIC. When the branch is missing on origin,
// branchAbsenceDetail asks the containment question anyway, because "you pushed
// this work under another name" and "you never pushed at all" are different
// mistakes with different fixes, and the submitter is still at the prompt to
// act on whichever one it is.
//
// # Stated bound
//
// When ls-remote cannot reach origin at all, this falls back to repoPath's
// local branch rather than refusing. That is a deliberate fail-open and it does
// not re-create the defect it fixes: an unreachable origin fails the merge
// worker's own `git fetch origin` first, which is classified as a network
// failure and retried, rather than being the permanent, caller-fixable mistake
// this check exists to surface. It matches validateTargetRef's handling of the
// same condition.
func validateSubmitBranch(repoPath, branch string) error {
	// GIT_TERMINAL_PROMPT=0 makes auth-required HTTPS remotes fail fast rather
	// than hang waiting for a username on stdin under launchd.
	cmd := exec.Command("git", "-C", repoPath, "ls-remote", "--heads", "origin", branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// An auth failure is doomed rather than transient, and it is the
		// submitter's to fix — say so here instead of at push time.
		if isAuthFailure(string(out)) {
			return formatPushAuthError(strings.TrimSpace(string(out)))
		}
		// No origin configured, or origin unreachable. See the stated bound.
		cmd2 := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/heads/"+branch)
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			return fmt.Errorf("branch %q not found in repo %s: %s", branch, repoPath, strings.TrimSpace(string(out2)))
		}
		return nil
	}
	if strings.TrimSpace(string(out)) != "" {
		return nil
	}
	// ls-remote answered, and answered empty: origin definitively has no head
	// by that name. Do NOT fall back to the local branch here — a local-only
	// branch is exactly the case that fails in the merge worker, and the whole
	// point of this check is that the caller hears about it now.
	return fmt.Errorf("branch %q is not on origin in repo %s%s; the merge checks it out as origin/%s, so push it before submitting: git push origin %s",
		branch, repoPath, branchAbsenceDetail(repoPath, branch), branch, branch)
}

// branchAbsenceDetail returns a clause naming WHY origin has no such head, so
// the refusal distinguishes mistakes that need different fixes. Returns the
// empty string when it cannot tell — a vague clause is worse than none, and the
// refusal itself already carries the actionable push command.
func branchAbsenceDetail(repoPath, branch string) string {
	shaOut, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/heads/"+branch).Output()
	if err != nil {
		return " and no local branch of that name exists either — check the name against `git rev-parse --abbrev-ref HEAD` in the worktree you meant"
	}
	sha := strings.TrimSpace(string(shaOut))

	// The gh#134 containment question, asked for the message and not for the
	// verdict: some other origin ref may already hold this exact head.
	holder := originRefHolding(repoPath, sha)
	if holder != "" {
		return fmt.Sprintf(" (it exists locally at %s, and %s already holds that head — but not under this name)", shortSHA(sha), holder)
	}
	return " (it exists locally at " + shortSHA(sha) + ", and no origin ref holds that head — it has never been pushed)"
}

// originRefHolding returns the first remote-tracking ref under origin that
// contains sha, or "" if none does. This is gh#134's durability predicate,
// reused verbatim in its own terms rather than re-derived: "does ANY origin ref
// hold this head".
//
// It reads the LOCAL remote-tracking refs, which can be stale — a branch pushed
// since the last fetch will not be seen. That is acceptable because this only
// decorates a refusal that has already been decided against the live remote; a
// stale answer costs a less specific sentence, never a wrong verdict.
func originRefHolding(repoPath, sha string) string {
	out, err := exec.Command("git", "-C", repoPath, "for-each-ref",
		"--contains", sha, "--format=%(refname)", "refs/remotes/origin/").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ref := strings.TrimSpace(line)
		// origin/HEAD is a symref at the default branch, not an independent
		// answer — gh#134's proposed predicate filters it for the same reason.
		if ref == "" || ref == "refs/remotes/origin/HEAD" {
			continue
		}
		return ref
	}
	return ""
}
