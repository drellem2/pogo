package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CrewWorktreeDir returns the worktree path for a crew agent.
// Layout: ~/.pogo/agents/<name>/worktree
func CrewWorktreeDir(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pogo", "agents", name, "worktree")
}

// EnsureCrewWorktree creates or reuses a git worktree for a crew agent.
// Unlike polecat worktrees (which are ephemeral), crew worktrees persist
// across restarts so the agent keeps its working state.
//
// The worktree is created at ~/.pogo/agents/<name>/worktree on a branch
// named <name>. If the worktree already exists, it is reused as-is.
func EnsureCrewWorktree(name, sourceRepo string) (string, error) {
	wtDir := CrewWorktreeDir(name)

	// If the worktree already exists, reuse it
	if _, err := os.Stat(filepath.Join(wtDir, ".git")); err == nil {
		return wtDir, nil
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(wtDir), 0755); err != nil {
		return "", fmt.Errorf("create agent dir: %w", err)
	}

	// Create the worktree with -B to force-create the branch (handles
	// the case where the branch exists from a previous worktree that
	// was removed but whose branch wasn't deleted).
	branchName := name
	cmd := exec.Command("git", "-C", sourceRepo, "worktree", "add", wtDir, "-B", branchName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("worktree creation failed: %v\n%s", err, out)
	}

	return wtDir, nil
}
