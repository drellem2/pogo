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
	PreservedWorktrees(workItemID, repo string) ([]gitgc.PreservedTree, error)
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
func (g GitPreservedWorktreeGate) PreservedWorktrees(workItemID, repo string) ([]gitgc.PreservedTree, error) {
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
// work is already sitting uncommitted in a retained worktree, or "" when
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
// There is nothing to submit. The disposition is a decision — commit the tree
// and land it, rescue what is worth keeping by hand, or rule the work spent —
// and that decision belongs to a human or a coordinator, which is the same
// stance `pogo check-stranded` takes for its two rows.
func (r *Registry) preservedWorktreeRefusal(workItemID, repo string) string {
	trees, err := r.getPreservedWorktreeGate().PreservedWorktrees(workItemID, repo)
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
	head := fmt.Sprintf("work item %s has a RETAINED WORKTREE that could NOT be read: %s",
		workItemID, preservedTreeSentence(t))
	if t.Outcome == "preserved" {
		head = fmt.Sprintf("work item %s already has UNCOMMITTED work in a RETAINED WORKTREE: %s",
			workItemID, preservedTreeSentence(t))
	}
	if len(trees) > 1 {
		rest := make([]string, 0, len(trees)-1)
		for _, o := range trees[1:] {
			rest = append(rest, preservedTreeSentence(o))
		}
		head += "; also " + strings.Join(rest, "; ")
	}
	return head + ". A polecat spawned now gets a FRESH worktree and cannot see those files, so it " +
		"re-derives work that already exists — and the original is destroyed by the next gc reap. " +
		"DO NOT remove the worktree to clear this: nothing else on this machine holds those files. " +
		"Read them first (`git -C " + t.Path + " status`; `pogo gc --list-preserved` lists every " +
		"retained tree and separates tracked edits from untracked paths), then decide — commit them " +
		"on " + preservedBranchPhrase(t) + " and land that branch, rescue what is worth keeping by " +
		"hand, or rule the work spent. Dispatch anyway with --preserved-override=\"<why>\" once you " +
		"have read the tree and concluded it holds nothing worth keeping"
}

// readTreesFirst orders positively-read trees ahead of unread ones, so a
// refusal that has both leads with the one it can describe.
func readTreesFirst(in []gitgc.PreservedTree) []gitgc.PreservedTree {
	out := make([]gitgc.PreservedTree, 0, len(in))
	for _, t := range in {
		if t.Outcome == "preserved" {
			out = append(out, t)
		}
	}
	for _, t := range in {
		if t.Outcome != "preserved" {
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
func preservedTreeSentence(t gitgc.PreservedTree) string {
	if t.Outcome != "preserved" {
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
	s := fmt.Sprintf("%s holds %d uncommitted path(s) — %d modified, %d untracked",
		t.Path, t.Total, t.Modified, t.Untracked)
	if t.Untracked > 0 {
		s += " (untracked paths are on no branch, in no stash and on no remote: this tree is their " +
			"only copy on this machine)"
	}
	if t.Branch != "" {
		s += ", on branch " + t.Branch
	} else if t.BranchError != "" {
		s += ", branch unreadable (" + t.BranchError + ")"
	}
	return s
}

// preservedBranchPhrase names the branch to commit on, or says plainly that it
// could not be read rather than emitting a remedy with a hole in it.
func preservedBranchPhrase(t gitgc.PreservedTree) string {
	if t.Branch == "" {
		return "the branch that tree has checked out"
	}
	return "`" + t.Branch + "`"
}
