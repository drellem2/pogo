package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentInfoLastActivity(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "activity-test",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Nudge to generate output
	if err := a.Nudge("hello"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	info := ExportInfo(a)
	if info.LastActivity == "" {
		t.Error("expected LastActivity to be set after output")
	}
	if !strings.Contains(info.LastActivity, "ago") && info.LastActivity != "just now" {
		t.Errorf("unexpected LastActivity format: %q", info.LastActivity)
	}
}

func TestAgentInfoLastActivityEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Spawn a process that exits immediately without producing visible output
	a, err := reg.Spawn(SpawnRequest{
		Name:    "no-activity",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check info immediately — the ring buffer's lastWrite is zero before any PTY output
	// Note: PTY setup may produce some initial output, so we just verify the field
	// is either empty or a valid "ago" string.
	info := ExportInfo(a)
	if info.LastActivity != "" && !strings.Contains(info.LastActivity, "ago") && info.LastActivity != "just now" {
		t.Errorf("unexpected LastActivity format: %q", info.LastActivity)
	}
}

func TestFormatLastActivity(t *testing.T) {
	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"just now", 0, "just now"},
		{"seconds", 5 * time.Second, "5s ago"},
		{"minutes", 2*time.Minute + 30*time.Second, "2m30s ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLastActivity(time.Now().Add(-tt.ago))
			if got != tt.want {
				t.Errorf("formatLastActivity(-%v) = %q, want %q", tt.ago, got, tt.want)
			}
		})
	}
}

// runGit runs a git command with a stable identity for tests.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// makeRepoWithOrigin creates a bare "origin" repo plus a working clone whose
// origin remote points at it. Returns (workDir, originDir).
func makeRepoWithOrigin(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	originDir := filepath.Join(root, "origin.git")
	workDir := filepath.Join(root, "work")

	if out, err := exec.Command("git", "init", "--bare", "-b", "main", originDir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "init", "-b", "main", workDir).CombinedOutput(); err != nil {
		t.Fatalf("init work: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", workDir, "remote", "add", "origin", originDir).CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v\n%s", err, out)
	}

	// Seed initial commit and push so origin/main exists.
	if err := os.WriteFile(filepath.Join(workDir, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workDir, "add", "seed.txt")
	runGit(t, workDir, "commit", "-m", "seed")
	runGit(t, workDir, "push", "-u", "origin", "main")
	return workDir, originDir
}

// TestResolvePolecatBaseRef_PrefersOriginBranch verifies the helper returns
// origin/<branch> when a target branch is supplied, even if the local checkout
// is behind origin. This is the core fix for mg-58a3.
func TestResolvePolecatBaseRef_PrefersOriginBranch(t *testing.T) {
	workDir, originDir := makeRepoWithOrigin(t)

	// Make a second clone that pushes a commit to origin/main directly.
	otherDir := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("git", "clone", originDir, otherDir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "added.txt"), []byte("merged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, otherDir, "add", "added.txt")
	runGit(t, otherDir, "commit", "-m", "merged via origin")
	runGit(t, otherDir, "push", "origin", "main")

	// At this point workDir's local main is BEHIND origin/main.
	// resolvePolecatBaseRef should fetch and return origin/main.
	got := resolvePolecatBaseRef(workDir, "main")
	if got != "origin/main" {
		t.Fatalf("resolvePolecatBaseRef = %q, want origin/main", got)
	}

	// Verify origin/main now contains the new commit (i.e. fetch happened).
	out, err := exec.Command("git", "-C", workDir, "log", "origin/main", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatalf("log origin/main: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "merged via origin") {
		t.Errorf("expected origin/main to include merged commit after fetch, got:\n%s", out)
	}
}

// TestResolvePolecatBaseRef_DefaultBranch verifies the helper falls back to
// origin/HEAD's branch when no explicit branch is supplied.
func TestResolvePolecatBaseRef_DefaultBranch(t *testing.T) {
	workDir, _ := makeRepoWithOrigin(t)
	// Set origin/HEAD so symbolic-ref works.
	runGit(t, workDir, "remote", "set-head", "origin", "main")

	got := resolvePolecatBaseRef(workDir, "")
	if got != "origin/main" {
		t.Fatalf("resolvePolecatBaseRef(empty branch) = %q, want origin/main", got)
	}
}

// TestResolvePolecatBaseRef_NoOrigin returns empty when origin is missing,
// allowing the caller to fall back to local HEAD (e.g. test fixtures).
func TestResolvePolecatBaseRef_NoOrigin(t *testing.T) {
	workDir := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "main", workDir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if got := resolvePolecatBaseRef(workDir, "main"); got != "" {
		t.Fatalf("resolvePolecatBaseRef = %q, want empty (no origin)", got)
	}
}
