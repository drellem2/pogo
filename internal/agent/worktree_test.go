package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// createTestRepo creates a bare-bones git repo for testing worktree operations.
func createTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "source-repo")

	for _, args := range [][]string{
		{"init", repo},
		{"-C", repo, "commit", "--allow-empty", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func TestCrewWorktreeDir(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	got := CrewWorktreeDir("mayor")
	want := filepath.Join(tmpHome, ".pogo", "agents", "mayor", "worktree")
	if got != want {
		t.Errorf("CrewWorktreeDir(mayor) = %q, want %q", got, want)
	}
}

func TestEnsureCrewWorktree_Creates(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := createTestRepo(t)

	wtDir, err := EnsureCrewWorktree("mayor", repo)
	if err != nil {
		t.Fatalf("EnsureCrewWorktree: %v", err)
	}

	// Verify worktree was created at expected path
	want := filepath.Join(tmpHome, ".pogo", "agents", "mayor", "worktree")
	if wtDir != want {
		t.Errorf("worktree dir = %q, want %q", wtDir, want)
	}

	// Verify .git exists (worktree link file)
	if _, err := os.Stat(filepath.Join(wtDir, ".git")); os.IsNotExist(err) {
		t.Error("worktree .git does not exist")
	}

	// Verify the branch is named "mayor"
	cmd := exec.Command("git", "-C", wtDir, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch: %v", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch != "mayor" {
		t.Errorf("branch = %q, want %q", branch, "mayor")
	}
}

func TestEnsureCrewWorktree_ReusesExisting(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := createTestRepo(t)

	// Create worktree first time
	wtDir1, err := EnsureCrewWorktree("mayor", repo)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Create a marker file to verify it's the same directory
	marker := filepath.Join(wtDir1, ".test-marker")
	os.WriteFile(marker, []byte("reuse-test"), 0644)

	// Second call should reuse
	wtDir2, err := EnsureCrewWorktree("mayor", repo)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if wtDir1 != wtDir2 {
		t.Errorf("paths differ: %q vs %q", wtDir1, wtDir2)
	}

	// Marker should still be there
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("marker file missing — worktree was recreated instead of reused")
	}
}

func TestEnsureCrewWorktree_DifferentAgents(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	repo := createTestRepo(t)

	wt1, err := EnsureCrewWorktree("mayor", repo)
	if err != nil {
		t.Fatalf("mayor worktree: %v", err)
	}

	wt2, err := EnsureCrewWorktree("planner", repo)
	if err != nil {
		t.Fatalf("planner worktree: %v", err)
	}

	if wt1 == wt2 {
		t.Error("different agents should get different worktree paths")
	}

	// Verify each has its own branch
	for _, tc := range []struct {
		dir, branch string
	}{
		{wt1, "mayor"},
		{wt2, "planner"},
	} {
		out, _ := exec.Command("git", "-C", tc.dir, "branch", "--show-current").Output()
		if strings.TrimSpace(string(out)) != tc.branch {
			t.Errorf("worktree %s: branch = %q, want %q", tc.dir, strings.TrimSpace(string(out)), tc.branch)
		}
	}
}

func TestEnsureCrewWorktree_InvalidRepo(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	_, err := EnsureCrewWorktree("mayor", "/nonexistent/repo")
	if err == nil {
		t.Error("expected error for invalid repo")
	}
}
