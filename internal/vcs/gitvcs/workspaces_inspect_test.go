package gitvcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// The callers' existing tests (gitgc, strandedwork, cmd/pogo) pin what these
// methods do for them and were not edited by slice 3b (mg-afad2). These pin the
// backend's own contract against real git.

// realpath resolves macOS's /var -> /private/var, which git reports resolved.
func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// linkedWorktree adds a linked worktree on a new branch to clone.
func linkedWorktree(t *testing.T, clone, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	git(t, clone, "worktree", "add", "-b", "polecat-"+name, dir)
	return dir
}

func TestWorkspacesDirty(t *testing.T) {
	_, clone := fixture(t, false)
	ctx := context.Background()
	ws := Workspaces{}
	dir := linkedWorktree(t, clone, "d1")

	// pogod's own artefacts alone: clean. The .claude/ directory is wholly
	// untracked, so git collapses it to `?? .claude/` and Dirty must open it.
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, ".claude/settings.local.json", "{}")
	write(t, dir, ".claude/pogo-mailhook.stamp", "")
	files, err := ws.Dirty(ctx, dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("Dirty with only pogod's artefacts = %q, %v; want clean", files, err)
	}

	// Positive control: a real untracked file under .claude/, a new file, and a
	// modified tracked file are all work.
	write(t, dir, ".claude/notes.md", "x")
	write(t, dir, "new.txt", "x")
	write(t, dir, "f", "edited\n")
	files, err = ws.Dirty(ctx, dir)
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	want := []string{"M f", "?? .claude/notes.md", "?? new.txt"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("Dirty = %q, want %q", files, want)
	}

	// Cannot-tell is an error, never clean.
	if _, err := ws.Dirty(ctx, filepath.Join(dir, "absent")); err == nil {
		t.Fatal("Dirty on an absent directory returned no error")
	}
	if _, err := ws.Dirty(ctx, ""); err == nil {
		t.Fatal("Dirty(\"\") returned no error")
	}
}

func TestWorkspacesDirtyUsesInjectedRunner(t *testing.T) {
	_, clone := fixture(t, false)
	var calls [][]string
	ws := Workspaces{RunSplit: func(name string, args ...string) ([]byte, []byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return RunSplit(time.Nanosecond, name, args...)
	}}
	_, err := ws.Dirty(context.Background(), clone)
	if !errors.Is(err, ErrCommandBudget) {
		t.Fatalf("Dirty under a 1ns budget = %v, want ErrCommandBudget", err)
	}
	want := [][]string{{"git", "-C", clone, "status", "--porcelain"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("runner saw %q, want %q", calls, want)
	}
}

func TestWorkspacesDescribe(t *testing.T) {
	_, clone := fixture(t, false)
	ctx := context.Background()
	ws := Workspaces{}
	dir := linkedWorktree(t, clone, "d2")

	got, err := ws.Describe(ctx, dir)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got.Dir != dir || got.Branch != "polecat-d2" || realpath(t, got.Repo.Path) != realpath(t, clone) {
		t.Fatalf("Describe = %+v, want Dir %s, Branch polecat-d2, Repo %s", got, dir, clone)
	}

	// Detached HEAD: no branch, and not an error.
	git(t, dir, "checkout", "--detach")
	got, err = ws.Describe(ctx, dir)
	if err != nil || got.Branch != "" || got.Repo.Path == "" {
		t.Fatalf("Describe on a detached HEAD = %+v, %v; want empty Branch, named Repo, no error", got, err)
	}

	// Not a worktree: git's raw error.
	plain := t.TempDir()
	if _, err := ws.Describe(ctx, plain); err == nil {
		t.Fatal("Describe on a plain directory returned no error")
	} else if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("Describe on a plain directory = %T (%v), want git's raw *exec.ExitError", err, err)
	}

	// A bare repo answers but has no conventional .git to name a repo by.
	bare := filepath.Join(t.TempDir(), "b.git")
	git(t, t.TempDir(), "init", "--bare", bare)
	_, err = ws.Describe(ctx, bare)
	var unnamed *vcs.UnnamedRepoError
	if !errors.As(err, &unnamed) || !strings.Contains(err.Error(), "is not a conventional worktree layout") {
		t.Fatalf("Describe on a bare repo = %v, want *vcs.UnnamedRepoError", err)
	}
}

func TestWorkspacesRoot(t *testing.T) {
	_, clone := fixture(t, false)
	ctx := context.Background()
	ws := Workspaces{}
	sub := filepath.Join(clone, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ws.Root(ctx, sub)
	if err != nil || realpath(t, got) != realpath(t, clone) {
		t.Fatalf("Root(%s) = %q, %v; want %s", sub, got, err, clone)
	}
	plain := t.TempDir()
	_, err = ws.Root(ctx, plain)
	if err == nil || !strings.Contains(err.Error(), plain+" is not inside a git worktree: fatal: not a git repository") {
		t.Fatalf("Root on a plain directory = %v", err)
	}
}

func TestWorkspacesRemove(t *testing.T) {
	_, clone := fixture(t, false)
	ctx := context.Background()
	ws := Workspaces{}
	repo := vcs.Repo{Path: clone}
	dir := linkedWorktree(t, clone, "d3")
	write(t, dir, "dirty.txt", "x") // --force removes a dirty tree too

	if err := ws.Remove(ctx, repo, dir); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Remove left %s behind (stat: %v)", dir, err)
	}
	if list := git(t, clone, "worktree", "list"); strings.Contains(list, "d3") {
		t.Fatalf("Remove left the registration: %s", list)
	}

	// A second Remove has nothing to remove: a *vcs.CommandError with git's text.
	err := ws.Remove(ctx, repo, dir)
	var ce *vcs.CommandError
	if !errors.As(err, &ce) || len(ce.Output) == 0 {
		t.Fatalf("Remove of a gone worktree = %v, want *vcs.CommandError carrying git's output", err)
	}
}

func TestWorkFetch(t *testing.T) {
	origin, clone := fixture(t, false)
	ctx := context.Background()
	repo := vcs.Repo{Path: clone}

	// Positive control: a commit pushed to origin after the clone is not seen
	// until Fetch.
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", origin, other)
	git(t, other, "checkout", "main")
	write(t, other, "h", "late\n")
	git(t, other, "add", "h")
	git(t, other, "commit", "-m", "late")
	git(t, other, "push", "origin", "main")
	tip := git(t, other, "rev-parse", "HEAD")
	if git(t, clone, "rev-parse", "origin/main") == tip {
		t.Fatal("the clone already sees the late commit, so nothing below is measured")
	}
	if err := (Work{}).Fetch(ctx, repo); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := git(t, clone, "rev-parse", "origin/main"); got != tip {
		t.Fatalf("after Fetch origin/main = %s, want %s", got, tip)
	}

	// No reachable origin: a *vcs.CommandError carrying git's output.
	git(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	err := (Work{}).Fetch(ctx, repo)
	var ce *vcs.CommandError
	if !errors.As(err, &ce) || len(ce.Output) == 0 {
		t.Fatalf("Fetch from a missing origin = %v, want *vcs.CommandError with output", err)
	}
}
