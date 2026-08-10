package gitgc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// TestDirtyWorktreeErrorSplitsModifiedFromUntracked is the positive control for
// mg-d45b's count half.
//
// A preserved tree reported ONE number, and the two things it fuses have
// different consequences. A modified tracked file has a committed version in the
// object store; an untracked file is on no branch, in no stash and on no remote,
// and the preserved tree is its only copy on the machine. Reporting `16` for
// both meant a reader deciding whether a preservation was urgent had to open the
// tree — the by-hand reconstruction this ticket exists to remove.
func TestDirtyWorktreeErrorSplitsModifiedFromUntracked(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-split")
	wtPath := r.worktree("polecat-split")

	// One tracked file, edited: recoverable from HEAD.
	if err := os.WriteFile(filepath.Join(wtPath, "seed.txt"), []byte("edited\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Three untracked: recoverable from nowhere.
	for _, name := range []string{"strandwatch.go", "notes.md", "wip.txt"} {
		dirty(t, wtPath, name, "package x\n")
	}

	err := RemoveWorktree(r.dir, wtPath, OwnerUnproven)
	var dwe *DirtyWorktreeError
	if !errors.As(err, &dwe) {
		t.Fatalf("want a *DirtyWorktreeError, got %T: %v", err, err)
	}

	if dwe.Total != 4 {
		t.Fatalf("Total = %d, want 4", dwe.Total)
	}
	if dwe.Modified != 1 {
		t.Errorf("Modified = %d, want 1 (seed.txt) — a tracked edit still has a "+
			"committed version, which is what makes it the LESS urgent half", dwe.Modified)
	}
	if dwe.Untracked != 3 {
		t.Errorf("Untracked = %d, want 3 — an untracked path exists on no branch, in no "+
			"stash and on no remote, so this is the count that makes a preserved tree urgent",
			dwe.Untracked)
	}
	// The invariant a consumer will rely on: the split accounts for everything.
	if dwe.Modified+dwe.Untracked != dwe.Total {
		t.Errorf("Modified(%d) + Untracked(%d) = %d, want Total %d — a split that does not "+
			"sum silently loses paths",
			dwe.Modified, dwe.Untracked, dwe.Modified+dwe.Untracked, dwe.Total)
	}
}

// TestDirtyCountsAreNotCappedWithTheFileList is the anti-regression for the way
// this fix could most plausibly reproduce the defect it fixes.
//
// `Files` is capped at dirtyFileListCap (10) for legibility. Computing the
// split from that capped slice — the obvious shortcut, and one that passes every
// small fixture — would under-report every tree with more than ten changes,
// which is a number reconstructed from a partial record: a smaller instance of
// exactly what mg-d45b is about. qbe37's real preservation had 16.
//
// So this fixture deliberately exceeds the cap in BOTH categories, and asserts
// the counts track the full list while `Files` stays capped.
func TestDirtyCountsAreNotCappedWithTheFileList(t *testing.T) {
	r := newTestRepo(t)

	// 12 tracked files, committed then edited, and 14 untracked. Both exceed
	// the 10-entry cap on their own, so a count taken from `Files` cannot
	// possibly reach either number.
	//
	// The commits land BEFORE the branch is cut, so the files are in the
	// worktree branch's HEAD and edits to them read as modifications. Committing
	// them on main afterwards leaves them absent from this branch, where git
	// correctly reports all 26 paths as untracked — which is a true answer to a
	// different question and makes the test assert nothing about the split.
	const wantModified, wantUntracked = 12, 14
	for i := 0; i < wantModified; i++ {
		r.commit("tracked"+strconv.Itoa(i)+".txt", "v1\n")
	}
	r.branch("polecat-many")
	wtPath := r.worktree("polecat-many")

	for i := 0; i < wantModified; i++ {
		name := "tracked" + strconv.Itoa(i) + ".txt"
		if err := os.WriteFile(filepath.Join(wtPath, name), []byte("v2\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < wantUntracked; i++ {
		dirty(t, wtPath, "new"+strconv.Itoa(i)+".txt", "new\n")
	}

	err := RemoveWorktree(r.dir, wtPath, OwnerUnproven)
	var dwe *DirtyWorktreeError
	if !errors.As(err, &dwe) {
		t.Fatalf("want a *DirtyWorktreeError, got %T: %v", err, err)
	}

	// The premise of the test: the list really is capped, so the counts cannot
	// have come from it.
	if len(dwe.Files) != dirtyFileListCap {
		t.Fatalf("Files has %d entries, want the cap %d — this test proves the counts are "+
			"independent of the cap, so it is meaningless if the list was not capped",
			len(dwe.Files), dirtyFileListCap)
	}
	if dwe.Modified != wantModified || dwe.Untracked != wantUntracked {
		t.Errorf("Modified/Untracked = %d/%d, want %d/%d — counted from the CAPPED file list "+
			"instead of the full porcelain output, which silently under-reports every tree "+
			"with more than %d changes",
			dwe.Modified, dwe.Untracked, wantModified, wantUntracked, dirtyFileListCap)
	}
	if dwe.Total != wantModified+wantUntracked {
		t.Errorf("Total = %d, want %d", dwe.Total, wantModified+wantUntracked)
	}

	// And the shortcut is shown to be WRONG rather than merely unused: counting
	// the capped list here produces a different answer, so the assertions above
	// are ones the naive implementation could not have passed. Without this an
	// unlucky fixture could satisfy both computations and the test would guard
	// nothing.
	if m, u := countPorcelain(dwe.Files); m == wantModified && u == wantUntracked {
		t.Errorf("counting the capped list gave %d/%d, the same as the full list — this "+
			"fixture cannot distinguish the two implementations, so raise the path counts "+
			"above the cap %d", m, u, dirtyFileListCap)
	}
}

// TestWorktreeBranchReadsTheCheckedOutBranch covers the other field mg-d45b
// requires. A retained worktree keeps its branch checked out — that is what
// pinning MEANS — so the branch is both the first thing a rescuer needs and the
// thing whose deletion the tree is blocking.
func TestWorktreeBranchReadsTheCheckedOutBranch(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-qbe37")
	wtPath := r.worktree("polecat-qbe37")

	got, err := WorktreeBranch(wtPath)
	if err != nil {
		t.Fatalf("WorktreeBranch: %v", err)
	}
	if got != "polecat-qbe37" {
		t.Errorf("WorktreeBranch = %q, want polecat-qbe37", got)
	}
}

// TestWorktreeBranchReportsADetachedHead pins the decision to pass git's own
// answer through rather than normalise it.
//
// A preserved worktree on a detached HEAD is a real and materially WORSE
// situation than one on a branch: there is no branch name to hand a rescuer, so
// the commits are reachable only by hash. Flattening that to "" would make it
// indistinguishable from a read that failed — which is the same
// absent-versus-unknown collapse this ticket is about.
func TestWorktreeBranchReportsADetachedHead(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-detach")
	wtPath := r.worktree("polecat-detach")

	cmd := exec.Command("git", "-C", wtPath, "checkout", "-q", "--detach")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("detaching: %v\n%s", err, out)
	}

	got, err := WorktreeBranch(wtPath)
	if err != nil {
		t.Fatalf("WorktreeBranch on a detached head must still answer, got error: %v", err)
	}
	if got != "HEAD" {
		t.Errorf("WorktreeBranch = %q, want the literal \"HEAD\" — git's own answer for a "+
			"detached head, passed through so it stays distinguishable from a failed read", got)
	}
}

// TestWorktreeBranchFailsOnAnUnreadableTree is the negative control: the error
// path the preservation record turns into `branch_error` has to be reachable.
func TestWorktreeBranchFailsOnAnUnreadableTree(t *testing.T) {
	if _, err := WorktreeBranch(""); err == nil {
		t.Error("WorktreeBranch(\"\") must fail rather than return an empty name")
	}
	if _, err := WorktreeBranch(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Error("WorktreeBranch on a missing tree must fail")
	}
}
