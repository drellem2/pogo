package gitgc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// removeWorktreeIndex puts a registered worktree back into the state
// `git worktree add` holds it in for the whole of its checkout: tree and admin
// dir present, index not yet written (drellem2/pogo#180).
func removeWorktreeIndex(t *testing.T, wt string) {
	t.Helper()
	out, err := exec.Command("git", "-C", wt, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		t.Fatalf("rev-parse --absolute-git-dir in %s: %v", wt, err)
	}
	index := filepath.Join(strings.TrimSpace(string(out)), "index")
	if err := os.Remove(index); err != nil {
		t.Fatalf("remove %s: %v", index, err)
	}
}

// TestPreservedForItemsSkipsAnUnpopulatedCheckout is the #180 reproduction: a
// polecat's worktree read mid-`git worktree add`. With no index, `git status`
// reports every tracked file as a staged deletion — the reporter's constant
// 3455 — and stall-watch raised a do-not-dispatch alarm on a polecat seconds
// into its spawn. Untracked files are present too, as they were in the
// reporter's samples (the checkout writes files before the index).
func TestPreservedForItemsSkipsAnUnpopulatedCheckout(t *testing.T) {
	r := newTestRepo(t)
	r.commit("a.txt", "a\n")
	r.commit("b.txt", "b\n")
	r.branch("polecat-pf180")
	wt := r.worktreeOwnedBy("pf180", "polecat-pf180")
	dirty(t, wt, "half-written.go", "package x\n")
	removeWorktreeIndex(t, wt)

	// The instrument check: without the skip, this tree IS what the alarm
	// fired on — the removal guard calls it dirty, every tracked file counted.
	// If this stops holding, the test below proves nothing.
	var dwe *DirtyWorktreeError
	if chk := checkWorktreeRemoval(wt, r.dir, ""); !errors.As(chk.Refusal, &dwe) || dwe.Modified < 3 {
		t.Fatalf("precondition: an index-less tree should read as dirty with every tracked file modified; got %v", chk.Refusal)
	}

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-f180"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	if trees := rep.Trees["mg-f180"]; len(trees) != 0 {
		t.Fatalf("an unpopulated checkout must not be reported as a preserved tree; got %+v", trees)
	}
	// Skipped, but not silently: the tree is named in the report.
	if len(rep.Unpopulated) != 1 || rep.Unpopulated[0] != wt {
		t.Errorf("Unpopulated = %v, want [%s]", rep.Unpopulated, wt)
	}
}

// TestPreservedForItemsStillReportsAPopulatedTree is the positive control for
// the skip: the SAME shape with its index present — an untracked file in a
// fully checked-out tree — is authored work and must still be reported.
func TestPreservedForItemsStillReportsAPopulatedTree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-pf180")
	wt := r.worktreeOwnedBy("pf180", "polecat-pf180")
	dirty(t, wt, "half-written.go", "package x\n")

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-f180"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	trees := rep.Trees["mg-f180"]
	if len(trees) != 1 {
		t.Fatalf("want 1 preserved tree, got %+v", rep.Trees)
	}
	if trees[0].Outcome != "preserved" || trees[0].Untracked != 1 || trees[0].Modified != 0 {
		t.Errorf("got outcome=%q modified=%d untracked=%d, want preserved/0/1",
			trees[0].Outcome, trees[0].Modified, trees[0].Untracked)
	}
	if len(rep.Unpopulated) != 0 {
		t.Errorf("a populated tree must not be listed as unpopulated: %v", rep.Unpopulated)
	}
}

// TestRemovalGuardStillRefusesAnIndexlessTree pins the guard #180 deliberately
// left alone. PreservedForItems skips an index-less tree; gc must NOT, because
// that tree is one `git worktree add` may still be creating, and reaping it
// would pull it out from under the spawn about to start an agent in it. See the
// CARE note on checkWorktreeRemoval.
func TestRemovalGuardStillRefusesAnIndexlessTree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-pf180")
	wt := r.worktreeOwnedBy("pf180", "polecat-pf180")
	removeWorktreeIndex(t, wt)

	if chk := checkWorktreeRemoval(wt, r.dir, ""); chk.Refusal == nil {
		t.Fatal("checkWorktreeRemoval cleared an index-less tree; its refusal there is load-bearing")
	}
	if err := RemoveWorktree(r.dir, wt, OwnerUnproven); err == nil {
		t.Fatal("RemoveWorktree reaped an index-less tree")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("the refused tree must still exist: %v", err)
	}
}

// TestUnpopulatedCheckoutNeedsPositiveEvidence: a tree whose .git pointer
// cannot be followed is NOT unpopulated — it goes to the probe, whose
// cannot-tell arm reports it. Skipping on unreadability would silence the probe
// where something is already wrong.
func TestUnpopulatedCheckoutNeedsPositiveEvidence(t *testing.T) {
	dir := t.TempDir()
	if unpopulatedCheckout(dir) {
		t.Error("no .git at all: want false")
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(dir, "nowhere")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if unpopulatedCheckout(dir) {
		t.Error("dangling gitdir pointer: want false")
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("garbage\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if unpopulatedCheckout(dir) {
		t.Error("unparseable pointer: want false")
	}
}
