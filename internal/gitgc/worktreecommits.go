package gitgc

import (
	"fmt"
	"os/exec"
	"strings"
)

// WorktreeCommitFinding is the answer to one question about a polecat's
// worktree: does its HEAD reach commits that exist NOWHERE ELSE — no ref under
// refs/remotes/origin/ holds them, and none has a patch-equivalent on the
// integration branch.
//
// # The population this exists for (mg-fcba)
//
// mg-836c taught the dispatch path to see a preserved worktree holding
// UNCOMMITTED work, and both halves of that landed: `spawn-polecat` refuses
// such an item and stall-watch's priority wake stops advertising it. The
// population left over is the one a polecat produces when it gets further —
// it COMMITS, and is stopped before it pushes. The pre-deploy stop creates that
// population deliberately, every night.
//
// Nothing in the dispatch path saw it, and the reason is worth stating per
// instrument rather than as one sentence, because they are blind for different
// reasons and one of them is not blind at all:
//
//   - `git status` is clean once a worker commits, so the mg-836c probe skips
//     the tree entirely (PreservedForItems used to `continue` on
//     chk.Refusal == nil) and both surfaces it feeds go quiet.
//   - the STRANDED-WORK gate is NOT blind to a committed polecat BRANCH, and
//     the ticket that asked for this said it was. strandedwork.Scan reads
//     refs/heads/polecat-* as well as refs/remotes/origin/polecat-*, and a
//     polecat worktree's branch is a ref in the SOURCE repo's namespace — so an
//     unpushed branch already refuses a spawn (mg-bfe0 documents this and
//     TestSpawnRefusedForLocalOnlyPreRegistration is that case end to end).
//   - what has no ref at all is a worktree on a DETACHED HEAD. Its commits are
//     reachable from the worktree's HEAD and from nothing else, so a scan over
//     refs cannot see them by construction. `~/.pogo/polecats/p6b2d` is in
//     exactly that state on this box (`branch HEAD` in `pogo gc
//     --list-preserved`).
//   - and stall-watch — the thing that actually advertises the item — reads no
//     refs whatever. It has an in-flight probe, a capacity probe and the
//     mg-836c preserved probe, and no branch probe of any kind. So priority-wake
//     said "claim or dispatch now" for a committed-but-unpushed item whatever
//     the spawn gate would later have done about it.
//
// # It is a FACT, not a verdict
//
// `pogo gc --list-preserved` opens by saying that nothing it prints is a
// verdict and that whether a tree may be reclaimed "needs someone to READ the
// files". That constraint is inherited here deliberately: this type reports
// which commits exist only in one place and never what to do about them.
// Callers refuse a dispatch or annotate a notice; nothing built on this
// discards, resumes, or commits anything.
type WorktreeCommitFinding struct {
	// Head is the commit the worktree's HEAD resolves to.
	Head string `json:"head,omitempty"`
	// Verdict is BranchDurable's answer about Head. Only AtRisk() verdicts —
	// local-only and unknown — are findings; durable is the common case and
	// the reason this is affordable.
	Verdict DurabilityVerdict `json:"verdict"`
	// Detail is BranchDurable's own sentence, kept for logs and JSON.
	//
	// IT IS NOT USER-FACING TEXT HERE, and that is a deliberate restriction
	// rather than an oversight. BranchDurable is written about a BRANCH: its
	// local-only sentence reads "N commit(s) on <ref> exist ONLY on this local
	// ref", and a detached worktree has no local ref for that sentence to be
	// true of — the commits are held by the worktree's HEAD alone, which is
	// strictly worse and would be described as better. Callers word the
	// local-only case themselves and surface this only on `unknown`, where the
	// sentence names which read failed and is accurate.
	Detail string `json:"detail,omitempty"`
	// Commits are the `git cherry -v` `+` lines — "<sha> <subject>", oldest
	// first — for the commits with no patch-equivalent on the integration
	// branch. Empty with a local-only verdict means the list could not be
	// re-read; see CommitsError.
	Commits []string `json:"commits,omitempty"`
	// CommitsError is why Commits is empty despite an at-risk verdict. A
	// missing list never softens the verdict: "there are commits here and I
	// could not list them" is the state not to dispatch over, and reporting it
	// as "no commits" is the collapse mg-65b2 records costing files.
	CommitsError string `json:"commits_error,omitempty"`
}

// AtRisk reports whether this worktree's HEAD may hold the only copy of some
// commits.
func (f WorktreeCommitFinding) AtRisk() bool { return f.Verdict.AtRisk() }

// WorktreeCommitsAtRisk asks BranchDurable about a WORKTREE's HEAD rather than
// about a named branch.
//
// The redirection is the whole trick and it is one line: resolve HEAD inside
// worktreeDir, then hand the resulting SHA to the predicate that already
// exists. BranchDurable's every step (any origin ref contains it; the
// integration ref contains it; every commit has a patch-equivalent there) is a
// question about a commit, not about a name, and resolveCommit accepts a SHA —
// so a detached worktree, whose HEAD names no ref anywhere, gets the same three
// tests as a branch does.
//
// # Why BranchDurable and not `git log HEAD --not --remotes`
//
// The obvious spelling over-reports on exactly the population this box produces
// most. THE REFINERY REBASES BEFORE MERGING, so a polecat's commits land on
// origin/main under different SHAs; `--not --remotes` is a SHA test and would
// call every merged-and-preserved tree "unpushed" forever. BranchDurable's
// third test compares patch ids for that reason and is measured on it — 71 of
// this repo's 146 polecat branches read `--no-merged main` while demonstrably
// having landed. A guard that fires on those is disarmed within the week.
//
// # repo is the SOURCE repository, not the worktree
//
// The refs a durability question is asked of live in the source repo's
// namespace; a linked worktree shares that namespace but is not it.
// PreservedForItems already resolves it per tree from the tree's own .git
// pointer (WorktreeSourceRepo), so callers pass what they read rather than what
// they assumed.
//
// An empty target resolves to DefaultTargetBranch inside BranchDurable.
func WorktreeCommitsAtRisk(worktreeDir, repo, target string) (WorktreeCommitFinding, error) {
	if worktreeDir == "" {
		return WorktreeCommitFinding{}, fmt.Errorf("no worktree path")
	}
	if repo == "" {
		return WorktreeCommitFinding{}, fmt.Errorf(
			"no source repository resolved for worktree %s, so its refs cannot be asked about", worktreeDir)
	}
	head, err := resolveCommit(worktreeDir, "HEAD")
	if err != nil {
		return WorktreeCommitFinding{}, fmt.Errorf("resolve HEAD in worktree %s: %w", worktreeDir, err)
	}

	f := WorktreeCommitFinding{Head: head}
	f.Verdict, f.Detail = BranchDurable(repo, head, target)
	if !f.Verdict.AtRisk() {
		return f, nil
	}

	// The list is read only for a tree that already produced a finding, so the
	// common case costs nothing extra. A failure to read it is recorded and
	// never allowed to clear the verdict.
	_, tcommit, terr := resolveIntegrationRef(repo, target)
	if terr != nil {
		f.CommitsError = terr.Error()
		return f, nil
	}
	commits, cerr := cherryAheadCommits(repo, tcommit, head)
	if cerr != nil {
		f.CommitsError = cerr.Error()
		return f, nil
	}
	f.Commits = commits
	return f, nil
}

// cherryAheadCommits returns the "<sha> <subject>" of every commit on head with
// no patch-equivalent in upstream — `git cherry -v`'s `+` lines, oldest first.
//
// `-v` rather than a second `git log`: the `+`/`-` split IS the patch-id
// comparison, so taking the subjects off the same command guarantees the list
// and the count describe the same commits. A `git log upstream..head` beside it
// would answer a SHA question next to a patch question and disagree with itself
// on precisely the rebase case both exist to survive.
func cherryAheadCommits(repo, upstream, head string) ([]string, error) {
	out, err := exec.Command("git", "-C", repo, "cherry", "-v", upstream, head).Output()
	if err != nil {
		return nil, fmt.Errorf("cherry -v %s %s: %w", upstream, head, err)
	}
	var commits []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+") {
			continue
		}
		if rest := strings.TrimSpace(strings.TrimPrefix(line, "+")); rest != "" {
			commits = append(commits, rest)
		}
	}
	return commits, nil
}
