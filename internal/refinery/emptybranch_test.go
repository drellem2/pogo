package refinery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// emptyBranchFixture is a bare origin plus a working clone whose build.sh gate
// counts its runs in a file outside both repos, so a test can tell "refused
// before the gates" from "ran the gates and failed".
type emptyBranchFixture struct {
	origin, work, gateMarker string
}

func newEmptyBranchFixture(t *testing.T) emptyBranchFixture {
	t.Helper()
	fx := emptyBranchFixture{origin: initBareOrigin(t, "main"), work: t.TempDir()}
	fx.gateMarker = filepath.Join(t.TempDir(), "gate-runs")
	run(t, fx.work, "git", "clone", fx.origin, ".")
	run(t, fx.work, "git", "config", "user.email", "test@test.com")
	run(t, fx.work, "git", "config", "user.name", "Test")
	gate := "#!/bin/sh\necho ran >> " + fx.gateMarker + "\nexit 0\n"
	os.WriteFile(filepath.Join(fx.work, "build.sh"), []byte(gate), 0o755)
	run(t, fx.work, "git", "add", ".")
	run(t, fx.work, "git", "commit", "-m", "counting gate")
	run(t, fx.work, "git", "push", "origin", "HEAD:main")
	return fx
}

// branchWithWork cuts branch from origin/main, commits one file, and pushes.
func (fx emptyBranchFixture) branchWithWork(t *testing.T, branch, file string) string {
	t.Helper()
	run(t, fx.work, "git", "fetch", "origin")
	run(t, fx.work, "git", "checkout", "-B", branch, "origin/main")
	os.WriteFile(filepath.Join(fx.work, file), []byte(branch+"\n"), 0o644)
	run(t, fx.work, "git", "add", ".")
	run(t, fx.work, "git", "commit", "-m", "work on "+branch)
	run(t, fx.work, "git", "push", "-f", "origin", branch)
	return strings.TrimSpace(gitOutput(t, fx.work, "rev-parse", "HEAD"))
}

// emptyBranch pushes branch at origin/main's tip with nothing ahead — the
// mg-3b86e shape: the polecat's work was uncommitted in its worktree.
func (fx emptyBranchFixture) emptyBranch(t *testing.T, branch string) string {
	t.Helper()
	run(t, fx.work, "git", "fetch", "origin")
	run(t, fx.work, "git", "push", "-f", "origin", "origin/main:refs/heads/"+branch)
	return fx.mainTip(t)
}

func (fx emptyBranchFixture) mainTip(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(gitOutput(t, fx.origin, "rev-parse", "main"))
}

func (fx emptyBranchFixture) gateRuns(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(fx.gateMarker)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "ran")
}

type callbackLog struct{ merged, failed []string }

func newCallbackRefinery(t *testing.T) (*Refinery, *callbackLog) {
	t.Helper()
	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cl := &callbackLog{}
	r.SetOnMerged(func(mr *MergeRequest) { cl.merged = append(cl.merged, mr.ID) })
	r.SetOnFailed(func(mr *MergeRequest) { cl.failed = append(cl.failed, mr.ID) })
	return r, cl
}

func submitAndProcess(t *testing.T, r *Refinery, origin, branch, author string) *MergeRequest {
	t.Helper()
	id, err := r.Submit(MergeRequest{RepoPath: origin, Branch: branch, TargetRef: "main", Author: author})
	if err != nil {
		t.Fatalf("submit %s: %v", branch, err)
	}
	r.processNext()
	mr := r.Get(id)
	if mr == nil {
		t.Fatalf("MR %s for %s vanished", id, branch)
	}
	return mr
}

// TestEmptyBranchRuleSeparatesTheThreeCases is the mg-c184d acceptance test,
// and it runs all three cases against ONE refinery and ONE origin on purpose:
// a suite that showed only the refusal could not tell "the rule works" from
// "everything is refused", and one that showed only the merges could not tell
// it from "nothing changed".
//
//  1. A normal REBASED merge (the branch is behind main) lands as before.
//  2. A fast-forward resubmit with a prior merged MR on record still resolves
//     as already-merged, naming the prior MR.
//  3. An empty branch (head == main's tip) is REFUSED as a defect — even
//     though, exactly as in the incident, main's tip is a commit a prior MR
//     really did land, just not one this branch or work item ever carried.
func TestEmptyBranchRuleSeparatesTheThreeCases(t *testing.T) {
	logPath := useTempEventLog(t)
	fx := newEmptyBranchFixture(t)
	r, cl := newCallbackRefinery(t)

	// --- 1. normal rebased merge --------------------------------------------
	rebasedHead := fx.branchWithWork(t, "polecat-rebased", "rebased.txt")
	// Advance main underneath it so the merge has to rebase.
	fx.branchWithWork(t, "advance", "advance.txt")
	run(t, fx.work, "git", "push", "origin", "advance:main")

	normal := submitAndProcess(t, r, fx.origin, "polecat-rebased", "mg-rebased")
	if normal.Status != StatusMerged || normal.AlreadyMerged {
		t.Fatalf("normal rebased merge: status=%s already_merged=%v err=%s", normal.Status, normal.AlreadyMerged, normal.Error)
	}
	if normal.MergedSHA == "" || normal.MergedSHA == rebasedHead {
		t.Errorf("a rebased merge lands as a NEW commit: merged_sha=%q branch head=%q", normal.MergedSHA, rebasedHead)
	}
	if normal.TargetAtStart == "" {
		t.Error("a branch found ahead of the target must record TargetAtStart before pushing")
	}
	if got := fx.gateRuns(t); got != 1 {
		t.Fatalf("gates ran %d times after the normal merge, want 1", got)
	}

	// --- 2. fast-forward merge, then resubmit after losing track -------------
	fx.branchWithWork(t, "polecat-ff", "ff.txt")
	first := submitAndProcess(t, r, fx.origin, "polecat-ff", "mg-ff")
	if first.Status != StatusMerged || first.AlreadyMerged {
		t.Fatalf("ff merge: status=%s already_merged=%v err=%s", first.Status, first.AlreadyMerged, first.Error)
	}
	resubmit := submitAndProcess(t, r, fx.origin, "polecat-ff", "mg-ff")
	if resubmit.Status != StatusMerged || !resubmit.AlreadyMerged {
		t.Fatalf("ff resubmit with a prior merged MR on record must resolve as already-merged: status=%s already_merged=%v err=%s",
			resubmit.Status, resubmit.AlreadyMerged, resubmit.Error)
	}
	if !strings.Contains(resubmit.GateOutput, first.ID) {
		t.Errorf("the no-op should name the prior MR that landed it (%s): %q", first.ID, resubmit.GateOutput)
	}
	if got := fx.gateRuns(t); got != 2 {
		t.Fatalf("gates ran %d times after the ff merge + resubmit, want 2 (the resubmit must not re-run them)", got)
	}

	// --- 3. empty branch: head == main's tip, nothing ahead ------------------
	tip := fx.emptyBranch(t, "polecat-empty")
	empty := submitAndProcess(t, r, fx.origin, "polecat-empty", "mg-empty")
	if empty.Status != StatusFailed {
		t.Fatalf("THE DEFECT: an empty branch resolved as %s (already_merged=%v, merged_sha=%s) — nothing was delivered",
			empty.Status, empty.AlreadyMerged, empty.MergedSHA)
	}
	if empty.FailureClass != ClassDefect {
		t.Errorf("class = %q, want %q", empty.FailureClass, ClassDefect)
	}
	if !strings.Contains(empty.Error, "branch carries no commits ahead of main") {
		t.Errorf("the error must name the condition plainly, got: %s", empty.Error)
	}
	if !strings.Contains(empty.Error, tip[:7]) {
		t.Errorf("the error should name the head it inspected (%s): %s", tip[:7], empty.Error)
	}
	if empty.AlreadyMerged || empty.MergedSHA != "" {
		t.Errorf("a refused branch carries no already_merged / merged_sha: %v / %q", empty.AlreadyMerged, empty.MergedSHA)
	}
	if empty.NotRetriedReason == "" || len(empty.Attempts) != 1 || empty.Attempts[0].Stage != stageEmptyBranch {
		t.Errorf("the refusal must be on the record like any terminal failure: reason=%q attempts=%+v", empty.NotRetriedReason, empty.Attempts)
	}
	if got := fx.gateRuns(t); got != 2 {
		t.Errorf("gates ran for an empty branch (%d total, want still 2)", got)
	}
	if fx.mainTip(t) != tip {
		t.Error("origin/main moved on a refused empty branch")
	}

	// Callbacks: OnFailed — never OnMerged — for the empty branch. That is
	// what keeps pogod from closing the item and reaping the author.
	if len(cl.failed) != 1 || cl.failed[0] != empty.ID {
		t.Errorf("OnFailed calls = %v, want exactly [%s]", cl.failed, empty.ID)
	}
	for _, id := range cl.merged {
		if id == empty.ID {
			t.Errorf("OnMerged fired for the empty branch %s", id)
		}
	}
	if len(cl.merged) != 3 {
		t.Errorf("OnMerged calls = %v, want the three real resolutions", cl.merged)
	}

	// Event trail: no refinery_merged for the empty branch, one terminal defect.
	all := readEvents(t, logPath)
	for _, ev := range filterEvents(all, "refinery_merged") {
		if ev.Details["merge_request_id"] == empty.ID {
			t.Errorf("a refinery_merged event was emitted for the empty branch: %v", ev.Details)
		}
	}
	var sawFailed bool
	for _, ev := range filterEvents(all, "refinery_merge_failed") {
		if ev.Details["merge_request_id"] != empty.ID {
			continue
		}
		sawFailed = true
		if ev.Details["class"] != string(ClassDefect) || ev.Details["terminal"] != true {
			t.Errorf("the failure event should be a terminal defect: %v", ev.Details)
		}
	}
	if !sawFailed {
		t.Error("no refinery_merge_failed event for the empty branch")
	}
}

// TestEmptyBranchNotVouchedForByAPriorNoOp pins why a prior AlreadyMerged entry
// does not count as a record. Before mg-c184d that flag was set on ancestry
// alone, so live history can hold false ones — and if a false one vouched for
// the next resubmit of the same empty branch, the defect would perpetuate
// itself through its own output.
func TestEmptyBranchNotVouchedForByAPriorNoOp(t *testing.T) {
	fx := newEmptyBranchFixture(t)
	r, _ := newCallbackRefinery(t)
	tip := fx.emptyBranch(t, "polecat-empty")

	r.mu.Lock()
	r.history = append(r.history, &MergeRequest{
		ID: "mr-false-done", RepoPath: fx.origin, Branch: "polecat-empty", TargetRef: "main",
		Author: "mg-empty", Status: StatusMerged, AlreadyMerged: true, MergedSHA: tip,
	})
	r.mu.Unlock()

	mr := submitAndProcess(t, r, fx.origin, "polecat-empty", "mg-empty")
	if mr.Status != StatusFailed || mr.FailureClass != ClassDefect {
		t.Fatalf("a prior already-merged no-op vouched for an empty branch: status=%s class=%s", mr.Status, mr.FailureClass)
	}

	// Positive control on the same fixture: the identical record WITHOUT the
	// no-op flag is a real prior merge, and it does vouch.
	r.mu.Lock()
	r.history = append(r.history, &MergeRequest{
		ID: "mr-real", RepoPath: fx.origin, Branch: "polecat-empty", TargetRef: "main",
		Author: "mg-empty", Status: StatusMerged, MergedSHA: tip,
	})
	r.mu.Unlock()
	again := submitAndProcess(t, r, fx.origin, "polecat-empty", "mg-empty")
	if again.Status != StatusMerged || !again.AlreadyMerged {
		t.Fatalf("control: a real prior merge containing the head must resolve already-merged, got status=%s err=%s", again.Status, again.Error)
	}
}

// TestPriorMergeMatchesByWorkItem covers the other half of "for this branch or
// work item": the author resubmits the same landed commits under a new branch
// name. The record is keyed by the work item, so it still vouches.
func TestPriorMergeMatchesByWorkItem(t *testing.T) {
	fx := newEmptyBranchFixture(t)
	r, _ := newCallbackRefinery(t)

	head := fx.branchWithWork(t, "polecat-one", "one.txt")
	if mr := submitAndProcess(t, r, fx.origin, "polecat-one", "mg-item"); mr.Status != StatusMerged {
		t.Fatalf("first merge: %s %s", mr.Status, mr.Error)
	}
	run(t, fx.work, "git", "push", "origin", head+":refs/heads/polecat-one-renamed")

	same := submitAndProcess(t, r, fx.origin, "polecat-one-renamed", "mg-item")
	if same.Status != StatusMerged || !same.AlreadyMerged {
		t.Errorf("same work item, renamed branch: status=%s already_merged=%v err=%s", same.Status, same.AlreadyMerged, same.Error)
	}

	run(t, fx.work, "git", "push", "origin", head+":refs/heads/polecat-other")
	other := submitAndProcess(t, r, fx.origin, "polecat-other", "mg-other")
	if other.Status != StatusFailed || other.FailureClass != ClassDefect {
		t.Errorf("a DIFFERENT work item submitting someone else's landed commit delivered nothing: status=%s class=%s",
			other.Status, other.FailureClass)
	}
}
