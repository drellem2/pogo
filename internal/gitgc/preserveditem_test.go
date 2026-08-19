package gitgc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOwnerMatchesItemAcrossNamingSpellings pins the string question the whole
// guard rests on. Pogo has named polecats several ways across its history and a
// gate that resolved only the current spelling would be blind to exactly the
// trees that survived an outage — the oldest ones.
func TestOwnerMatchesItemAcrossNamingSpellings(t *testing.T) {
	match := []struct{ owner, item string }{
		{"p516e", "mg-516e"},   // the shape the finding was measured on
		{"p516e", "516e"},      // caller passed the bare code
		{"516e", "mg-516e"},    // bare name
		{"mg-516e", "mg-516e"}, // fully qualified
		{"cat-mg-516e", "mg-516e"},
		{"polecat-516e", "mg-516e"},
		{"w836c", "mg-836c"}, // the prefixed-letter form pogod hands out
		{"516e-retry", "mg-516e"},
		{"P516E", "mg-516e"}, // case
	}
	for _, c := range match {
		if !OwnerMatchesItem(c.owner, c.item) {
			t.Errorf("OwnerMatchesItem(%q, %q) = false, want true", c.owner, c.item)
		}
	}

	// And it must not match everything: a gate that refuses every dispatch is
	// disarmed within the day.
	noMatch := []struct{ owner, item string }{
		{"p516e", "mg-9d4e"},
		{"p516e", ""},
		{"", "mg-516e"},
		{"mayor", "mg-516e"},
	}
	for _, c := range noMatch {
		if OwnerMatchesItem(c.owner, c.item) {
			t.Errorf("OwnerMatchesItem(%q, %q) = true, want false", c.owner, c.item)
		}
	}
}

// TestPreservedForItemsFindsTheUncommittedTree is the positive control, built
// in the exact shape of the 2026-08-19 finding: an OPEN work item whose polecat
// died with uncommitted files in its worktree, zero commits ahead of the target,
// so nothing defined over branches or commits can see the work at all.
func TestPreservedForItemsFindsTheUncommittedTree(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-p516e")
	wt := r.worktreeOwnedBy("p516e", "polecat-p516e")

	// One tracked modification and one untracked file. The untracked half is
	// the urgent one — it exists in this tree and nowhere else.
	dirty(t, wt, "seed.txt", "edited but never committed\n")
	dirty(t, wt, "checkprogress.go", "package main // a whole new command\n")

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-516e"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	trees := rep.Trees["mg-516e"]
	if len(trees) != 1 {
		t.Fatalf("want exactly 1 retained tree for mg-516e, got %d (%+v)", len(trees), rep.Trees)
	}
	got := trees[0]
	if got.Path != wt {
		t.Errorf("Path = %q, want %q", got.Path, wt)
	}
	if got.Outcome != "preserved" {
		t.Errorf("Outcome = %q, want %q", got.Outcome, "preserved")
	}
	if got.Modified != 1 || got.Untracked != 1 {
		t.Errorf("split = %d modified / %d untracked, want 1/1 — the split is what tells a "+
			"recoverable edit from the only copy of a file", got.Modified, got.Untracked)
	}
	if got.Branch != "polecat-p516e" {
		t.Errorf("Branch = %q, want polecat-p516e — the branch is what a rescuer commits on", got.Branch)
	}
	if got.Repo != r.dir {
		t.Errorf("Repo = %q, want %q", got.Repo, r.dir)
	}
	// The file list must carry the untracked path by name: a refusal that
	// cannot say what is in the tree cannot stop the reflex remedy.
	if !strings.Contains(strings.Join(got.Files, " "), "checkprogress.go") {
		t.Errorf("Files must name the untracked path, got %v", got.Files)
	}
	if !rep.Any() {
		t.Error("Any() = false with a tree reported")
	}
}

// TestPreservedForItemsIgnoresCleanAndForeignTrees is the negative control, and
// it is the one that keeps the gate usable. A guard that fired on a clean tree,
// or on some other item's tree, would refuse dispatches nobody asked it about —
// and a gate that refuses everything is disarmed rather than fixed.
func TestPreservedForItemsIgnoresCleanAndForeignTrees(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-p516e")
	r.branch("polecat-p9d4e")
	r.worktreeOwnedBy("p516e", "polecat-p516e") // clean: nothing written into it
	other := r.worktreeOwnedBy("p9d4e", "polecat-p9d4e")
	dirty(t, other, "elsewhere.txt", "another item's work\n")

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-516e"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	if len(rep.Trees) != 0 {
		t.Fatalf("a clean tree for the asked item and a dirty tree for another item must both "+
			"be silent; got %+v", rep.Trees)
	}

	// The other item's tree IS found when it is the one asked about — the
	// silence above is attribution, not blindness.
	rep2, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-9d4e"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	if len(rep2.Trees["mg-9d4e"]) != 1 {
		t.Fatalf("want the dirty tree for mg-9d4e, got %+v", rep2.Trees)
	}
}

// TestPreservedForItemsEmptyItemsIsNeverAWildcard. An empty id list must answer
// for nothing. A guard that widened to "every item" when handed no items would
// refuse dispatches it was never asked about, which is the failure mode that
// gets a gate deleted rather than debugged.
func TestPreservedForItemsEmptyItemsIsNeverAWildcard(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-p516e")
	wt := r.worktreeOwnedBy("p516e", "polecat-p516e")
	dirty(t, wt, "work.txt", "uncommitted\n")

	rep, err := PreservedForItems(PreservedItemOptions{PolecatsDir: r.polecatsDir()})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	if rep.Any() {
		t.Fatalf("an empty Items list must report nothing, got %+v", rep.Trees)
	}
}

// TestPreservedForItemsRepoFilterKeepsUnresolvableTrees. The filter drops trees
// that RESOLVED to a different repo. A tree whose .git pointer could not be read
// is kept, because dropping it would make the guard silent in exactly the case
// where something is already wrong with the tree.
func TestPreservedForItemsRepoFilterKeepsUnresolvableTrees(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-p516e")
	wt := r.worktreeOwnedBy("p516e", "polecat-p516e")
	dirty(t, wt, "work.txt", "uncommitted\n")

	// Filtering to a foreign repo drops it: the tree positively belongs
	// elsewhere.
	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Repo:        filepath.Join(r.dir, "not-this-repo"),
		Items:       []string{"mg-516e"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	if rep.Any() {
		t.Fatalf("a tree resolving to another repo must be filtered out, got %+v", rep.Trees)
	}

	// Corrupt the pointer so the repo cannot be resolved at all, and the same
	// filter must now KEEP it.
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("garbage\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rep2, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Repo:        filepath.Join(r.dir, "not-this-repo"),
		Items:       []string{"mg-516e"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	trees := rep2.Trees["mg-516e"]
	if len(trees) != 1 {
		t.Fatalf("a tree whose repo could not be resolved must survive the filter, got %+v", rep2.Trees)
	}
	// And it must be reported as UNREAD rather than as a positively-read dirty
	// tree: with a broken .git pointer `git status` fails, and claiming
	// "uncommitted work" there would be a fact nobody established.
	if trees[0].Outcome != "undetermined" {
		t.Errorf("Outcome = %q, want %q for a tree git could not read", trees[0].Outcome, "undetermined")
	}
	if trees[0].StatusError == "" {
		t.Error("an undetermined tree must carry the status failure — which way git broke is the " +
			"only thing separating a corrupt tree from an unreadable disk")
	}
}

// TestPreservedForItemsMissingPolecatsDirIsNotAnError. A host that has never
// run a polecat has no polecats directory, and that is not evidence of
// anything. Reporting it as an error would make every gate on a fresh box fail
// its check and log a scary line about a state that is entirely normal.
func TestPreservedForItemsMissingPolecatsDirIsNotAnError(t *testing.T) {
	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: filepath.Join(t.TempDir(), "never-created"),
		Items:       []string{"mg-516e"},
	})
	if err != nil {
		t.Fatalf("a missing polecats dir must not be an error, got %v", err)
	}
	if rep.Any() {
		t.Fatalf("want no trees, got %+v", rep.Trees)
	}
}

// TestPreservedForItemsSkipsOrphanDirs. A directory under polecats/ with no
// .git entry is a phase-1b orphan: no index, no HEAD, so "holds uncommitted
// work" is not a property it has. Gating a dispatch on one would be a refusal
// with no remedy behind it.
func TestPreservedForItemsSkipsOrphanDirs(t *testing.T) {
	r := newTestRepo(t)
	orphan := filepath.Join(r.polecatsDir(), "p516e")
	if err := os.MkdirAll(orphan, 0755); err != nil {
		t.Fatal(err)
	}
	dirty(t, orphan, "stuff.txt", "not in any worktree\n")

	rep, err := PreservedForItems(PreservedItemOptions{
		PolecatsDir: r.polecatsDir(),
		Items:       []string{"mg-516e"},
	})
	if err != nil {
		t.Fatalf("PreservedForItems: %v", err)
	}
	if rep.Any() {
		t.Fatalf("an orphan dir is not a worktree and must not gate anything, got %+v", rep.Trees)
	}
}
