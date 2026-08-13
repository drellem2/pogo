package gitgc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sharedPolecats builds the one directory every repository's worktrees live
// under, the way ~/.pogo/polecats is shared by every repo the fleet works.
//
// The sharing is not incidental to these tests: it is the reason the listing
// exists at a directory rather than at a repo. A repo-scoped listing of
// retained trees reports a fraction of the population while looking complete,
// and the population that pins branches and holds unread work is all of them.
func sharedPolecats(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "polecats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Match sweep_test's newTestRepo: git canonicalizes worktree paths, and on
	// macOS t.TempDir() sits under a symlink.
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir
}

// addWorktree registers one of r's branches at <polecats>/<owner>.
func addWorktree(t *testing.T, r *testRepo, polecats, owner, branch string) string {
	t.Helper()
	path := filepath.Join(polecats, owner)
	r.git("worktree", "add", "-q", path, branch)
	return path
}

// findTree returns the listed tree with the given owner.
func findTree(t *testing.T, trees []PreservedTree, owner string) PreservedTree {
	t.Helper()
	for _, tr := range trees {
		if tr.Owner == owner {
			return tr
		}
	}
	t.Fatalf("no tree owned by %q in %+v", owner, trees)
	return PreservedTree{}
}

// TestScanPreservedSpansRepositories is the headline control for mg-f4c0.
//
// The retained population accumulated to twenty-three trees across four
// repositories with nobody reading it, and the only instrument that could see
// it was `ls`. `pogo gc` cannot: it takes one --repo and reports what it would
// do there. So the first thing this scan must do is span repositories and
// attribute each tree to the right one — the reclaim command is repo-scoped, so
// a listing that could not say WHICH repo would be a listing an operator cannot
// act on.
func TestScanPreservedSpansRepositories(t *testing.T) {
	polecats := sharedPolecats(t)

	one := newTestRepo(t)
	one.branch("polecat-a1b2")
	wtOne := addWorktree(t, one, polecats, "a1b2", "polecat-a1b2")
	dirty(t, wtOne, "irreplaceable.go", "package one // exists nowhere else\n")

	two := newTestRepo(t)
	two.branch("polecat-c3d4")
	wtTwo := addWorktree(t, two, polecats, "c3d4", "polecat-c3d4")
	dirty(t, wtTwo, "other.go", "package two\n")

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats,
		Tickets:     TicketIndex{"mg-a1b2": TicketArchived, "mg-c3d4": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Retained) != 2 {
		t.Fatalf("want both repositories' retained trees, got %d: %+v", len(rep.Retained), rep.Retained)
	}

	a := findTree(t, rep.Retained, "a1b2")
	if a.Repo != one.dir {
		t.Errorf("tree a1b2 attributed to repo %q, want %q — the reclaim command is repo-scoped, "+
			"so a wrong attribution points the operator at the wrong repository", a.Repo, one.dir)
	}
	if a.Path != wtOne || a.Branch != "polecat-a1b2" {
		t.Errorf("tree a1b2 = %+v, want path %q branch polecat-a1b2", a, wtOne)
	}
	if a.WorkItemID != "mg-a1b2" || a.TicketState != "archived" {
		t.Errorf("tree a1b2 work item = %q (%s), want mg-a1b2 (archived)", a.WorkItemID, a.TicketState)
	}
	if a.Outcome != "preserved" {
		t.Errorf("outcome = %q, want %q — the vocabulary must match the worktree_preserved event "+
			"so a listing row and a spine event about the same tree join", a.Outcome, "preserved")
	}
	if a.Untracked != 1 || a.Modified != 0 {
		t.Errorf("split = %d modified / %d untracked, want 0/1 — the untracked half is the urgent "+
			"one and the count that makes a tree actionable", a.Modified, a.Untracked)
	}

	b := findTree(t, rep.Retained, "c3d4")
	if b.Repo != two.dir || b.Path != wtTwo {
		t.Errorf("tree c3d4 = %+v, want path %q in repo %q", b, wtTwo, two.dir)
	}

	// And the filter still narrows to one repository when an operator asks.
	filtered, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats, Repo: two.dir,
		Tickets: TicketIndex{"mg-a1b2": TicketArchived, "mg-c3d4": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Retained) != 1 || filtered.Retained[0].Owner != "c3d4" {
		t.Fatalf("--repo filter should leave only c3d4, got %+v", filtered.Retained)
	}
	// A narrowed listing must say how much of the directory it is not showing.
	// A filtered report that reads like a full one is the same failure as a
	// truncated file list that does not say it truncated.
	if filtered.OtherRepoCount != 1 {
		t.Errorf("OtherRepoCount = %d, want 1", filtered.OtherRepoCount)
	}
	if out := filtered.Summary(); !strings.Contains(out, "1 tree(s) in other repositories not shown") {
		t.Errorf("the filtered report must account for what the filter removed, got:\n%s", out)
	}
}

// TestScanPreservedFilterKeepsTreesWhoseRepoIsUnresolvable applies this
// ticket's own finding to its remedy.
//
// The defect being fixed is a retained tree nothing reports. A --repo filter
// that drops a tree whose .git pointer could not be READ would reproduce it
// exactly — silently, and precisely in the case where something is already
// wrong with the tree. So the filter excludes only trees that positively
// resolved to a DIFFERENT repository; an unresolvable one might be this one and
// is shown, with its repository reported as unresolved.
func TestScanPreservedFilterKeepsTreesWhoseRepoIsUnresolvable(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	r.branch("polecat-a1b2")
	wt := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	dirty(t, wt, "irreplaceable.go", "package x\n")
	damageGitPointer(t, wt) // .git present, pointer garbage: repo unresolvable

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats, Repo: r.dir,
		Tickets: TicketIndex{"mg-a1b2": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Retained) != 1 {
		t.Fatalf("a tree whose repo could not be resolved must survive a --repo filter — dropping "+
			"it is this ticket's defect one layer down. Got %+v", rep.Retained)
	}
	if rep.Retained[0].RepoError == "" {
		t.Error("the resolution failure must be reported, never left as a bare empty repo field")
	}
	if out := rep.Summary(); !strings.Contains(out, "repository UNRESOLVED") {
		t.Errorf("the report must say the repository is unknown rather than grouping the tree "+
			"under a repo it was never shown to belong to, got:\n%s", out)
	}
}

// TestScanPreservedForceColumnAgreesWithTheSweep pins the claim the listing
// makes that nothing else in the report does.
//
// `--force` reads as "reclaims everything listed", and it is not: the sweep
// checks the OWNER'S TICKET STATE before it ever consults the dirty guard, so
// --force overrides the guard and never the state check. A retained tree whose
// work item is still in flight survives the flag entirely.
//
// So the column is not asserted against a hand-written expectation — it is
// asserted against what Sweep ACTUALLY DOES with the same inputs. A column that
// merely restated the rule could drift from the sweep and be wrong in the
// direction that costs files: an operator told "--force would not take this"
// about a tree --force takes.
func TestScanPreservedForceColumnAgreesWithTheSweep(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	r.branch("polecat-a1b2") // archived — concluded
	r.branch("polecat-c3d4") // in-flight
	concluded := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	inflight := addWorktree(t, r, polecats, "c3d4", "polecat-c3d4")
	dirty(t, concluded, "wip.go", "package x\n")
	dirty(t, inflight, "wip.go", "package y\n")

	tickets := TicketIndex{"mg-a1b2": TicketArchived, "mg-c3d4": TicketInFlight}

	rep, err := ScanPreserved(PreservedScanOptions{PolecatsDir: polecats, Tickets: tickets})
	if err != nil {
		t.Fatal(err)
	}
	if got := findTree(t, rep.Retained, "a1b2").ForceReclaims; got != "yes" {
		t.Errorf("ForceReclaims for the concluded item = %q, want yes", got)
	}
	if got := findTree(t, rep.Retained, "c3d4").ForceReclaims; got != "no" {
		t.Errorf("ForceReclaims for the in-flight item = %q, want no — --force overrides the dirty "+
			"guard, not the ticket-state check", got)
	}

	// The oracle: what the sweep does, not what the listing says it does.
	res, err := Sweep(Options{
		Repo: r.dir, LivePolecats: map[string]bool{}, Tickets: tickets,
		PolecatsDir: polecats, DryRun: true, Force: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	removed := map[string]bool{}
	for _, w := range res.WorktreesRemoved {
		removed[w.Path] = true
	}
	kept := map[string]bool{}
	for _, w := range res.WorktreesKept {
		kept[w.Path] = true
	}
	if !removed[concluded] {
		t.Errorf("the sweep with --force should reclaim the concluded item's tree; it did not:\n%s",
			res.Summary())
	}
	if !kept[inflight] || removed[inflight] {
		t.Errorf("the sweep with --force must NOT reclaim an in-flight item's tree; that is exactly "+
			"the claim the listing's force column makes:\n%s", res.Summary())
	}
}

// TestScanPreservedSeparatesTreesInUseByLivePolecats keeps the headline number
// honest.
//
// A running polecat's tree is dirty by construction — that is what working
// looks like — and folding those into the retained count would report a
// population that needs an owner when most of it already has one. Worse, it
// would invite an operator to act on a tree somebody is editing.
func TestScanPreservedSeparatesTreesInUseByLivePolecats(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	r.branch("polecat-a1b2")
	r.branch("polecat-c3d4")
	deadTree := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	liveTree := addWorktree(t, r, polecats, "c3d4", "polecat-c3d4")
	dirty(t, deadTree, "wip.go", "package x\n")
	dirty(t, liveTree, "wip.go", "package y\n")

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir:  polecats,
		LivePolecats: map[string]bool{"c3d4": true},
		Tickets:      TicketIndex{"mg-a1b2": TicketArchived, "mg-c3d4": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Retained) != 1 || rep.Retained[0].Owner != "a1b2" {
		t.Fatalf("retained should hold only the dead owner's tree, got %+v", rep.Retained)
	}
	if len(rep.InUse) != 1 || rep.InUse[0].Owner != "c3d4" {
		t.Fatalf("the live owner's tree must be reported as in use, not dropped and not retained, "+
			"got %+v", rep.InUse)
	}
	if rep.InUse[0].ForceReclaims != "no" {
		t.Errorf("a live owner's tree is never gc's to take, whatever --force says; got %q",
			rep.InUse[0].ForceReclaims)
	}
	out := rep.Summary()
	if !strings.Contains(out, "in use by a live polecat") || !strings.Contains(out, liveTree) {
		t.Errorf("the report must show that the scan SAW the live tree and did not count it, got:\n%s", out)
	}
}

// TestSummaryNeverTruncatesUntrackedPaths is the asymmetry that makes this
// listing worth reading.
//
// A modified tracked file has a committed version in the object store; the
// worst case of not naming it is a lost edit git can still describe. An
// untracked path is on no branch, in no stash and on no remote — the tree is
// its only copy anywhere on the machine, and `~/.pogo/polecats/qbe37` held an
// entire 1450-line package that way. Truncating those would hide exactly the
// files the listing exists to surface, and would hide them silently.
//
// Modified entries ARE capped, and the cap must announce itself: an
// unannounced truncation is how a reader concludes they have seen the tree.
func TestSummaryNeverTruncatesUntrackedPaths(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	n := preservedModifiedCap + 5

	// Tracked files, committed on main so the worktree branch has them, then
	// modified inside the tree.
	for i := 0; i < n; i++ {
		r.commit(fmt.Sprintf("tracked%02d.txt", i), "original\n")
	}
	r.branch("polecat-a1b2")
	wt := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	for i := 0; i < n; i++ {
		dirty(t, wt, fmt.Sprintf("tracked%02d.txt", i), "edited\n")
	}
	for i := 0; i < n; i++ {
		dirty(t, wt, fmt.Sprintf("untracked%02d.go", i), "package x\n")
	}

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats, Tickets: TicketIndex{"mg-a1b2": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	tree := findTree(t, rep.Retained, "a1b2")
	if tree.Modified != n || tree.Untracked != n {
		t.Fatalf("split = %d modified / %d untracked, want %d/%d", tree.Modified, tree.Untracked, n, n)
	}
	if len(tree.Files) != 2*n {
		t.Errorf("the RECORD must hold the full file list uncapped (the cap is the renderer's job); "+
			"got %d entries, want %d", len(tree.Files), 2*n)
	}

	out := rep.Summary()
	for i := 0; i < n; i++ {
		if want := fmt.Sprintf("untracked%02d.go", i); !strings.Contains(out, want) {
			t.Fatalf("untracked path %s is missing from the report — untracked entries are never "+
				"capped, because this tree is their only copy. Got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, fmt.Sprintf("and %d more", n-preservedModifiedCap)) {
		t.Errorf("a capped modified list must name its own overflow count, got:\n%s", out)
	}
	if !strings.Contains(out, "git -C "+wt+" status") {
		t.Errorf("a capped list must name the command that shows the rest, got:\n%s", out)
	}
}

// TestSummaryRefusesToRenderAVerdict pins the report's central restraint.
//
// `~/.pogo/polecats/p687f` held seven modified files, all of them regenerated
// suite output. A reader sampled two, saw timing churn, and concluded "residue,
// safe to reclaim"; the third held three new registry entries. Any classifier
// cheap enough to run over the whole population would have made that mistake
// for every tree at once. So the report states facts and says, in the text a
// reader cannot skip, that it is not deciding.
func TestSummaryRefusesToRenderAVerdict(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	r.branch("polecat-a1b2")
	wt := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	dirty(t, wt, "out_a3b.txt", "regenerated? authored? unknowable from here\n")

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats, Tickets: TicketIndex{"mg-a1b2": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := rep.Summary()
	if !strings.Contains(out, "NOTHING BELOW IS A VERDICT") {
		t.Errorf("the report must say it is not deciding, got:\n%s", out)
	}
	// Scanned OUTSIDE the preamble, because the preamble quotes the wrong
	// conclusion verbatim in order to name it. That is the one place the phrase
	// belongs; anywhere else it would be the report reaching one.
	body := strings.Replace(out, preservedPreamble, "", 1)
	if body == out {
		t.Fatal("the preamble is missing from the report, so this assertion is checking nothing")
	}
	for _, forbidden := range []string{"safe to reclaim", "likely regenerable", "probably safe"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the report must never grade a tree (found %q) — that judgement needs someone "+
				"who read the files. Got:\n%s", forbidden, out)
		}
	}

	// The blast radius of the reclaim command sits ABOVE the trees it covers,
	// with a count. A per-tree notice cannot convey it, and a reader who takes
	// the command out of one is acting on all of them.
	if !strings.Contains(out, "repo-scoped and forced") {
		t.Errorf("the report must state that the reclaim command is repo-scoped and forced, got:\n%s", out)
	}
	if !strings.Contains(out, "pogo gc --repo="+r.dir+" --apply --force") {
		t.Errorf("the report must give the exact reclaim command for each repository, got:\n%s", out)
	}
}

// TestScanPreservedReportsUnreadableTreesAsUndetermined keeps the two retention
// outcomes apart.
//
// "There is uncommitted work here" and "I could not look" are different claims,
// and only the first licenses a reader to go hunting files. Folding the second
// into the first states a fact about the tree that nobody established — the
// same separation DirtyWorktreeError and UndeterminedWorktreeError exist for.
func TestScanPreservedReportsUnreadableTreesAsUndetermined(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	r.branch("polecat-a1b2")
	wt := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	dirty(t, wt, "maybe_irreplaceable.go", "package x\n")
	// The pointer survives — so the repository is still resolvable — while
	// git's admin dir for this worktree does not, which is what breaks status.
	if err := os.RemoveAll(filepath.Join(r.dir, ".git", "worktrees", "a1b2")); err != nil {
		t.Fatal(err)
	}

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats, Tickets: TicketIndex{"mg-a1b2": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	tree := findTree(t, rep.Retained, "a1b2")
	if tree.Outcome != "undetermined" {
		t.Fatalf("outcome = %q, want undetermined — a tree we could not read must not be reported "+
			"as one we read", tree.Outcome)
	}
	if tree.StatusError == "" {
		t.Error("the status failure must be reported — which way git broke is what a rescuer acts on")
	}
	if tree.Total != 0 || tree.Files != nil {
		t.Errorf("an unread tree must carry no file counts: a count is meaningful only when the "+
			"tree was actually read. Got total=%d files=%v", tree.Total, tree.Files)
	}
	// The repository must still be resolvable: half this population is retained
	// precisely because git broke there, and that is when the operator most
	// needs to know which repo to point the reclaim at.
	if tree.Repo != r.dir {
		t.Errorf("repo = %q (%s), want %q resolved from the .git pointer without running git",
			tree.Repo, tree.RepoError, r.dir)
	}

	out := rep.Summary()
	if !strings.Contains(out, "UNKNOWN") || !strings.Contains(out, "not a report of an empty tree") {
		t.Errorf("the report must say the contents are unknown rather than implying the tree is "+
			"empty, got:\n%s", out)
	}
}

// TestScanPreservedPartitionsThePolecatsDir keeps the counts a partition rather
// than a selection.
//
// The retained list is only trustworthy if a reader can see that everything
// else was accounted for. A clean tree and a directory that is not a worktree
// at all are both legitimately absent from the list — and an unexplained
// absence is indistinguishable from a tree the scan missed.
func TestScanPreservedPartitionsThePolecatsDir(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	r.branch("polecat-a1b2")
	r.branch("polecat-c3d4")
	retained := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	addWorktree(t, r, polecats, "c3d4", "polecat-c3d4") // left clean
	dirty(t, retained, "wip.go", "package x\n")

	// A gh #31 orphan dir: files, no .git. It has no index and no HEAD, so
	// "uncommitted" is not a property it has — listing it among trees holding
	// uncommitted work would be a claim nobody can make.
	orphan := filepath.Join(polecats, "e5f6")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "leftover.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stray FILE beside the directories (the real polecats dir has one).
	if err := os.WriteFile(filepath.Join(polecats, "qbe37-modified.patch"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := ScanPreserved(PreservedScanOptions{
		PolecatsDir: polecats,
		Tickets:     TicketIndex{"mg-a1b2": TicketArchived, "mg-c3d4": TicketArchived},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Retained) != 1 || rep.Retained[0].Owner != "a1b2" {
		t.Fatalf("retained = %+v, want only a1b2", rep.Retained)
	}
	if rep.CleanCount != 1 {
		t.Errorf("CleanCount = %d, want 1", rep.CleanCount)
	}
	if rep.NotWorktreeCount != 1 {
		t.Errorf("NotWorktreeCount = %d, want 1 (the orphan dir; the stray file is not a directory)",
			rep.NotWorktreeCount)
	}
	if out := rep.Summary(); !strings.Contains(out, "1 clean, 1 not linked worktrees") {
		t.Errorf("the report must account for what it skipped, got:\n%s", out)
	}
}

// TestScanPreservedDegradesWithoutWorkItemStates: the tree listing is the half
// nothing else can produce, so it must survive an unavailable `mg`. What is
// lost is the ticket column and the force answer, and both must SAY they are
// unknown rather than defaulting to a confident wrong value — "no" would tell
// an operator --force leaves a tree alone that --force takes.
func TestScanPreservedDegradesWithoutWorkItemStates(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	r.branch("polecat-a1b2")
	wt := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")
	dirty(t, wt, "wip.go", "package x\n")

	// Tickets nil and `mg` unreachable: an empty PATH makes LoadTicketIndex
	// fail the way a missing binary does.
	t.Setenv("PATH", t.TempDir())
	rep, err := ScanPreserved(PreservedScanOptions{PolecatsDir: polecats})
	if err != nil {
		t.Fatalf("an unavailable work-item index must not kill the listing: %v", err)
	}
	if len(rep.Retained) != 1 {
		t.Fatalf("the tree listing must still land, got %+v", rep.Retained)
	}
	if rep.TicketsLoaded {
		t.Error("TicketsLoaded should be false when the index could not be read")
	}
	if got := rep.Retained[0].ForceReclaims; got != "unknown" {
		t.Errorf("ForceReclaims = %q, want unknown — a confident answer here is a wrong answer", got)
	}
	if len(rep.Notes) == 0 {
		t.Error("the degradation must be stated in the report, not inferred from an empty column")
	}
}

// TestWorktreeDirtyIgnoresGitWarningsOnStderr pins the mg-f4c0 input fix.
//
// `git status --porcelain` writes warnings to stderr and exits 0 — an
// unreadable subdirectory is the common one — and this function used to read
// git's COMBINED output, so every warning was counted as a dirty entry. The
// consequence is not a cosmetic miscount: the retention guard acts on this
// list, so a tree whose only status output is a warning reads as dirty, is
// preserved, pins its branch, and is never reclaimed. That is a silent producer
// of the very population mg-f4c0 exists to consume. It also mis-splits the
// count that decides urgency, since a warning carries no `??` prefix and so
// lands in the tracked-changes half.
func TestWorktreeDirtyIgnoresGitWarningsOnStderr(t *testing.T) {
	r := newTestRepo(t)
	r.branch("polecat-a1b2")
	wt := r.worktree("polecat-a1b2")
	dirty(t, wt, "real.go", "package x\n")

	noread := filepath.Join(wt, "noread")
	if err := os.MkdirAll(noread, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noread, "hidden.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(noread, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(noread, 0o755) })

	// Confirm the reproduction actually reproduces before asserting on it. As
	// root, or on a filesystem that ignores the mode, git reads the directory
	// happily and there is no warning to be misparsed — a green assertion there
	// would be a test that proves nothing.
	cmd := exec.Command("git", "-C", wt, "status", "--porcelain")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git status should still succeed with an unreadable subdir: %v", err)
	}
	if !strings.Contains(stderr.String(), "could not open directory") {
		t.Skipf("could not reproduce a stderr warning from git status (running as root?): %q", stderr.String())
	}

	isDirty, files, err := WorktreeDirty(wt)
	if err != nil {
		t.Fatalf("WorktreeDirty should succeed — git exited 0: %v", err)
	}
	if !isDirty {
		t.Fatal("the tree holds a real untracked file and must read dirty")
	}
	for _, f := range files {
		if strings.Contains(f, "warning:") || strings.Contains(f, "could not open directory") {
			t.Fatalf("a stderr warning was counted as an uncommitted change — the guard acts on "+
				"this list, so a tree whose ONLY output is a warning would be preserved forever. "+
				"Got: %v", files)
		}
	}
	modified, untracked := countPorcelain(files)
	if modified != 0 || untracked != 1 {
		t.Errorf("split = %d modified / %d untracked, want 0/1; a warning has no `??` prefix, so "+
			"counting it inflates the tracked half — the one that reads as recoverable. Got: %v",
			modified, untracked, files)
	}
}

// TestWorktreeSourceRepoReadsThePointerNotGit: half the retained population is
// retained BECAUSE git broke in the tree, and that is exactly when the operator
// needs to know which repository to point the reclaim at. So the resolver reads
// the .git pointer file — a plain read — and only falls back to running git.
func TestWorktreeSourceRepoReadsThePointerNotGit(t *testing.T) {
	polecats := sharedPolecats(t)
	r := newTestRepo(t)
	r.branch("polecat-a1b2")
	wt := addWorktree(t, r, polecats, "a1b2", "polecat-a1b2")

	if got, err := WorktreeSourceRepo(wt); err != nil || got != r.dir {
		t.Fatalf("WorktreeSourceRepo = %q, %v; want %q", got, err, r.dir)
	}

	// Break git in the tree; the pointer is untouched, so the answer must not
	// change.
	if err := os.RemoveAll(filepath.Join(r.dir, ".git", "worktrees", "a1b2")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WorktreeDirty(wt); err == nil {
		t.Fatal("precondition: git status should now fail in this tree")
	}
	if got, err := WorktreeSourceRepo(wt); err != nil || got != r.dir {
		t.Fatalf("with git broken WorktreeSourceRepo = %q, %v; want %q — the pointer read is the "+
			"whole point of the ordering", got, err, r.dir)
	}

	// A pointer that is not a linked-worktree admin dir is REPORTED, never
	// silently answered: an empty repo field and a repo field nobody
	// implemented look identical to every reader.
	damageGitPointer(t, wt)
	if got, err := WorktreeSourceRepo(wt); err == nil {
		t.Fatalf("a garbage pointer must not resolve to a repository, got %q", got)
	}
}
