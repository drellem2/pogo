package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
}

// TestRepoIsPogoHome pins mg-752a3's comparison: every spelling of the live
// tree matches, and nothing else does — in particular not a separate repo that
// merely LIVES under $POGO_HOME, which is where every polecat worktree of
// another repo sits ($POGO_HOME/polecats/<name>).
func TestRepoIsPogoHome(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	// t.TempDir is under /var/folders on macOS, itself a symlink to
	// /private/var, so the unresolved spelling is exercised on every arm.
	root := t.TempDir()
	home := filepath.Join(root, "home")
	pogoHome := filepath.Join(home, ".pogo")
	initRepo(t, pogoHome)
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", pogoHome)

	link := filepath.Join(root, "pogo-link")
	if err := os.Symlink(pogoHome, link); err != nil {
		t.Fatal(err)
	}
	// A linked worktree of the live repo shares its origin.
	liveWorktree := filepath.Join(root, "live-wt")
	gitIn(t, pogoHome, "worktree", "add", "-q", "-b", "side", liveWorktree)

	// A different repo, and a worktree of it placed UNDER $POGO_HOME the way
	// spawn-polecat places every polecat tree.
	sibling := filepath.Join(root, "dev", "pogo")
	initRepo(t, sibling)
	polecatTree := filepath.Join(pogoHome, "polecats", "pabc")
	gitIn(t, sibling, "worktree", "add", "-q", "-b", "polecat-pabc", polecatTree)

	cwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(cwd) })
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, repo string
		want       bool
	}{
		{"exact", pogoHome, true},
		{"trailing slash", pogoHome + "/", true},
		{"tilde", "~/.pogo", true},
		{"relative", ".pogo", true},
		{"symlink", link, true},
		{"linked worktree of the live repo", liveWorktree, true},
		{"subdirectory of the live tree", filepath.Join(pogoHome, "polecats"), true},
		// Positive control for the negatives: the same machinery answers
		// false for a repo that is not the live tree.
		{"sibling repo", sibling, false},
		{"polecat worktree of another repo under $POGO_HOME", polecatTree, false},
		{"nonexistent path", filepath.Join(root, "nope"), false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, resolved := RepoIsPogoHome(tc.repo)
			if got != tc.want {
				t.Errorf("RepoIsPogoHome(%q) = %v (compared against %s), want %v", tc.repo, got, resolved, tc.want)
			}
		})
	}
}
