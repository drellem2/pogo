package strandedwork

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// drellem2/pogo#147 (mg-dbb75): the stranded-push alert fired on every PR-track
// polecat, because a branch awaiting review and a branch whose work was lost
// look the same against the default branch. Two changes, each tested here:
//
//	(a) Pushed means "on origin under SOME ref", not "origin/<Branch> exists";
//	(b) CheckOpenPR turns a resubmit into awaiting_review when an open PR owns
//	    the head — and a probe that fails leaves the alert standing.

// --- (a) Pushed is an on-origin fact, not a name lookup ----------------------

// TestCrossPushedBranchIsPushed: a local branch whose head is on origin under
// ANOTHER name (cross-pushed onto a PR head, or a checkout of one). Its work is
// durable, so it must not be called LOCAL-ONLY, and the remedy must not begin
// with a push that would only mint a second origin ref for the same commits.
func TestCrossPushedBranchIsPushed(t *testing.T) {
	r := newRepo(t)
	r.branch("feature-under-review", "main")
	r.commit("f.md", "feat: a coworker's change (mg-4b23)")
	r.push("feature-under-review")
	r.branch("polecat-4b23", "feature-under-review")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-4b23", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Ref != "refs/heads/polecat-4b23" {
		t.Fatalf("Ref = %q, want the local head (origin has no polecat-4b23)", f.Ref)
	}
	if !f.Pushed {
		t.Fatalf("Pushed = false for a head that is origin/feature-under-review's tip: %+v", f)
	}
	if f.OriginRef != "refs/remotes/origin/feature-under-review" {
		t.Errorf("OriginRef = %q, want refs/remotes/origin/feature-under-review", f.OriginRef)
	}
	// Still stranded: (a) changes what the alert SAYS, never whether it fires.
	if f.Disposition != DispositionResubmit {
		t.Errorf("disposition = %q, want %q — part (a) must change no verdict", f.Disposition, DispositionResubmit)
	}
	sum := f.Summary()
	if strings.Contains(sum, LocalOnlyWarning) || strings.Contains(sum, "LOCAL-ONLY") {
		t.Errorf("Summary calls durable work local-only: %s", sum)
	}
	if strings.Contains(sum, "push origin") {
		t.Errorf("Summary prescribes a push of work already on origin: %s", sum)
	}
	if !strings.Contains(sum, "pogo refinery submit feature-under-review") {
		t.Errorf("Summary's submit does not name the branch origin has — the refinery refuses any other: %s", sum)
	}
}

// TestLocalOnlyBranchIsStillLocalOnly is the positive control for the test
// above: a head no origin ref reaches keeps the warning and the push.
func TestLocalOnlyBranchIsStillLocalOnly(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-p0fc6", "main")
	r.commit("f.md", "feat: never pushed (mg-0fc6)")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-p0fc6", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Pushed || f.OriginRef != "" {
		t.Fatalf("Pushed=%t OriginRef=%q for a branch on no origin ref", f.Pushed, f.OriginRef)
	}
	if sum := f.Summary(); !strings.Contains(sum, LocalOnlyWarning) || !strings.Contains(sum, "push origin polecat-p0fc6") {
		t.Errorf("local-only Summary lost its warning or its push: %s", sum)
	}
}

// TestOriginRefPrefersAnExactTip: when one origin ref's tip IS the head and
// another merely contains it, the exact one is named — it is the one a submit
// can name without also merging somebody else's later commits.
func TestOriginRefPrefersAnExactTip(t *testing.T) {
	r := newRepo(t)
	r.branch("a-folded", "main")
	r.commit("f.md", "feat: the work (mg-77aa)")
	r.branch("z-exact", "a-folded")
	r.push("z-exact")
	r.checkout("a-folded")
	r.commit("g.md", "feat: somebody's later work (mg-88bb)")
	r.push("a-folded")
	r.branch("polecat-77aa", "z-exact")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-77aa", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.OriginRef != "refs/remotes/origin/z-exact" {
		t.Errorf("OriginRef = %q, want refs/remotes/origin/z-exact (exact tip beats the earlier-sorting container)", f.OriginRef)
	}

	// Folded only: the container is still ON ORIGIN, so still Pushed.
	r.git("push", "-q", "origin", "--delete", "z-exact")
	r.git("update-ref", "-d", "refs/remotes/origin/z-exact")
	f, err = Inspect(r.dir, "polecat-77aa", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !f.Pushed || f.OriginRef != "refs/remotes/origin/a-folded" {
		t.Errorf("Pushed=%t OriginRef=%q, want true and refs/remotes/origin/a-folded", f.Pushed, f.OriginRef)
	}
}

// TestPushedUnderOwnNameKeepsItsOwnRef: the ordinary pushed case is unchanged.
func TestPushedUnderOwnNameKeepsItsOwnRef(t *testing.T) {
	r := newRepo(t)
	r.branch("polecat-9a19", "main")
	r.commit("audit.md", "feat(audit): finished (mg-9a19)")
	r.push("polecat-9a19")
	r.checkout("main")

	f, err := Inspect(r.dir, "polecat-9a19", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !f.Pushed || f.OriginRef != f.Ref || f.OriginBranch() != "polecat-9a19" {
		t.Errorf("Pushed=%t OriginRef=%q Ref=%q OriginBranch=%q", f.Pushed, f.OriginRef, f.Ref, f.OriginBranch())
	}
}

func TestSubmitRemedyNamesTheOriginBranchForCrossPushedWork(t *testing.T) {
	got := SubmitRemedy("/repo", "polecat-4b23", "mg-4b23", "feature-under-review")
	if want := "pogo refinery submit feature-under-review --repo=/repo --author=mg-4b23"; got != want {
		t.Errorf("SubmitRemedy(cross-pushed) = %q, want %q", got, want)
	}
}

// --- (b) awaiting_review, and the direction a failed probe fails -------------

// pushedStranded is a pushed, unmerged, ordinary resubmit finding.
func pushedStranded(t *testing.T) (*repo, Finding) {
	t.Helper()
	r := newRepo(t)
	r.branch("polecat-pa95f", "main")
	r.commit("f.md", "feat: built, PR open, waiting for review (mg-a95f)")
	r.push("polecat-pa95f")
	r.checkout("main")
	f, err := Inspect(r.dir, "polecat-pa95f", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if f.Disposition != DispositionResubmit {
		t.Fatalf("fixture: disposition = %q, want resubmit", f.Disposition)
	}
	return r, f
}

func TestCheckOpenPRMarksAwaitingReview(t *testing.T) {
	r, f := pushedStranded(t)
	var asked string
	f.CheckOpenPR(r.dir, func(dir, branch string) (int, error) {
		asked = branch
		return 146, nil
	})
	if asked != "polecat-pa95f" {
		t.Errorf("probe asked about %q, want polecat-pa95f", asked)
	}
	if f.Disposition != DispositionAwaitingReview || f.PR != 146 || f.PRBranch != "polecat-pa95f" {
		t.Fatalf("got disposition=%q pr=%d pr_branch=%q, want awaiting_review #146", f.Disposition, f.PR, f.PRBranch)
	}
	if f.Stranded() {
		t.Error("an open PR's head reported as stranded")
	}
	if sum := f.Summary(); !strings.Contains(sum, "#146") || !strings.Contains(sum, "awaiting review") {
		t.Errorf("Summary does not name the PR: %s", sum)
	}
}

// TestCheckOpenPRAsksAboutTheOriginName: the reviewer-checkout shape from the
// issue — the local branch is not on origin, its head is the PR's head under
// the PR's branch name, and that name is the one GitHub knows.
func TestCheckOpenPRAsksAboutTheOriginName(t *testing.T) {
	r := newRepo(t)
	r.branch("cms-hotfix-no-presave", "main")
	r.commit("f.md", "fix: a human's PR (mg-4b23)")
	r.push("cms-hotfix-no-presave")
	r.branch("polecat-4b23", "cms-hotfix-no-presave")
	r.checkout("main")
	f, err := Inspect(r.dir, "polecat-4b23", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var asked string
	f.CheckOpenPR(r.dir, func(_, branch string) (int, error) { asked = branch; return 30, nil })
	if asked != "cms-hotfix-no-presave" || f.Disposition != DispositionAwaitingReview {
		t.Errorf("asked %q, disposition %q; want cms-hotfix-no-presave and awaiting_review", asked, f.Disposition)
	}
}

func TestCheckOpenPRNoPRStaysStranded(t *testing.T) {
	r, f := pushedStranded(t)
	f.CheckOpenPR(r.dir, func(string, string) (int, error) { return 0, nil })
	if f.Disposition != DispositionResubmit || !f.Stranded() || f.PRProbeError != "" {
		t.Errorf("no PR: disposition=%q probe_error=%q, want resubmit and no error", f.Disposition, f.PRProbeError)
	}
}

// TestCheckOpenPRProbeFailureKeepsTheAlert pins the direction. The probe can
// only ever REMOVE an alert, and it fails when the network does — which is when
// mg-9a19 happened. If this test is changed to expect a suppression, the
// detector goes silently blind during every network incident. See CheckOpenPR.
func TestCheckOpenPRProbeFailureKeepsTheAlert(t *testing.T) {
	r, f := pushedStranded(t)
	f.CheckOpenPR(r.dir, func(string, string) (int, error) {
		return 0, errors.New("gh pr view polecat-pa95f: could not resolve host github.com")
	})
	if f.Disposition != DispositionResubmit || !f.Stranded() {
		t.Fatalf("a failed PR probe changed the verdict to %q — it must alert, never suppress", f.Disposition)
	}
	if !strings.Contains(f.PRProbeError, "could not resolve host") {
		t.Errorf("PRProbeError = %q, want the probe's error recorded", f.PRProbeError)
	}
	if f.PR != 0 || f.PRBranch != "" {
		t.Errorf("PR=%d PRBranch=%q set by a failed probe", f.PR, f.PRBranch)
	}
}

// TestGitHubOpenPRFailureKeepsTheAlert is the same pin through the PRODUCTION
// probe, with a gh that fails the way an outage makes it fail — so a later
// change to the shared helper's error handling (e.g. mapping an error to "no
// PR") cannot slip past the test above, which injects its own probe.
func TestGitHubOpenPRFailureKeepsTheAlert(t *testing.T) {
	r, f := pushedStranded(t)
	fakeGH(t, `echo "error connecting to api.github.com" >&2; exit 1`)
	f.CheckOpenPR(r.dir, GitHubOpenPR)
	if f.Disposition != DispositionResubmit || f.PRProbeError == "" {
		t.Fatalf("gh failure: disposition=%q probe_error=%q, want resubmit with the error recorded", f.Disposition, f.PRProbeError)
	}

	// Positive control: the same production probe DOES suppress on an open PR,
	// so the negative above is about the failure and not a probe that never fires.
	_, f = pushedStranded(t)
	fakeGH(t, `echo '{"state":"OPEN","number":146}'`)
	f.CheckOpenPR(r.dir, GitHubOpenPR)
	if f.Disposition != DispositionAwaitingReview || f.PR != 146 {
		t.Fatalf("gh open PR: disposition=%q pr=%d, want awaiting_review #146", f.Disposition, f.PR)
	}
}

// TestCheckOpenPRLeavesOtherDispositionsAlone: pre-registration outranks every
// suppression, and a local-only branch has no origin name to ask about.
func TestCheckOpenPRLeavesOtherDispositionsAlone(t *testing.T) {
	called := false
	probe := func(string, string) (int, error) { called = true; return 1, nil }

	r := newRepo(t)
	r.branch("polecat-paaf6", "main")
	r.commit("predictions.md", "predictions: three of five will be caught (mg-aaf6)")
	r.push("polecat-paaf6")
	r.checkout("main")
	f, err := Inspect(r.dir, "polecat-paaf6", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	f.CheckOpenPR(r.dir, probe)
	if f.Disposition != DispositionPreRegistration {
		t.Errorf("pre-registration reclassified to %q", f.Disposition)
	}

	r.branch("polecat-p0fc6", "main")
	r.commit("g.md", "feat: never pushed (mg-0fc6)")
	r.checkout("main")
	f, err = Inspect(r.dir, "polecat-p0fc6", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	f.CheckOpenPR(r.dir, probe)
	if f.Disposition != DispositionResubmit {
		t.Errorf("local-only reclassified to %q", f.Disposition)
	}
	if called {
		t.Error("probe called for a pre-registration or local-only finding")
	}
}

// fakeGH puts a stub `gh` running script first on PATH for the test.
func fakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
