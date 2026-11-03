package strandwatch

import (
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
)

// The tests below are drellem2/pogo#160's. The reporter classified all 64
// orphan_branch rows on their host by hand and found every one landed, so the
// detector exited 1 permanently. Each fixture fixes one way a landed branch
// reaches this row, and — the half that matters more — each way a test that
// could NOT establish landing must leave the row reported.

// realReaper is cmd/pogo's adapter, re-stated: the tests run the reaper's own
// predicate rather than a stub of it, so a change to gitgc.BranchDurable that
// stops settling a rebase-landed branch fails here.
func realReaper(repo, branch, target string) (Durability, string) {
	v, detail := gitgc.BranchDurable(repo, branch, target)
	switch v {
	case gitgc.DurabilityDurable:
		return DurabilityDurable, detail
	case gitgc.DurabilityLocalOnly:
		return DurabilityLocalOnly, detail
	default:
		return DurabilityUnknown, detail
	}
}

func noMergedPRs(string, string) ([]MergedPR, error) { return nil, nil }

func orphanOpts(r *repo, branch string) Options {
	return Options{
		Items:         board(Item{ID: "mg-6476", Status: "available", Repo: r.dir}),
		LiveAgents:    fleet(),
		Worktrees:     trees(Worktree{Path: "/tmp/" + branch, Repo: r.dir, Branch: branch}),
		Target:        "main",
		BranchDurable: realReaper,
		MergedPRs:     noMergedPRs,
	}
}

func (r *repo) rev(ref string) string {
	r.t.Helper()
	return strings.TrimSpace(r.git("rev-parse", ref))
}

// squashTwo leaves branch with two local-only commits and lands their combined
// diff on origin/main as ONE new commit, the way a GitHub squash merge does.
// No commit on main is patch-equivalent to either branch commit, so `git
// cherry` reads both as `+` — the population gh#149 measured BranchDurable
// cannot reach. Returns the squash commit.
func (r *repo) squashTwo(branch string) string {
	r.t.Helper()
	r.branch(branch, "main")
	r.commit("orphan.md", "feat: first half (mg-160a)")
	r.commit("orphan.md", "feat: second half (mg-160a)")
	r.checkout("main")
	r.git("merge", "-q", "--squash", branch)
	r.git("commit", "-q", "-m", "feat: both halves (#160)")
	r.push("main")
	return r.rev("HEAD")
}

func settledOrphans(rep Report) []Excluded {
	var out []Excluded
	for _, e := range rep.Excluded {
		if e.Kind == KindOrphanBranch {
			out = append(out, e)
		}
	}
	return out
}

// TestOrphanLandedUnderADifferentShaIsSettledNotReported is the test the
// triage named as missing: the only negative control the orphan row had was a
// branch PUSHED under its own sha. The refinery rebases, so a landed polecat
// branch's commits are on main under NEW shas and on no remote ref under their
// own — which is exactly what LocalOnlyCommits alone reports, and exactly what
// the reaper already clears.
func TestOrphanLandedUnderADifferentShaIsSettledNotReported(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-pc-rev-c5d5a10", "revert: drop the hStep variant (mg-05d3)")
	local := r.rev("polecat-pc-rev-c5d5a10")
	r.commit("other.md", "chore: main moved on")
	r.git("cherry-pick", local)
	r.push("main")
	if r.rev("main") == local {
		t.Fatal("fixture: the landed commit must have a NEW sha, or this is the pushed case")
	}

	rep, err := Scan(orphanOpts(r, "polecat-pc-rev-c5d5a10"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if n := len(rowsOfKind(rep, KindOrphanBranch)); n != 0 {
		t.Fatalf("orphan_branch rows = %d, want 0: the work is on origin/main under a rewritten sha "+
			"and the reaper calls the branch durable.\n%s", n, Render(rep, true))
	}
	s := settledOrphans(rep)
	if len(s) != 1 || s[0].Branch != "polecat-pc-rev-c5d5a10" || rep.OrphansLanded != 1 {
		t.Fatalf("settled = %+v, OrphansLanded = %d; want the branch SETTLED into the exclusions, "+
			"named and counted — a suppression nobody can see is a miss", s, rep.OrphansLanded)
	}
	if !strings.Contains(s[0].Reason, "patch-equivalent") {
		t.Errorf("Reason = %q, want the reaper's own detail line", s[0].Reason)
	}
	if rep.Actionable() {
		t.Errorf("Actionable() = true; a landed orphan must not drive exit 1.\n%s", Render(rep, false))
	}
	out := Render(rep, false)
	if !strings.Contains(out, "1 orphan branch(es), all landed") {
		t.Errorf("default render lacks the all-landed count line:\n%s", out)
	}
	if strings.Contains(out, "polecat-pc-rev-c5d5a10") {
		t.Errorf("default render names the settled branch; it belongs under --all:\n%s", out)
	}
	if all := Render(rep, true); !strings.Contains(all, "polecat-pc-rev-c5d5a10") {
		t.Errorf("--all render does not name the settled branch:\n%s", all)
	}
}

// TestGenuinelyUnlandedOrphanStillExitsNonZero is the bar the mayor set on the
// triage: quieter must not cost the signal. Both tests run and neither can
// settle, so the row and the exit code are exactly what they were.
func TestGenuinelyUnlandedOrphanStillExitsNonZero(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-p516e", "feat: the only copy of this work (mg-516e)")

	rep, err := Scan(orphanOpts(r, "polecat-p516e"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	rows := rowsOfKind(rep, KindOrphanBranch)
	if len(rows) != 1 || !rep.Actionable() {
		t.Fatalf("orphan rows = %d, Actionable = %v; want 1 and true.\n%s", len(rows), rep.Actionable(), Render(rep, true))
	}
	if rep.OrphansLanded != 0 {
		t.Errorf("OrphansLanded = %d, want 0", rep.OrphansLanded)
	}
	if !strings.Contains(rows[0].Durability, "exist ONLY on this local ref") {
		t.Errorf("Durability = %q, want the reaper's local-only detail on the row", rows[0].Durability)
	}
	if !strings.Contains(rows[0].LandedCheck, "no MERGED pull request") {
		t.Errorf("LandedCheck = %q, want the squash test's answer stated", rows[0].LandedCheck)
	}
	if out := Render(rep, false); strings.Contains(out, "all landed") {
		t.Errorf("render claims all landed over a genuine orphan:\n%s", out)
	}
}

// TestSquashLandedOrphanIsSettledByItsMergedPR is the reporter's test #1: the
// branch the reaper cannot clear, because a squash merge leaves no patch id
// behind, is settled by a MERGED PR whose head is the branch tip and whose
// merge commit is an ancestor of the target.
func TestSquashLandedOrphanIsSettledByItsMergedPR(t *testing.T) {
	r := newRepo(t)
	squash := r.squashTwo("polecat-p160a")
	tip := r.rev("polecat-p160a")

	opts := orphanOpts(r, "polecat-p160a")
	// Control: the reaper alone does NOT settle this branch. Without this the
	// test would pass on a fixture tier 1 already handles.
	opts.MergedPRs = noMergedPRs
	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rowsOfKind(rep, KindOrphanBranch)) != 1 {
		t.Fatalf("control: the reaper alone settled a squash-landed branch; fixture is not the squash "+
			"case.\n%s", Render(rep, true))
	}

	opts.MergedPRs = func(repo, branch string) ([]MergedPR, error) {
		if branch != "polecat-p160a" {
			t.Errorf("MergedPRs asked about %q", branch)
		}
		return []MergedPR{{Number: 160, MergeCommit: squash, HeadCommit: tip}}, nil
	}
	rep, err = Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if n := len(rowsOfKind(rep, KindOrphanBranch)); n != 0 || rep.Actionable() {
		t.Fatalf("orphan rows = %d, Actionable = %v; want the squash test to settle it.\n%s",
			n, rep.Actionable(), Render(rep, true))
	}
	s := settledOrphans(rep)
	if len(s) != 1 || !strings.Contains(s[0].Reason, "PR #160") {
		t.Errorf("settled = %+v, want the PR named in the reason", s)
	}
}

// TestUnavailableGhLeavesTheOrphanReported is the degraded path the triage
// named as the one that rots: it only runs when something is broken. A gh that
// is missing, unauthenticated or rate-limited must cost a suppression and
// never a finding — and must SAY the test did not run.
func TestUnavailableGhLeavesTheOrphanReported(t *testing.T) {
	r := newRepo(t)
	r.squashTwo("polecat-p160b")

	opts := orphanOpts(r, "polecat-p160b")
	opts.MergedPRs = func(string, string) ([]MergedPR, error) {
		return nil, errors.New("gh pr list --head polecat-p160b: HTTP 401: Bad credentials")
	}
	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	rows := rowsOfKind(rep, KindOrphanBranch)
	if len(rows) != 1 || !rep.Actionable() {
		t.Fatalf("orphan rows = %d, Actionable = %v; a failed gh query must leave the row REPORTED.\n%s",
			len(rows), rep.Actionable(), Render(rep, true))
	}
	if !strings.Contains(rows[0].LandedCheck, "DID NOT RUN") || !strings.Contains(rows[0].LandedCheck, "401") {
		t.Errorf("LandedCheck = %q, want the failure stated, not skipped", rows[0].LandedCheck)
	}
	if out := Render(rep, false); !strings.Contains(out, "DID NOT RUN") {
		t.Errorf("render does not say the squash test did not run:\n%s", out)
	}
}

// TestCommitsAfterTheMergeAreNotSettledByThePR guards the hole the squash test
// would otherwise open: a polecat that kept committing after its PR merged
// holds work the merge never carried. The merge commit IS an ancestor of main;
// that alone must not settle the branch.
func TestCommitsAfterTheMergeAreNotSettledByThePR(t *testing.T) {
	r := newRepo(t)
	squash := r.squashTwo("polecat-p160c")
	mergedHead := r.rev("polecat-p160c")
	r.checkout("polecat-p160c")
	r.commit("later.md", "feat: written after the PR merged (mg-160c)")
	r.checkout("main")

	opts := orphanOpts(r, "polecat-p160c")
	opts.MergedPRs = func(string, string) ([]MergedPR, error) {
		return []MergedPR{{Number: 161, MergeCommit: squash, HeadCommit: mergedHead}}, nil
	}
	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	rows := rowsOfKind(rep, KindOrphanBranch)
	if len(rows) != 1 || !rep.Actionable() {
		t.Fatalf("orphan rows = %d; a branch with commits after its merged head must stay reported.\n%s",
			len(rows), Render(rep, true))
	}
	if !strings.Contains(rows[0].LandedCheck, "never carried") {
		t.Errorf("LandedCheck = %q, want the uncovered tip named", rows[0].LandedCheck)
	}
}

// TestAMergeCommitThisCloneDoesNotHoldIsNotALanding: the PR says merged, but
// the merge commit is not in this clone (or not on the target). git answers
// with an error, not a "no", and an error must not settle.
func TestAMergeCommitThisCloneDoesNotHoldIsNotALanding(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-p160d", "feat: claimed merged elsewhere (mg-160d)")
	tip := r.rev("polecat-p160d")

	opts := orphanOpts(r, "polecat-p160d")
	opts.MergedPRs = func(string, string) ([]MergedPR, error) {
		return []MergedPR{{Number: 162, MergeCommit: strings.Repeat("ab", 20), HeadCommit: tip}}, nil
	}
	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rowsOfKind(rep, KindOrphanBranch)) != 1 || rep.OrphansLanded != 0 {
		t.Fatalf("a merge commit absent from this clone settled the branch.\n%s", Render(rep, true))
	}
}

// TestAnUnknownDurabilityVerdictIsUnjudgedNotSettled: the reaper could not
// judge, so neither can this report. The row becomes KindUnjudged, which
// already reaches the exit code.
func TestAnUnknownDurabilityVerdictIsUnjudgedNotSettled(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-p160e", "feat: the reaper cannot judge this (mg-160e)")

	opts := orphanOpts(r, "polecat-p160e")
	opts.BranchDurable = func(string, string, string) (Durability, string) {
		return DurabilityUnknown, "git cherry could not compare"
	}
	opts.MergedPRs = func(string, string) ([]MergedPR, error) {
		t.Error("the squash test must not settle a branch the reaper could not judge")
		return nil, nil
	}
	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	un := rowsOfKind(rep, KindUnjudged)
	if len(un) != 1 || un[0].Branch != "polecat-p160e" || !rep.Actionable() || rep.OrphansLanded != 0 {
		t.Fatalf("unjudged = %+v, Actionable = %v; want one unjudged row.\n%s", un, rep.Actionable(), Render(rep, true))
	}
	if !strings.Contains(un[0].Error, "git cherry could not compare") {
		t.Errorf("Error = %q, want the reaper's detail", un[0].Error)
	}
}

// TestMixedLandedAndUnlandedOrphansCountBoth: one landed, one not. The count
// line must not say "all landed", and the genuine one still drives exit 1.
func TestMixedLandedAndUnlandedOrphansCountBoth(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-p160f", "feat: landed by rebase (mg-160f)")
	r.commit("other.md", "chore: main moved on")
	r.git("cherry-pick", r.rev("polecat-p160f"))
	r.push("main")
	r.localOnly("polecat-p160g", "feat: the only copy (mg-160g)")

	opts := orphanOpts(r, "x")
	opts.Worktrees = trees(
		Worktree{Path: "/tmp/f", Repo: r.dir, Branch: "polecat-p160f"},
		Worktree{Path: "/tmp/g", Repo: r.dir, Branch: "polecat-p160g"},
	)
	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	rows := rowsOfKind(rep, KindOrphanBranch)
	if len(rows) != 1 || rows[0].Branch != "polecat-p160g" || rep.OrphansLanded != 1 || !rep.Actionable() {
		t.Fatalf("rows = %d, landed = %d, Actionable = %v.\n%s", len(rows), rep.OrphansLanded, rep.Actionable(), Render(rep, true))
	}
	out := Render(rep, false)
	if strings.Contains(out, "all landed") || !strings.Contains(out, "1 orphan branch(es) landed and settled") {
		t.Errorf("count line wrong for a mixed population:\n%s", out)
	}
}

// TestUnwiredLandednessTestsAreStatedInTheFrame: a run without either source
// reports every local-only orphan (the old behaviour) and says why.
func TestUnwiredLandednessTestsAreStatedInTheFrame(t *testing.T) {
	r := newRepo(t)
	r.localOnly("polecat-p160h", "feat: x (mg-160h)")
	opts := orphanOpts(r, "polecat-p160h")
	opts.BranchDurable, opts.MergedPRs = nil, nil
	rep, err := Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	frame := strings.Join(rep.Frame, "\n")
	for _, want := range []string{"durability test (gitgc.BranchDurable) was NOT", "squash test was NOT consulted"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame lacks %q:\n%s", want, frame)
		}
	}
	rows := rowsOfKind(rep, KindOrphanBranch)
	if len(rows) != 1 || !strings.Contains(rows[0].LandedCheck, "NOT CONSULTED") {
		t.Errorf("rows = %+v; want the row reported and saying the test was not consulted", rows)
	}
}
