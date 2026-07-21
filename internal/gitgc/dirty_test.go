package gitgc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dirty writes an uncommitted file into a worktree and returns its path.
func dirty(t *testing.T, wtPath, name, content string) string {
	t.Helper()
	p := filepath.Join(wtPath, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRemoveWorktreeRefusesDirtyUntracked is the positive control for mg-ee02,
// in the exact shape that motivated it: the work lost on polecat ea45 was a
// NEW, untracked file (a 201-line trust_hook_race_test.go). A dirtiness check
// that only looked at tracked modifications would have sailed straight past
// the one file the ticket exists because of, so this control leads with the
// untracked case and the tracked case follows below.
//
// On the pre-fix code this test FAILS: `git worktree remove --force` overrides
// git's own refusal, and the unconditional os.RemoveAll behind it finishes the
// job even when git declines.
func TestRemoveWorktreeRefusesDirtyUntracked(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-wip")
	wtPath := r.worktree("polecat-wip")

	racetest := dirty(t, wtPath, "trust_hook_race_test.go", "package main // 201 lines of irreplaceable\n")

	err := RemoveWorktree(r.dir, wtPath)
	if err == nil {
		t.Fatal("RemoveWorktree must refuse a worktree with uncommitted work; it returned nil")
	}

	// The refusal must be typed, so callers can distinguish "there was work
	// here" from "the disk is broken" and report accordingly.
	var dwe *DirtyWorktreeError
	if !errors.As(err, &dwe) {
		t.Fatalf("want a *DirtyWorktreeError, got %T: %v", err, err)
	}

	// And it must NAME what is uncommitted — an operator who cannot see what
	// was preserved cannot decide whether to rescue it.
	if !strings.Contains(err.Error(), "trust_hook_race_test.go") {
		t.Errorf("refusal must name the uncommitted file, got: %v", err)
	}
	if dwe.Path != wtPath {
		t.Errorf("DirtyWorktreeError.Path = %q, want %q", dwe.Path, wtPath)
	}

	// The whole point: the file survives.
	if _, err := os.Stat(racetest); err != nil {
		t.Fatalf("THE WORK WAS DESTROYED — uncommitted file should survive a refused removal: %v", err)
	}
	// The directory itself survives too.
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree dir should survive a refused removal: %v", err)
	}
	// And the registration is intact, so the operator can still use git to
	// inspect and rescue the tree rather than having to hand-reattach it.
	wts, _ := ListWorktrees(r.dir)
	found := false
	for _, wt := range wts {
		if wt.Path == wtPath {
			found = true
		}
	}
	if !found {
		t.Error("registration should survive a refused removal so the tree stays inspectable")
	}
}

// TestRemoveWorktreeRefusesDirtyTracked covers the other half of dirty:
// modifications to files git already tracks.
func TestRemoveWorktreeRefusesDirtyTracked(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-mod")
	wtPath := r.worktree("polecat-mod")

	seed := filepath.Join(wtPath, "seed.txt")
	if err := os.WriteFile(seed, []byte("modified, uncommitted"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveWorktree(r.dir, wtPath); err == nil {
		t.Fatal("RemoveWorktree must refuse a worktree with modified tracked files")
	}
	got, err := os.ReadFile(seed)
	if err != nil {
		t.Fatalf("modified file should survive: %v", err)
	}
	if string(got) != "modified, uncommitted" {
		t.Errorf("modification should survive intact, got %q", got)
	}
}

// TestRemoveWorktreeRefusalSurvivesTheRemoveAllPath is the control the mayor
// flagged as the one a naive repair will not pass.
//
// There are two destructive steps stacked in RemoveWorktree, and the second is
// unconditional: `git worktree remove` runs first, and os.RemoveAll runs
// afterwards WHETHER OR NOT git succeeded — with git's error deliberately
// discarded. Simply dropping the --force flag restores git's refusal and
// changes nothing observable, because RemoveAll still deletes the directory.
//
// This test pins the RemoveAll path specifically: git is made to refuse
// independently (the worktree is dirty, and we assert git alone declines), and
// the assertion is that the directory is STILL THERE afterwards. A fix that
// only removes --force compiles, reads correctly, and fails right here.
func TestRemoveWorktreeRefusalSurvivesTheRemoveAllPath(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-removeall")
	wtPath := r.worktree("polecat-removeall")
	keep := dirty(t, wtPath, "wip.go", "package wip\n")

	// Establish that git ITSELF refuses this removal without --force. This is
	// the guard the production code opted out of; asserting it here means the
	// test proves the fix restores a real guarantee rather than inventing one.
	out, gerr := git(r.dir, "worktree", "remove", wtPath)
	if gerr == nil {
		t.Fatalf("expected git to refuse removing a dirty worktree; it succeeded: %s", out)
	}

	if err := RemoveWorktree(r.dir, wtPath); err == nil {
		t.Fatal("RemoveWorktree must refuse")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("os.RemoveAll ran anyway and destroyed the work: %v", err)
	}
}

// TestRemoveWorktreeStillReapsClean is the negative control. A fix that
// converts a data-loss bug into a worktree leak is a different defect, not a
// fix — reaping a finished polecat must keep working exactly as before.
func TestRemoveWorktreeStillReapsClean(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-done")
	wtPath := r.worktree("polecat-done")

	if err := RemoveWorktree(r.dir, wtPath); err != nil {
		t.Fatalf("a clean worktree must still be reaped: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("clean worktree dir should be gone, stat err = %v", err)
	}
	// The registration must go too, or the branch stays pinned and the
	// branch-deletion phase leaks (TestRemoveWorktreeFreesCheckedOutBranch).
	wts, _ := ListWorktrees(r.dir)
	for _, wt := range wts {
		if wt.Path == wtPath {
			t.Errorf("registration should be gone: %s", wt.Path)
		}
	}
}

// TestRemoveWorktreeIgnoresIgnoredFiles: build artifacts are not work. A
// polecat worktree routinely accumulates gitignored output (./bin, coverage
// files); if those counted as dirty, every polecat would refuse to reap and
// the fix would leak worktrees universally — the failure mode that makes this
// dangerous, because it would look like it was working.
func TestRemoveWorktreeIgnoresIgnoredFiles(t *testing.T) {
	r := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(r.dir, ".gitignore"), []byte("bin/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r.git("add", ".gitignore")
	r.git("commit", "-q", "-m", "ignore bin")
	r.branch("polecat-artifacts")
	wtPath := r.worktree("polecat-artifacts")

	if err := os.MkdirAll(filepath.Join(wtPath, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	dirty(t, wtPath, "bin/pogod", "ELF-ish\n")

	if err := RemoveWorktree(r.dir, wtPath); err != nil {
		t.Fatalf("gitignored build output must not block reaping: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree should be reaped, stat err = %v", err)
	}
}

// TestRemoveWorktreeForceOverrides: the operator's escape hatch. Preservation
// without a way to reclaim is an unbounded leak, so the refusal must be
// overridable — deliberately, by a caller that has said so in as many words.
func TestRemoveWorktreeForceOverrides(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-forced")
	wtPath := r.worktree("polecat-forced")
	dirty(t, wtPath, "wip.go", "package wip\n")

	if err := RemoveWorktreeForce(r.dir, wtPath); err != nil {
		t.Fatalf("RemoveWorktreeForce: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("force must reclaim a dirty worktree, stat err = %v", err)
	}
}

// TestRemoveWorktreeIdempotentOnMissingDir: removal of an already-gone
// worktree stays a no-op success. The dirty check must not turn "nothing to
// do" into an error — the exit hook calls this on every polecat exit,
// including ones whose tree is already gone.
func TestRemoveWorktreeIdempotentOnMissingDir(t *testing.T) {
	r := newTestRepo(t)
	if err := RemoveWorktree(r.dir, filepath.Join(r.dir, "..", "never-existed")); err != nil {
		t.Errorf("removing a nonexistent worktree should succeed: %v", err)
	}
	if err := RemoveWorktree(r.dir, ""); err != nil {
		t.Errorf("empty worktree dir should be a no-op: %v", err)
	}
}

// TestWorktreeDirtyUnclassifiableProceeds pins the one hole this fix leaves
// open, so it is a documented decision rather than a surprise.
//
// A legacy worktree whose .git pointer was stripped (the pre-gh#88 unlink
// shape) cannot be asked whether it is dirty — `git status` fails outright.
// Such a directory is treated as NOT dirty and is reclaimed, because the
// alternative regresses gh #31: those orphans would accumulate forever with
// nothing able to prove them clean. The exposure is real but bounded — it
// applies only to worktrees git can no longer see.
func TestWorktreeDirtyUnclassifiableProceeds(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-unlinked")
	wtPath := r.worktree("polecat-unlinked")
	dirty(t, wtPath, "wip.go", "package wip\n")

	// Strip the .git pointer — now it is just a directory of files.
	if err := os.RemoveAll(filepath.Join(wtPath, ".git")); err != nil {
		t.Fatal(err)
	}

	isDirty, _, err := WorktreeDirty(wtPath)
	if err == nil {
		t.Error("expected WorktreeDirty to report that it could not classify an unlinked worktree")
	}
	if isDirty {
		t.Error("an unclassifiable worktree must not be reported dirty")
	}
	if err := RemoveWorktree(r.dir, wtPath); err != nil {
		t.Errorf("an unclassifiable worktree must still be reclaimable: %v", err)
	}
}

// TestSweepKeepsDirtyWorktree: the GC sweep gets the same protection as the
// exit hook, and for the same reason. A concluded ticket certifies that the
// WORK was accepted — it says nothing about files sitting uncommitted in the
// tree, which are unmerged by definition. Without this, the exit-hook fix
// would be undone an hour later by the periodic sweep.
func TestSweepKeepsDirtyWorktree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-arch")
	wt := r.worktree("polecat-arch")
	keep := dirty(t, wt, "unfinished.go", "package unfinished\n")

	res, err := Sweep(Options{
		Repo:    r.dir,
		Tickets: TicketIndex{"mg-arch": TicketArchived},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("sweep destroyed uncommitted work: %v", err)
	}
	if len(res.WorktreesRemoved) != 0 {
		t.Errorf("dirty worktree should not be removed, got %+v", res.WorktreesRemoved)
	}
	// It must be REPORTED as kept, not silently skipped — silent preservation
	// is how worktrees accumulate unnoticed, which is the cost of choosing
	// preservation over refusal.
	var kept *WorktreeAction
	for i := range res.WorktreesKept {
		if res.WorktreesKept[i].Path == wt {
			kept = &res.WorktreesKept[i]
		}
	}
	if kept == nil {
		t.Fatalf("dirty worktree must be reported as kept, got %+v", res.WorktreesKept)
	}
	if !strings.Contains(kept.Reason, "uncommitted") {
		t.Errorf("kept reason should explain uncommitted work, got %q", kept.Reason)
	}
	// Its branch stays pinned, because the worktree still holds it. Asserting
	// this keeps the trade-off honest rather than implicit.
	branches, _ := ListPolecatBranches(r.dir)
	found := false
	for _, b := range branches {
		if b == "polecat-arch" {
			found = true
		}
	}
	if !found {
		t.Error("branch of a kept worktree must not be deleted")
	}
}

// TestSweepForceReclaimsDirtyWorktree: the operator's deliberate override, and
// the answer to "preserved trees accumulate forever".
func TestSweepForceReclaimsDirtyWorktree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-arch")
	wt := r.worktree("polecat-arch")
	dirty(t, wt, "unfinished.go", "package unfinished\n")

	res, err := Sweep(Options{
		Repo:    r.dir,
		Tickets: TicketIndex{"mg-arch": TicketArchived},
		Force:   true,
	})
	if err != nil {
		t.Fatalf("Sweep --force: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("--force must reclaim the dirty worktree, stat err = %v", err)
	}
	if len(res.WorktreesRemoved) != 1 {
		t.Errorf("want 1 worktree removed under --force, got %+v", res.WorktreesRemoved)
	}
}

// TestSweepStillReapsCleanWorktree is the negative control at the sweep layer:
// ordinary GC of a finished polecat is unchanged.
func TestSweepStillReapsCleanWorktree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-arch")
	wt := r.worktree("polecat-arch")

	res, err := Sweep(Options{
		Repo:    r.dir,
		Tickets: TicketIndex{"mg-arch": TicketArchived},
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("clean worktree should still be reaped, stat err = %v", err)
	}
	if len(res.WorktreesRemoved) != 1 {
		t.Errorf("want 1 worktree removed, got %+v", res.WorktreesRemoved)
	}
}
