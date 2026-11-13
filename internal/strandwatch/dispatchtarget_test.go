package strandwatch

import (
	"strings"
	"testing"
)

// mg-929cf: check-stranded's stranded remedy is a paste-ready submit, and
// `pogo refinery submit` defaults to main. An item dispatched at a release
// branch (its `branch:` field) must print --target=<branch>, and must be
// judged against that branch — otherwise pasting the remedy submits the work
// to main, the defect mg-faa29 fixed for pogod's own stranded checks.

func TestStrandedRemedyCarriesTheItemsDispatchTarget(t *testing.T) {
	r := newRepo(t)
	r.branch("release/x", "main")
	r.commit("rel.md", "chore: release cut")
	r.push("release/x")
	r.branch("polecat-q929a", "release/x")
	r.commit("fix.md", "fix: back-port for the release (mg-929a)")
	r.push("polecat-q929a")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-929a", Status: "available", Repo: r.dir, Branch: "release/x"}),
		LiveAgents: fleet(),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-929a")
	if !ok {
		t.Fatalf("no row for mg-929a.\n%s", Render(rep, true))
	}
	if row.Kind != KindStranded {
		t.Fatalf("Kind = %q, want %q.\n%s", row.Kind, KindStranded, Render(rep, true))
	}
	// Judged against release/x: only the polecat's own commit is unmerged.
	// Against main the release cut's commit would count too.
	if row.Unmerged != 1 {
		t.Errorf("Unmerged = %d, want 1 — the branch must be judged against release/x, not main", row.Unmerged)
	}
	if !strings.Contains(row.Remedy(), "pogo refinery submit") ||
		!strings.Contains(row.Remedy(), "--target=release/x") {
		t.Errorf("Remedy() = %q, want a submit carrying --target=release/x; without it the "+
			"paste submits the release-branch work to main", row.Remedy())
	}
}

func TestStrandedRemedyOmitsTargetForADefaultBranchItem(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q929b", "main")
	r.commit("fix.md", "fix: ordinary work (mg-929b)")
	r.push("polecat-q929b")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-929b", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-929b")
	if !ok || row.Kind != KindStranded {
		t.Fatalf("want a stranded row for mg-929b.\n%s", Render(rep, true))
	}
	if !strings.Contains(row.Remedy(), "pogo refinery submit") {
		t.Errorf("Remedy() = %q, want a submit", row.Remedy())
	}
	if strings.Contains(row.Remedy(), "--target") {
		t.Errorf("Remedy() = %q, want NO --target for an item aimed at the default branch", row.Remedy())
	}
}

// A branch already merged into its release target is not stranded, even
// though main lacks it.
func TestBranchLandedOnItsReleaseTargetIsNotStranded(t *testing.T) {
	r := newRepo(t)
	r.branch("release/x", "main")
	r.commit("rel.md", "chore: release cut")
	r.push("release/x")
	r.branch("polecat-q929c", "release/x")
	r.commit("fix.md", "fix: back-port (mg-929c)")
	r.push("polecat-q929c")
	r.checkout("release/x")
	r.commit("other.md", "chore: something else landed on the release first")
	r.git("rebase", "-q", "release/x", "polecat-q929c")
	r.checkout("release/x")
	r.git("merge", "-q", "--ff-only", "polecat-q929c")
	r.push("release/x")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-929c", Status: "available", Repo: r.dir, Branch: "release/x"}),
		LiveAgents: fleet(),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-929c")
	if ok && row.Kind == KindStranded {
		t.Errorf("Kind = stranded for a branch merged into its dispatch target release/x; "+
			"it was judged against main.\n%s", Render(rep, true))
	}
}

// A dispatch target not yet on origin: judged against the default branch,
// the remedy still names the target, and the report says why the two differ.
func TestMissingDispatchTargetStillNamedInRemedy(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-q929d", "main")
	r.commit("fix.md", "fix: work for a release not cut yet (mg-929d)")
	r.push("polecat-q929d")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-929d", Status: "available", Repo: r.dir, Branch: "release/y"}),
		LiveAgents: fleet(),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	row, ok := rowFor(rep, "mg-929d")
	if !ok || row.Kind != KindStranded {
		t.Fatalf("want a stranded row for mg-929d.\n%s", Render(rep, true))
	}
	if !row.TargetMissing {
		t.Errorf("TargetMissing = false for a target on no ref")
	}
	if !strings.Contains(row.Remedy(), "--target=release/y") {
		t.Errorf("Remedy() = %q, want --target=release/y", row.Remedy())
	}
	if !strings.Contains(Render(rep, false), "release/y is not on origin yet") {
		t.Errorf("render does not say the dispatch target is missing.\n%s", Render(rep, false))
	}
}
