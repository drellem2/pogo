package gitgc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PreservedForItems answers, for a named set of work items, whether any of them
// has a surviving worktree that gc will not reclaim — a tree holding
// uncommitted work, or one whose contents could not be read at all.
//
// # Why this exists beside ScanPreserved (mg-836c)
//
// ScanPreserved is the READER's instrument: it enumerates the whole retained
// population so an operator can decide what to rescue. Everything it needs to
// do that — the age walk, the full file list, the ticket index, the
// force-reclaim column — makes it "an operator command rather than something to
// put on a tick", in its own words.
//
// This is the GUARD's instrument, and it answers a much narrower question:
// "does item X have a tree". The narrowing is what makes it affordable on a
// dispatch and on a heartbeat, and it is done in the one place that costs
// nothing — the DIRECTORY NAME. A polecat's worktree is named after the polecat
// and a polecat is named after its work item (OwnerMatchesItem), so the
// candidate set is computed from `os.ReadDir` alone and `git status` runs only
// on the trees that could possibly matter. In the steady state that is zero
// trees per call.
//
// # What it is for
//
// Detection, preservation, notification and a standing list all existed before
// this and all worked. What nothing did was GATE DISPATCH on any of it: an item
// whose only copy of its work sat in a preserved tree still read `available`,
// still drew a priority-wake advertising it as ready, and still spawned a
// worker that got a fresh worktree and could not see the files. The preserved
// notice said so itself, in capitals, and fired ONCE — to a single addressee who
// was inside the outage it was reporting on. A one-shot message is exactly as
// reliable as its recipient; a gate is a standing property of the item.
//
// # It does NOT consult liveness, for StrandedWorkGate's reason
//
// A running polecat is not evidence that dispatching a second one is safe — it
// is the precondition for the harm. Callers that need the distinction (the
// stall-watch surfaces, which already drop live-worker items into a different
// notice) filter on their own liveness probe; this function reports the tree
// either way.
//
// # It does NOT measure age
//
// newestWrite walks every file in the tree, and the age decides nothing here:
// a guard refuses on the presence of the work, not on how old it is. Age is
// what `pogo gc --list-preserved` is for, and the refusal points there.
func PreservedForItems(opts PreservedItemOptions) (PreservedItemReport, error) {
	rep := PreservedItemReport{Trees: map[string][]PreservedTree{}}
	if opts.PolecatsDir == "" {
		return rep, fmt.Errorf("scan preserved worktrees: no polecats dir")
	}
	want := make([]string, 0, len(opts.Items))
	for _, id := range opts.Items {
		if strings.TrimSpace(id) != "" {
			want = append(want, id)
		}
	}
	if len(want) == 0 {
		return rep, nil
	}

	entries, err := os.ReadDir(opts.PolecatsDir)
	if err != nil {
		// A missing polecats dir is not an error and not evidence of anything:
		// a host that has never run a polecat has no such directory. Every
		// other read failure IS reported, because the caller's failure
		// direction depends on knowing the difference.
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, fmt.Errorf("read polecats dir %s: %w", opts.PolecatsDir, err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		owner := e.Name()
		// The cheap half: which of the wanted items could this directory
		// possibly belong to. Nothing has been executed yet at this point.
		var mine []string
		for _, id := range want {
			if OwnerMatchesItem(owner, id) {
				mine = append(mine, id)
			}
		}
		if len(mine) == 0 {
			continue
		}

		path := filepath.Join(opts.PolecatsDir, owner)
		// No `.git` entry means this is not a linked worktree — an orphan dir
		// with no index and no HEAD, so "holds uncommitted work" is not a
		// property it has. Skipped rather than reported: gating a dispatch on a
		// directory nobody can read work out of would be a refusal with no
		// remedy behind it.
		if _, lerr := os.Lstat(filepath.Join(path, ".git")); lerr != nil {
			continue
		}

		tree := PreservedTree{Path: path, Owner: owner}
		if repo, rerr := WorktreeSourceRepo(path); rerr == nil {
			tree.Repo = repo
		} else {
			tree.RepoError = rerr.Error()
		}
		// Same rule as ScanPreserved's filter, and it matters more here: a tree
		// whose .git pointer could not be read might belong to this repo, and
		// dropping it would make the guard silent in exactly the case where
		// something is already wrong with the tree.
		if opts.Repo != "" && tree.Repo != "" && !sameRepoPath(tree.Repo, opts.Repo) {
			continue
		}

		// The expensive half, reached only by a tree that names one of the
		// wanted items. The same guard the sweep and the exit hook consult,
		// called rather than re-implemented, so this cannot claim a tree is
		// retained that gc would happily reap.
		chk := checkWorktreeRemoval(path)
		if chk.Refusal == nil {
			continue
		}
		var dwe *DirtyWorktreeError
		var uwe *UndeterminedWorktreeError
		switch {
		case errors.As(chk.Refusal, &dwe):
			tree.Outcome = "preserved"
			if _, files, ferr := WorktreeDirty(path); ferr == nil {
				tree.Files = files
			} else {
				tree.Files = dwe.Files
				rep.Errors = append(rep.Errors, fmt.Sprintf(
					"re-read status of %s for the full file list: %v (using the capped list)", path, ferr))
			}
			tree.Total, tree.Modified, tree.Untracked = dwe.Total, dwe.Modified, dwe.Untracked
		case errors.As(chk.Refusal, &uwe):
			tree.Outcome = "undetermined"
			tree.StatusError = uwe.Err.Error()
		default:
			tree.Outcome = "retained"
			tree.StatusError = chk.Refusal.Error()
		}

		if branch, berr := WorktreeBranch(path); berr == nil {
			tree.Branch = branch
		} else {
			tree.BranchError = berr.Error()
		}

		for _, id := range mine {
			t := tree
			t.WorkItemID = id
			rep.Trees[id] = append(rep.Trees[id], t)
		}
	}

	for id := range rep.Trees {
		sortPreserved(rep.Trees[id])
	}
	return rep, nil
}

// PreservedItemOptions configures PreservedForItems.
type PreservedItemOptions struct {
	// PolecatsDir is the directory to scan — $POGO_HOME/polecats in production
	// (DefaultPolecatsDir). Required.
	PolecatsDir string
	// Repo, when non-empty, drops trees that RESOLVED to a different
	// repository. A tree whose repo could not be resolved is kept — see the
	// filter's comment.
	Repo string
	// Items are the work-item ids to answer for. An empty slice answers for
	// nothing and does no work; it is never a wildcard, because a guard that
	// silently widened to every item would refuse dispatches it was never
	// asked about.
	Items []string
}

// PreservedItemReport is what PreservedForItems found.
type PreservedItemReport struct {
	// Trees maps work-item id -> the retained trees attributable to it. An
	// item with no entry has no such tree; an item absent from Items was never
	// asked about, which is a different fact and is why the caller supplies the
	// list.
	Trees map[string][]PreservedTree `json:"trees"`
	// Errors are non-fatal read failures encountered along the way. They never
	// suppress a finding — a tree that produced an error is still reported.
	Errors []string `json:"errors,omitempty"`
}

// Any reports whether the scan found a tree for any item.
func (r PreservedItemReport) Any() bool { return len(r.Trees) > 0 }

// IDs returns the work items that have a retained tree, sorted.
func (r PreservedItemReport) IDs() []string {
	out := make([]string, 0, len(r.Trees))
	for id := range r.Trees {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// OwnerMatchesItem reports whether a polecat NAME — a worktree directory's
// basename, equivalently a `polecat-` branch's suffix — resolves to
// workItemID.
//
// It is candidateIDs run in the one direction that needs no ticket index. The
// index-backed OwnerState answers "which work item is this tree's, out of all
// of them", and needs `mg` to be reachable to do it; this answers "is this
// tree THIS item's", which is a string question. A guard must not depend on a
// subprocess it can be denied.
//
// Comparison is on the bare 4-hex code: candidateIDs emits both the `mg-`
// prefixed and unprefixed spellings of every candidate, so normalising both
// sides costs nothing and makes a caller's `836c` and `mg-836c` the same
// question.
func OwnerMatchesItem(owner, workItemID string) bool {
	want := bareItemID(workItemID)
	if owner == "" || want == "" {
		return false
	}
	// Lowercased before candidate generation, not merely at comparison: the
	// last-resort 4-hex recovery inside candidateIDs is a lowercase regex, so an
	// upper- or mixed-case directory name would yield no hex candidate at all
	// and the match would fail silently rather than loudly.
	for _, c := range candidateIDs(strings.ToLower(owner)) {
		if strings.EqualFold(bareItemID(c), want) {
			return true
		}
	}
	return false
}

// sameRepoPath compares two repository paths, resolving symlinks before
// declaring them different.
//
// The comparison has to survive a symlinked path because BOTH sides are read
// from different places: opts.Repo comes from a caller (a spawn request's
// --repo, typed by a person or a coordinator) and tree.Repo is read out of the
// worktree's own `.git` pointer, which git wrote after canonicalising. On macOS
// `/var` is a symlink to `/private/var`, so those two spellings of one
// repository compare unequal on a plain string test — and the failure direction
// is silent: the guard drops the tree and dispatches.
//
// EvalSymlinks failing is not fatal. A path that cannot be resolved falls back
// to the literal comparison, which is what this function did before.
func sameRepoPath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// bareItemID strips the `mg-` prefix so the two spellings of an id compare
// equal.
func bareItemID(id string) string {
	id = strings.TrimSpace(id)
	if rest, ok := strings.CutPrefix(strings.ToLower(id), "mg-"); ok {
		return rest
	}
	return strings.ToLower(id)
}
