package refinery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCancelQueuedMRReportsRemoval keeps the historic behaviour intact and
// pins the outcome it now reports.
func TestCancelQueuedMRReportsRemoval(t *testing.T) {
	r := newProgressTestRefinery(t, time.Second)
	origin := initBareOrigin(t, "main")
	id, err := r.Submit(MergeRequest{RepoPath: origin, Branch: "main", TargetRef: "main", Author: "cat"})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := r.Cancel(id)
	if err != nil {
		t.Fatalf("cancelling a queued MR must succeed: %v", err)
	}
	if outcome != CancelRemovedFromQueue {
		t.Errorf("outcome = %q, want %q", outcome, CancelRemovedFromQueue)
	}
	if mr := r.Get(id); mr.Status != StatusCancelled {
		t.Errorf("status = %q, want %q", mr.Status, StatusCancelled)
	}
	if len(r.queue) != 0 {
		t.Errorf("cancelled MR left in the queue: %d entries", len(r.queue))
	}
}

// TestCancelFinishedMRIsRefusedWithItsStatus checks a terminal MR is refused,
// and that the refusal names the status so the caller knows why.
func TestCancelFinishedMRIsRefusedWithItsStatus(t *testing.T) {
	r := newProgressTestRefinery(t, time.Second)
	mr := &MergeRequest{ID: "mr-done", Status: StatusMerged}
	r.byID[mr.ID] = mr

	if _, err := r.Cancel("mr-done"); err == nil {
		t.Fatal("cancelling a merged MR must fail")
	} else if !strings.Contains(err.Error(), "already final") || !strings.Contains(err.Error(), "merged") {
		t.Errorf("refusal should name the status and say it is final, got: %v", err)
	}

	if _, err := r.Cancel("mr-nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown MR should report not found, got: %v", err)
	}
}

// TestCancelReachesProcessingMR is mg-8595's third item. Cancel used to refuse
// anything not queued — it worked only on the state least in need of it, so a
// gate that hung had no recovery short of restarting pogod.
//
// The MR must end as CANCELLED, not FAILED: a cancelled merge did not fail on
// its merits, and the ticket documents the cost of conflating the two (a work
// item reopened by a redundant operator action).
func TestCancelReachesProcessingMR(t *testing.T) {
	r := newProgressTestRefinery(t, 20*time.Millisecond)
	origin := initBareOrigin(t, "main")
	seedBranchWithGate(t, origin, "feature-hang", `quality_gate = "sleep 60"`)

	var failedFired, mergedFired bool
	r.SetOnFailed(func(*MergeRequest) { failedFired = true })
	r.SetOnMerged(func(*MergeRequest) { mergedFired = true })

	id, err := r.Submit(MergeRequest{RepoPath: origin, Branch: "feature-hang", TargetRef: "main", Author: "cat-hang"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.processNext()
	}()

	// Wait for the gate to actually be running — proved by the heartbeat,
	// which is the point of the whole change.
	waitForBeat(t, r, id)

	outcome, err := r.Cancel(id)
	if err != nil {
		t.Fatalf("cancelling a processing MR must now succeed: %v", err)
	}
	if outcome != CancelRequestedInFlight {
		t.Errorf("outcome = %q, want %q", outcome, CancelRequestedInFlight)
	}

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("cancel did not stop the running gate — the merge is still going")
	}

	mr := r.Get(id)
	if mr.Status != StatusCancelled {
		t.Errorf("status = %q, want %q (error: %s)", mr.Status, StatusCancelled, mr.Error)
	}
	if failedFired {
		t.Error("onFailed must NOT fire for a cancelled MR — that is what reopens a work item")
	}
	if mergedFired {
		t.Error("onMerged must not fire for a cancelled MR")
	}
	if got := r.AuthorFailureCount("cat-hang"); got != 0 {
		t.Errorf("a cancelled MR must not count against the author's failure streak, got %d", got)
	}
	if !strings.Contains(mr.Error, "cancelled by operator") {
		t.Errorf("the recorded error should name cancellation as the cause, got: %s", mr.Error)
	}
}

// TestCancelOnRecoveredProcessingMRIsRefusedHonestly covers the MR that reads
// as processing but is not in flight in this daemon — a restart-recovery case.
// There is nothing to kill, and reporting success would be a lie of exactly
// the kind this ticket is about.
func TestCancelOnRecoveredProcessingMRIsRefusedHonestly(t *testing.T) {
	r := newProgressTestRefinery(t, time.Second)
	mr := &MergeRequest{ID: "mr-ghost", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	_, err := r.Cancel("mr-ghost")
	if err == nil {
		t.Fatal("cancelling an MR that is not actually in flight must not report success")
	}
	if !strings.Contains(err.Error(), "not in flight") {
		t.Errorf("refusal should say it is not in flight here, got: %v", err)
	}
}

// TestCancelKillsTheWholeGateProcessTree checks the kill reaches descendants,
// not just the shell. `sh -c` forks for anything compound, so killing the
// shell alone leaves the real work running: it keeps using the worktree it was
// told to stop using, and it holds the output pipe open, which stalls the
// runner that carries the heartbeat.
func TestCancelKillsTheWholeGateProcessTree(t *testing.T) {
	r := newProgressTestRefinery(t, 20*time.Millisecond)
	wtDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "grandchild.log")

	// A gate that backgrounds a grandchild appending forever, then waits. The
	// grandchild is not the process the context kills directly.
	writeGateConfig(t, wtDir, "[gates]\ncommands = [\"sh -c 'while true; do echo tick >> "+marker+"; sleep 0.05; done' & sleep 60\"]\ntimeout = \"300ms\"\n")

	mr := &MergeRequest{ID: "mr-tree", Status: StatusProcessing}
	r.byID[mr.ID] = mr

	start := time.Now()
	if _, _, err := r.runQualityGates(context.Background(), wtDir, wtDir, mr); err == nil {
		t.Fatal("expected the gate to be killed by its timeout")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the kill stalled for %s — a surviving descendant is holding the output pipe", elapsed)
	}

	// The grandchild must stop writing. Sample twice with a gap: if it is
	// still alive the file keeps growing.
	time.Sleep(300 * time.Millisecond)
	first := fileSize(t, marker)
	time.Sleep(400 * time.Millisecond)
	second := fileSize(t, marker)
	if second != first {
		t.Errorf("grandchild survived the kill: marker file grew from %d to %d bytes", first, second)
	}
	if first == 0 {
		t.Fatal("test setup: the grandchild never ran, so this proves nothing")
	}
}

// TestCancelDuringGitStepStopsAtTheNextBoundary checks a cancel that lands
// while no gate is running is still honoured. Without the boundary check it
// would be swallowed until the next gate started — and if that never happened,
// silently ignored.
func TestCancelDuringGitStepStopsAtTheNextBoundary(t *testing.T) {
	r := newProgressTestRefinery(t, time.Second)
	origin := initBareOrigin(t, "main")
	seedBranchWithGate(t, origin, "feature-boundary", `quality_gate = "true"`)

	id, err := r.Submit(MergeRequest{RepoPath: origin, Branch: "feature-boundary", TargetRef: "main", Author: "cat-b"})
	if err != nil {
		t.Fatal(err)
	}
	mr := r.Get(id)

	// Mark the request cancelled before any attempt begins, the state a
	// cancel arriving during the fetch/rebase steps leaves behind.
	r.mu.Lock()
	mr.Status = StatusProcessing
	r.processing = mr
	r.beginProcessingLocked()
	r.requestInFlightCancelLocked(mr)
	r.mu.Unlock()

	_, err = r.processMerge(mr)
	if !isCancelled(err) {
		t.Fatalf("processMerge must report cancellation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "before-attempt") {
		t.Errorf("the error should name the boundary it stopped at, got: %v", err)
	}
}

// TestCancelEmitsItsOwnEventNotAFailure checks the event log separates a
// cancelled merge from a failed one. Anything counting merge failures — an
// author's failure streak, a reliability trend — would otherwise count operator
// actions as branch defects, which is the same class of defect as the one this
// ticket is about.
func TestCancelEmitsItsOwnEventNotAFailure(t *testing.T) {
	logPath := useTempEventLog(t)

	r := newProgressTestRefinery(t, 20*time.Millisecond)
	origin := initBareOrigin(t, "main")
	seedBranchWithGate(t, origin, "polecat-cat-canc", `quality_gate = "sleep 60"`)

	id, err := r.Submit(MergeRequest{
		RepoPath:  origin,
		Branch:    "polecat-cat-canc",
		TargetRef: "main",
		Author:    "cat-canc",
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.processNext()
	}()
	waitForBeat(t, r, id)
	if _, err := r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("cancel did not stop the gate")
	}

	all := readEvents(t, logPath)
	cancelled := filterEvents(all, "refinery_merge_cancelled")
	failed := filterEvents(all, "refinery_merge_failed")
	merged := filterEvents(all, "refinery_merged")

	if len(cancelled) == 0 {
		t.Fatal("expected a refinery_merge_cancelled event, got none")
	}
	if len(failed) != 0 {
		t.Errorf("a cancelled merge must not emit refinery_merge_failed, got %d", len(failed))
	}
	if len(merged) != 0 {
		t.Errorf("a cancelled merge must not emit refinery_merged, got %d", len(merged))
	}

	ev := cancelled[len(cancelled)-1]
	if ev.Agent != "refinery" {
		t.Errorf("cancelled.agent = %q, want refinery", ev.Agent)
	}
	if ev.WorkItemID != "canc" {
		t.Errorf("cancelled.work_item_id = %q, want canc", ev.WorkItemID)
	}
	if stage, ok := ev.Details["stage"].(string); !ok || stage == "" || stage == "unknown" {
		t.Errorf("cancelled event must name the stage it stopped at, got %v", ev.Details["stage"])
	}
}

// seedBranchWithGate pushes a branch to origin carrying the given
// .pogo/refinery.toml, so the refinery picks that gate up when it processes it.
func seedBranchWithGate(t *testing.T, origin, branch, gateToml string) {
	t.Helper()
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", origin, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "checkout", "-b", branch)
	writeGateConfig(t, workDir, gateToml)
	if err := os.WriteFile(filepath.Join(workDir, branch+".txt"), []byte(branch), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "seed "+branch)
	run(t, workDir, "git", "push", "origin", branch)
}

// waitForBeat blocks until the MR's gate has beaten at least once, proving the
// gate is genuinely running before the test acts on it.
func waitForBeat(t *testing.T, r *Refinery, id string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		mr := r.byID[id]
		beating := mr != nil && mr.Progress != nil && mr.Progress.Beats > 0 && mr.Progress.EndTime.IsZero()
		r.mu.Unlock()
		if beating {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no heartbeat observed — the gate never started, or the heartbeat is broken")
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
