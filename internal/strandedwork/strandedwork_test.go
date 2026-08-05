package strandedwork

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests below run against REAL git repositories rather than a faked command
// runner, and that is deliberate. Every fact this package reports is a fact about
// git's own patch-identity arithmetic — `git cherry` deciding that a
// rebase-rewritten commit is the same commit — and a fake runner would only
// prove that the parser reads strings the author already believed git emits. The
// negative control in TestInspectRebasedBranchIsClean is the one that cannot be
// written any other way: it is the case where a plausible implementation
// (`rev-list target..branch`) passes every unit test and fires on every healthy
// branch in production.

// repo is a throwaway git repository with an origin, built per test.
type repo struct {
	t      *testing.T
	dir    string
	origin string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	r := &repo{t: t, dir: filepath.Join(root, "work"), origin: filepath.Join(root, "origin.git")}

	run(t, root, "git", "init", "--bare", "--initial-branch=main", r.origin)
	run(t, root, "git", "init", "--initial-branch=main", r.dir)
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.git("config", "commit.gpgsign", "false")
	r.git("remote", "add", "origin", r.origin)

	r.commit("main", "chore: initial commit")
	r.git("push", "-q", "origin", "main")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	return run(r.t, r.dir, "git", args...)
}

// commit writes file and commits it with the given subject.
func (r *repo) commit(file, subject string) string {
	r.t.Helper()
	path := filepath.Join(r.dir, file)
	prev, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(prev, []byte(subject+"\n")...), 0644); err != nil {
		r.t.Fatalf("write %s: %v", path, err)
	}
	r.git("add", file)
	r.git("commit", "-q", "-m", subject)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

// branch creates and checks out a new branch off ref.
func (r *repo) branch(name, ref string) {
	r.t.Helper()
	r.git("checkout", "-q", "-b", name, ref)
}

func (r *repo) checkout(ref string) {
	r.t.Helper()
	r.git("checkout", "-q", ref)
}

func (r *repo) push(branch string) {
	r.t.Helper()
	r.git("push", "-q", "origin", branch)
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// A committer identity has to exist even for `git init`-time hooks on hosts
	// with no global config; -c on every call would be noise, so set it here.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// --- Case (a): finished work pushed and never merged -------------------------

// TestInspectPushedUnmergedBranchSaysResubmit is the mg-9a19 shape: a polecat
// pushed finished work, the merge never landed, and the item went back to
// available/. The check has to say RESUBMIT — not "dispatch a worker".
func TestInspectPushedUnmergedBranchSaysResubmit(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-9a19", "main")
	r.commit("audit.md", "feat(audit): drift battery, all five cases caught (mg-9a19)")
	r.push("polecat-9a19")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-9a19", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !f.Stranded() {
		t.Fatalf("branch with pushed unmerged work reported as not stranded: %+v", f)
	}
	if f.Disposition != DispositionResubmit {
		t.Errorf("disposition = %q, want %q", f.Disposition, DispositionResubmit)
	}
	if !f.Pushed {
		t.Errorf("Pushed = false; the work was pushed and the finding must say so (ref=%s)", f.Ref)
	}
	if f.Ref != "refs/remotes/origin/polecat-9a19" {
		t.Errorf("Ref = %q, want the remote-tracking ref: it is the copy a resubmit would merge", f.Ref)
	}
	if len(f.Unmerged) != 1 {
		t.Fatalf("Unmerged = %d commit(s), want 1: %+v", len(f.Unmerged), f.Unmerged)
	}
	if f.WorkItemID != "mg-9a19" {
		t.Errorf("WorkItemID = %q, want mg-9a19 (recovered from the commit subject)", f.WorkItemID)
	}
	if f.PreRegistration != nil {
		t.Errorf("PreRegistration = %+v, want nil: no commit here records predictions", f.PreRegistration)
	}
	if s := f.Summary(); !strings.Contains(s, "Resubmit") || strings.Contains(s, "dispatch a worker at this item,") == false {
		t.Errorf("Summary does not name the remedy: %q", s)
	}
}

// --- Case (b): an unmerged pre-registration commit ---------------------------

// TestInspectPreRegistrationBranchRefusesPlainRedispatch is the mg-f3ff /
// mg-fcb2 shape, and the one where getting it wrong is silent. The check must
// report a DIFFERENT disposition from case (a), name the commit a re-dispatch
// has to branch from, and say not to amend it.
func TestInspectPreRegistrationBranchRefusesPlainRedispatch(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-f3ff", "main")
	preSHA := r.commit("predictions.md", "predictions: the drift battery will catch 3 of 5 cases (mg-f3ff)")
	r.push("polecat-f3ff")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-f3ff", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Disposition != DispositionPreRegistration {
		t.Fatalf("disposition = %q, want %q — a plain resubmit verdict here loses the control silently",
			f.Disposition, DispositionPreRegistration)
	}
	if f.PreRegistration == nil {
		t.Fatal("PreRegistration is nil: a re-dispatch has no commit to branch from")
	}
	if f.PreRegistration.SHA != preSHA {
		t.Errorf("PreRegistration.SHA = %s, want %s (the commit a re-dispatch must branch FROM)",
			f.PreRegistration.SHA, preSHA)
	}
	s := f.Summary()
	for _, want := range []string{"PRE-REGISTRATION", "unamended", preSHA[:12]} {
		if !strings.Contains(s, want) {
			t.Errorf("Summary missing %q — a reader acting on this line would branch from the target: %q", want, s)
		}
	}
}

// TestPreRegistrationOutranksResubmit: a branch carrying BOTH ordinary work and
// an unmerged pre-registration commit must report the pre-registration
// disposition. The two verdicts are not additive — following resubmit advice on
// a pre-registration branch is harmless, following resubmit advice INSTEAD of
// pre-registration advice is the corruption.
func TestPreRegistrationOutranksResubmit(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-fcb2", "main")
	preSHA := r.commit("predictions.md", "predictions: two of the six will fail")
	r.commit("analysis.md", "feat(analysis): script the battery (mg-fcb2)")
	r.push("polecat-fcb2")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-fcb2", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Disposition != DispositionPreRegistration {
		t.Fatalf("disposition = %q, want %q", f.Disposition, DispositionPreRegistration)
	}
	if f.PreRegistration == nil || f.PreRegistration.SHA != preSHA {
		t.Fatalf("PreRegistration = %+v, want the OLDEST pre-registration commit %s", f.PreRegistration, preSHA)
	}
	if len(f.Unmerged) != 2 {
		t.Errorf("Unmerged = %d, want 2 (both commits are stranded)", len(f.Unmerged))
	}
	if f.WorkItemID != "mg-fcb2" {
		t.Errorf("WorkItemID = %q, want mg-fcb2", f.WorkItemID)
	}
}

// TestPreRegistrationIsFoundBeneathFourCommits is the polecat-65eb shape,
// measured off the real branch by doctor during the incident triage:
//
//	afafa50  evidence: the committed transcript ...        <- tip
//	09009a3  docs+audit: ...
//	eff227c  audit: ...
//	cb97486  audit: mg-65eb's ROW LEDGER ...
//	880fc15  predictions: mg-65eb's predictions ...        <- base
//
// A pre-registration commit is by construction the FIRST commit on its branch,
// so it gets buried as the work proceeds — which means a tip-only check fails on
// exactly the branches carrying the most work. Doctor's census of the five
// affected branches:
//
//	b2af  1 commit   tip IS the predictions: commit   tip-only: right
//	d53d  1 commit   tip IS the predictions: commit   tip-only: right
//	f3ff  3 commits  predictions: at the base         tip-only: WRONG
//	fcb2  3 commits  predictions: at the base         tip-only: WRONG
//	65eb  5 commits  predictions: at the base         tip-only: WRONG
//
// And the failure is not a missing warning. Those branches fall through to
// DispositionResubmit, whose advice is "submit the branch" — told to a
// mid-audit branch, that pushes incomplete work into the refinery over the very
// commit that must not be disturbed.
//
// This test discriminates the two implementations and nothing else does at this
// depth: narrow the loop in Inspect to the tip commit and only this test and
// TestPreRegistrationOutranksResubmit go red.
func TestPreRegistrationIsFoundBeneathFourCommits(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-65eb", "main")
	preSHA := r.commit("predictions.md", "predictions: mg-65eb's predictions for the INDEPENDENT AUDIT")
	r.commit("ledger.md", "audit: mg-65eb's ROW LEDGER")
	r.commit("audit.md", "audit: the battery")
	r.commit("docs.md", "docs+audit: write it up")
	tip := r.commit("evidence.md", "evidence: the committed transcript of the mg-65eb suite")
	r.push("polecat-65eb")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-65eb", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(f.Unmerged) != 5 {
		t.Fatalf("Unmerged = %d, want 5 — the fixture is not at the depth that discriminates", len(f.Unmerged))
	}
	if f.Unmerged[len(f.Unmerged)-1].SHA != tip {
		t.Fatalf("commits are not oldest-first; the tip is at index 0, so 'buried' means nothing here")
	}
	if f.Disposition != DispositionPreRegistration {
		t.Fatalf("disposition = %q, want %q — the pre-registration commit is four commits down, "+
			"and a resubmit verdict here tells the next actor to push a mid-audit branch into "+
			"the refinery over the commit that must not be disturbed",
			f.Disposition, DispositionPreRegistration)
	}
	if f.PreRegistration == nil || f.PreRegistration.SHA != preSHA {
		t.Fatalf("PreRegistration = %+v, want the base commit %s", f.PreRegistration, preSHA)
	}
}

// TestMergedPreRegistrationDoesNotForceThatDisposition draws the other edge of
// case (b). A pre-registration commit that ALREADY landed on the target is safe:
// a worker branching from the target inherits it and cannot amend it. Reporting
// pre-registration there would refuse dispatches that are perfectly fine, which
// is how a gate earns a reputation for being wrong and gets disarmed.
func TestMergedPreRegistrationDoesNotForceThatDisposition(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-aaaa", "main")
	r.commit("predictions.md", "predictions: the battery will catch 3 of 5")
	r.push("polecat-aaaa")
	// The predictions land on main; follow-up work stays on the branch.
	r.checkout("main")
	r.git("merge", "-q", "--ff-only", "polecat-aaaa")
	r.push("main")
	r.checkout("polecat-aaaa")
	r.commit("analysis.md", "feat(analysis): results (mg-aaaa)")
	r.push("polecat-aaaa")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-aaaa", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Disposition != DispositionResubmit {
		t.Fatalf("disposition = %q, want %q: the pre-registration commit is already on the target, "+
			"so branching from the target preserves it", f.Disposition, DispositionResubmit)
	}
	if f.PreRegistration != nil {
		t.Errorf("PreRegistration = %+v, want nil", f.PreRegistration)
	}
}

// --- Negative controls: the check must be silent on healthy input ------------

// TestInspectRebasedBranchIsClean is the control that decides whether this check
// is usable at all. The refinery merges by rebasing onto the target, so a branch
// that merged SUCCESSFULLY has every commit present upstream under a different
// sha. The obvious implementation — `rev-list target..branch` — reports all of
// them as absent and fires on every healthy merged branch in the repo.
//
// This is also the test whose answer would DIFFER under that implementation:
// swap `git cherry` for `rev-list` in cherry() and this test, and only this
// test, goes red.
func TestInspectRebasedBranchIsClean(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-bbbb", "main")
	branchSHA := r.commit("feature.md", "feat: the work (mg-bbbb)")
	r.push("polecat-bbbb")

	// main moves on, then takes the branch's patch under a NEW sha — exactly
	// what the refinery's rebase-then-ff-merge produces.
	r.checkout("main")
	r.commit("other.md", "chore: unrelated main commit")
	r.git("cherry-pick", branchSHA)
	r.push("main")

	f, err := Inspect(r.dir, "polecat-bbbb", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Stranded() {
		t.Fatalf("a merged (rebase-rewritten) branch reported as STRANDED: %s\n"+
			"a detector that fires on healthy input teaches its readers to skip the line", f.Summary())
	}
	if f.Equivalent != 1 {
		t.Errorf("Equivalent = %d, want 1: the commit is upstream under a different sha", f.Equivalent)
	}
	if newSHA := strings.TrimSpace(r.git("rev-parse", "refs/remotes/origin/main")); newSHA == branchSHA {
		t.Fatalf("test is not exercising a rewrite: main tip %s equals the branch commit", newSHA)
	}
}

// TestInspectAbsentBranchIsCleanButNotFound: "there is no branch" and "the
// branch is merged" are different facts and Found is what separates them.
func TestInspectAbsentBranchIsCleanButNotFound(t *testing.T) {
	r := newRepo(t)
	f, err := Inspect(r.dir, "polecat-nope", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Stranded() || f.Found {
		t.Fatalf("absent branch: Stranded=%v Found=%v, want false/false", f.Stranded(), f.Found)
	}
	if !strings.Contains(f.Summary(), "no branch") {
		t.Errorf("Summary does not distinguish absence from merged: %q", f.Summary())
	}
}

// --- The unpushed case -------------------------------------------------------

// TestInspectLocalOnlyBranchIsStranded. A polecat that committed but wedged
// before pushing has work that exists only on a local head — and the worktree
// holding it is precisely what git-gc reaps once the agent is gone. That makes
// local-only work the more urgent case, not a lesser one, so it must be reported
// with Pushed=false rather than skipped.
func TestInspectLocalOnlyBranchIsStranded(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-cccc", "main")
	r.commit("wip.md", "feat: never pushed (mg-cccc)")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-cccc", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !f.Stranded() {
		t.Fatalf("local-only work reported as not stranded: %+v", f)
	}
	if f.Pushed {
		t.Errorf("Pushed = true for a branch that was never pushed (ref=%s)", f.Ref)
	}
	if f.Ref != "refs/heads/polecat-cccc" {
		t.Errorf("Ref = %q, want the local head", f.Ref)
	}
}

// --- The target must never fail into a clean verdict -------------------------

// TestUnresolvableTargetIsAnError. "The comparison could not be made" reported
// as "nothing is stranded" is the exact shape of the defect this package closes,
// one level up. It must be an error.
func TestUnresolvableTargetIsAnError(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-dddd", "main")
	r.commit("x.md", "feat: work (mg-dddd)")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-dddd", "no-such-target")
	if err == nil {
		t.Fatalf("Inspect against an unresolvable target returned no error, finding=%+v", f)
	}
	if f.Stranded() {
		t.Errorf("errored finding also claims stranded work; callers must not act on it")
	}
	if !strings.Contains(err.Error(), "no-such-target") {
		t.Errorf("error does not name the unresolved target: %v", err)
	}
}

// TestResolveTargetPrefersOrigin. A local main that is behind origin would
// report already-merged work as stranded. The shared history is the one that
// decides whether work "landed".
func TestResolveTargetPrefersOrigin(t *testing.T) {
	r := newRepo(t)
	got, err := ResolveTarget(r.dir, "main")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if got != "refs/remotes/origin/main" {
		t.Errorf("ResolveTarget = %q, want refs/remotes/origin/main", got)
	}
	// An empty target asks the repository, and must land on the same ref.
	got, err = ResolveTarget(r.dir, "")
	if err != nil {
		t.Fatalf("ResolveTarget(\"\"): %v", err)
	}
	if got != "refs/remotes/origin/main" {
		t.Errorf("ResolveTarget(\"\") = %q, want refs/remotes/origin/main", got)
	}
}

// --- Fetch -------------------------------------------------------------------

// TestFetchFailureDoesNotDisarmTheCheck. The incident that produced this package
// was a NETWORK OUTAGE: five polecats wedged when github became unreachable, and
// they were stopped — and would have been re-dispatched — while it was still
// unreachable. A check that declines to answer without a successful fetch is off
// during exactly the window it exists for.
//
// So: origin is broken, Fetch reports the failure honestly, and Inspect still
// returns the right verdict from the refs on disk. A push made from the
// polecat's own worktree already updated the local remote-tracking ref, which is
// why the on-disk answer is usually the right one anyway.
func TestFetchFailureDoesNotDisarmTheCheck(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-9a19", "main")
	r.commit("audit.md", "feat(audit): finished (mg-9a19)")
	r.push("polecat-9a19")
	r.checkout("main")

	// Break origin the way an outage does: the ref is still on disk, the remote
	// is not reachable.
	r.git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	fresh, err := Fetch(r.dir)
	if err == nil || fresh {
		t.Fatalf("Fetch against an unreachable origin: fresh=%v err=%v, want false and an error", fresh, err)
	}

	f, ierr := Inspect(r.dir, "polecat-9a19", "main")
	if ierr != nil {
		t.Fatalf("Inspect after a failed fetch: %v", ierr)
	}
	if f.Disposition != DispositionResubmit {
		t.Fatalf("disposition = %q, want %q — the check stood down because the network was "+
			"down, which is the window it exists for", f.Disposition, DispositionResubmit)
	}
}

// --- Scan --------------------------------------------------------------------

// TestScanFindsBothCasesAndSkipsTheClean walks a repo holding all three shapes
// at once — the population a real host has — and checks that the two stranded
// branches are reported with the RIGHT dispositions and the merged one is not
// reported at all.
func TestScanFindsBothCasesAndSkipsTheClean(t *testing.T) {
	r := newRepo(t)

	// (a) finished work, pushed, unmerged.
	r.branch("polecat-9a19", "main")
	r.commit("audit.md", "feat(audit): the whole thing (mg-9a19)")
	r.push("polecat-9a19")

	// (b) a pre-registration commit, pushed, never submitted.
	r.checkout("main")
	r.branch("polecat-f3ff", "main")
	r.commit("predictions.md", "predictions: three of six will fail")
	r.push("polecat-f3ff")

	// clean: merged by rebase, the healthy majority.
	r.checkout("main")
	r.branch("polecat-bbbb", "main")
	merged := r.commit("done.md", "feat: landed (mg-bbbb)")
	r.push("polecat-bbbb")
	r.checkout("main")
	r.commit("drift.md", "chore: main moved on")
	r.git("cherry-pick", merged)
	r.push("main")

	findings, errs := Scan(r.dir, "main")
	if len(errs) != 0 {
		t.Fatalf("Scan errors: %v", errs)
	}
	byBranch := map[string]Finding{}
	for _, f := range findings {
		byBranch[f.Branch] = f
	}
	if len(findings) != 2 {
		t.Fatalf("Scan found %d stranded branch(es), want 2: %v", len(findings), byBranch)
	}
	if got := byBranch["polecat-9a19"].Disposition; got != DispositionResubmit {
		t.Errorf("polecat-9a19 disposition = %q, want %q", got, DispositionResubmit)
	}
	if got := byBranch["polecat-f3ff"].Disposition; got != DispositionPreRegistration {
		t.Errorf("polecat-f3ff disposition = %q, want %q", got, DispositionPreRegistration)
	}
	if _, ok := byBranch["polecat-bbbb"]; ok {
		t.Errorf("Scan reported the merged branch polecat-bbbb as stranded")
	}
}

// TestScanSkipsNonPolecatBranches keeps the scan from reporting a human's
// long-lived topic branch as abandoned polecat output.
func TestScanSkipsNonPolecatBranches(t *testing.T) {
	r := newRepo(t)
	r.branch("daniel/experiment", "main")
	r.commit("x.md", "wip: personal branch")
	r.push("daniel/experiment")
	r.checkout("main")

	findings, errs := Scan(r.dir, "main")
	if len(errs) != 0 {
		t.Fatalf("Scan errors: %v", errs)
	}
	if len(findings) != 0 {
		t.Fatalf("Scan reported non-polecat branches: %+v", findings)
	}
}

// TestIsPreRegistrationMatching pins the subject-prefix rule: case-insensitive,
// leading whitespace tolerated, and a mention anywhere other than the start does
// not count (a commit ABOUT predictions is not a pre-registration).
func TestIsPreRegistrationMatching(t *testing.T) {
	for _, tc := range []struct {
		subject string
		want    bool
	}{
		{"predictions: a, b, c", true},
		{"Predictions: a, b, c", true},
		{"  predictions: leading space", true},
		{"predictions", false},
		{"feat: record predictions: for later", false},
		{"docs(predictions): explain the format", false},
		{"", false},
	} {
		if got := (Commit{Subject: tc.subject}).IsPreRegistration(); got != tc.want {
			t.Errorf("IsPreRegistration(%q) = %v, want %v", tc.subject, got, tc.want)
		}
	}
}
