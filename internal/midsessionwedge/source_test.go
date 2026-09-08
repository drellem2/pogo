package midsessionwedge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGitDirFollowsTheLinkedWorktreePointer is the one that would fail silently.
//
// Every polecat runs in a LINKED worktree, where `.git` is a FILE holding
// `gitdir: <path>`, not a directory. A probe that stats <worktree>/.git/HEAD
// finds nothing, reports no movement for every polecat on the machine, and
// looks exactly like a fleet that never commits — so the exonerating clause
// would quietly never fire, on precisely the population it was written for.
func TestGitDirFollowsTheLinkedWorktreePointer(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "repo", ".git", "worktrees", "polecat-x")
	if err := os.MkdirAll(filepath.Join(real, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+real+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitDir(wt); got != real {
		t.Fatalf("gitDir = %q, want %q — a linked worktree's .git is a file, and not "+
			"following it makes every polecat read as never having moved", got, real)
	}
}

func TestGitDirAcceptsAPlainRepository(t *testing.T) {
	root := t.TempDir()
	g := filepath.Join(root, ".git")
	if err := os.MkdirAll(g, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitDir(root); got != g {
		t.Errorf("gitDir = %q, want %q", got, g)
	}
}

func TestGitDirOnANonWorktreeIsEmpty(t *testing.T) {
	if got := gitDir(t.TempDir()); got != "" {
		t.Errorf("gitDir on a directory with no .git = %q, want empty", got)
	}
}

// TestWorktreeMovedReadsTheGitMetadata: a commit rewrites HEAD and appends
// logs/HEAD, and that must register as movement.
func TestWorktreeMovedReadsTheGitMetadata(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "gitdir")
	if err := os.MkdirAll(filepath.Join(real, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+real), 0o644); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-time.Hour)
	head := filepath.Join(real, "logs", "HEAD")
	if err := os.WriteFile(head, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{wt, real, head} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	since := time.Now().Add(-30 * time.Minute)
	moved, at, ok := worktreeMoved(wt, since)
	if !ok {
		t.Fatal("worktreeMoved reported no worktree for a directory that has one")
	}
	if moved {
		t.Errorf("reported movement at %s for metadata an hour older than the quiet run", at)
	}

	fresh := time.Now()
	if err := os.Chtimes(head, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	moved, at, ok = worktreeMoved(wt, since)
	if !ok || !moved {
		t.Fatalf("a commit inside the quiet run did not register: moved=%v at=%s ok=%v", moved, at, ok)
	}
}

// TestWorktreeMovedOnAMissingDirIsNotAJudgement. There is a difference between
// "did not move" and "there is nothing here to read", and only the first may
// ever clear an alarm.
func TestWorktreeMovedOnAMissingDirIsNotAJudgement(t *testing.T) {
	if _, _, ok := worktreeMoved(filepath.Join(t.TempDir(), "nope"), time.Now()); ok {
		t.Error("reported a readable worktree for a path that does not exist")
	}
	if _, _, ok := worktreeMoved("", time.Now()); ok {
		t.Error("reported a readable worktree for an empty path")
	}
}

// TestNilRegistryIsAnErrorNotAnEmptyFleet. "Nothing to judge" and "could not
// judge" must not be the same reading — the failure this whole lineage exists
// to stop.
func TestNilRegistryIsAnErrorNotAnEmptyFleet(t *testing.T) {
	if _, err := RegistrySource(nil)(time.Now()); err == nil {
		t.Error("RegistrySource(nil) returned no error; an unreadable fleet must not read as an empty one")
	}
	if err := RegistryRecover(nil)("x"); err == nil {
		t.Error("RegistryRecover(nil) returned no error")
	}
	if _, err := RegistrySubmits(nil)("x"); err == nil {
		t.Error("RegistrySubmits(nil) returned no error")
	}
	if _, ok := RegistryWorktree(nil)("x", time.Now()); ok {
		t.Error("RegistryWorktree(nil) claimed a readable worktree")
	}
}
