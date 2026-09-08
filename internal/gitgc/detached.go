package gitgc

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// WorktreeDetached reports whether worktreeDir's HEAD names no branch.
//
// # Why this exists rather than reading WorktreeBranch (mg-8d25)
//
// `git rev-parse --abbrev-ref HEAD` prints the literal string "HEAD" for a
// detached worktree, so callers have been inferring detachment from a branch
// name that happens to read "HEAD". That works, and it is an inference off a
// string rather than an answer to the question.
//
// The instrument that looks obvious is worse, and the way it fails is the whole
// reason this function is separate. `git symbolic-ref -q --short HEAD` prints
// NOTHING for a detached HEAD **and** nothing for a directory that is not a
// worktree at all, so a check that reads only its stdout labels every orphan
// directory "detached". mg-8d25 was filed with 19 such directories on this host
// mislabelled that way on a first pass, which would have reported a large fake
// population of trees at risk.
//
// The EXIT CODE is the discriminator, and it is what this reads. Measured on
// this host, 2026-09-03:
//
//	attached worktree      -> exit 0, prints refs/heads/<branch>
//	detached worktree      -> exit 1, prints nothing
//	not a repository       -> exit 128, "fatal: not a git repository"
//
// So exit 1 is detachment, exit 0 is a branch, and anything else is a failure
// to ask — reported as an error rather than folded into either answer.
func WorktreeDetached(worktreeDir string) (bool, error) {
	if worktreeDir == "" {
		return false, fmt.Errorf("empty worktree path")
	}
	stdout, stderr, err := runSplit("git", "-C", worktreeDir, "symbolic-ref", "-q", "HEAD")
	if err == nil {
		if strings.TrimSpace(string(stdout)) == "" {
			// git exited 0 and named no ref. Report it rather than answering
			// "attached", which is the answer that licenses a removal.
			return false, fmt.Errorf("symbolic-ref %s: exited 0 naming no ref", worktreeDir)
		}
		return false, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("symbolic-ref %s: %w: %s", worktreeDir, err, strings.TrimSpace(string(stderr)))
}

// OrphanCommitsError reports a removal refused because the worktree is on a
// DETACHED HEAD holding commits that exist nowhere else — removing it would
// leave them reachable from no ref at all.
//
// # Why detachment is half the condition and not the whole of it (mg-8d25)
//
// A worktree on a BRANCH loses nothing to `git worktree remove`: the branch ref
// survives in the source repo's namespace and keeps the objects, and the sweep's
// phase 2 asks BranchDurable before deleting that ref. A DETACHED worktree's
// commits are held by the per-worktree HEAD under `.git/worktrees/<name>/`, which
// the removal deletes, so nothing is left holding them.
//
// Detachment ALONE is not the hazard and must not be the trigger. Measured on
// this host on 2026-09-03 there was exactly one detached polecat worktree
// (`~/.pogo/polecats/p6b2d`, HEAD 11b8803) and its commits were reachable from
// refs — `git log --oneline --not --all` was empty. A guard keyed on detachment
// would have refused that tree, and a guard that refuses the harmless common
// case teaches its reader to pass --force by reflex, which disarms it for the
// case it exists for. So both halves are required: detached AND at risk.
//
// # It refuses; it does not classify content and does not discard
//
// `pogo gc --list-preserved` opens by saying that nothing it prints is a verdict
// and that whether a tree may be reclaimed "needs someone to READ the files".
// This error inherits that: it names which commits exist in one place only and
// stops. `pogo gc --apply --force` remains the way out, exactly as for a dirty
// tree.
type OrphanCommitsError struct {
	Path string
	// Target is the integration branch the durability question was asked
	// against, so a reader can tell which comparison produced the refusal.
	Target string
	// Finding is WorktreeCommitsAtRisk's answer. On the probe-failure path its
	// Verdict is DurabilityUnknown and Detail is empty; ProbeError says why.
	Finding WorktreeCommitFinding
	// DetachedKnown is false when detachment itself could not be established.
	// The refusal still stands in that case — see checkWorktreeRemoval — but
	// the sentence must not claim a detached HEAD nobody observed.
	DetachedKnown bool
	// DetachError is why detachment could not be established, when it could not.
	DetachError string
	// ProbeError is why the durability question could not be asked at all —
	// an unresolvable source repo or an unresolvable HEAD. A question we failed
	// to ask is not an answer of "durable" (see DurabilityUnknown).
	ProbeError string
}

func (e *OrphanCommitsError) Error() string {
	head := e.Finding.Head
	if head == "" {
		head = "HEAD"
	}
	where := "is on a DETACHED HEAD"
	if !e.DetachedKnown {
		where = fmt.Sprintf("could not be shown to be on a branch (%s)", e.DetachError)
	}
	target := e.Target
	if target == "" {
		target = DefaultTargetBranch
	}
	switch {
	case e.ProbeError != "":
		return fmt.Sprintf("worktree %s %s and whether it holds commits that exist nowhere else "+
			"could NOT be established (%s) — that is not a report of none, refusing to remove",
			e.Path, where, e.ProbeError)
	case e.Finding.Verdict == DurabilityUnknown:
		return fmt.Sprintf("worktree %s %s at %s and whether its commits exist anywhere else "+
			"could NOT be established (%s) — that is not a report of none, refusing to remove",
			e.Path, where, shortSHA(head), e.Finding.Detail)
	case len(e.Finding.Commits) == 0:
		return fmt.Sprintf("worktree %s %s at %s holding commits that exist ONLY here — no ref "+
			"under refs/remotes/origin/ holds them and none has a patch-equivalent on %s; how many "+
			"is unknown, the list could not be read (%s). Removing this tree orphans them, "+
			"refusing to remove", e.Path, where, shortSHA(head), target, e.Finding.CommitsError)
	default:
		return fmt.Sprintf("worktree %s %s at %s holding %d commit(s) that exist ONLY here — no ref "+
			"under refs/remotes/origin/ holds them and none has a patch-equivalent on %s. Removing "+
			"this tree orphans them, refusing to remove",
			e.Path, where, shortSHA(head), len(e.Finding.Commits), target)
	}
}

// shortSHA renders a commit at the length an operator can retype. A full
// 40-character sha in a refusal line is the part that gets truncated by whatever
// renders it, and the abbreviation is what `git show` wants anyway.
func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// checkOrphanCommits is the DETACHED-HEAD arm of checkWorktreeRemoval, applied
// to a tree `git status` already read as clean.
//
// It returns nil for everything that is not the loss case, and the population it
// clears is nearly all of it: an attached worktree returns nil without asking
// the durability question at all, because the branch ref outlives the removal.
//
// repo empty means "resolve it from the tree's own .git pointer". target empty
// resolves to DefaultTargetBranch inside BranchDurable.
func checkOrphanCommits(worktreeDir, repo, target string) error {
	detached, derr := WorktreeDetached(worktreeDir)
	if derr == nil && !detached {
		// The common case, and the cheap one: one `git symbolic-ref` and out.
		return nil
	}
	e := &OrphanCommitsError{Path: worktreeDir, Target: target, DetachedKnown: derr == nil}
	if derr != nil {
		// Fail CLOSED, and say which half failed. This is reachable only for a
		// tree whose `git status` succeeded moments earlier, so the population
		// is close to empty — but "we could not tell whether removing this
		// orphans commits" is not a licence to remove it, and it is the same
		// rule the cannot-tell status arm applies one step up.
		e.DetachError = derr.Error()
	}
	if repo == "" {
		// Best effort. An unresolved repo reaches WorktreeCommitsAtRisk as an
		// empty string and comes back as ProbeError, which refuses.
		if r, rerr := WorktreeSourceRepo(worktreeDir); rerr == nil {
			repo = r
		}
	}
	find, ferr := WorktreeCommitsAtRisk(worktreeDir, repo, target)
	if ferr != nil {
		e.ProbeError = ferr.Error()
		e.Finding.Verdict = DurabilityUnknown
		return e
	}
	if !find.AtRisk() {
		// Detached, and every commit under it is held somewhere the removal
		// cannot reach past. This is `p6b2d` on 2026-09-03 and it is the reason
		// detachment alone is not the trigger.
		return nil
	}
	e.Finding = find
	return e
}
