package refinery

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/gitvcs"
)

// `pogo refinery submit --item` (docs/design/enth-vcs.md §5, "Submit"; slice
// 6, mg-30da0).
//
// On an enth repo the work item is the identity of the work: there is no
// branch. Submit lists the item's unlanded Changes through enthd, refuses an
// empty listing as a defect (it would deliver nothing, mg-c184d) and an
// ambiguous one (more than one stack), and records the submitted ids on the
// MR. A listed Change is on enthd, so "does the refinery reach it" (R1) is
// the listing itself.
//
// On git --item is accepted too, and resolves to the worker's branch through
// the spawn record — pogod's registry entry for the item's live polecat — so
// the polecat template's submit line can be the same for both VCSs. A given
// branch still wins, and is refused only if the item's live polecat is on a
// different one.

// ItemWorker is what the spawn record says about the polecat working an item.
type ItemWorker struct {
	// Name is the polecat's agent name ($POGO_AGENT_NAME), which is also its
	// Pogo-Worker trailer value.
	Name string
	// Dir is its workspace.
	Dir string
}

// ItemResolver finds the live polecat working item. pogod installs one backed
// by its agent registry.
type ItemResolver func(item string) (ItemWorker, bool)

var itemResolver atomic.Pointer[ItemResolver]

// SetItemResolver installs the spawn-record lookup --item resolves through.
// nil uninstalls it: a git --item with no branch is then refused.
func SetItemResolver(f ItemResolver) {
	if f == nil {
		itemResolver.Store(nil)
		return
	}
	itemResolver.Store(&f)
}

func resolveItemWorker(item string) (ItemWorker, bool) {
	f := itemResolver.Load()
	if f == nil || item == "" {
		return ItemWorker{}, false
	}
	return (*f)(item)
}

// enthRepoFor returns the enth settings of repoPath's project record, when
// the record says enth.
func enthRepoFor(repoPath string) (vcs.EnthRepo, bool) {
	f := repoVCS.Load()
	if f == nil {
		return vcs.EnthRepo{}, false
	}
	kind, e := (*f)(repoPath)
	if kind != vcs.Enth {
		return vcs.EnthRepo{}, false
	}
	if e == nil {
		return vcs.EnthRepo{}, true
	}
	return *e, true
}

// enthWork is the enth backend Submit lists through. A var so tests can
// point it at a sandboxed enth.
var enthWork = func(e vcs.EnthRepo) enthvcs.Backend { return enthvcs.Backend{Repo: e} }

// gitItemBranch resolves a git --item to the worker's branch: the branch the
// item's live polecat has checked out, read from its worktree.
var gitItemBranch = func(w ItemWorker) (string, error) {
	ws, err := gitvcs.Workspaces{}.Describe(context.Background(), w.Dir)
	if err != nil {
		return "", err
	}
	if ws.Branch == "" {
		return "", fmt.Errorf("%s has a detached HEAD", w.Dir)
	}
	return ws.Branch, nil
}

// resolveSubmitIdentity settles what an MR carries before any git check runs.
// It returns isEnth so Submit can skip the git-only validation.
func resolveSubmitIdentity(req *MergeRequest) (isEnth bool, err error) {
	e, isEnth := enthRepoFor(req.RepoPath)
	if isEnth {
		return true, resolveEnthSubmission(req, e)
	}
	if req.Item == "" {
		return false, nil
	}
	w, ok := resolveItemWorker(req.Item)
	if !ok {
		if req.Branch != "" {
			return false, nil // nothing to cross-check; the branch is the identity
		}
		return false, fmt.Errorf("--item %s: no live polecat is working that item, so it does not name a branch — pass the branch: pogo refinery submit <branch> --item=%s", req.Item, req.Item)
	}
	b, berr := gitItemBranch(w)
	if berr != nil {
		if req.Branch != "" {
			return false, nil
		}
		return false, fmt.Errorf("--item %s: polecat %s's worktree %s does not name a branch (%v) — pass the branch", req.Item, w.Name, w.Dir, berr)
	}
	if req.Branch == "" {
		req.Branch = b
		return false, nil
	}
	if req.Branch != b {
		return false, fmt.Errorf("--item %s is polecat %s's work, on branch %s, not %s: refusing to submit one item under another's branch", req.Item, w.Name, b, req.Branch)
	}
	return false, nil
}

// resolveEnthSubmission lists the item's Changes and records them on req.
func resolveEnthSubmission(req *MergeRequest, e vcs.EnthRepo) error {
	if req.Branch != "" {
		return fmt.Errorf("%s is an enth repo: a submission is named by its work item, not a branch — pogo refinery submit --item=<id> (got branch %q)", req.RepoPath, req.Branch)
	}
	if req.Item == "" {
		return fmt.Errorf("%s is an enth repo: --item is required (the work item names the work)", req.RepoPath)
	}
	if req.PostMergeTag != "" || req.AutoCreateTargetRef {
		return fmt.Errorf("%s is an enth repo: --post-merge-tag and --auto-create-target are not supported on enth yet (enth-vcs slice 12)", req.RepoPath)
	}
	b := enthWork(e)
	ctx := context.Background()
	if _, err := b.RefTip(ctx, req.TargetRef); err != nil {
		return fmt.Errorf("target ref %q not found on %s (%s): %w", req.TargetRef, e.Repo, e.Remote, err)
	}
	// The worker filter is the normal case and makes ambiguity rare (§5);
	// without a live polecat for the item, list by the item alone.
	worker := ""
	if w, ok := resolveItemWorker(req.Item); ok {
		worker = w.Name
	}
	s, err := b.FindStack(ctx, req.Item, worker)
	var amb *vcs.AmbiguousWorkError
	switch {
	case errors.Is(err, vcs.ErrNoWork):
		return fmt.Errorf("class=%s: %s carries nothing ahead of %s — nothing would be delivered: %v", ClassDefect, req.Item, req.TargetRef, err)
	case errors.As(err, &amb):
		return fmt.Errorf("refusing to guess which work to land: %w", err)
	case err != nil:
		return fmt.Errorf("listing %s's Changes on %s: %w", req.Item, e.Remote, err)
	}
	req.EnthWorker = s.Worker
	req.EnthSubmitted = append([]string(nil), s.Revs...)
	return nil
}

// enthLandUnsupported is how the merge worker fails an enth MR until enth
// landing exists (slice 7, mg-1fe2b): before any git step, which would act on
// a checkout that is not where the work lands.
func enthLandUnsupported(mr *MergeRequest) error {
	return fmt.Errorf("MR %s is an enth submission (%s, %d Change(s)): landing through enth is enth-vcs slice 7 (mg-1fe2b), not built yet — nothing was landed: %w",
		mr.ID, mr.Item, len(mr.EnthSubmitted), vcs.ErrUnsupported)
}
