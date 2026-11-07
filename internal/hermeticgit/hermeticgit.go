// Package hermeticgit lets a test ask git about a directory tree without
// depending on that tree being a git work tree.
//
// A checkout of this repository is not always a git work tree: a tarball or an
// export (`git archive HEAD | tar -x`) has the tracked files and no .git. A
// test that shells out to git against the checkout then fails with "not a git
// repository" for a reason that has nothing to do with what it tests.
//
// WorkTreeEnv gives git a throwaway bare repository of its own via GIT_DIR and
// points GIT_WORK_TREE at the real tree, so git treats the tree as a work tree
// without anything being written into it. It was first written for
// cmd/pogo's TestCommitMsgHookVerdicts (mg-01dfe) and lifted here so the next
// test with the same need reuses it rather than writing a second one.
//
// This package has no tests of its own: WorkTreeEnv carries its positive
// control inline and fails the calling test if the override does not resolve
// to the expected root.
package hermeticgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// WorkTreeEnv returns environment overrides that make workTree a git work tree
// backed by a fresh, empty bare repository in a temp dir, without touching
// workTree itself. Append them last to a command's environment so they override
// the inherited one. The repository's index starts empty: `git ls-files` lists
// nothing until something is added.
func WorkTreeEnv(t testing.TB, workTree string) []string {
	t.Helper()
	gitDir := filepath.Join(t.TempDir(), "repo.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", gitDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
	env := []string{
		"GIT_DIR=" + gitDir,
		"GIT_WORK_TREE=" + workTree,
		"GIT_CEILING_DIRECTORIES=",
	}
	// Positive control: the override must actually yield a work tree rooted
	// at workTree, or the caller fails for a reason its test does not cover.
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = workTree
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hermetic work tree setup: git rev-parse failed: %v (%s)", err, out)
	}
	if got, want := SamePath(strings.TrimSpace(string(out))), SamePath(workTree); got != want {
		t.Fatalf("hermetic work tree resolves to %q, want %q", got, want)
	}
	return env
}

// SamePath returns p with symlinks resolved (macOS's /var -> /private/var,
// for one), so two spellings of one directory compare equal. It returns p
// unchanged if it cannot be resolved.
func SamePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
