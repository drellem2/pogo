package gitgc

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestPolecatTreeReposNamesEachTreesOwnRepository is drellem2/pogo#162's
// second cause. pogod's sweep set came from the registry, which forgets a
// repository the moment its last polecat exits; the trees left behind still
// name their repository, and this is what reads it off them.
//
// The polecats dir holds the population production holds: linked worktrees of
// two different repositories (two trees in one of them, so dedup is
// exercised), a reaped shell with no .git, a tree whose .git pointer names a
// repository that is gone, and a full clone that is its own repository.
func TestPolecatTreeReposNamesEachTreesOwnRepository(t *testing.T) {
	polecats := sharedPolecats(t)

	one := newTestRepo(t)
	one.branch("polecat-a1b2")
	one.branch("polecat-c3d4")
	addWorktree(t, one, polecats, "a1b2", "polecat-a1b2")
	addWorktree(t, one, polecats, "c3d4", "polecat-c3d4")

	two := newTestRepo(t)
	two.branch("polecat-e5f6")
	addWorktree(t, two, polecats, "e5f6", "polecat-e5f6")

	// A reaped shell: files, no .git. The orphan-dir scan's business.
	if err := os.MkdirAll(filepath.Join(polecats, "0047", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A .git pointer at a repository that no longer exists.
	broken := filepath.Join(polecats, "dead")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".git"),
		[]byte("gitdir: /nonexistent/repo/.git/worktrees/dead\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A full clone under the polecats dir: its common dir is its own .git.
	clone := filepath.Join(polecats, "clone")
	if out, err := exec.Command("git", "init", "-q", clone).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", clone, err, out)
	}

	repos, skipped, err := PolecatTreeRepos(polecats)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{one.dir, two.dir}
	sort.Strings(want)
	if !reflect.DeepEqual(repos, want) {
		t.Errorf("repos = %v, want %v — each tree must name its own repository, once", repos, want)
	}
	joined := strings.Join(skipped, "\n")
	if !strings.Contains(joined, broken) {
		t.Errorf("a tree with a .git that git would not answer for must be reported, not dropped; skipped:\n%s", joined)
	}
	if !strings.Contains(joined, clone) {
		t.Errorf("a full clone under the polecats dir is not a source repository and must be reported; skipped:\n%s", joined)
	}
	if strings.Contains(joined, "0047") {
		t.Errorf("a reaped shell with no .git is the orphan scan's business and must be skipped silently; skipped:\n%s", joined)
	}
}

// TestPolecatTreeReposMissingDirIsNoTrees: no polecats dir means no trees, not
// a failure that would stop a sweep.
func TestPolecatTreeReposMissingDirIsNoTrees(t *testing.T) {
	repos, skipped, err := PolecatTreeRepos(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(repos) != 0 || len(skipped) != 0 {
		t.Errorf("missing dir: repos=%v skipped=%v err=%v, want all empty", repos, skipped, err)
	}
}
