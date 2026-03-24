package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// initBareRepo creates a bare repo and a clone of it, returning (bareDir, cloneDir).
func initBareRepo(t *testing.T, parent string, name string) (string, string) {
	t.Helper()
	bareDir := filepath.Join(parent, name+".git")
	cloneDir := filepath.Join(parent, name)

	// Create bare repo with explicit default branch "main"
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %s: %v", out, err)
	}

	// Clone it
	cmd = exec.Command("git", "clone", bareDir, cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %s: %v", out, err)
	}

	// Create initial commit on main
	gitInDir(t, cloneDir, "config", "user.email", "test@test.com")
	gitInDir(t, cloneDir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, cloneDir, "add", "README.md")
	gitInDir(t, cloneDir, "commit", "-m", "initial")
	gitInDir(t, cloneDir, "push", "origin", "main")

	return bareDir, cloneDir
}

func gitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %s: %v", args, dir, out, err)
	}
	return string(out)
}

func TestPruneWorktrees_MergedBranches(t *testing.T) {
	tmp := t.TempDir()

	// Set up a bare repo and a "source" clone (simulates the user's repo)
	bareDir, sourceDir := initBareRepo(t, tmp, "myrepo")

	// Create a feature branch, push it, then merge it to main
	gitInDir(t, sourceDir, "checkout", "-b", "polecat-abc")
	if err := os.WriteFile(filepath.Join(sourceDir, "feature.txt"), []byte("feat"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, sourceDir, "add", "feature.txt")
	gitInDir(t, sourceDir, "commit", "-m", "feature commit")
	gitInDir(t, sourceDir, "push", "origin", "polecat-abc")

	// Merge the feature to main and push
	gitInDir(t, sourceDir, "checkout", "main")
	gitInDir(t, sourceDir, "merge", "--ff-only", "polecat-abc")
	gitInDir(t, sourceDir, "push", "origin", "main")

	// Create the refinery worktree clone (simulates ensureWorktree)
	wtDir := filepath.Join(tmp, "worktrees")
	refineryClone := filepath.Join(wtDir, "myrepo")
	cmd := exec.Command("git", "clone", bareDir, refineryClone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone for refinery: %s: %v", out, err)
	}

	// Create the local branch in the refinery clone (simulates checkout -B during merge)
	gitInDir(t, refineryClone, "checkout", "-b", "polecat-abc", "origin/polecat-abc")
	gitInDir(t, refineryClone, "checkout", "main")

	// Create refinery and prune
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	results := r.PruneWorktrees()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	res := results[0]
	if res.Repo != "myrepo" {
		t.Errorf("expected repo myrepo, got %s", res.Repo)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if len(res.PrunedBranches) != 1 || res.PrunedBranches[0] != "polecat-abc" {
		t.Errorf("expected pruned branch polecat-abc, got %v", res.PrunedBranches)
	}
}

func TestPruneWorktrees_NoMergedBranches(t *testing.T) {
	tmp := t.TempDir()

	bareDir, sourceDir := initBareRepo(t, tmp, "myrepo")

	// Create a feature branch that is NOT merged to main
	gitInDir(t, sourceDir, "checkout", "-b", "polecat-xyz")
	if err := os.WriteFile(filepath.Join(sourceDir, "wip.txt"), []byte("wip"), 0644); err != nil {
		t.Fatal(err)
	}
	gitInDir(t, sourceDir, "add", "wip.txt")
	gitInDir(t, sourceDir, "commit", "-m", "wip")
	gitInDir(t, sourceDir, "push", "origin", "polecat-xyz")
	gitInDir(t, sourceDir, "checkout", "main")

	// Refinery clone with the unmerged branch
	wtDir := filepath.Join(tmp, "worktrees")
	refineryClone := filepath.Join(wtDir, "myrepo")
	cmd := exec.Command("git", "clone", bareDir, refineryClone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %s: %v", out, err)
	}
	gitInDir(t, refineryClone, "checkout", "-b", "polecat-xyz", "origin/polecat-xyz")
	gitInDir(t, refineryClone, "checkout", "main")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	results := r.PruneWorktrees()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].PrunedBranches) != 0 {
		t.Errorf("expected no pruned branches, got %v", results[0].PrunedBranches)
	}
}

func TestPruneWorktrees_EmptyDir(t *testing.T) {
	wtDir := t.TempDir()

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	results := r.PruneWorktrees()
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty dir, got %d", len(results))
	}
}
