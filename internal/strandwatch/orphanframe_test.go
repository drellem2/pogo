package strandwatch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The tests below are mg-ded2's, and they share one shape: each fixes a state
// the item join could not report AT ALL, so the assertion that matters is not
// "the row says the right thing" but "a row exists". A detector whose population
// excludes the at-risk case fails silently and fails green.

// trees builds a Worktrees source over a fixed list.
func trees(wts ...Worktree) func() ([]Worktree, error) {
	return func() ([]Worktree, error) { return wts, nil }
}

func rowsOfKind(rep Report, k Kind) []Row {
	var out []Row
	for _, r := range rep.Rows {
		if r.Kind == k {
			out = append(out, r)
		}
	}
	return out
}

// localOnly leaves a branch with one commit that no remote ref contains — the
// state git-gc destroys and the one `git cherry` cannot distinguish from the 435
// ordinary unmerged branches next to it.
func (r *repo) localOnly(branch, subject string) {
	r.t.Helper()
	r.branch(branch, "main")
	r.commit("orphan.md", subject)
	r.checkout("main")
}

// --- GAP 1: the branch whose work item is closed -----------------------------

// TestOrphanBranchRowIsTheRowTheItemJoinCannotProduce is the ticket's headline
// measurement, as a fixture: a polecat worktree holding a commit on no remote
// ref, inside a repository the sweep covers cleanly, whose work item is closed
// and therefore outside OpenStatuses by construction.
//
// Before mg-ded2 this produced NOTHING — not a row, not a count, not an
// exclusion — while the repository's coverage line read `items=3, fetched=true,
// error=-` and the closing sentence was a flat all-clear.
func TestOrphanBranchRowIsTheRowTheItemJoinCannotProduce(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-pc-rev-c5d5a10", "revert: drop the hStep variant (mg-05d3)")

	rep, err := Scan(Options{
		// The board holds an OPEN item, so the repo is in scope and fully covered.
		// The orphan's own item is closed and therefore absent, which is the point.
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir, Title: "something else"}),
		LiveAgents: fleet(),
		Worktrees:  trees(Worktree{Path: "/tmp/pc-rev", Repo: r.dir, Branch: "polecat-pc-rev-c5d5a10"}),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	orphans := rowsOfKind(rep, KindOrphanBranch)
	if len(orphans) != 1 {
		t.Fatalf("orphan_branch rows = %d, want 1; the branch holds a commit on no remote ref "+
			"and no open item names it, which is the population this report used to exclude.\n%s",
			len(orphans), Render(rep, true))
	}
	got := orphans[0]
	if got.Branch != "polecat-pc-rev-c5d5a10" {
		t.Errorf("Branch = %q, want polecat-pc-rev-c5d5a10", got.Branch)
	}
	if got.Unmerged != 1 {
		t.Errorf("Unmerged = %d, want 1 commit on no remote ref", got.Unmerged)
	}
	if got.Item.ID != "" {
		t.Errorf("Item.ID = %q, want empty: this row has no item, which is what makes it this row", got.Item.ID)
	}
	if got.Subject() != "polecat-pc-rev-c5d5a10" {
		t.Errorf("Subject() = %q; a row with no item must still name something a reader can chase", got.Subject())
	}
	if !rep.Actionable() {
		t.Error("Actionable() = false; the orphan row must reach the exit code, or the sweep still exits 0 over it")
	}
	// The remedy must NOT be `refinery submit`: it takes an --author and there is
	// no open item to pass.
	if strings.Contains(got.Remedy(), "refinery submit") {
		t.Errorf("Remedy() = %q, must not recommend `refinery submit`: there is no item to submit under", got.Remedy())
	}
	if !strings.Contains(got.Remedy(), "push origin") {
		t.Errorf("Remedy() = %q, want the push that makes the object durable", got.Remedy())
	}
}

// TestOrphanBranchIsNotReportedWhenTheWORKISPUSHED is the negative control, and
// it is the one that keeps this row bounded. A branch of a closed item that is
// fully on origin has no local-only copy to lose, and reporting it would grow the
// report by the 435-branch population the ticket explicitly ruled out.
func TestOrphanBranchIsNotReportedWhenTheWorkIsPushed(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-gt-ffbd", "main")
	r.commit("done.md", "feat: work that landed and was pushed (mg-e7ff)")
	r.push("polecat-gt-ffbd")
	r.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(Worktree{Path: "/tmp/gt-ffbd", Repo: r.dir, Branch: "polecat-gt-ffbd"}),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if n := len(rowsOfKind(rep, KindOrphanBranch)); n != 0 {
		t.Errorf("orphan_branch rows = %d, want 0: the branch is fully on origin, nothing can be lost, "+
			"and this is the bound that keeps the row from becoming a 435-row wall.\n%s", n, Render(rep, true))
	}
}

// TestOrphanBranchSkipsLivePolecatsAndOpenItemBranches checks the two narrowing
// filters that stop this row firing on healthy input. A live polecat has unpushed
// commits because that is what work in progress is, and a branch some open item
// names is the item join's to report — reporting it twice under two Kinds with
// two opposite remedies is worse than reporting it once.
func TestOrphanBranchSkipsLivePolecatsAndOpenItemBranches(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-alive1", "wip: a running polecat's unpushed commit")
	r.localOnly("polecat-w6476", "feat: an open item's own unpushed work (mg-6476)")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet("alive1"),
		Worktrees: trees(
			Worktree{Path: "/tmp/alive1", Repo: r.dir, Branch: "polecat-alive1"},
			Worktree{Path: "/tmp/w6476", Repo: r.dir, Branch: "polecat-w6476"},
		),
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, row := range rowsOfKind(rep, KindOrphanBranch) {
		t.Errorf("orphan_branch row for %s: a live polecat's branch and an open item's branch "+
			"are both excluded, the first so the detector does not fire on healthy input and the "+
			"second so one branch is not reported under two opposite remedies.\n%s",
			row.Branch, Render(rep, true))
	}
	// The open item's branch must still be reported — by the join, as stranded.
	if _, ok := rowFor(rep, "mg-6476"); !ok {
		t.Errorf("no stranded row for mg-6476; excluding it from the orphan half must not "+
			"silence the half that owns it.\n%s", Render(rep, true))
	}
}

// TestOrphanMeasurementFailureIsARowAndNotACleanVerdict. The natural shape of
// this predicate answers "no local-only commits" whenever git fails, which is the
// direction that turns an orphan into an all-clear — the same reason
// KindUnjudged exists one level up.
func TestOrphanMeasurementFailureIsARowAndNotACleanVerdict(t *testing.T) {
	r := newRepo(t)
	// A worktree pointing at a branch in a repository that is not there. The
	// branch listing for the repo itself is what fails.
	missing := r.dir + "-does-not-exist"

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(Worktree{Path: "/tmp/gone", Repo: missing, Branch: "polecat-gone1"}),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// The repository could not be listed, so it is a coverage row with an error
	// rather than a silent omission — and it is still IN repos[].
	var found bool
	for _, c := range rep.Repos {
		if c.Repo == missing {
			found = true
			if c.Error == "" {
				t.Errorf("coverage for %s has no error; an unlistable repo discovered from a "+
					"worktree must not read like a clean one", missing)
			}
		}
	}
	if !found {
		t.Errorf("repo %s absent from repos[]; a repository the sweep could not read must never "+
			"drop out silently — that is the whole principle.\n%s", missing, Render(rep, true))
	}
}

// --- GAP 2: the repository no open item names --------------------------------

// TestRepoHoldingWorktreesButNamedByNoOpenItemIsCovered is the macguffin shape.
// It appeared NOWHERE in the old report — not in repos[], not as an error, not as
// a count — so the output was indistinguishable from one where every repository
// on the box had been scanned.
//
// Note what this test does NOT assert: there is no finding. gt-ffbd was clean, 0
// unpushed commits, present on origin. The defect is that the report could not
// distinguish that from the other case, so the fix is a COVERAGE row and not a
// finding.
func TestRepoHoldingWorktreesButNamedByNoOpenItemIsCovered(t *testing.T) {
	named := newRepo(t)
	unnamed := newRepo(t)
	unnamed.branch("polecat-gt-ffbd", "main")
	unnamed.commit("clean.md", "feat: pushed work on a closed item (mg-e7ff)")
	unnamed.push("polecat-gt-ffbd")
	unnamed.checkout("main")

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: named.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(Worktree{Path: "/tmp/gt-ffbd", Repo: unnamed.dir, Branch: "polecat-gt-ffbd"}),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var cov *RepoCoverage
	for i := range rep.Repos {
		if rep.Repos[i].Repo == unnamed.dir {
			cov = &rep.Repos[i]
		}
	}
	if cov == nil {
		t.Fatalf("repo %s absent from repos[]; it holds a polecat worktree and no open item names "+
			"it, which used to make it invisible — the output was indistinguishable from a full "+
			"sweep.\n%s", unnamed.dir, Render(rep, true))
	}
	if cov.NamedByOpenItem {
		t.Error("NamedByOpenItem = true; no open item names this repo, and a reader has to be able " +
			"to tell a discovered repo from a board-named one")
	}
	if cov.Worktrees != 1 {
		t.Errorf("Worktrees = %d, want 1", cov.Worktrees)
	}
	if rep.ReposDiscovered != 1 {
		t.Errorf("ReposDiscovered = %d, want 1", rep.ReposDiscovered)
	}
	out := Render(rep, false)
	if !strings.Contains(out, "NO OPEN ITEM NAMES THIS REPO") {
		t.Errorf("render does not say the repo is named by no open item:\n%s", out)
	}
}

// --- GAP 3: --repo that matches nothing --------------------------------------

// TestBareNameAndFictionalRepoAreNoLongerIndistinguishableFromClean reproduces
// the exact measured triple. All three printed byte-identical all-clears and
// exited 0; the absolute path found a real finding.
//
// This is the worst of the three gaps because it FABRICATES AGREEMENT: it was
// found inside evidence mailed to argue a conclusion the command appeared to
// support, and anyone re-running the quoted form would have read the all-clear as
// confirmation.
func TestBareNameAndFictionalRepoAreNoLongerIndistinguishableFromClean(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-w6476", "main")
	r.commit("work.md", "feat: stranded work (mg-6476)")
	r.push("polecat-w6476")
	r.checkout("main")

	opts := func(repos ...string) Options {
		return Options{
			Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir, Title: "the finding"}),
			LiveAgents: fleet(),
			Worktrees:  trees(),
			Repos:      repos,
			Target:     "main",
		}
	}

	// The control: the absolute path. One finding, and NOT blind.
	abs, err := Scan(opts(r.dir))
	if err != nil {
		t.Fatalf("Scan(absolute): %v", err)
	}
	if _, ok := rowFor(abs, "mg-6476"); !ok {
		t.Fatalf("control failed: the absolute --repo form must still find the row.\n%s", Render(abs, true))
	}
	if why := abs.Blind(); why != "" {
		t.Fatalf("control failed: Blind() = %q on a run that resolved a real repository", why)
	}

	// The two broken forms. Each must be BLIND, and their renders must not read
	// as the clean one.
	for _, name := range []string{"one_third_width_three", "this-repo-does-not-exist-anywhere"} {
		rep, serr := Scan(opts(name))
		if serr != nil {
			t.Fatalf("Scan(%q): %v", name, serr)
		}
		if rep.Blind() == "" {
			t.Errorf("--repo %q: Blind() = \"\"; a value naming no repository this sweep can see "+
				"measured nothing, by the command's own definition of exit 3", name)
		}
		if len(rep.ReposUnmatched) != 1 || rep.ReposUnmatched[0].Repo != name {
			t.Errorf("--repo %q: ReposUnmatched = %v, want exactly this name", name, rep.ReposUnmatched)
		}
		out := Render(rep, false)
		if strings.Contains(out, "No open work item has work already sitting on a branch.") {
			t.Errorf("--repo %q renders the all-clear sentence; that sentence is the one that "+
				"travels, and it is what manufactured support for a claim.\n%s", name, out)
		}
		if !strings.Contains(out, "MATCHED NOTHING") {
			t.Errorf("--repo %q render does not say it matched nothing:\n%s", name, out)
		}
		if !strings.Contains(out, "NO VERDICT") {
			t.Errorf("--repo %q render gives a verdict over zero repositories:\n%s", name, out)
		}
		// And the strongest form of the assertion: it must not render the same as
		// a clean run over a real repository.
		if out == Render(abs, false) {
			t.Errorf("--repo %q renders byte-identically to the absolute form", name)
		}
	}
}

// TestAnUnmatchedRepoIsBlindEvenWhenAnotherMatched. A typo in one of several
// --repo values would otherwise answer confidently about the subset — the exact
// "bounded answer read as a census" shape this ticket is about.
func TestAnUnmatchedRepoIsBlindEvenWhenAnotherMatched(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(),
		Repos:      []string{r.dir, "typo-in-this-one"},
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.Blind() == "" {
		t.Errorf("Blind() = \"\" with one unmatched --repo; the caller asked about a repository and "+
			"got no answer about it, and answering on the subset is the defect.\n%s", Render(rep, true))
	}
}

// TestARealRepoWithNoOpenItemsIsANarrowerSweepAndNotABlindOne is the control
// that keeps the exit-3 rule from being a blunt instrument. `--repo <a real
// repository the board happens not to name>` is a legitimate narrower sweep: it
// resolves, it is scanned, and its coverage row says so.
func TestARealRepoWithNoOpenItemsIsANarrowerSweepAndNotABlindOne(t *testing.T) {
	named := newRepo(t)
	other := newRepo(t)

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: named.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(),
		Repos:      []string{other.dir},
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if why := rep.Blind(); why != "" {
		t.Errorf("Blind() = %q; %s is a real git repository that resolved and was scanned", why, other.dir)
	}
	if len(rep.Repos) != 1 || rep.Repos[0].Repo != other.dir {
		t.Fatalf("repos = %+v, want exactly %s", rep.Repos, other.dir)
	}
	if rep.Repos[0].NamedByOpenItem {
		t.Error("NamedByOpenItem = true; no open item names it")
	}
	if rep.ItemsOutOfScope != 1 {
		t.Errorf("ItemsOutOfScope = %d, want 1", rep.ItemsOutOfScope)
	}
}

// --- THE FRAME ---------------------------------------------------------------

// TestFrameIsStatedOnACleanRun is the cheapest of the three repairs and the one
// with the widest catch. On 2026-08-19 a coordinator read a clean sweep as the
// population of at-risk work and acted on it; the output was correct and it was
// read correctly, and the failure was entirely in what the output did not say
// about itself.
//
// Neither row-level repair would have caught the macguffin case — that worktree
// is clean, so no finding fires on it either. Only a stated boundary does.
func TestFrameIsStatedOnACleanRun(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Rows) != 0 {
		t.Fatalf("expected a clean run, got %d rows", len(rep.Rows))
	}
	if len(rep.Frame) == 0 {
		t.Fatal("Frame is empty on a clean run; a boundary stated only when something is found " +
			"is not stated at all — the clean run is the one that gets read as a census")
	}
	joined := strings.Join(rep.Frame, " ")
	for _, want := range []string{"OPEN WORK ITEMS", "outside"} {
		if !strings.Contains(joined, want) {
			t.Errorf("frame does not mention %q:\n%s", want, joined)
		}
	}
	out := Render(rep, false)
	if !strings.Contains(out, "WHAT THIS REPORT CANNOT SEE") {
		t.Errorf("clean render omits the frame:\n%s", out)
	}
	// The all-clear must still be there — the frame qualifies the verdict, it does
	// not replace it.
	if !strings.Contains(out, "No open work item has work already sitting on a branch.") {
		t.Errorf("clean render lost its verdict:\n%s", out)
	}
}

// TestFrameCountsAreDerivedFromTheRunAndNotAsserted. THE REMEDY IS AN ARTIFACT OF
// THE SAME KIND AS THE DEFECT. A frame is a claim about coverage, so a frame that
// carried fixed prose would rot exactly the way a design doc does — and it would
// rot in the direction of overstating reach, which is the defect it repairs. The
// numbers in it must move with the run.
func TestFrameCountsAreDerivedFromTheRunAndNotAsserted(t *testing.T) {
	r := newRepo(t)
	small, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	big, err := Scan(Options{
		Items: board(
			Item{ID: "mg-6476", Status: "available", Repo: r.dir},
			Item{ID: "mg-8bab", Status: "claimed", Repo: r.dir},
		),
		LiveAgents: fleet(),
		Worktrees:  trees(Worktree{Path: "/tmp/x", Repo: r.dir, Branch: "polecat-xyz11"}),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if strings.Join(small.Frame, "\n") == strings.Join(big.Frame, "\n") {
		t.Errorf("the frame is identical across runs with 1 vs 2 items and 0 vs 1 worktrees; "+
			"a boundary that does not move with the run is fixed prose and rots.\nframe:\n%s",
			strings.Join(small.Frame, "\n"))
	}
	if !strings.Contains(strings.Join(small.Frame, " "), fmt.Sprintf("%d item(s)", small.ItemsScanned)) {
		t.Errorf("frame does not carry the run's own item count %d:\n%s",
			small.ItemsScanned, strings.Join(small.Frame, "\n"))
	}
	if !strings.Contains(strings.Join(big.Frame, " "), "1 polecat worktree(s)") {
		t.Errorf("frame does not carry the run's own worktree count:\n%s", strings.Join(big.Frame, "\n"))
	}
}

// TestFrameSaysTheQuestionFailedWhenWorktreesCannotBeEnumerated. "0 repositories
// discovered because there are none" and "0 because the enumeration failed" must
// not render alike — that is the same defect one level down, and it is the one
// mg-8baa fixed for items.
func TestFrameSaysTheQuestionFailedWhenWorktreesCannotBeEnumerated(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  func() ([]Worktree, error) { return nil, errors.New("polecats dir unreadable") },
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.WorktreesUnreadable == "" {
		t.Error("WorktreesUnreadable is empty after the enumeration failed")
	}
	joined := strings.Join(rep.Frame, " ")
	if !strings.Contains(joined, "COULD NOT BE ENUMERATED") {
		t.Errorf("frame does not distinguish a failed enumeration from an empty one:\n%s", joined)
	}
	// And it must differ from the frame of a run where there genuinely are none.
	none, _ := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(),
		Target:     "main",
	})
	if strings.Join(none.Frame, " ") == joined {
		t.Error("a failed worktree enumeration frames identically to a run with no worktrees")
	}
}

// TestFrameSaysWorktreesWereNotConsultedWhenNoSourceIsWired. A caller that wires
// no worktree source gets the OLD population, and the frame must say so rather
// than describing a discovery pass that did not run.
func TestFrameSaysWorktreesWereNotConsultedWhenNoSourceIsWired(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	joined := strings.Join(rep.Frame, " ")
	if !strings.Contains(joined, "were not enumerated on this run") {
		t.Errorf("frame claims a discovery pass that did not run:\n%s", joined)
	}
}

// --- THE NEGATIVE CONTROLS ---------------------------------------------------

// TestTheSameFixtureIsSILENTUnderTheOLDPopULATION is the control that makes
// every assertion above mean something. It runs the identical fixture through
// the code path a caller gets when no worktree source is wired — which IS the
// old behaviour, exactly — and asserts the report is EMPTY and CLEAN.
//
// Without this, "the orphan row fires" proves only that some row exists; it does
// not prove the row was previously impossible, which is the entire claim of the
// ticket. The measured statement was "PolecatBranches() enumerates it correctly.
// It still does not appear."
func TestTheSameFixtureIsSilentUnderTheOldPopulation(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-pc-rev-c5d5a10", "revert: drop the hStep variant (mg-05d3)")

	old, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		// No Worktrees source: the item join and nothing else.
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(old.Rows) != 0 || old.Actionable() {
		t.Fatalf("the control is broken: the item-join-only run already reports %d row(s), so the "+
			"orphan tests above prove nothing about what the join could not see.\n%s",
			len(old.Rows), Render(old, true))
	}
	if !strings.Contains(Render(old, false), "No open work item has work already sitting on a branch.") {
		t.Fatalf("the control is broken: the item-join-only run does not print the all-clear it "+
			"printed on 2026-08-19.\n%s", Render(old, false))
	}
	// Same fixture, worktrees wired: a row.
	now, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(Worktree{Path: "/tmp/pc-rev", Repo: r.dir, Branch: "polecat-pc-rev-c5d5a10"}),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rowsOfKind(now, KindOrphanBranch)) != 1 {
		t.Fatalf("same fixture, worktrees wired: want exactly one orphan row.\n%s", Render(now, true))
	}
	// And the branch IS enumerable by the old machinery, which is what made the
	// silence a population defect rather than a discovery failure.
	if len(old.Repos) != 1 || old.Repos[0].Branches < 1 {
		t.Errorf("PolecatBranches did not see the branch under the old population either; the "+
			"defect being reproduced is that it was enumerated and still not reported: %+v", old.Repos)
	}
}

// TestTheOldPopulationCouldNotSeeTheREPOEither is gap 2's control, and it is the
// stronger of the two: the repository produced NO output at all — not in
// repos[], not as an error, not as a count — so the report was indistinguishable
// from one that had scanned it.
func TestTheOldPopulationCouldNotSeeTheRepoEither(t *testing.T) {
	named := newRepo(t)
	unnamed := newRepo(t)
	unnamed.localOnly("polecat-gt-ffbd", "feat: work on a closed item")

	old, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: named.dir}),
		LiveAgents: fleet(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if strings.Contains(Render(old, true), unnamed.dir) {
		t.Fatalf("the control is broken: %s already appears in the item-join-only report", unnamed.dir)
	}
	for _, c := range old.Repos {
		if c.Repo == unnamed.dir {
			t.Fatalf("the control is broken: %s is already in repos[]", unnamed.dir)
		}
	}
}

// --- THE REMEDY AUDITED AGAINST ITS OWN DEFECT -------------------------------
//
// A remedy is an artifact of the same kind as the defect and is subject to it.
// The defect here is "a member of the population leaves the report without
// appearing in it", and the fix introduced a NEW population — the worktrees —
// with its own ways to lose a member. The three below are the ones found by
// enumerating them, all inside a repair that already demonstrably worked.

// TestAnUnreadableWorktreeIsNamedAndNotDropped. The enumerator's natural shape is
// `if err != nil { continue }`, which makes an unreadable worktree
// indistinguishable from a reaped shell — 19 of the 58 directories on this box
// legitimately are reaped shells, so the silent branch is the well-travelled one.
func TestAnUnreadableWorktreeIsNamedAndNotDropped(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees: trees(
			Worktree{Path: "/tmp/broken", Error: "has a .git but git would not answer"},
			Worktree{Path: "/tmp/detached", Repo: r.dir, Error: "detached HEAD"},
		),
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.WorktreesUnreadableList) != 2 {
		t.Fatalf("WorktreesUnreadableList = %d entries, want 2; a worktree that could not be read "+
			"is not a worktree that holds nothing.\n%s", len(rep.WorktreesUnreadableList), Render(rep, true))
	}
	if rep.WorktreesSeen != 2 {
		t.Errorf("WorktreesSeen = %d, want 2: they were enumerated, they just could not be read", rep.WorktreesSeen)
	}
	out := Render(rep, false)
	for _, want := range []string{"/tmp/broken", "/tmp/detached", "WORKTREE NOT READ"} {
		if !strings.Contains(out, want) {
			t.Errorf("render does not name %q:\n%s", want, out)
		}
	}
	if !strings.Contains(strings.Join(rep.Frame, " "), "COULD NOT BE READ") {
		t.Errorf("frame does not carry the unread worktrees:\n%s", strings.Join(rep.Frame, "\n"))
	}
}

// TestFrameStatesWorktreesINSCOPEAndNotOnlyTheEnumerationCount. Quoting the
// enumeration count where a coverage count belongs is mg-8baa's defect in
// worktree units: under --repo the report enumerated 39 worktrees and entered 0
// of them into the sweep, and a frame saying only "39 were enumerated" overstates
// its own reach exactly as "112 items scanned" did.
func TestFrameStatesWorktreesInScopeAndNotOnlyTheEnumerationCount(t *testing.T) {
	inScope := newRepo(t)
	outOfScope := newRepo(t)

	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: inScope.dir}),
		LiveAgents: fleet(),
		Worktrees: trees(
			Worktree{Path: "/tmp/a", Repo: inScope.dir, Branch: "polecat-aaa11"},
			Worktree{Path: "/tmp/b", Repo: outOfScope.dir, Branch: "polecat-bbb22"},
			Worktree{Path: "/tmp/c", Repo: outOfScope.dir, Branch: "polecat-ccc33"},
		),
		Repos:  []string{inScope.dir},
		Target: "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.WorktreesSeen != 3 || rep.WorktreesInScope != 1 {
		t.Fatalf("WorktreesSeen/InScope = %d/%d, want 3/1", rep.WorktreesSeen, rep.WorktreesInScope)
	}
	joined := strings.Join(rep.Frame, " ")
	if !strings.Contains(joined, "3 polecat worktree(s) were enumerated on this host and 1 entered this sweep") {
		t.Errorf("frame does not distinguish enumerated from in-scope; the enumeration count read "+
			"as coverage is the defect this ticket repairs, one population down:\n%s", joined)
	}
}

// TestFrameDoesNotASSERTWhatItHasNotMeasured. The first draft of the frame said
// polecat branches with no worktree "hold no local-only copy" — a claim about
// hundreds of refs this run never looked at, in the voice of a measurement. A
// boundary statement that smuggles in an unmeasured reassurance is the same
// failure as the report it replaced.
func TestFrameDoesNotAssertWhatItHasNotMeasured(t *testing.T) {
	r := newRepo(t)
	rep, err := Scan(Options{
		Items:      board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents: fleet(),
		Worktrees:  trees(),
		Target:     "main",
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	joined := strings.Join(rep.Frame, " ")
	if strings.Contains(joined, "they hold no local-only copy") {
		t.Error("the frame asserts a property of branches this run never read; a boundary statement " +
			"may say what was not looked at, never what the unlooked-at thing contains")
	}
	if !strings.Contains(joined, "NOT looked at") {
		t.Errorf("frame does not say the unwatched branches were not looked at:\n%s", joined)
	}
}
