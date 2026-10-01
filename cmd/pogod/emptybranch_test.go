package main

import (
	"context"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// mg-c184d, pogod's half. The refinery now FAILS a branch that carries no
// commits ahead of its target instead of resolving it as already-merged, and
// pogod needs no policy change for that to protect the work item: a failed MR
// never reaches OnMerged, so nothing marks the item done and nothing reaps the
// polecat. These tests pin that end to end — a real refinery, real git, and the
// same OnMerged/OnFailed wiring main.go installs — so a later change to either
// side cannot quietly reintroduce the mg-3b86e false-done.

// emptyBranchOutcome is what pogod's callbacks did with one merge request.
type emptyBranchOutcome struct {
	mr          *refinery.MergeRequest
	merged      bool
	completedID string
	stopped     []string
	reopen      reopenResult
	reopenCalls []string
}

// runThroughPogodCallbacks submits branch for workItem and drives a real
// refinery loop until the MR resolves, with the merged path wired to
// reapMergedPolecat and the failed path to reopenAfterFailure, as in main.go.
// The polecat is registered under its bare id, as pogod registers it.
func runThroughPogodCallbacks(t *testing.T, origin, branch, workItem string) emptyBranchOutcome {
	t.Helper()
	bare := workItem[len("mg-"):]
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		bare: {Name: bare, WorkItemID: workItem, Type: agent.TypePolecat},
	}}
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	var out emptyBranchOutcome
	complete := func(id, resultJSON string) error {
		out.completedID = id
		return nil
	}

	r, err := refinery.New(refinery.Config{
		Enabled:      true,
		PollInterval: 50 * time.Millisecond,
		WorktreeDir:  t.TempDir(),
		MacguffinDir: "",
		StatePath:    "",
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	r.SetOnMerged(func(mr *refinery.MergeRequest) {
		out.mr, out.merged = mr, true
		reapMergedPolecat(reg, mr, complete, resolvePostMergeWork(reg, mr, nil), backstop, nil)
		done <- struct{}{}
	})
	r.SetOnFailed(func(mr *refinery.MergeRequest) {
		out.mr = mr
		out.reopen = reopenAfterFailure(mr, r.History(), func(id string) error {
			out.reopenCalls = append(out.reopenCalls, id)
			return nil
		})
		done <- struct{}{}
	})

	if _, err := r.Submit(refinery.MergeRequest{
		RepoPath: origin, Branch: branch, TargetRef: "main", Author: workItem,
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go r.Start(ctx)
	defer func() { cancel(); r.Stop() }()

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("timed out waiting for the refinery to resolve the merge request")
	}
	out.stopped = reg.stopped
	return out
}

// TestEmptyBranchSubmitLeavesItemOpenAndPolecatAlive is the mg-3b86e incident
// replayed: the polecat's branch is main's tip with nothing ahead. Before
// mg-c184d this closed the item done and reaped the polecat with its work still
// uncommitted in the worktree.
func TestEmptyBranchSubmitLeavesItemOpenAndPolecatAlive(t *testing.T) {
	fx := newPRFlowFixture(t, "mg-3b86e", "main", "")
	gitRun(t, fx.origin, "branch", "-f", "polecat-empty", "main")

	out := runThroughPogodCallbacks(t, fx.origin, "polecat-empty", fx.workItem)

	if out.merged {
		t.Fatalf("THE DEFECT: an empty branch reached OnMerged (already_merged=%v merged_sha=%s)",
			out.mr.AlreadyMerged, out.mr.MergedSHA)
	}
	if out.mr.Status != refinery.StatusFailed || out.mr.FailureClass != refinery.ClassDefect {
		t.Errorf("status=%s class=%s, want failed/defect", out.mr.Status, out.mr.FailureClass)
	}
	if out.completedID != "" {
		t.Errorf("pogod marked %s done on an empty-branch submit", out.completedID)
	}
	if len(out.stopped) != 0 {
		t.Errorf("pogod stopped %v — the polecat must stay alive to commit and resubmit", out.stopped)
	}
	// Nothing by this item ever landed, so the failure path's reopen guard
	// does not decline on a "landed" record — the empty MR is not one.
	if out.reopen.Outcome == reopenDeclinedLanded {
		t.Errorf("the reopen guard treated the empty MR as landed work: %+v", out.reopen.Landed)
	}
}

// TestRealBranchSubmitStillClosesAndReaps is the control on the same wiring:
// without it, the test above cannot tell "empty branches are refused" from
// "nothing closes anything any more".
func TestRealBranchSubmitStillClosesAndReaps(t *testing.T) {
	fx := newPRFlowFixture(t, "mg-3b86e", "main", "")

	out := runThroughPogodCallbacks(t, fx.origin, fx.polecatBranch, fx.workItem)

	if !out.merged {
		t.Fatalf("a branch with a real commit did not merge: %s (%s)", out.mr.Status, out.mr.Error)
	}
	if out.completedID != fx.workItem {
		t.Errorf("completed %q, want %s", out.completedID, fx.workItem)
	}
	if len(out.stopped) != 1 || out.stopped[0] != "3b86e" {
		t.Errorf("stopped %v, want [3b86e]", out.stopped)
	}
}
