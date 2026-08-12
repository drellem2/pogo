package refinery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newBranchValidationRefinery builds a queue-only Refinery: the poll interval
// never ticks, so nothing here reaches the merge worker. That is the point —
// these tests are about what the SUBMITTER is told, at the prompt, before any
// of that machinery runs.
func newBranchValidationRefinery(t *testing.T) *Refinery {
	t.Helper()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestSubmitRefusesABranchThatExistsNowhere is the mg-586d repro. Before the
// fix this submit returned an MR id — a success-shaped answer — and the failure
// arrived later in the merge worker, at
//
//	git checkout -B <branch> origin/<branch>   (merge.go:593, exit 128)
//
// read by whoever monitors the refinery rather than by the person who typed the
// command and could fix it with one push.
func TestSubmitRefusesABranchThatExistsNowhere(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	r := newBranchValidationRefinery(t)

	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "polecat-nowhere",
		TargetRef: "main",
		Author:    "mg-586d",
	})
	if err == nil {
		t.Fatalf("submit accepted a branch that exists nowhere and returned MR id %q", id)
	}
	if !strings.Contains(err.Error(), "polecat-nowhere") {
		t.Errorf("expected the refusal to name the branch, got: %v", err)
	}
	// Nothing may be queued: an MR id handed back is the artifact that travels,
	// and it must not exist for an operation that cannot succeed.
	if n := len(r.Queue()); n != 0 {
		t.Errorf("expected an empty queue after a refused submit, got %d item(s)", n)
	}
}

// TestSubmitRefusesALocalOnlyBranchAndSaysToPushIt covers the shape the
// original probe hit: the branch exists in the submitter's repo and nowhere on
// origin. The refusal has to be actionable at the prompt, so it names the push.
//
// Note the product decision this pins by NOT taking it: submit refuses a
// local-only branch rather than pushing it. Making submit push first is a
// bigger change and a product call (mg-586d says so explicitly); the minimum
// this ticket owes the caller is that they learn now.
func TestSubmitRefusesALocalOnlyBranchAndSaysToPushIt(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "checkout", "-b", "polecat-local")
	os.WriteFile(filepath.Join(workDir, "local.txt"), []byte("work\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "local-only work")

	r := newBranchValidationRefinery(t)
	_, err := r.Submit(MergeRequest{
		RepoPath:  workDir,
		Branch:    "polecat-local",
		TargetRef: "main",
		Author:    "mg-586d",
	})
	if err == nil {
		t.Fatal("submit accepted a branch that exists only locally")
	}
	msg := err.Error()
	for _, want := range []string{"polecat-local", "git push origin polecat-local", "never been pushed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the refusal to contain %q, got: %v", want, msg)
		}
	}
}

// TestSubmitRefusalNamesTheOriginRefThatHoldsTheHead exercises the gh#134 /
// mg-1539 containment question — asked here for the DIAGNOSTIC, not the
// verdict. "You pushed this work under another name" and "you never pushed at
// all" are different mistakes with different fixes, and the submitter is still
// at the prompt to act on whichever one it is.
func TestSubmitRefusalNamesTheOriginRefThatHoldsTheHead(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "checkout", "-b", "polecat-builder")
	os.WriteFile(filepath.Join(workDir, "work.txt"), []byte("work\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "the work")
	run(t, workDir, "git", "push", "origin", "polecat-builder")
	// A second name for the same head — the reviewer shape from gh#134. The
	// work is durable; the branch is still not submittable.
	run(t, workDir, "git", "branch", "polecat-reviewer")

	r := newBranchValidationRefinery(t)
	_, err := r.Submit(MergeRequest{
		RepoPath:  workDir,
		Branch:    "polecat-reviewer",
		TargetRef: "main",
		Author:    "mg-586d",
	})
	if err == nil {
		t.Fatal("submit accepted a branch whose head is on origin only under another name")
	}
	msg := err.Error()
	if !strings.Contains(msg, "refs/remotes/origin/polecat-builder") {
		t.Errorf("expected the refusal to name the origin ref holding the head, got: %v", msg)
	}
	if !strings.Contains(msg, "git push origin polecat-reviewer") {
		t.Errorf("expected the refusal to name the push that fixes it, got: %v", msg)
	}
}

// TestValidateSubmitBranchAcceptsWhatTheMergeWorkerCanCheckOut pins the
// accepting half, in both shapes of "origin" the merge worker resolves:
// a bare repo used directly as the remote (the test shape, where ensureWorktree
// clones repoPath and fixRemoteURL leaves origin pointing at it), and a working
// clone whose own origin is propagated into the refinery's clone.
func TestValidateSubmitBranchAcceptsWhatTheMergeWorkerCanCheckOut(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	seedBranch(t, originDir, "polecat-pushed")

	t.Run("bare repo as origin — local refs are what the clone will see", func(t *testing.T) {
		if err := validateSubmitBranch(originDir, "polecat-pushed"); err != nil {
			t.Errorf("expected a branch present in the bare origin to validate, got: %v", err)
		}
		if err := validateSubmitBranch(originDir, "polecat-absent"); err == nil {
			t.Error("expected a branch absent from the bare origin to be refused")
		}
	})

	t.Run("working clone — ls-remote against its own origin decides", func(t *testing.T) {
		workDir := t.TempDir()
		run(t, workDir, "git", "clone", originDir, ".")
		if err := validateSubmitBranch(workDir, "polecat-pushed"); err != nil {
			t.Errorf("expected a branch on origin to validate from a working clone, got: %v", err)
		}
	})
}

// TestSubmitBranchCheckRunsBeforeTheTargetRefIsAutoCreated pins the ordering.
// createTargetRef pushes a new branch to the real remote, so a submit that is
// going to be refused must be refused first — otherwise a doomed submit leaves
// a stray ref behind on origin, which is the same shape of defect (a side
// effect landing somewhere the caller is not) that this ticket closes.
func TestSubmitBranchCheckRunsBeforeTheTargetRefIsAutoCreated(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	r := newBranchValidationRefinery(t)

	_, err := r.Submit(MergeRequest{
		RepoPath:            originDir,
		Branch:              "polecat-nowhere",
		TargetRef:           "brand-new-integration",
		Author:              "mg-586d",
		AutoCreateTargetRef: true,
	})
	if err == nil {
		t.Fatal("expected the submit to be refused for its branch")
	}
	if !strings.Contains(err.Error(), "polecat-nowhere") {
		t.Errorf("expected the branch refusal, not the target-ref path, got: %v", err)
	}
	refs := runOut(t, originDir, "git", "for-each-ref", "--format=%(refname)", "refs/heads/")
	if strings.Contains(refs, "refs/heads/brand-new-integration") {
		t.Errorf("a refused submit auto-created the target ref anyway; refs are:\n%s", refs)
	}
}

// TestValidateSubmitBranchFallsBackWhenOriginIsUnreachable pins the stated
// bound in validateSubmitBranch's doc comment: an origin that cannot be reached
// is not evidence about the branch, so the local ref decides. This is a
// deliberate fail-open, and it does not re-create the defect — an unreachable
// origin fails the merge worker's own `git fetch origin` first, which is a
// retried network failure rather than the permanent, caller-fixable mistake
// this check exists to surface.
func TestValidateSubmitBranchFallsBackWhenOriginIsUnreachable(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "checkout", "-b", "polecat-local")
	os.WriteFile(filepath.Join(workDir, "local.txt"), []byte("work\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "local-only work")
	// Point origin at a path that does not exist: ls-remote now fails rather
	// than answering "no such head".
	run(t, workDir, "git", "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	if err := validateSubmitBranch(workDir, "polecat-local"); err != nil {
		t.Errorf("expected the local ref to decide when origin is unreachable, got: %v", err)
	}
	if err := validateSubmitBranch(workDir, "polecat-absent"); err == nil {
		t.Error("expected a branch absent locally to be refused when origin is unreachable")
	}
}

// TestValidateSubmitBranchDoesNotBlockTheAlreadyMergedRecovery guards the one
// path that could plausibly regress: a polecat that lost track of a merged MR
// and resubmits (gh #34) must still reach probeAlreadyMerged, which resolves it
// as merged without re-running gates.
//
// It cannot regress, and this pins why: probeAlreadyMerged answers by resolving
// refs/remotes/origin/<branch> after a fetch, so it ALREADY requires exactly
// what this check requires. The new refusal is strictly weaker than the probe's
// own precondition — when the probe can answer, this validates; when the branch
// was reaped from origin after the merge, the probe could never answer either
// and the pipeline failed at checkout, which is the case this now catches at
// the prompt instead.
func TestValidateSubmitBranchDoesNotBlockTheAlreadyMergedRecovery(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "checkout", "-b", "polecat-landed")
	os.WriteFile(filepath.Join(workDir, "landed.txt"), []byte("work\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "work that lands")
	run(t, workDir, "git", "push", "origin", "polecat-landed")
	// The branch is now an ancestor of main on origin, and still present under
	// its own name — the exact state a resubmit-after-losing-track sees.
	run(t, workDir, "git", "push", "origin", "polecat-landed:main")

	if err := validateSubmitBranch(workDir, "polecat-landed"); err != nil {
		t.Errorf("an already-merged branch still on origin must validate, got: %v", err)
	}
}

// TestValidateSubmitBranchDoesNotLetAStaleLocalRefMaskOrigin is the branch-side
// twin of the #10 regression already pinned for target refs: when ls-remote
// answers and answers empty, that is definitive, and a same-named local branch
// must not rescue it. It is exactly the local-only case the merge worker fails.
func TestValidateSubmitBranchDoesNotLetAStaleLocalRefMaskOrigin(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "branch", "ghost")

	err := validateSubmitBranch(workDir, "ghost")
	if err == nil {
		t.Fatal("a stale local branch masked a branch missing from origin")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected the refusal to name the branch, got: %v", err)
	}
}
