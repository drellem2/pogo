package gitgc

import (
	"fmt"
	"sort"
	"strings"
)

// Options configures a single GC sweep.
type Options struct {
	// Repo is the git repository to sweep (the source repo whose .git
	// holds the polecat branches and worktree registrations).
	Repo string
	// TargetBranch is the merge target a *done* ticket's branch must be
	// merged into before deletion. Empty defaults to DefaultTargetBranch.
	TargetBranch string
	// LivePolecats is the do-not-touch set, keyed by polecat name (which
	// equals a branch's "polecat-" suffix and its worktree basename).
	// pogod fills this from its agent registry so a sweep never disturbs a
	// running polecat — even one whose worktree was unlinked at refinery
	// submit time and so is no longer git-checked-out.
	LivePolecats map[string]bool
	// Tickets, when non-nil, supplies work-item states directly. When nil,
	// Sweep loads them via LoadTicketIndex (`mg list`). Injecting a map
	// keeps Sweep unit-testable without the mg binary.
	Tickets TicketIndex
	// DryRun reports what would be done without deleting anything.
	DryRun bool
	// Logf, when set, receives a line per action for progress logging.
	Logf func(format string, args ...any)
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// BranchAction records the GC decision for one polecat branch.
type BranchAction struct {
	Branch string
	ID     string // resolved work-item ID; "" when unknown
	State  TicketState
	Reason string
}

// WorktreeAction records the GC decision for one polecat worktree.
type WorktreeAction struct {
	Path   string
	Branch string
	Reason string
}

// Result is the outcome of a sweep.
type Result struct {
	Repo             string
	DryRun           bool
	BranchesDeleted  []BranchAction
	BranchesKept     []BranchAction
	WorktreesRemoved []WorktreeAction
	WorktreesKept    []WorktreeAction
	PruneOutput      string
	Errors           []string
}

// Sweep runs one GC pass over opts.Repo:
//
//  1. Remove worktrees of concluded, non-live polecats, then `git worktree
//     prune` any registration whose directory has vanished.
//  2. Delete `polecat-*` branches whose ticket is concluded — archived
//     unconditionally, done only once merged into the target branch —
//     skipping any branch that is live or still checked out.
//
// Worktrees are handled before branches so that removing a worktree frees
// its branch for deletion in the same pass. Sweep is conservative: an
// unresolvable ticket, an in-flight ticket, or a live polecat is always
// kept. Errors on individual items are collected into Result.Errors and do
// not abort the sweep.
func Sweep(opts Options) (Result, error) {
	if opts.TargetBranch == "" {
		opts.TargetBranch = DefaultTargetBranch
	}
	tickets := opts.Tickets
	if tickets == nil {
		loaded, err := LoadTicketIndex()
		if err != nil {
			return Result{}, fmt.Errorf("load ticket index: %w", err)
		}
		tickets = loaded
	}

	res := Result{Repo: opts.Repo, DryRun: opts.DryRun}

	// --- Phase 1: worktrees ---------------------------------------------
	worktrees, err := ListWorktrees(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list worktrees: %w", err)
	}

	// Branches whose worktree was (or would be) removed this pass — they
	// become un-checked-out and thus deletable in phase 2.
	freed := map[string]bool{}

	for _, wt := range worktrees {
		if wt.Main || wt.Bare || !wt.IsPolecat() {
			continue
		}
		name := BranchSuffix(wt.Branch)
		if opts.LivePolecats[name] {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: wt.Path, Branch: wt.Branch, Reason: "live polecat",
			})
			continue
		}
		_, state := tickets.BranchState(wt.Branch)
		if !state.Concluded() {
			res.WorktreesKept = append(res.WorktreesKept, WorktreeAction{
				Path: wt.Path, Branch: wt.Branch, Reason: "ticket " + state.String(),
			})
			continue
		}
		freed[wt.Branch] = true
		action := WorktreeAction{Path: wt.Path, Branch: wt.Branch, Reason: "ticket " + state.String()}
		if opts.DryRun {
			opts.logf("would remove worktree %s (%s)", wt.Path, wt.Branch)
		} else {
			if err := RemoveWorktree(opts.Repo, wt.Path); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("remove worktree %s: %v", wt.Path, err))
				continue
			}
			opts.logf("removed worktree %s (%s)", wt.Path, wt.Branch)
		}
		res.WorktreesRemoved = append(res.WorktreesRemoved, action)
	}

	// Drop registrations whose directory is already gone.
	if pruneOut, err := PruneWorktrees(opts.Repo, opts.DryRun); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("worktree prune: %v", err))
	} else {
		res.PruneOutput = pruneOut
	}

	// --- Phase 2: branches ----------------------------------------------
	branches, err := ListPolecatBranches(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list branches: %w", err)
	}
	checkedOut, err := CheckedOutBranches(opts.Repo)
	if err != nil {
		return res, fmt.Errorf("list checked-out branches: %w", err)
	}

	for _, br := range branches {
		name := BranchSuffix(br)
		id, state := tickets.BranchState(br)
		action := BranchAction{Branch: br, ID: id, State: state}

		if opts.LivePolecats[name] {
			action.Reason = "live polecat"
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		// A branch checked out in a worktree we did not remove cannot be
		// deleted — git refuses, and we should not want to.
		if checkedOut[br] && !freed[br] {
			action.Reason = "checked out in a worktree"
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		if !state.Concluded() {
			action.Reason = "ticket " + state.String()
			res.BranchesKept = append(res.BranchesKept, action)
			continue
		}
		// Done (but not archived) tickets must be merged before deletion;
		// archived tickets are deleted regardless — the work has concluded.
		if state == TicketDone {
			merged, err := BranchMerged(opts.Repo, br, opts.TargetBranch)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("merge check %s: %v", br, err))
				action.Reason = "merge check failed"
				res.BranchesKept = append(res.BranchesKept, action)
				continue
			}
			if !merged {
				action.Reason = "ticket done but branch not merged into " + opts.TargetBranch
				res.BranchesKept = append(res.BranchesKept, action)
				continue
			}
			action.Reason = "ticket done, merged"
		} else {
			action.Reason = "ticket archived"
		}

		if opts.DryRun {
			opts.logf("would delete branch %s (%s)", br, action.Reason)
		} else {
			if err := DeleteBranch(opts.Repo, br); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("delete branch %s: %v", br, err))
				continue
			}
			opts.logf("deleted branch %s (%s)", br, action.Reason)
		}
		res.BranchesDeleted = append(res.BranchesDeleted, action)
	}

	return res, nil
}

// Summary renders a human-readable multi-line report of a sweep result.
func (r Result) Summary() string {
	var b strings.Builder
	verb := "removed"
	if r.DryRun {
		verb = "would remove"
	}
	delVerb := "deleted"
	if r.DryRun {
		delVerb = "would delete"
	}
	fmt.Fprintf(&b, "git GC sweep of %s%s\n", r.Repo, dryRunTag(r.DryRun))
	fmt.Fprintf(&b, "  worktrees: %s %d, kept %d\n", verb, len(r.WorktreesRemoved), len(r.WorktreesKept))
	fmt.Fprintf(&b, "  branches:  %s %d, kept %d\n", delVerb, len(r.BranchesDeleted), len(r.BranchesKept))

	if len(r.WorktreesRemoved) > 0 {
		fmt.Fprintf(&b, "  worktrees %s:\n", verb)
		for _, w := range sortedWorktrees(r.WorktreesRemoved) {
			fmt.Fprintf(&b, "    %s (%s)\n", w.Path, w.Branch)
		}
	}
	if len(r.BranchesDeleted) > 0 {
		fmt.Fprintf(&b, "  branches %s:\n", delVerb)
		for _, br := range sortedBranches(r.BranchesDeleted) {
			fmt.Fprintf(&b, "    %s — %s\n", br.Branch, br.Reason)
		}
	}
	if r.PruneOutput != "" {
		fmt.Fprintf(&b, "  worktree prune: %s\n", strings.ReplaceAll(r.PruneOutput, "\n", "; "))
	}
	if len(r.Errors) > 0 {
		fmt.Fprintf(&b, "  errors (%d):\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "    %s\n", e)
		}
	}
	return b.String()
}

func dryRunTag(dry bool) string {
	if dry {
		return " (dry run)"
	}
	return ""
}

func sortedBranches(in []BranchAction) []BranchAction {
	out := append([]BranchAction(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Branch < out[j].Branch })
	return out
}

func sortedWorktrees(in []WorktreeAction) []WorktreeAction {
	out := append([]WorktreeAction(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
