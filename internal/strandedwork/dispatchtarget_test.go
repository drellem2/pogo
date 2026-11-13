package strandedwork

import (
	"errors"
	"strings"
	"testing"
)

// mg-faa29: a polecat dispatched at a release branch is judged against THAT
// branch, not the repo default. The incident: p4f7c8 was spawned with
// --branch release/v0.13.0, its MR merged into release/v0.13.0, and pogod still
// mailed `[stranded-push] ... do NOT dispatch` with Target origin/main and a
// remedy that submitted to main.

// releaseCut builds the fixture: release/x on origin, a polecat branch cut from
// it carrying one commit, pushed. merged decides whether that commit has landed
// on release/x (rebased, as the refinery does). Either way it is NOT on main.
func releaseCut(t *testing.T, merged bool) *repo {
	t.Helper()
	r := newRepo(t)
	r.branch("release/x", "main")
	r.commit("release.md", "chore: cut release/x")
	r.push("release/x")

	r.branch("polecat-pfaa29", "release/x")
	r.commit("CHANGELOG", "fix: back-port for release/x (mg-faa29)")
	r.push("polecat-pfaa29")

	if merged {
		// Land it the refinery's way: a rebase onto the target, which rewrites
		// the sha. `git cherry` must still see it as present.
		r.checkout("release/x")
		r.commit("other.md", "chore: something else landed first")
		r.git("cherry-pick", "polecat-pfaa29")
		r.push("release/x")
	}
	r.checkout("main")
	r.commit("main.md", "feat: main moved on")
	r.push("main")
	return r
}

// The acceptance case: merged into release/x but not main draws nothing.
func TestBranchMergedIntoItsDispatchTargetIsNotStranded(t *testing.T) {
	r := releaseCut(t, true)

	f, err := InspectFor(r.dir, "polecat-pfaa29", "release/x")
	if err != nil {
		t.Fatalf("InspectFor: %v", err)
	}
	if f.Stranded() {
		t.Fatalf("branch merged into its target release/x reported stranded: %s", f.Summary())
	}
	if f.Target != "refs/remotes/origin/release/x" {
		t.Errorf("Target = %q, want refs/remotes/origin/release/x", f.Target)
	}

	// Control on the instrument: against the DEFAULT branch the same branch is
	// stranded — which is exactly the false alarm this fixes. If this did not
	// fire, the negative above would say nothing.
	def, err := InspectFor(r.dir, "polecat-pfaa29", "")
	if err != nil {
		t.Fatalf("InspectFor(default): %v", err)
	}
	if !def.Stranded() {
		t.Fatalf("control did not fire: the branch is not on main, so judged against main it must read stranded")
	}
}

// The positive control: the same branch, NOT merged into release/x, is still
// stranded — and its remedy submits to release/x, not to main.
func TestBranchUnmergedIntoItsDispatchTargetIsStrandedAndRemedyNamesTarget(t *testing.T) {
	r := releaseCut(t, false)

	f, err := InspectFor(r.dir, "polecat-pfaa29", "release/x")
	if err != nil {
		t.Fatalf("InspectFor: %v", err)
	}
	if !f.Stranded() {
		t.Fatalf("branch unmerged into its target release/x reported not stranded: %+v", f)
	}
	if f.SubmitTarget != "release/x" {
		t.Errorf("SubmitTarget = %q, want release/x", f.SubmitTarget)
	}
	if s := f.Summary(); !strings.Contains(s, "--target=release/x") {
		t.Errorf("summary remedy does not aim at release/x; pasted, it submits to main:\n%s", s)
	}
}

func TestDefaultTargetRemedyCarriesNoTargetFlag(t *testing.T) {
	r := releaseCut(t, false)
	f, err := InspectFor(r.dir, "polecat-pfaa29", "")
	if err != nil {
		t.Fatalf("InspectFor: %v", err)
	}
	if f.SubmitTarget != "" || strings.Contains(f.Summary(), "--target") {
		t.Errorf("default-branch finding grew a --target: SubmitTarget=%q\n%s", f.SubmitTarget, f.Summary())
	}
}

// A target the refinery has not created yet (it creates it at submit) is not a
// "could not check": nothing can have merged into it. Judged against the
// default, flagged, and the remedy still names the target.
func TestMissingDispatchTargetFallsBackToDefaultAndKeepsTheTarget(t *testing.T) {
	r := releaseCut(t, false)

	if _, err := Inspect(r.dir, "polecat-pfaa29", "release/nope"); !errors.Is(err, ErrNoTargetRef) {
		t.Fatalf("Inspect against a missing target: err = %v, want ErrNoTargetRef", err)
	}
	f, err := InspectFor(r.dir, "polecat-pfaa29", "release/nope")
	if err != nil {
		t.Fatalf("InspectFor: %v", err)
	}
	if !f.TargetMissing || f.SubmitTarget != "release/nope" {
		t.Errorf("TargetMissing=%v SubmitTarget=%q, want true and release/nope", f.TargetMissing, f.SubmitTarget)
	}
	if !f.Stranded() {
		t.Errorf("fallback judgement against main should read stranded here: %+v", f)
	}
	if !strings.Contains(f.Summary(), "--target=release/nope") {
		t.Errorf("remedy dropped the target:\n%s", f.Summary())
	}
}
