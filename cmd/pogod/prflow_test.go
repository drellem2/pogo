package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// End-to-end coverage for mg-7746: the refinery marked a PR-flow work item
// done the instant its branch merged into the *integration* branch, so the
// polecat was stopped before it ever ran `gh pr create`. The unit tests in
// reap_test.go hand-set the flag that drives that decision; these tests drive
// the real thing — real git repos, a real refinery loop, real default-branch
// detection — because this bug survived a polecat template that *looked*
// correct and a passing unit suite.

// gitRun runs a git command in dir and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// prFlowFixture is a bare origin with a default branch, optionally an
// integration branch carved off it, and a polecat branch holding one commit.
//
// workItem is the fixture's work-item id, carried so a suite can build the
// scenario around a REAL item from the audit trail rather than around this
// file's own (mg-d86e uses mg-ca3c and mg-9f17, the two releases that were
// reported complete with no tag).
type prFlowFixture struct {
	origin        string // what a polecat passes as --repo
	defaultBranch string
	integration   string
	polecatBranch string
	workItem      string
}

func newPRFlowFixture(t *testing.T, workItem, defaultBranch, integration string) prFlowFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	origin := t.TempDir()
	gitRun(t, origin, "init", "--bare", "-b", defaultBranch)

	work := t.TempDir()
	gitRun(t, work, "clone", origin, ".")
	// A gate that always passes, so the merge outcome under test turns on
	// PR-flow classification and nothing else.
	os.WriteFile(filepath.Join(work, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	os.WriteFile(filepath.Join(work, "README.md"), []byte("# fixture\n"), 0644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-m", "initial commit")
	gitRun(t, work, "push", "origin", defaultBranch)

	base := defaultBranch
	if integration != "" && integration != defaultBranch {
		gitRun(t, work, "push", "origin", defaultBranch+":"+integration)
		base = integration
	}

	branch := "polecat-" + workItem
	gitRun(t, work, "checkout", "-b", branch, "origin/"+base)
	os.WriteFile(filepath.Join(work, "feature.txt"), []byte("work\n"), 0644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-m", "feat: polecat work ("+workItem+")")
	gitRun(t, work, "push", "origin", branch)

	return prFlowFixture{
		origin:        origin,
		defaultBranch: defaultBranch,
		integration:   integration,
		polecatBranch: branch,
		workItem:      workItem,
	}
}

// mergeOutcome records what the reap path did in response to a merge.
type mergeOutcome struct {
	completedID     string
	completedResult string
	stopped         []string
	armed           bool
	postMerge       postMergeVerdict
}

// runMergeThroughReap submits the fixture's polecat branch at targetRef, runs a
// real refinery loop until the MR resolves, and reports what reapMergedPolecat
// did — the exact wiring pogod uses in main.go, including the work-item probe
// resolved before the reap (mg-d86e). declares may be nil, which is what pogod
// does when no probe is configured: nothing is declared and the fast path runs.
func runMergeThroughReap(t *testing.T, fx prFlowFixture, targetRef string, declares func(string) (bool, error)) (mergeOutcome, *refinery.MergeRequest) {
	t.Helper()

	bareName := strings.TrimPrefix(fx.workItem, "mg-")
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		bareName: {Name: bareName, WorkItemID: fx.workItem, Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	var out mergeOutcome
	complete := func(id, resultJSON string) error {
		out.completedID = id
		out.completedResult = resultJSON
		return nil
	}

	r, err := refinery.New(refinery.Config{
		Enabled:      true,
		PollInterval: 50 * time.Millisecond,
		WorktreeDir:  t.TempDir(),
		// Disable the QA gate and persistence so the test never reads or
		// writes the host's real ~/.macguffin or ~/.pogo state.
		MacguffinDir: "",
		StatePath:    "",
	})
	if err != nil {
		t.Fatal(err)
	}

	resolved := make(chan *refinery.MergeRequest, 1)
	r.SetOnMerged(func(mr *refinery.MergeRequest) {
		postMerge := resolvePostMergeWork(reg, mr, declares)
		out.postMerge = postMerge
		reapMergedPolecat(reg, mr, complete, postMerge, backstop)
		resolved <- mr
	})
	failed := make(chan *refinery.MergeRequest, 1)
	r.SetOnFailed(func(mr *refinery.MergeRequest) { failed <- mr })

	id, err := r.Submit(refinery.MergeRequest{
		RepoPath:  fx.origin,
		Branch:    fx.polecatBranch,
		TargetRef: targetRef,
		Author:    fx.workItem,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	t.Logf("submitted MR %s target=%s", id, targetRef)

	ctx, cancel := context.WithCancel(context.Background())
	go r.Start(ctx)
	defer func() { cancel(); r.Stop() }()

	select {
	case mr := <-resolved:
		out.stopped = reg.stopped
		backstop.mu.Lock()
		_, out.armed = backstop.timers[bareName]
		backstop.mu.Unlock()
		return out, mr
	case mr := <-failed:
		t.Fatalf("merge failed unexpectedly: %s (gate output: %s)", mr.Error, mr.GateOutput)
	case <-time.After(90 * time.Second):
		t.Fatal("timed out waiting for the refinery to resolve the merge request")
	}
	return out, nil
}

// TestPRFlowMerge_LeavesWorkItemClaimed is the mg-7746 regression. A merge into
// an integration branch is an intermediate step of a PR flow — the deliverable
// is the PR to the default branch, which the polecat has not opened yet. The
// refinery must NOT mark the item done and must NOT stop the polecat.
func TestPRFlowMerge_LeavesWorkItemClaimed(t *testing.T) {
	fx := newPRFlowFixture(t, "mg-7746", "main", "daed-101-integration")
	out, mr := runMergeThroughReap(t, fx, fx.integration, nil)

	if !mr.PRFlow {
		t.Errorf("MR merged into integration branch %q (default is %q) but PRFlow=false — the refinery cannot tell PR flow from completion",
			mr.TargetRef, fx.defaultBranch)
	}
	if out.completedID != "" {
		t.Errorf("refinery called mg done for %q on an integration-branch merge; completion is the opened PR, not this merge (result=%q)",
			out.completedID, out.completedResult)
	}
	if len(out.stopped) != 0 {
		t.Errorf("refinery stopped polecat %v before it could open the PR", out.stopped)
	}
	if !out.armed {
		t.Error("expected the bounded backstop to be armed for the deferred polecat")
	}
}

// TestDefaultBranchMerge_StillMarksDone is the fast-path acceptance control:
// when the merge lands on the repo's default branch there is no PR pending, so
// the refinery's mg done IS correct completion and must be preserved.
func TestDefaultBranchMerge_StillMarksDone(t *testing.T) {
	fx := newPRFlowFixture(t, "mg-7746", "main", "")
	out, mr := runMergeThroughReap(t, fx, "main", nil)

	if mr.PRFlow {
		t.Errorf("merge into the default branch %q was misclassified as PR flow", mr.TargetRef)
	}
	if out.completedID != "mg-7746" {
		t.Errorf("expected mg done for mg-7746 on a default-branch merge, got %q", out.completedID)
	}
	if len(out.stopped) != 1 || out.stopped[0] != "7746" {
		t.Errorf("expected the merged polecat to be stopped, got %v", out.stopped)
	}
	if out.armed {
		t.Error("no backstop should be armed on the fast path")
	}
}

// TestDefaultBranchMerge_ResultRecordsTarget covers the second-order damage in
// mg-7746: mayor's classification logic reads `target`/`pr_flow` out of the
// result sidecar, and the refinery omitted both — turning a documented
// secondary signal into a misleading one.
func TestDefaultBranchMerge_ResultRecordsTarget(t *testing.T) {
	fx := newPRFlowFixture(t, "mg-7746", "main", "")
	out, _ := runMergeThroughReap(t, fx, "main", nil)

	var result map[string]any
	if err := json.Unmarshal([]byte(out.completedResult), &result); err != nil {
		t.Fatalf("result sidecar is not valid JSON: %v (%q)", err, out.completedResult)
	}
	if result["target"] != "main" {
		t.Errorf("result sidecar omits the merge target: %q", out.completedResult)
	}
	if _, ok := result["pr_flow"]; ok {
		t.Errorf("a default-branch merge must not claim pr_flow: %q", out.completedResult)
	}
	if result["completed_by"] != "refinery" {
		t.Errorf("result sidecar must still record completed_by=refinery: %q", out.completedResult)
	}
}
