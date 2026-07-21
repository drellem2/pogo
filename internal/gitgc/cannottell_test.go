package gitgc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// damageGitPointer makes `git status` fail inside wtPath FOR REAL — no stub,
// no fake — by corrupting the worktree's .git pointer file so it names a
// gitdir that does not exist.
//
// This is deliberately NOT the same damage as
// TestWorktreeDirtyUnclassifiableProceeds, which DELETES .git and so produces
// the pre-gh#88 "stripped pointer" shape. That shape is the one mg-ee02's doc
// comment described. The whole point of mg-4d45 is that the predicate admitted
// a much wider population than that comment claimed, so the control has to
// exercise a member of the population the comment did NOT cover: a tree that
// is still a worktree, still has a .git, and still fails.
func damageGitPointer(t *testing.T, wtPath string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wtPath, ".git"), []byte("gitdir: /nonexistent/garbage\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WorktreeDirty(wtPath); err == nil {
		t.Fatal("setup did not produce a genuine git status failure; this is not the cannot-tell case")
	}
}

// TestCannotTellRefusedWhenOwnerUnproven is the mg-4d45 positive control.
//
// Before this fix, a genuine `git status` failure returned nil from
// RemoveWorktree and the files were gone — verified by running exactly this
// setup against the pre-fix code, which reported `RemoveWorktree returned:
// <nil>` and `stat irreplaceable.go: no such file or directory`.
//
// The correlation is what makes this the worst arm to fail open on: status
// fails precisely when .git is damaged, which is when the working files are
// least recoverable.
func TestCannotTellRefusedWhenOwnerUnproven(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-damaged")
	wtPath := r.worktree("polecat-damaged")
	precious := dirty(t, wtPath, "irreplaceable.go", "package wip // the only copy\n")

	damageGitPointer(t, wtPath)

	err := RemoveWorktree(r.dir, wtPath, OwnerUnproven)
	if err == nil {
		t.Fatal("cannot-tell with an unproven owner must refuse; RemoveWorktree returned nil")
	}

	// The refusal must name the STATUS FAILURE as the reason. Reporting this
	// as "dirty" would be a different and false claim: we never established
	// that there is uncommitted work here, only that we could not look.
	var uwe *UndeterminedWorktreeError
	if !errors.As(err, &uwe) {
		t.Fatalf("want *UndeterminedWorktreeError, got %T: %v", err, err)
	}
	var dwe *DirtyWorktreeError
	if errors.As(err, &dwe) {
		t.Error("cannot-tell must NOT be reported as dirty — that claims knowledge we do not have")
	}
	if uwe.Path != wtPath {
		t.Errorf("UndeterminedWorktreeError.Path = %q, want %q", uwe.Path, wtPath)
	}
	if !strings.Contains(err.Error(), "cannot determine") {
		t.Errorf("refusal must say it could not determine dirtiness, got: %v", err)
	}

	// The whole point: the file survives.
	if _, serr := os.Stat(precious); serr != nil {
		t.Fatalf("THE WORK WAS DESTROYED — a tree we could not read must survive: %v", serr)
	}
	if _, serr := os.Stat(wtPath); serr != nil {
		t.Fatalf("worktree dir should survive a refused removal: %v", serr)
	}
}

// TestCannotTellReclaimedWhenOwnerGone is the other half, and it is not
// optional: a fix that only refuses converts a data-loss bug into a worktree
// leak, and unreaped worktrees are a real problem in this repo (gh #31).
//
// When liveness has been positively excluded, nobody is coming back for these
// files and the orphan is genuinely garbage.
func TestCannotTellReclaimedWhenOwnerGone(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-orphan")
	wtPath := r.worktree("polecat-orphan")
	dirty(t, wtPath, "leftover.go", "package leftover\n")

	damageGitPointer(t, wtPath)

	if err := RemoveWorktree(r.dir, wtPath, OwnerGone); err != nil {
		t.Fatalf("an orphan whose owner is gone must still be reclaimable: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be gone, stat gave: %v", err)
	}
}

// TestCleanStillReapsUnderBothOwnerships guards the regression that would
// matter most in the other direction: the common case is a clean tree at a
// polecat's exit, and it must still reap. If it stopped, every polecat exit
// would leak a worktree and pin its branch.
func TestCleanStillReapsUnderBothOwnerships(t *testing.T) {
	for _, owner := range []WorktreeOwner{OwnerUnproven, OwnerGone} {
		t.Run(owner.String(), func(t *testing.T) {
			r := newTestRepo(t)
			r.branch("polecat-clean")
			wtPath := r.worktree("polecat-clean")

			if err := RemoveWorktree(r.dir, wtPath, owner); err != nil {
				t.Fatalf("a clean worktree must still reap: %v", err)
			}
			if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
				t.Fatalf("clean worktree should be gone, stat gave: %v", err)
			}
		})
	}
}

// TestAbsentWorktreeIsNotCannotTell pins the boundary that keeps the refusal
// from over-firing. WorktreeDirty errors on a missing directory too, so a
// naive "any error refuses" would make removing an already-gone worktree fail
// — and this function is documented as safe to call when the directory, the
// registration, or both are already gone.
//
// There are no files to protect in an absent directory. "There is nothing
// here" and "there may be something here I cannot read" are different facts.
func TestAbsentWorktreeIsNotCannotTell(t *testing.T) {
	r := newTestRepo(t)
	missing := filepath.Join(r.dir, "..", "never-existed")

	for _, owner := range []WorktreeOwner{OwnerUnproven, OwnerGone} {
		if err := RemoveWorktree(r.dir, missing, owner); err != nil {
			t.Errorf("removing a nonexistent worktree (%s) should succeed: %v", owner, err)
		}
	}
}
