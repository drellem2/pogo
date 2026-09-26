package agent

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/strandedwork"
)

// The dispatch gate inspects only strandedwork.ItemCandidates rather than every
// polecat branch (mg-110b, drellem2/pogo#175), and the claim that licenses that
// is EXACTNESS: the refusal must be the one Scan-then-AttributableTo gave. These
// tests are built so that claim can fail. Each fixture branch is attributable
// by ONE route only, and the positive control below proves the subject-route
// fixtures really do need the subject route — a name-only prefilter must fail
// them, or the parity they pass says nothing.

// prefilterFixture is a repo with one branch per attribution shape, and the ids
// to ask about.
func prefilterFixture(t *testing.T) (repo string, ids []string) {
	t.Helper()
	repo = strandedRepo(t)

	// Name AND subject: the ordinary shape.
	pushBranch(t, repo, "polecat-9a19", "a.md", "feat: the whole thing (mg-9a19)")

	// SUBJECT ONLY: the branch name does not contain "5ub1". Local-only, too.
	gitRun(t, repo, "checkout", "-q", "-b", "polecat-zzq1", "main")
	writeCommit(t, repo, "b.md", "feat: named by subject only (mg-5ub1)")
	writeCommit(t, repo, "b.md", "chore: a later commit that names nothing")
	gitRun(t, repo, "checkout", "-q", "main")

	// REMOTE-ONLY and subject only: origin/polecat-rem2 exists, refs/heads does not.
	pushBranch(t, repo, "polecat-rem2", "c.md", "feat: on origin only (mg-7aem)")
	gitRun(t, repo, "branch", "-q", "-D", "polecat-rem2")

	// CASE: the subject spells the id in upper case; AttributableTo uses EqualFold.
	pushBranch(t, repo, "polecat-kase", "d.md", "feat: shouted (mg-CAFE)")

	// Two ids in one subject — only the first is what Inspect captures, so the
	// prefilter must not miss the branch for either.
	pushBranch(t, repo, "polecat-twin", "e.md", "fix: both (mg-d0d1) (mg-e0e1)")

	// Name only: a pre-registration commit names no item.
	pushBranch(t, repo, "polecat-f3ff", "f.md", "predictions: three of six will fail")

	// Merged by rebase: a candidate by both routes, and NOT stranded. Inspect,
	// not the prefilter, has to be what drops it.
	gitRun(t, repo, "checkout", "-q", "-b", "polecat-bbbb", "main")
	merged := writeCommit(t, repo, "g.md", "feat: landed (mg-bbbb)")
	gitRun(t, repo, "push", "-q", "origin", "polecat-bbbb")
	gitRun(t, repo, "checkout", "-q", "main")
	writeCommit(t, repo, "h.md", "chore: main moved on")
	gitRun(t, repo, "cherry-pick", merged)
	gitRun(t, repo, "push", "-q", "origin", "main")

	// A reviewer's pointer at the builder's head: carried, not stranded.
	pushBranch(t, repo, "polecat-paaf6", "i.md", "feat: builder work (mg-aaf6)")
	gitRun(t, repo, "branch", "-q", "polecat-p1c60", "polecat-paaf6")
	gitRun(t, repo, "push", "-q", "origin", "polecat-p1c60")

	return repo, []string{"mg-9a19", "mg-5ub1", "mg-7aem", "mg-cafe", "mg-CAFE",
		"mg-d0d1", "mg-e0e1", "mg-f3ff", "mg-bbbb", "mg-aaf6", "mg-1c60", "mg-none"}
}

// fullScanBranches is the reference: what the gate did before mg-110b.
func fullScanBranches(t *testing.T, repo, id string) []string {
	t.Helper()
	findings, errs := strandedwork.Scan(repo, "main")
	if len(errs) != 0 {
		t.Fatalf("Scan: %v", errs)
	}
	return attributedBranches(findings, id)
}

// prefilteredBranches runs the gate's second half over a given candidate list.
func prefilteredBranches(t *testing.T, repo, id string, candidates []string) []string {
	t.Helper()
	findings, errs := strandedwork.ScanBranches(repo, "main", candidates)
	if len(errs) != 0 {
		t.Fatalf("ScanBranches: %v", errs)
	}
	return attributedBranches(findings, id)
}

func attributedBranches(findings []strandedwork.Finding, id string) []string {
	var out []string
	for _, f := range findings {
		if AttributableTo(f, id) {
			out = append(out, f.Branch)
		}
	}
	sort.Strings(out)
	return out
}

func itemCandidates(t *testing.T, repo, id string) []string {
	t.Helper()
	targetRef, err := strandedwork.ResolveTarget(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := strandedwork.ItemCandidates(repo, targetRef, id)
	if err != nil {
		t.Fatalf("ItemCandidates(%s): %v", id, err)
	}
	return c
}

// nameOnlyCandidates is the deliberately WEAKENED prefilter — the subject route
// dropped — used only as the positive control.
func nameOnlyCandidates(t *testing.T, repo, id string) []string {
	t.Helper()
	all, err := strandedwork.PolecatBranches(repo)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range all {
		if strandedwork.BranchMatchesItem(b, id) {
			out = append(out, b)
		}
	}
	return out
}

// TestPrefilterMatchesFullScanForEveryId is the parity test: for every id in the
// fixture, the candidate-scoped gate keeps exactly the branches the full scan did.
func TestPrefilterMatchesFullScanForEveryId(t *testing.T) {
	repo, ids := prefilterFixture(t)
	nonEmpty := 0
	for _, id := range ids {
		want := fullScanBranches(t, repo, id)
		got := prefilteredBranches(t, repo, id, itemCandidates(t, repo, id))
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: prefilter kept %v, full scan kept %v", id, got, want)
		}
		if len(want) > 0 {
			nonEmpty++
		}
	}
	// Parity between two empty answers proves nothing; most ids must have findings.
	if nonEmpty < 7 {
		t.Fatalf("only %d id(s) had any finding — the fixture is not exercising the gate", nonEmpty)
	}
}

// TestNameOnlyPrefilterFailsTheSubjectFixtures is the POSITIVE CONTROL for the
// parity test. If a name-only prefilter also passed these, the fixtures would
// not be exercising the subject route they are named for, and the parity above
// would hold for the wrong reason.
func TestNameOnlyPrefilterFailsTheSubjectFixtures(t *testing.T) {
	repo, _ := prefilterFixture(t)
	for id, branch := range map[string]string{
		"mg-5ub1": "polecat-zzq1", // subject only, local-only branch
		"mg-7aem": "polecat-rem2", // subject only, origin-only branch
		"mg-cafe": "polecat-kase", // subject only, different case
	} {
		want := fullScanBranches(t, repo, id)
		if fmt.Sprint(want) != fmt.Sprint([]string{branch}) {
			t.Fatalf("%s: full scan kept %v, want [%s] — fixture is wrong", id, want, branch)
		}
		weak := prefilteredBranches(t, repo, id, nameOnlyCandidates(t, repo, id))
		if fmt.Sprint(weak) == fmt.Sprint(want) {
			t.Errorf("%s: a NAME-ONLY prefilter also found %v — this fixture does not need the "+
				"subject route, so the parity test cannot catch it being dropped", id, weak)
		}
		if got := prefilteredBranches(t, repo, id, itemCandidates(t, repo, id)); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: the real prefilter kept %v, want %v", id, got, want)
		}
	}
}

// TestPrefilterSkipsUnrelatedBranches is the point of the change: an item's
// candidates are its own branches, not the repo.
func TestPrefilterSkipsUnrelatedBranches(t *testing.T) {
	repo, _ := prefilterFixture(t)
	targetRef, err := strandedwork.ResolveTarget(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, total, err := strandedwork.ItemCandidates(repo, targetRef, "mg-5ub1")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(c) != "[polecat-zzq1]" {
		t.Errorf("candidates for mg-5ub1 = %v, want [polecat-zzq1]", c)
	}
	if total < 9 {
		t.Errorf("total polecat branches = %d, want the whole fixture (>= 9)", total)
	}
	if c, _, _ := strandedwork.ItemCandidates(repo, targetRef, "mg-none"); len(c) != 0 {
		t.Errorf("candidates for an id nothing names = %v, want none", c)
	}
}

// TestGateRefusesOnEverySubjectRoute runs the three subject-only shapes through
// the production gate end to end, the path pogod actually takes at dispatch.
func TestGateRefusesOnEverySubjectRoute(t *testing.T) {
	repo, _ := prefilterFixture(t)
	for id, branch := range map[string]string{
		"mg-5ub1": "polecat-zzq1",
		"mg-7aem": "polecat-rem2",
		"mg-cafe": "polecat-kase",
	} {
		got, err := GitStrandedWorkGate{}.StrandedFindings(id, repo, "main")
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(got) != 1 || got[0].Branch != branch {
			t.Errorf("%s: gate found %v, want exactly %s", id, got, branch)
		}
	}
	if got, err := (GitStrandedWorkGate{}).StrandedFindings("mg-7aem", repo, "main"); err == nil && len(got) == 1 {
		if !strings.HasPrefix(got[0].Ref, "refs/remotes/origin/") {
			t.Errorf("remote-only branch read from %s, want the origin ref", got[0].Ref)
		}
	}
}
