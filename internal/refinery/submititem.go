package refinery

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/gitvcs"
)

// `pogo refinery submit --item` (docs/design/enth-vcs.md §5, "Submit"; slice
// 6, mg-30da0).
//
// On an enth repo submit is REFUSED (mg-5b904): an enth repo has no merge
// queue, and the worker lands its own work (selfland.go, enth-vcs §6). The
// refusal names `pogo land`. Until then (slice 6) submit listed the item's
// Changes and queued them for the refinery's enth land path, which is gone.
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

// resolveEnthSubmission refuses: the refinery does not land enth work, the
// worker does (enth-vcs §6, mg-5b904). The refusal names the verb.
func resolveEnthSubmission(req *MergeRequest, _ vcs.EnthRepo) error {
	return enthSubmitRefusal(req)
}
