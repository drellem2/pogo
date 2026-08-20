package agent

import (
	"fmt"
	"log"
	"strings"

	"github.com/drellem2/pogo/internal/gitgc"
)

// PreservedWorktreeGate answers, at the moment of dispatch, whether a work item
// already has UNCOMMITTED work sitting in a worktree that gc will not reclaim.
//
// # The gap this closes (mg-836c)
//
// Every other half of this mechanism already existed and already worked. pogod
// detects a polecat exiting with a dirty tree, PRESERVES the tree rather than
// reaping it, emits worktree_preserved, mails the coordinator a per-tree notice
// naming the worktree, the repo, the item, the file list and the prohibition in
// capitals, and `pogo gc --list-preserved` reports the whole standing
// population. Nothing gated DISPATCH on any of it.
//
// So the item stayed `available`, stall-watch and priority-wake advertised it as
// unclaimed and ready, and `spawn-polecat` did not refuse. On 2026-08-19 a
// dispatch at such an item was stopped only by accident — `git worktree add`
// failed because the branch was still checked out at the old tree — and that
// error names a different reason, whose obvious remedy (remove the stale
// worktree, re-dispatch) would have destroyed the only copy of sixteen files.
//
// The preserved notice had said all of this correctly, six hours earlier. It
// fires ONCE, says so, and was addressed to a coordinator that was itself one of
// the agents down in the outage it was reporting on, into a mailbox holding 905
// unread. A one-shot notice to a single addressee is exactly as reliable as that
// addressee. This gate is the same knowledge held as a standing property of the
// item instead — which is the difference between a notice and a guard.
//
// # Why it is a SEPARATE gate from StrandedWorkGate
//
// They cover disjoint populations by construction, and the stranded gate's
// blindness here is deliberate rather than a defect: it is defined over
// COMMITS — refs read out of the repo — and a polecat commits at the END of its
// life. The state this gate covers is the normal mid-flight state of every
// worker, and it is precisely what a crash, a stop or an outage leaves behind.
// The two also have opposite remedies: a stranded branch is merged
// (`refinery submit`), and there is nothing to submit here until somebody
// commits. Folding them together would send a reader to a command that cannot
// run.
//
// An interface so the handler is testable without a polecats directory,
// mirroring DispatchGate and StrandedWorkGate.
type PreservedWorktreeGate interface {
	// PreservedWorktrees returns the retained worktrees attributable to
	// workItemID in repo, or an error if the question could not be answered.
	// target is the integration branch the spawn would land on; it spares a
	// rebase-landed tree a false finding (PreservedItemOptions.Target).
	PreservedWorktrees(workItemID, repo, target string) ([]gitgc.PreservedTree, error)
}

// GitPreservedWorktreeGate is the production PreservedWorktreeGate: it looks
// under the polecats directory for a tree named after this work item and asks
// git whether it is dirty.
type GitPreservedWorktreeGate struct {
	// PolecatsDir overrides the scan root. Empty resolves via
	// gitgc.DefaultPolecatsDir — the same single source of truth the spawn path
	// itself uses to place a new worktree, so the guard and the thing it guards
	// cannot look at different directories.
	PolecatsDir string
}

// PreservedWorktrees implements PreservedWorktreeGate.
//
// THE FAILURE DIRECTION IS OPEN at the edges and CLOSED at the centre, and the
// split is worth stating rather than leaving to be discovered. No id, no
// polecats directory, an unreadable polecats directory — all dispatch, matching
// MGDispatchGate and GitStrandedWorkGate, because a guard that halts the fleet
// over one bad path gets disarmed rather than fixed, and `--id` is optional by
// design.
//
// But a tree that was FOUND and could not be READ refuses. That is not the same
// failure: the directory is there, it is named after this item, and what we
// could not establish is whether it holds the only copy of somebody's work. gc
// already refuses to reclaim such a tree for exactly this reason ("if we could
// not read the tree, we do not act on it"), and a gate that dispatched over one
// would be strictly less careful than the reaper it exists to cover for. The
// blast radius is one item, and the override clears it.
func (g GitPreservedWorktreeGate) PreservedWorktrees(workItemID, repo, target string) ([]gitgc.PreservedTree, error) {
	if workItemID == "" {
		return nil, nil
	}
	dir := g.PolecatsDir
	if dir == "" {
		resolved, err := gitgc.DefaultPolecatsDir()
		if err != nil {
			return nil, fmt.Errorf("resolve polecats dir: %w", err)
		}
		dir = resolved
	}
	rep, err := gitgc.PreservedForItems(gitgc.PreservedItemOptions{
		PolecatsDir: dir,
		Repo:        repo,
		Items:       []string{workItemID},
		Target:      target,
	})
	if err != nil {
		return nil, err
	}
	for _, e := range rep.Errors {
		log.Printf("preserved-worktree gate: %s (the scan continued)", e)
	}
	return rep.Trees[workItemID], nil
}

// SetPreservedWorktreeGate installs the gate consulted before a polecat is
// dispatched. Passing nil restores the default, which is
// GitPreservedWorktreeGate{} — functional, not a no-op, for the same reason
// SetDispatchGate's default is: a guard that only engages once someone
// remembers to wire it is absent in every deployment where they didn't.
func (r *Registry) SetPreservedWorktreeGate(g PreservedWorktreeGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preservedWorktreeGate = g
}

func (r *Registry) getPreservedWorktreeGate() PreservedWorktreeGate {
	r.mu.RLock()
	g := r.preservedWorktreeGate
	r.mu.RUnlock()
	if g == nil {
		return GitPreservedWorktreeGate{}
	}
	return g
}

// preservedWorktreeRefusal returns the refusal message for a work item whose
// work is already sitting in a worktree the fleet cannot otherwise reach —
// uncommitted, or committed and held by nothing but that tree — or "" when
// dispatch is allowed.
//
// THE MESSAGE NAMES THE TREE, and that is most of its job. The one thing the
// reader must not do is the reflex remedy — remove the stale worktree and
// re-dispatch — and the only way to make that visible is to say which directory
// holds the files and how many of them exist nowhere else. An untracked path in
// a preserved tree is on no branch, in no stash and on no remote: that tree is
// its only copy anywhere on the machine.
//
// IT DOES NOT PRESCRIBE `refinery submit`, unlike the stranded-work refusal.
// For the uncommitted population there is nothing to submit; for the committed
// one the refinery merges origin/<branch> and REFUSES a branch that is not on
// origin (mg-586d), so the command still cannot run. The disposition is a
// decision — make the work reachable and land it, rescue what is worth keeping
// by hand, or rule the work spent — and that decision belongs to a human or a
// coordinator, which is the same stance `pogo check-stranded` takes for its two
// rows. See preservedRemedy for how the first branch of it is worded per tree.
//
// # The committed half (mg-fcba)
//
// mg-836c built this gate over `git status` alone, and that left the state a
// polecat reaches when it gets FURTHER: committed, not pushed, then stopped.
// The pre-deploy stop produces that state on purpose every night. `git status`
// is clean then, so the probe skipped the tree and this function was never
// called about it.
//
// The neighbouring guard is NOT blind to a committed polecat BRANCH — the
// stranded-work gate reads refs/heads as well as refs/remotes/origin and
// refuses an unpushed branch already (mg-bfe0). What has no ref at all is a
// worktree on a DETACHED HEAD, whose commits are reachable from that tree's own
// HEAD and from nothing else. Both are covered here now, and the overlap with
// the stranded gate is deliberate: two refusals naming the same work is a
// legible outcome, one silence is not.
func (r *Registry) preservedWorktreeRefusal(workItemID, repo, target string) string {
	trees, err := r.getPreservedWorktreeGate().PreservedWorktrees(workItemID, repo, target)
	if err != nil {
		// Loud but not fatal — see the fail-open rationale on
		// PreservedWorktrees.
		log.Printf("preserved-worktree gate: could not check work item %s in %s: %v — "+
			"dispatching WITHOUT the preserved-worktree check; if this item's work is sitting "+
			"uncommitted in a retained worktree, this spawn is about to re-derive it and the next "+
			"gc reap destroys the original", workItemID, repo, err)
		return ""
	}
	if len(trees) == 0 {
		return ""
	}

	// The headline states only what was established. A tree that was found and
	// could NOT be read is still a refusal — see PreservedWorktrees — but
	// asserting "has UNCOMMITTED work" about it would be a claim nobody made,
	// and a reader who opens the tree and finds nothing learns that this gate
	// overstates. Ordered so the positively-read trees lead, whatever the scan
	// order: that is the disposition whose advice must not be crowded out.
	trees = readTreesFirst(trees)
	t := trees[0]
	head := fmt.Sprintf("work item %s %s: %s",
		workItemID, preservedHeadline(t), preservedTreeSentence(t))
	if len(trees) > 1 {
		rest := make([]string, 0, len(trees)-1)
		for _, o := range trees[1:] {
			rest = append(rest, preservedTreeSentence(o))
		}
		head += "; also " + strings.Join(rest, "; ")
	}
	return head + ". A polecat spawned now gets a FRESH worktree and cannot see any of it, so it " +
		"re-derives work that already exists — and the original is destroyed by the next gc reap. " +
		"DO NOT remove the worktree to clear this: nothing else on this machine holds it. " +
		"Read it first (`git -C " + t.Path + " status`; `git -C " + t.Path +
		" log --oneline HEAD --not --remotes` for the commits that never left this box — that " +
		"one is a SHA test, so it can list a commit this gate already cleared as having landed by " +
		"rebase; `pogo gc " +
		"--list-preserved` lists every retained tree, splits tracked edits from untracked paths " +
		"and names the commits that exist nowhere else), then decide — " + preservedRemedy(t) +
		", rescue what is worth keeping by hand, or rule the work spent. Dispatch anyway with " +
		"--preserved-override=\"<why>\" once you have read the tree and concluded it holds nothing " +
		"worth keeping"
}

// preservedHeadline names WHICH of the two losses this tree is, because they
// are not the same loss and the remedy differs (mg-fcba).
//
// Uncommitted work has to be committed by somebody before anything can carry
// it; committed-and-unpushed work already exists as commits and needs only to
// be made reachable. Saying "UNCOMMITTED" about the second would send a reader
// looking for dirty files in a tree `git status` calls clean — and the obvious
// conclusion from that is that the gate is wrong, which is how a gate gets
// overridden rather than read.
func preservedHeadline(t gitgc.PreservedTree) string {
	switch {
	case t.Outcome == "preserved" && t.Commits != nil:
		return "already has UNCOMMITTED work AND COMMITS THAT EXIST NOWHERE ELSE in a RETAINED WORKTREE"
	case t.Outcome == "preserved":
		return "already has UNCOMMITTED work in a RETAINED WORKTREE"
	case t.Outcome == "unpushed":
		return "already has COMMITS THAT EXIST NOWHERE ELSE, in a worktree"
	default:
		return "has a RETAINED WORKTREE that could NOT be read"
	}
}

// preservedRemedy names the first disposition — the one that keeps the work —
// in terms of what this particular tree actually holds.
//
// IT NEVER PRESCRIBES `refinery submit` BARE. The refinery merges origin/<branch>
// and refuses a branch that is not on origin (mg-586d), so "submit it" is a
// command that cannot run for exactly the population this gate reports. And a
// DETACHED tree has no branch to push at all: naming one would be a remedy with
// a hole in it, which is the failure strandedgate records under mg-bfe0.
func preservedRemedy(t gitgc.PreservedTree) string {
	detached := t.Branch == "" || t.Branch == "HEAD"
	switch {
	case t.Commits != nil && detached:
		return "give those commits a ref before anything else (`git -C " + t.Path +
			" switch -c <branch>`), then push it and land it"
	case t.Commits != nil && t.Outcome == "preserved":
		return "commit the rest on `" + t.Branch + "`, then push that branch and land it"
	case t.Commits != nil:
		return "push `" + t.Branch + "` and land it (the commits are already made)"
	default:
		return "commit them on " + preservedBranchPhrase(t) + " and land that branch"
	}
}

// readTreesFirst orders positively-read trees ahead of unread ones, so a
// refusal that has both leads with the one it can describe.
//
// "Positively read" is any tree something was established about — a dirty tree,
// and since mg-fcba a clean one holding commits that exist nowhere else. Only
// the tree git could not read at all goes last.
func readTreesFirst(in []gitgc.PreservedTree) []gitgc.PreservedTree {
	out := make([]gitgc.PreservedTree, 0, len(in))
	for _, t := range in {
		if t.Outcome == "preserved" || t.Outcome == "unpushed" {
			out = append(out, t)
		}
	}
	for _, t := range in {
		if t.Outcome != "preserved" && t.Outcome != "unpushed" {
			out = append(out, t)
		}
	}
	return out
}

// preservedTreeSentence renders one tree as a clause a reader can act on.
//
// The MODIFIED/UNTRACKED SPLIT is carried rather than a single total, because
// the two halves are different facts about what happens if the tree goes: a
// modified tracked file still has its committed version in the object store, so
// the worst case is a lost edit, while an untracked path exists in this tree
// and nowhere else. A bare "16 dirty files" cannot tell those apart, and the
// reader deciding whether to override is deciding exactly that.
//
// The COMMITS clause is a third such fact and the worst of the three: a commit
// no origin ref holds, with no patch-equivalent on the integration branch, has
// no copy anywhere — and on a detached HEAD no ref keeps it reachable either.
func preservedTreeSentence(t gitgc.PreservedTree) string {
	if t.Outcome != "preserved" && t.Outcome != "unpushed" {
		// The tree is retained but was not READ. Saying "uncommitted work" here
		// would be a claim nobody established; naming the status failure is the
		// honest version, and it is still a refusal (see PreservedWorktrees).
		why := t.StatusError
		if why == "" {
			why = "reason unrecorded"
		}
		return fmt.Sprintf("%s exists and could NOT be read (%s), so whether it holds the only copy "+
			"of this item's work is unknown", t.Path, why)
	}
	var s string
	if t.Outcome == "preserved" {
		s = fmt.Sprintf("%s holds %d uncommitted path(s) — %d modified, %d untracked",
			t.Path, t.Total, t.Modified, t.Untracked)
		if t.Untracked > 0 {
			s += " (untracked paths are on no branch, in no stash and on no remote: this tree is the " +
				"only copy of their git objects — whether it is the only copy of their CONTENT " +
				"needs a `cmp` against the same path upstream)"
		}
	} else {
		s = t.Path + " is CLEAN — `git status` reports no uncommitted files"
	}
	if c := t.Commits; c != nil {
		s += ", " + preservedCommitsClause(t)
	}
	switch {
	case t.Branch == "HEAD":
		// Already reported as detached by the commits clause, and repeating it
		// as ", on branch HEAD" invites the reader to treat HEAD as a branch
		// name — the one reading git's literal passthrough exists to prevent.
	case t.Branch != "":
		s += ", on branch " + t.Branch
	case t.BranchError != "":
		s += ", branch unreadable (" + t.BranchError + ")"
	}
	return s
}

// preservedCommitsClause states the committed half without overstating it. An
// `unknown` durability verdict is reported as itself: a question we failed to
// ask is not an answer of "none", and it is still a refusal.
func preservedCommitsClause(t gitgc.PreservedTree) string {
	c := t.Commits
	if c.Verdict == gitgc.DurabilityUnknown {
		return "and whether it holds commits that exist nowhere else could NOT be established (" +
			c.Detail + ") — which is not a report of none"
	}
	// The COUNT is stated only when it was read. A local-only verdict whose list
	// could not be re-read would otherwise print "0 COMMIT(S) THAT EXIST
	// NOWHERE ELSE" — true of the list, false of the tree, and the number is
	// the half a skimming reader keeps. That is this ticket's own defect shape
	// one layer in: a zero that reads as "nothing here".
	s := "and holds COMMIT(S) THAT EXIST NOWHERE ELSE, HOW MANY IS UNKNOWN"
	if c.CommitsError != "" {
		s += " (" + c.CommitsError + ")"
	}
	if len(c.Commits) > 0 {
		s = fmt.Sprintf("and holds %d COMMIT(S) THAT EXIST NOWHERE ELSE", len(c.Commits))
	}
	s += " — no ref under refs/remotes/origin/ holds them and none has a patch-equivalent on the " +
		"integration branch"
	if t.Branch == "" || t.Branch == "HEAD" {
		s += "; the tree is on a DETACHED HEAD, so NO REF holds them either and removing the tree " +
			"orphans them"
	}
	return s
}

// preservedBranchPhrase names the branch to commit on, or says plainly that it
// could not be read rather than emitting a remedy with a hole in it.
func preservedBranchPhrase(t gitgc.PreservedTree) string {
	if t.Branch == "" || t.Branch == "HEAD" {
		return "the branch that tree has checked out"
	}
	return "`" + t.Branch + "`"
}
