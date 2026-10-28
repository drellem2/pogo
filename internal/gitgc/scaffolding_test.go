package gitgc

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/hookarm"
)

// TestScanPreservedCountsScaffoldingOnlyTrees is drellem2/pogo#162's
// --list-preserved half: a tree kept only for files pogod wrote must read
// apart from a tree kept for work.
//
// The kept-for-scaffolding shape that survives 21db891 is a stamp under a
// wholly untracked directory: `git status` collapses it to `?? sub/`, and the
// removal guard opens only a collapsed .claude/, so the tree is retained. The
// control beside it holds the same stamp AND a real file, and must not count.
func TestScanPreservedCountsScaffoldingOnlyTrees(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newBareIgnoreRepo(t)
	r.branch("polecat-a1b2")
	r.branch("polecat-c3d4")
	stampOnly := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	realWork := addWorktree(t, r, polecats, "c3d4", "polecat-c3d4")

	nested := filepath.Join("sub", ".claude", hookarm.StampName)
	writeArtefact(t, stampOnly, nested)
	writeArtefact(t, realWork, nested)
	writeArtefact(t, realWork, filepath.Join("sub", "work.go"))

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats,
		Tickets:     TicketIndex{"mg-a1b2": TicketArchived, "mg-c3d4": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Precondition: both are retained. If the guard ever opens every collapsed
	// directory, the stamp-only tree goes clean and this test needs a new shape.
	if rep.RetainedCount != 2 {
		t.Fatalf("precondition: want both trees retained, got %d: %+v", rep.RetainedCount, rep.Retained)
	}
	if !findTree(t, rep.Retained, "a1b2").ScaffoldingOnly {
		t.Errorf("a tree whose only uncommitted path is pogod's stamp must read scaffolding-only")
	}
	if findTree(t, rep.Retained, "c3d4").ScaffoldingOnly {
		t.Errorf("a tree holding a real file beside the stamp must NOT read scaffolding-only")
	}
	if rep.RetainedScaffoldingOnly != 1 {
		t.Errorf("retained_scaffolding_only = %d, want 1", rep.RetainedScaffoldingOnly)
	}

	out := rep.Summary()
	if !strings.Contains(out, "of those, 1 hold ONLY pogod's own scaffolding") {
		t.Errorf("the headline must count scaffolding-only trees, got:\n%s", out)
	}
	if strings.Count(out, "SCAFFOLDING ONLY:") != 1 {
		t.Errorf("exactly the stamp-only tree's row must be marked, got:\n%s", out)
	}
	// The stamp-only row lists its path without the only-copy warning, which
	// is about objects nobody else holds; the real-work row keeps it.
	stampRow := StreamedTree(findTree(t, rep.Retained, "a1b2"))
	if strings.Contains(stampRow, "only copy OF THE GIT OBJECTS") || !strings.Contains(stampRow, "sub/") {
		t.Errorf("the scaffolding-only row must list its path without the only-copy warning, got:\n%s", stampRow)
	}
	if !strings.Contains(StreamedTree(findTree(t, rep.Retained, "c3d4")), "only copy OF THE GIT OBJECTS") {
		t.Errorf("a tree holding real untracked work must keep the only-copy warning")
	}
	// The streamed listing renders the same row and the same counts.
	if !strings.Contains(StreamedTree(findTree(t, rep.Retained, "a1b2")), "SCAFFOLDING ONLY:") {
		t.Errorf("the streamed row must carry the marker too")
	}
	if !strings.Contains(rep.StreamedTail(), "of those, 1 hold ONLY pogod's own scaffolding") {
		t.Errorf("the streamed tail must carry the count too, got:\n%s", rep.StreamedTail())
	}
}

// TestScaffoldingOnlyIsFalseForTrackedChanges: the exclusion is untracked-only,
// so a modified tracked .claude/settings.local.json is work, not scaffolding.
func TestScaffoldingOnlyIsFalseForTrackedChanges(t *testing.T) {
	r := newBareIgnoreRepo(t)
	writeArtefact(t, r.dir, hookarm.SettingsRelPath)
	r.git("add", hookarm.SettingsRelPath)
	r.git("commit", "-q", "-m", "track settings")
	writeArtefact(t, r.dir, filepath.Join(".claude", hookarm.StampName))
	dirty(t, r.dir, hookarm.SettingsRelPath, "edited\n")

	only, err := scaffoldingOnly(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	if only {
		t.Errorf("a modified TRACKED settings file is a change git can see against HEAD, not scaffolding")
	}
	// Positive control: drop the edit and only the untracked stamp remains.
	r.git("checkout", "--", hookarm.SettingsRelPath)
	if only, err = scaffoldingOnly(r.dir); err != nil || !only {
		t.Errorf("with only the untracked stamp left, want scaffolding-only, got %v (err %v)", only, err)
	}
}
