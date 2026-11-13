package agent

import (
	"strings"
	"testing"
)

// mg-faa29: the release-time stranded-work report judges a polecat's branch
// against the branch it was DISPATCHED to. p4f7c8 was spawned with --branch
// release/v0.13.0, merged there, and was still mailed `[stranded-push] ... do
// NOT dispatch` with a remedy that submitted it to main.

// releaseCutRepo: release/x on origin, polecat-pfaa29 cut from it with one
// pushed commit, which is (merged) or is not (!merged) on release/x. It is
// never on main.
func releaseCutRepo(t *testing.T, merged bool) string {
	t.Helper()
	repo := strandedRepo(t)
	gitRun(t, repo, "checkout", "-q", "-b", "release/x", "main")
	writeCommit(t, repo, "release.md", "chore: cut release/x")
	gitRun(t, repo, "push", "-q", "origin", "release/x")
	gitRun(t, repo, "checkout", "-q", "-b", "polecat-pfaa29", "release/x")
	writeCommit(t, repo, "CHANGELOG", "fix: back-port (mg-faa29)")
	gitRun(t, repo, "push", "-q", "origin", "polecat-pfaa29")
	if merged {
		gitRun(t, repo, "checkout", "-q", "release/x")
		writeCommit(t, repo, "other.md", "chore: landed first")
		gitRun(t, repo, "cherry-pick", "polecat-pfaa29")
		gitRun(t, repo, "push", "-q", "origin", "release/x")
	}
	gitRun(t, repo, "checkout", "-q", "main")
	return repo
}

func releaseWithTarget(t *testing.T, repo, recorded string, items fakeBrancher) []StrandedAlert {
	t.Helper()
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})
	if items == nil {
		items = fakeBrancher{}
	}
	reg.SetWorkItemBrancher(items)
	a := livePolecat("pfaa29", "mg-faa29")
	a.SourceRepo = repo
	a.TargetBranch = recorded
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
	return mail()
}

// Acceptance: merged into its target release/x, not main — no stranded-push.
func TestReleaseOfBranchMergedIntoItsTargetSendsNoStrandedPush(t *testing.T) {
	repo := releaseCutRepo(t, true)
	if sent := releaseWithTarget(t, repo, "release/x", nil); len(sent) != 0 {
		_, body := sent[0].Message()
		t.Fatalf("a polecat whose branch merged into its dispatch target release/x drew %d stranded-push "+
			"mail(s) — it was judged against the default branch (mg-faa29):\n%s", len(sent), body)
	}
}

// The same, with the target known only from the item's `branch:` field (an
// agent record that predates TargetBranch).
func TestReleaseFallsBackToTheItemsBranchField(t *testing.T) {
	repo := releaseCutRepo(t, true)
	if sent := releaseWithTarget(t, repo, "", fakeBrancher{"mg-faa29": "release/x"}); len(sent) != 0 {
		t.Fatalf("with the target only on the item's branch: field, a merged branch drew %d mail(s)", len(sent))
	}
}

// Positive control: unmerged into release/x, it IS stranded, and the remedy
// submits to release/x.
func TestReleaseOfBranchUnmergedIntoItsTargetStillAlertsWithTargetedRemedy(t *testing.T) {
	repo := releaseCutRepo(t, false)
	sent := releaseWithTarget(t, repo, "release/x", nil)
	if len(sent) != 1 {
		t.Fatalf("an unmerged release-branch polecat sent %d mails, want 1", len(sent))
	}
	_, body := sent[0].Message()
	if !strings.Contains(body, "refs/remotes/origin/release/x") {
		t.Errorf("the alert does not name release/x as its target:\n%s", body)
	}
	if !strings.Contains(body, "--target=release/x") {
		t.Errorf("the paste-ready remedy does not aim at release/x; pasted, it submits to main:\n%s", body)
	}
}

// Control on the fixture: with no target anywhere, the merged-to-release branch
// is judged against main and alerts — the pre-mg-faa29 false alarm. If this did
// not fire, the acceptance test's silence would prove nothing.
func TestReleaseWithNoTargetJudgesAgainstDefault(t *testing.T) {
	repo := releaseCutRepo(t, true)
	if sent := releaseWithTarget(t, repo, "", nil); len(sent) != 1 {
		t.Fatalf("control: with no dispatch target the branch (not on main) should alert; sent %d", len(sent))
	}
}
