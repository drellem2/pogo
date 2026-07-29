package gitgc

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// damageGitPointer makes `git status` fail inside wtPath FOR REAL — no stub,
// no fake — by corrupting the worktree's .git pointer file so it names a
// gitdir that does not exist.
//
// This is deliberately NOT the same damage as
// TestWorktreeDirtyUnclassifiableIsRefused, which DELETES .git and so produces
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

// ageTree backdates every file and directory in wtPath so the tree looks
// abandoned rather than freshly written, which is what an ORPHAN actually looks
// like on disk.
//
// Nothing DECIDES on this any more — cannot-tell refuses unconditionally since
// gh #97 — so ageing a fixture never changes an outcome. It changes what the
// refusal REPORTS, which is the whole remaining job of mtime here, and a test
// asserting "untouched 30 days" needs a tree that has plausibly been sitting
// for thirty days rather than for four milliseconds.
func ageTree(t *testing.T, wtPath string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	err := filepath.WalkDir(wtPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, when, when)
	})
	if err != nil {
		t.Fatalf("ageTree %s: %v", wtPath, err)
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

// TestCannotTellRefusedUnderEVERYOwnership is what mg-4d45's
// TestCannotTellReclaimedWhenOwnerGone became (gh #97).
//
// That test asserted the other half of mg-4d45's design: an unreadable tree
// whose owner was PROVABLY DEAD was reclaimed, on the reasoning that nobody is
// coming back for an orphan's files and that leaking worktrees is its own
// defect (gh #31). The reasoning was sound and the arm is gone anyway, because
// the evidence it rested on does not survive contact with the case it governs:
// death evidence is exactly what a recent write contradicts, and a veto built
// to catch that contradiction can only expire, never resolve it.
//
// So this now asserts the inverse, and asserts it across BOTH ownerships in one
// loop, because that is the point: ownership no longer discriminates here. If a
// future change re-introduces an OwnerGone arm on the cannot-tell path, this
// goes red rather than silently permitting it.
//
// The cost is real and is accepted: the gh #31 orphan this used to reclaim is
// now pinned until a human clears it or --force takes it.
func TestCannotTellRefusedUnderEVERYOwnership(t *testing.T) {
	for _, owner := range []WorktreeOwner{OwnerUnproven, OwnerGone} {
		t.Run(owner.String(), func(t *testing.T) {
			r := newTestRepo(t)
			r.branch("polecat-orphan")
			wtPath := r.worktree("polecat-orphan")
			leftover := dirty(t, wtPath, "leftover.go", "package leftover\n")

			damageGitPointer(t, wtPath)
			// Aged well past any window a veto could ever have used, so the
			// refusal cannot be mistaken for "it was recently written".
			ageTree(t, wtPath, 30*24*time.Hour)

			err := RemoveWorktree(r.dir, wtPath, owner)
			if err == nil {
				t.Fatal("a tree we could not read must be refused under EVERY ownership; got nil")
			}
			var uwe *UndeterminedWorktreeError
			if !errors.As(err, &uwe) {
				t.Fatalf("want *UndeterminedWorktreeError, got %T: %v", err, err)
			}
			// The age is REPORTED. It is the only thing a human has to decide
			// the pin with, since nothing will ever clear it automatically.
			if !uwe.UntouchedKnown {
				t.Error("the age must be measured and reported on a permanent refusal")
			}
			if !strings.Contains(err.Error(), "untouched 30 days") {
				t.Errorf("refusal must report how long the tree has been untouched, got: %v", err)
			}
			if _, serr := os.Stat(leftover); serr != nil {
				t.Fatalf("THE WORK WAS DESTROYED under %s: %v", owner, serr)
			}
		})
	}
}

// TestCleanStillReapsUnderBothOwnerships guards the regression that would
// matter most in the other direction: the common case is a clean tree at a
// polecat's exit, and it must still reap. If it stopped, every polecat exit
// would leak a worktree and pin its branch.
//
// It loops both ownerships for the same reason the refusal test does: ownership
// discriminates nothing, so OwnerGone is dormant API rather than a live input
// (see WorktreeOwner). Naming it here is a control against an arm coming back,
// not a claim that it selects anything.
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
//
// Both ownerships again, and again because neither is read: OwnerGone appears
// here as dormant API under test, not as a governing value (see WorktreeOwner).
func TestAbsentWorktreeIsNotCannotTell(t *testing.T) {
	r := newTestRepo(t)
	missing := filepath.Join(r.dir, "..", "never-existed")

	for _, owner := range []WorktreeOwner{OwnerUnproven, OwnerGone} {
		if err := RemoveWorktree(r.dir, missing, owner); err != nil {
			t.Errorf("removing a nonexistent worktree (%s) should succeed: %v", owner, err)
		}
	}
}
