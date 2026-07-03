package search

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/pkg/plugin"
)

func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "user.name=pogo-test", "-c", "user.email=pogo-test@example.com"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func readExclude(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("reading info/exclude: %v", err)
	}
	return string(data)
}

func TestEnsurePogoGitExcludedAppendsPattern(t *testing.T) {
	gitOrSkip(t)
	repo := t.TempDir()
	runGit(t, repo, "init")

	ensurePogoGitExcluded(repo)

	if got := readExclude(t, repo); !strings.Contains(got, "\n"+pogoExcludePattern+"\n") &&
		!strings.HasPrefix(got, pogoExcludePattern+"\n") {
		t.Errorf("info/exclude missing %q pattern:\n%s", pogoExcludePattern, got)
	}
}

func TestEnsurePogoGitExcludedSkipsWhenAlreadyIgnored(t *testing.T) {
	gitOrSkip(t)
	for _, existing := range []string{".pogo", ".pogo/", "/.pogo", "/.pogo/"} {
		repo := t.TempDir()
		runGit(t, repo, "init")
		excludePath := filepath.Join(repo, ".git", "info", "exclude")
		if err := os.WriteFile(excludePath, []byte(existing+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ensurePogoGitExcluded(repo)

		if got := readExclude(t, repo); got != existing+"\n" {
			t.Errorf("exclude with existing %q line was modified:\n%s", existing, got)
		}
	}
}

func TestEnsurePogoGitExcludedOncePerProcess(t *testing.T) {
	gitOrSkip(t)
	repo := t.TempDir()
	runGit(t, repo, "init")

	ensurePogoGitExcluded(repo)
	first := readExclude(t, repo)
	ensurePogoGitExcluded(repo)

	if got := readExclude(t, repo); got != first {
		t.Errorf("second call modified info/exclude:\nfirst:\n%s\nsecond:\n%s", first, got)
	}
	if strings.Count(readExclude(t, repo), pogoExcludePattern) != 1 {
		t.Errorf("pattern appended more than once:\n%s", readExclude(t, repo))
	}
}

func TestEnsurePogoGitExcludedHandlesMissingTrailingNewline(t *testing.T) {
	gitOrSkip(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	excludePath := filepath.Join(repo, ".git", "info", "exclude")
	if err := os.WriteFile(excludePath, []byte("*.tmp"), 0644); err != nil {
		t.Fatal(err)
	}

	ensurePogoGitExcluded(repo)

	got := readExclude(t, repo)
	if !strings.HasPrefix(got, "*.tmp\n") {
		t.Errorf("existing pattern corrupted by append:\n%s", got)
	}
	if !strings.Contains(got, "\n"+pogoExcludePattern+"\n") {
		t.Errorf("info/exclude missing %q pattern:\n%s", pogoExcludePattern, got)
	}
}

func TestEnsurePogoGitExcludedIgnoresNonGitDirs(t *testing.T) {
	dir := t.TempDir()

	ensurePogoGitExcluded(dir)

	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git created in non-git dir (err=%v)", err)
	}
}

// A project dir nested inside some other repo must not edit the outer repo's
// exclude file — only true repo roots qualify.
func TestEnsurePogoGitExcludedIgnoresNestedNonRootDirs(t *testing.T) {
	gitOrSkip(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	nested := filepath.Join(repo, "sub", "project")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}

	ensurePogoGitExcluded(nested)

	if data, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); err == nil {
		if strings.Contains(string(data), pogoExcludePattern+"\n") {
			t.Errorf("outer repo's info/exclude was modified for a nested dir:\n%s", data)
		}
	}
}

// An invalid .git entry (empty dir, like the _testdata fixtures) makes git
// resolve upward to an enclosing repo; that outer repo's exclude file must
// stay untouched.
func TestEnsurePogoGitExcludedIgnoresInvalidDotGit(t *testing.T) {
	gitOrSkip(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	fake := filepath.Join(repo, "sub", "project")
	if err := os.MkdirAll(filepath.Join(fake, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	ensurePogoGitExcluded(fake)

	if data, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); err == nil {
		if strings.Contains(string(data), pogoExcludePattern+"\n") {
			t.Errorf("outer repo's info/exclude was modified for an invalid nested .git:\n%s", data)
		}
	}
	if _, err := os.Stat(filepath.Join(fake, ".git", "info", "exclude")); !os.IsNotExist(err) {
		t.Errorf("exclude file created inside invalid .git dir (err=%v)", err)
	}
}

// Linked worktrees share one info/exclude in the common git dir; the pattern
// must land there so the worktree's git status is clean too.
func TestEnsurePogoGitExcludedResolvesLinkedWorktree(t *testing.T) {
	gitOrSkip(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "f.txt")
	runGit(t, repo, "commit", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", wt)

	ensurePogoGitExcluded(wt)

	if got := readExclude(t, repo); !strings.Contains(got, pogoExcludePattern+"\n") {
		t.Errorf("shared info/exclude missing %q after worktree index:\n%s", pogoExcludePattern, got)
	}
}

// End-to-end through the indexing entry point: indexing a real git repo must
// leave `git status` clean — the .pogo/ dir it writes is excluded on the spot.
func TestIndexExcludesPogoDirFromGitStatus(t *testing.T) {
	gitOrSkip(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "main.go")
	runGit(t, repo, "commit", "-m", "init")

	basicSearch := createBasicSearch()
	root, err := absolute(repo)
	if err != nil {
		t.Fatal(err)
	}
	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	basicSearch.Index(&req)
	time.Sleep(1 * time.Second)

	if _, err := os.Stat(filepath.Join(repo, ".pogo", "search")); err != nil {
		t.Fatalf("index did not create .pogo/search: %v", err)
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.Contains(string(out), ".pogo") {
		t.Errorf(".pogo shows up in git status after indexing:\n%s", out)
	}
}
