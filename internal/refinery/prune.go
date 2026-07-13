package refinery

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// PruneResult describes what was cleaned up during a worktree prune.
type PruneResult struct {
	// Repo is the worktree clone directory basename.
	Repo string `json:"repo"`
	// PrunedBranches lists local branches that were deleted because they
	// are fully merged into the target (main).
	PrunedBranches []string `json:"pruned_branches,omitempty"`
	// Error is set if pruning this worktree clone failed.
	Error string `json:"error,omitempty"`
}

// PruneWorktrees iterates over all worktree clones under WorktreeDir,
// deletes local branches that have been merged to main, and prunes
// stale remote-tracking references. Returns a result per worktree clone.
func (r *Refinery) PruneWorktrees() []PruneResult {
	entries, err := os.ReadDir(r.cfg.WorktreeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []PruneResult{{Error: fmt.Sprintf("read worktree dir: %v", err)}}
	}

	var results []PruneResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		wtDir := filepath.Join(r.cfg.WorktreeDir, entry.Name())
		// Skip directories that aren't git repos
		if _, err := os.Stat(filepath.Join(wtDir, ".git")); err != nil {
			continue
		}
		result := pruneWorktreeClone(wtDir, entry.Name())
		results = append(results, result)
	}
	return results
}

// pruneWorktreeClone prunes merged branches from a single worktree clone.
func pruneWorktreeClone(wtDir, name string) PruneResult {
	result := PruneResult{Repo: name}

	// Prune stale remote-tracking references first
	if _, err := gitCmdOutput(wtDir, "fetch", "--prune", "origin"); err != nil {
		// Non-fatal: remote may be unreachable, but we can still prune local branches
		log.Printf("refinery: prune: fetch --prune failed for %s: %v", name, err)
	}

	// Ensure we're on main so we can delete other branches
	if _, err := gitCmdOutput(wtDir, "checkout", "main"); err != nil {
		result.Error = fmt.Sprintf("checkout main: %v", err)
		return result
	}

	// Realign main to origin so merged detection is accurate. A plain
	// 'git pull --ff-only' silently aborts when the local target has diverged
	// from origin ("Not possible to fast-forward"), leaving a polluted or
	// divergent target in place — so prune would not be an operator escape
	// hatch for such a clone. Hard-reset to the fetched origin/main instead:
	// the clone's target then matches origin regardless of prior local state.
	// Mirrors the merge-path target reset (merge.go). (mg-58f6)
	if _, err := gitCmdOutput(wtDir, "reset", "--hard", "origin/main"); err != nil {
		// Non-fatal: origin/main may be absent (fetch above failed, or the
		// remote is unreachable). Fall back to pruning against the local main.
		log.Printf("refinery: prune: reset main to origin/main failed for %s: %v", name, err)
	}

	// List branches merged into main (excluding main itself)
	output, err := gitCmdOutput(wtDir, "branch", "--merged", "main")
	if err != nil {
		result.Error = fmt.Sprintf("list merged branches: %v", err)
		return result
	}

	for _, line := range strings.Split(output, "\n") {
		branch := strings.TrimSpace(line)
		// Skip empty lines, current branch marker, and main/master
		branch = strings.TrimPrefix(branch, "* ")
		branch = strings.TrimSpace(branch)
		if branch == "" || branch == "main" || branch == "master" {
			continue
		}

		if _, err := gitCmdOutput(wtDir, "branch", "-d", branch); err != nil {
			log.Printf("refinery: prune: failed to delete branch %s in %s: %v", branch, name, err)
			continue
		}
		result.PrunedBranches = append(result.PrunedBranches, branch)
		log.Printf("refinery: prune: deleted merged branch %s in %s", branch, name)
	}

	return result
}
