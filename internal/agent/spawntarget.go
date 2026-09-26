package agent

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/workitem"
)

// The spawn TARGET: which branch a polecat's work is aimed at (drellem2/pogo#176).
//
// One value decides three things at once: the ref the worktree is based on
// (resolvePolecatBaseRef), the refinery submit target the template renders
// ({{.Branch}} → `--target`), and the PR base on the gh-issue track. Before this
// file, that value reached a spawn only through --branch, and --branch was set
// only when the dispatcher remembered to copy the work item's `branch:` field by
// hand. One forgotten flag put all three on the repo's default branch, and a
// worker aimed at an integration branch started tens of commits behind it.
//
// So the item's own field is now the default, and a --branch that CONTRADICTS
// it is refused: two sources disagreeing about where the work lands is not a
// thing pogod can resolve by precedence, because either answer silently
// retargets somebody's merge.

// WorkItemBrancher answers which branch a work item targets, at the moment of
// dispatch. A separate interface from WorkItemTyper for the reason that one is
// separate from DispatchGate: a different question of the same file, failing in
// a different direction — a missing item here means "no default", never a
// refusal.
type WorkItemBrancher interface {
	// WorkItemBranch returns the item's `branch:` value and whether the item
	// was read at all. A found item with no `branch:` line returns ("", true).
	WorkItemBranch(workItemID string) (branch string, found bool)
}

// MGWorkItemBrancher is the production WorkItemBrancher: it reads the item out
// of the macguffin store, as MGWorkItemTyper does.
type MGWorkItemBrancher struct {
	// Root overrides the macguffin store location. Empty resolves via
	// macguffinStoreRoot, which under a test binary is a throwaway store.
	Root string
}

// WorkItemBranch implements WorkItemBrancher. Every failure to read reports
// found=false, which the caller treats as "the item names no branch" — the
// behaviour every spawn had before this existed, so an unreadable store costs
// the default and nothing else.
func (m MGWorkItemBrancher) WorkItemBranch(workItemID string) (string, bool) {
	if workItemID == "" {
		return "", false
	}
	root := macguffinStoreRoot(m.Root)
	if root == "" {
		return "", false
	}
	item, found, err := workitem.FindFrom(filepath.Join(root, "work"), workItemID)
	if err != nil {
		log.Printf("spawn target: could not read work item %s from %s: %v — "+
			"not defaulting --branch from it", workItemID, root, err)
		return "", false
	}
	if !found {
		return "", false
	}
	return strings.TrimSpace(item.Branch), true
}

// SetWorkItemBrancher installs the branch reader consulted at dispatch. Passing
// nil restores the default, MGWorkItemBrancher{}.
func (r *Registry) SetWorkItemBrancher(b WorkItemBrancher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workItemBrancher = b
}

func (r *Registry) getWorkItemBrancher() WorkItemBrancher {
	r.mu.RLock()
	b := r.workItemBrancher
	r.mu.RUnlock()
	if b == nil {
		return MGWorkItemBrancher{}
	}
	return b
}

// Where a spawn's target branch came from, reported on the spawn response.
const (
	TargetFromRequest  = "request"   // --branch, agreeing with the item or the item named none
	TargetFromWorkItem = "work_item" // defaulted from the item's `branch:` field
)

// resolveSpawnTarget decides the spawn's target branch. It returns the target,
// where it came from ("" when there is no target at all), and a refusal when
// an explicit --branch contradicts the item's `branch:`.
//
// The three cases:
//
//  1. The item names a branch and --branch is empty: the item's branch is the
//     target. This is the fix — nobody has to remember to copy it.
//  2. The item names a branch and --branch names a DIFFERENT one: refused. Not
//     resolved by precedence, because either winner silently retargets the
//     merge; the dispatcher has to fix the item or drop the flag.
//  3. Otherwise --branch (possibly empty) stands, as it always has.
func (r *Registry) resolveSpawnTarget(spawnReq SpawnPolecatAPIRequest) (target, from, refusal string) {
	explicit := strings.TrimSpace(spawnReq.Branch)
	itemBranch := ""
	if spawnReq.Id != "" {
		itemBranch, _ = r.getWorkItemBrancher().WorkItemBranch(spawnReq.Id)
	}
	switch {
	case itemBranch != "" && explicit == "":
		return itemBranch, TargetFromWorkItem, ""
	case itemBranch != "" && explicit != itemBranch:
		return "", "", fmt.Sprintf("--branch %q contradicts work item %s, whose `branch:` is %q. "+
			"The target decides the worktree base, the refinery submit target and the PR base, so "+
			"pogod will not pick one for you: either side would silently retarget the merge. Drop "+
			"--branch to use the item's branch, or correct the item's `branch:` field and dispatch again "+
			"(drellem2/pogo#176)", explicit, spawnReq.Id, itemBranch)
	case explicit != "":
		return explicit, TargetFromRequest, ""
	}
	return "", "", ""
}

// SpawnBase reports, on the spawn response, what the worktree was actually
// based on and why — so the dispatcher sees the answer at the moment it can
// still act on it, instead of discovering it from a stale tree.
type SpawnBase struct {
	// Target is the branch the work is aimed at; empty means the repo default.
	Target string `json:"target,omitempty"`
	// TargetFrom is TargetFromRequest or TargetFromWorkItem; empty with Target.
	TargetFrom string `json:"target_from,omitempty"`
	// BaseRef is the ref the worktree was created from. Empty means local HEAD
	// (no usable origin), or no worktree at all when NoWorktree is set.
	BaseRef string `json:"base_ref,omitempty"`
	// Warning is set when the base is NOT the target: the target is not on
	// origin, so the worktree fell back to the default branch (correct — the
	// refinery creates the target at submit — but it must not be silent), or
	// origin was unusable and the worktree is based on local HEAD.
	Warning string `json:"warning,omitempty"`
}

// SpawnPolecatAPIResponse is the 201 body of POST /agents/spawn-polecat: the
// agent record, plus the base report. AgentInfo is embedded so its fields stay
// top-level, and a client that decodes the body as a bare AgentInfo still works.
type SpawnPolecatAPIResponse struct {
	AgentInfo
	Base *SpawnBase `json:"base,omitempty"`
}

// polecatBaseWarning returns a warning when the base is not what the target
// asked for, or "" when it is. It says nothing about an adopted base (that is
// the point of adopting).
//
// baseRef "" means resolvePolecatBaseRef found no usable origin — none
// configured, a fetch that failed or timed out, or no default branch found
// on it — and the worktree was based on local HEAD. That is also not the
// target, and a --json consumer sees only this field, so it is warned about
// too.
func polecatBaseWarning(target, baseRef string, adopted bool) string {
	if target == "" || adopted || baseRef == "origin/"+target {
		return ""
	}
	if baseRef == "" {
		return fmt.Sprintf("target %s not honoured — origin was unusable (no remote, a failed fetch, or no default branch on it), "+
			"so the worktree is based on local HEAD", target)
	}
	return fmt.Sprintf("target %s not on origin — based on %s; the refinery will create %s at submit",
		target, baseRef, target)
}
