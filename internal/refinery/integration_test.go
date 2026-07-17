package refinery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEndToEndMergeLoop simulates the full autonomous cycle:
// 1. A "polecat" creates a feature branch and pushes it
// 2. The refinery picks it up, runs quality gates, and merges
// 3. The merged code appears on main
//
// This test uses real git repos but does not require macguffin or the agent
// registry — it focuses on the refinery's merge-queue behavior.
func TestEndToEndMergeLoop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// === Set up "origin" bare repo ===
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// === Set up a working clone with an initial commit ===
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	// Create a build.sh that always passes (quality gate)
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit with build gate")
	run(t, workDir, "git", "push", "origin", "main")

	// === Simulate polecat work: create feature branch ===
	run(t, workDir, "git", "checkout", "-b", "polecat-gt-abc")
	os.WriteFile(filepath.Join(workDir, "feature.go"), []byte("package main\n// new feature\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: add feature (gt-abc)")
	run(t, workDir, "git", "push", "origin", "polecat-gt-abc")

	// === Set up refinery ===
	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour, // manual processing
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Track merge events
	var mergedID string
	r.SetOnMerged(func(mr *MergeRequest) {
		mergedID = mr.ID
	})
	var failedID string
	r.SetOnFailed(func(mr *MergeRequest) {
		failedID = mr.ID
	})

	// === Submit to merge queue (what the polecat would do via mg done) ===
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "polecat-gt-abc",
		TargetRef: "main",
		Author:    "cat-abc",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify queued
	queue := r.Queue()
	if len(queue) != 1 {
		t.Fatalf("expected 1 queued, got %d", len(queue))
	}
	if queue[0].Author != "cat-abc" {
		t.Errorf("author = %q, want cat-abc", queue[0].Author)
	}

	// === Process the merge ===
	r.processNext()

	// === Verify merge succeeded ===
	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found after processing")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s, gate output: %s)", mr.Status, mr.Error, mr.GateOutput)
	}
	if mergedID != id {
		t.Errorf("onMerged callback got ID %q, want %q", mergedID, id)
	}
	if failedID != "" {
		t.Errorf("onFailed should not have been called, got ID %q", failedID)
	}

	// === Verify merged code is on main at origin ===
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "feature.go")); os.IsNotExist(err) {
		t.Error("feature.go not found on main after merge")
	}
	// Original file should still be there
	if _, err := os.Stat(filepath.Join(verifyDir, "main.go")); os.IsNotExist(err) {
		t.Error("main.go missing from main after merge")
	}

	// History should have the merged item
	history := r.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(history))
	}
	if history[0].Status != StatusMerged {
		t.Errorf("history status = %s, want merged", history[0].Status)
	}

	// Queue should be empty
	if len(r.Queue()) != 0 {
		t.Error("queue should be empty after processing")
	}
}

// TestResubmitAlreadyMergedBranch reproduces the double-merge from gh #34: a
// polecat loses track of its merged MR and re-submits the same branch. The
// refinery must detect that the branch already landed on the target and
// resolve the second MR as merged WITHOUT re-running gates or pushing — and
// still fire OnMerged so the event-driven polecat stop (mg-ff34) happens.
func TestResubmitAlreadyMergedBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	logPath := useTempEventLog(t)

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	// Gate script counts its runs in a file outside both repos, so we can
	// assert the no-op resubmit did not re-run gates.
	gateMarker := filepath.Join(t.TempDir(), "gate-runs")
	gate := "#!/bin/sh\necho ran >> " + gateMarker + "\nexit 0\n"
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte(gate), 0755)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial with counting gate")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "polecat-mg-dup")
	os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("feature"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: feature (mg-dup)")
	run(t, workDir, "git", "push", "origin", "polecat-mg-dup")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var mergedIDs []string
	r.SetOnMerged(func(mr *MergeRequest) {
		mergedIDs = append(mergedIDs, mr.ID)
	})

	submit := func() string {
		t.Helper()
		id, err := r.Submit(MergeRequest{
			RepoPath:  originDir,
			Branch:    "polecat-mg-dup",
			TargetRef: "main",
			Author:    "mg-dup",
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	// First cycle: normal merge.
	firstID := submit()
	r.processNext()
	first := r.Get(firstID)
	if first == nil || first.Status != StatusMerged {
		t.Fatalf("first MR: expected merged, got %+v", first)
	}
	if first.AlreadyMerged {
		t.Error("first MR should not be flagged already_merged")
	}

	mainSHA := gitOutput(t, workDir, "ls-remote", "origin", "main")

	// Second cycle: the polecat re-submits the same (now merged) branch.
	secondID := submit()
	r.processNext()
	second := r.Get(secondID)
	if second == nil || second.Status != StatusMerged {
		t.Fatalf("resubmitted MR: expected merged (terminal for poll loops), got %+v", second)
	}
	if !second.AlreadyMerged {
		t.Error("resubmitted MR should be flagged already_merged")
	}
	if !strings.Contains(second.GateOutput, "already merged") {
		t.Errorf("gate output should note the no-op, got %q", second.GateOutput)
	}
	if len(mergedIDs) != 2 || mergedIDs[1] != secondID {
		t.Errorf("OnMerged should fire for the no-op resolution too (polecat stop), got %v", mergedIDs)
	}

	// Nothing new landed: target unchanged, gates ran exactly once.
	if got := gitOutput(t, workDir, "ls-remote", "origin", "main"); got != mainSHA {
		t.Errorf("origin/main moved on a no-op resubmit: %q -> %q", mainSHA, got)
	}
	if data, err := os.ReadFile(gateMarker); err != nil || strings.Count(string(data), "ran") != 1 {
		t.Errorf("gates should run exactly once, marker = %q (err %v)", string(data), err)
	}

	// Event trail: one real attempt+merge, then a merged event flagged
	// already_merged with no second attempt.
	all := readEvents(t, logPath)
	if got := len(filterEvents(all, "refinery_merge_attempted")); got != 1 {
		t.Errorf("expected 1 refinery_merge_attempted, got %d", got)
	}
	merged := filterEvents(all, "refinery_merged")
	if len(merged) != 2 {
		t.Fatalf("expected 2 refinery_merged events, got %d", len(merged))
	}
	if flag, _ := merged[0].Details["already_merged"].(bool); flag {
		t.Error("first refinery_merged should not carry already_merged")
	}
	if flag, _ := merged[1].Details["already_merged"].(bool); !flag {
		t.Errorf("second refinery_merged should carry already_merged=true, details = %v", merged[1].Details)
	}
}

// TestFailedTargetPushDoesNotPoisonReusedClone reproduces the persistent-clone
// reuse bug from gh #80 / mg-f1db. ensureWorktree keeps ONE clone per repo. If
// a cycle's local ff-merge to the target lands but the subsequent
// `git push origin <target>` fails (protected branch, transient remote error),
// the clone's local target is left AHEAD of origin and never rolled back. Under
// the old code the NEXT MR reusing that clone did `checkout <target>` +
// `pull --ff-only`, which aborts "Not possible to fast-forward" and was returned
// non-retryable — wedging every later MR through that clone.
//
// The fix hard-resets the target to origin/<target> at the start of the merge
// phase, so a poisoned/ahead target self-heals. This test drives the real path:
// a pre-receive hook on origin rejects the first MR's push to main (after its
// local merge already landed), leaving the reused clone poisoned; the hook is
// then removed and a second MR must merge cleanly through the same clone.
func TestFailedTargetPushDoesNotPoisonReusedClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// === origin bare repo + working clone with a passing gate on main ===
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit with build gate")
	run(t, workDir, "git", "push", "origin", "main")

	// feature-1 forks main — this MR's push will be rejected below.
	run(t, workDir, "git", "checkout", "-b", "feature-1")
	os.WriteFile(filepath.Join(workDir, "feature1.txt"), []byte("feature 1"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: feature 1")
	run(t, workDir, "git", "push", "origin", "feature-1")

	// feature-2 forks main independently — the second MR, which must recover.
	run(t, workDir, "git", "checkout", "main")
	run(t, workDir, "git", "checkout", "-b", "feature-2")
	os.WriteFile(filepath.Join(workDir, "feature2.txt"), []byte("feature 2"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: feature 2")
	run(t, workDir, "git", "push", "origin", "feature-2")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Install a pre-receive hook on origin that rejects every push. The
	// refinery's local ff-merge of feature-1 still succeeds, so the reused
	// clone is left with local main ahead of origin — the poison state.
	hookPath := filepath.Join(originDir, "hooks", "pre-receive")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho 'remote: rejected by protected-branch policy' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	id1, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-1",
		TargetRef: "main",
		Author:    "cat-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()
	if mr1 := r.Get(id1); mr1 == nil || mr1.Status != StatusFailed {
		t.Fatalf("MR #1 should fail (push rejected by hook), got %+v", mr1)
	}

	// Remove the hook: pushes to main now succeed. The clone is still poisoned
	// (local main ahead of origin from feature-1's un-pushed local merge).
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}

	// MR #2 reuses the SAME persistent clone. Without the target reset it fails
	// with "Not possible to fast-forward"; with it, the clone realigns to
	// origin/main and feature-2 lands.
	id2, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-2",
		TargetRef: "main",
		Author:    "cat-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()
	mr2 := r.Get(id2)
	if mr2 == nil || mr2.Status != StatusMerged {
		t.Fatalf("MR #2 should merge through the reused clone, got %+v (error: %s)", mr2, func() string {
			if mr2 != nil {
				return mr2.Error
			}
			return "<nil>"
		}())
	}

	// origin/main must carry feature-2 but NOT feature-1: feature-1's poisoned
	// local merge was discarded by the target reset and was never pushed.
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "feature2.txt")); os.IsNotExist(err) {
		t.Error("feature2.txt not found on main after MR #2 merged")
	}
	if _, err := os.Stat(filepath.Join(verifyDir, "feature1.txt")); !os.IsNotExist(err) {
		t.Error("feature1.txt should NOT be on main — MR #1's push was rejected and its local merge must be discarded by the reset")
	}
}

// gitOutput runs a git command in dir and returns its trimmed stdout.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// TestEndToEndMergeRejection simulates a failed merge:
// 1. Polecat creates a branch
// 2. Refinery processes it but quality gate fails
// 3. The failure is recorded and the callback fires
func TestEndToEndMergeRejection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	// Quality gate that always fails
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\necho 'BUILD FAILED: syntax error'\nexit 1\n"), 0755)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial with failing gate")
	run(t, workDir, "git", "push", "origin", "main")

	// Feature branch
	run(t, workDir, "git", "checkout", "-b", "polecat-gt-bad")
	os.WriteFile(filepath.Join(workDir, "bad.go"), []byte("package main\n// broken\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: broken feature")
	run(t, workDir, "git", "push", "origin", "polecat-gt-bad")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	var failedMR *MergeRequest
	r.SetOnFailed(func(mr *MergeRequest) {
		failedMR = mr
	})

	id, _ := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "polecat-gt-bad",
		TargetRef: "main",
		Author:    "cat-bad",
	})

	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", mr.Status)
	}
	if mr.Error == "" {
		t.Error("expected non-empty error")
	}
	if failedMR == nil {
		t.Fatal("onFailed callback should have fired")
	}
	if failedMR.Author != "cat-bad" {
		t.Errorf("failed author = %q, want cat-bad", failedMR.Author)
	}

	// Verify main is unchanged — bad.go should NOT be on main
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "bad.go")); !os.IsNotExist(err) {
		t.Error("bad.go should NOT be on main after failed merge")
	}
}

// TestMultipleMergeRequests verifies FIFO ordering of the merge queue.
func TestMultipleMergeRequests(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial")
	run(t, workDir, "git", "push", "origin", "main")

	// Create two feature branches, stacked (so both can ff-merge sequentially)
	run(t, workDir, "git", "checkout", "-b", "polecat-1")
	os.WriteFile(filepath.Join(workDir, "feat1.txt"), []byte("feature 1"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat 1")
	run(t, workDir, "git", "push", "origin", "polecat-1")

	// polecat-2 builds on polecat-1 so it can ff after polecat-1 merges
	run(t, workDir, "git", "checkout", "-b", "polecat-2")
	os.WriteFile(filepath.Join(workDir, "feat2.txt"), []byte("feature 2"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat 2")
	run(t, workDir, "git", "push", "origin", "polecat-2")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	var mergeOrder []string
	r.SetOnMerged(func(mr *MergeRequest) {
		mergeOrder = append(mergeOrder, mr.Branch)
	})

	r.Submit(MergeRequest{RepoPath: originDir, Branch: "polecat-1", Author: "cat-1"})
	r.Submit(MergeRequest{RepoPath: originDir, Branch: "polecat-2", Author: "cat-2"})

	// Process both
	r.processNext()
	r.processNext()

	if len(mergeOrder) != 2 {
		t.Fatalf("expected 2 merges, got %d", len(mergeOrder))
	}
	if mergeOrder[0] != "polecat-1" {
		t.Errorf("first merge = %q, want polecat-1", mergeOrder[0])
	}
	if mergeOrder[1] != "polecat-2" {
		t.Errorf("second merge = %q, want polecat-2", mergeOrder[1])
	}

	// Both features should be on main
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	for _, f := range []string{"feat1.txt", "feat2.txt"} {
		if _, err := os.Stat(filepath.Join(verifyDir, f)); os.IsNotExist(err) {
			t.Errorf("%s not found on main", f)
		}
	}
}

// TestBranchCheckedOutInWorktree simulates the scenario from GitHub issue #4:
// a polecat's worktree is still live when the refinery tries to process the MR.
// The refinery should still succeed because it fetches from the real remote
// (bare origin), not from a local dev repo with linked worktrees.
func TestBranchCheckedOutInWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// === Set up "origin" bare repo ===
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// === Set up a "dev repo" (simulates the user's working clone) ===
	devDir := t.TempDir()
	run(t, devDir, "git", "clone", originDir, ".")
	run(t, devDir, "git", "config", "user.email", "test@test.com")
	run(t, devDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(devDir, "README.md"), []byte("# Test"), 0644)
	run(t, devDir, "git", "add", ".")
	run(t, devDir, "git", "commit", "-m", "initial commit")
	run(t, devDir, "git", "push", "origin", "main")

	// === Simulate polecat: create a linked worktree on a feature branch ===
	// This is what `git worktree add` does when spawning a polecat.
	polecatDir := filepath.Join(t.TempDir(), "polecat-wt")
	run(t, devDir, "git", "worktree", "add", "-b", "polecat-wt", polecatDir)

	// Make changes in the polecat worktree
	os.WriteFile(filepath.Join(polecatDir, "feature.txt"), []byte("feature"), 0644)
	run(t, polecatDir, "git", "add", ".")
	run(t, polecatDir, "git", "commit", "-m", "feat: add feature (wt)")
	run(t, polecatDir, "git", "push", "origin", "polecat-wt")

	// The polecat worktree is STILL LIVE — branch polecat-wt is checked out there.
	// This is the scenario that caused the stall in issue #4.

	// === Set up refinery pointing at the bare origin (not the dev repo) ===
	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Submit with origin (bare repo) as RepoPath — this is the correct path
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "polecat-wt",
		TargetRef: "main",
		Author:    "cat-wt",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}

	// Verify merge landed on main
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "feature.txt")); os.IsNotExist(err) {
		t.Error("feature.txt not found on main after merge")
	}
}

// TestFixRemoteURLRejectsLocalDevRepo verifies that fixRemoteURL returns an
// error when the source repo is a non-bare repo with no usable remote,
// preventing the "already checked out" stall.
func TestFixRemoteURLRejectsLocalDevRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Create a non-bare repo with no remotes (simulates a dev repo
	// whose origin we can't resolve).
	noRemoteDir := t.TempDir()
	run(t, noRemoteDir, "git", "init", "-b", "main")
	run(t, noRemoteDir, "git", "config", "user.email", "test@test.com")
	run(t, noRemoteDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(noRemoteDir, "README.md"), []byte("# Test"), 0644)
	run(t, noRemoteDir, "git", "add", ".")
	run(t, noRemoteDir, "git", "commit", "-m", "initial")

	// Clone it (simulates ensureWorktree's clone step)
	cloneDir := t.TempDir()
	run(t, cloneDir, "git", "clone", "--no-local", noRemoteDir, ".")

	// fixRemoteURL should reject because source is non-bare with no remote
	err := fixRemoteURL(cloneDir, noRemoteDir)
	if err == nil {
		t.Fatal("expected error from fixRemoteURL for non-bare repo without remotes")
	}
	if !strings.Contains(err.Error(), "no remote configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestFixRemoteURLAllowsBareRepo verifies that fixRemoteURL accepts a bare
// repo as the source (the common case in tests and when RepoPath points
// directly at the origin).
func TestFixRemoteURLAllowsBareRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	bareDir := t.TempDir()
	run(t, bareDir, "git", "init", "--bare", "-b", "main")

	// Create a working repo to be the "clone"
	workDir := t.TempDir()
	run(t, workDir, "git", "init", "-b", "main")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	run(t, workDir, "git", "remote", "add", "origin", bareDir)

	// fixRemoteURL should succeed because source is a bare repo
	err := fixRemoteURL(workDir, bareDir)
	if err != nil {
		t.Fatalf("fixRemoteURL should accept bare repo, got: %v", err)
	}
}

// TestEnsureWorktreeReclonesStaleAlternates verifies that ensureWorktree
// detects a stale clone with git alternates (from a pre-fix clone without
// --no-local) and re-clones it cleanly.
func TestEnsureWorktreeReclonesStaleAlternates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Set up bare origin
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// Set up a dev repo with an initial commit
	devDir := t.TempDir()
	run(t, devDir, "git", "clone", originDir, ".")
	run(t, devDir, "git", "config", "user.email", "test@test.com")
	run(t, devDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(devDir, "README.md"), []byte("# Test"), 0644)
	run(t, devDir, "git", "add", ".")
	run(t, devDir, "git", "commit", "-m", "initial")
	run(t, devDir, "git", "push", "origin", "main")

	// Create a feature branch and push it
	run(t, devDir, "git", "checkout", "-b", "feat-reclone")
	os.WriteFile(filepath.Join(devDir, "feat.txt"), []byte("feature"), 0644)
	run(t, devDir, "git", "add", ".")
	run(t, devDir, "git", "commit", "-m", "feat")
	run(t, devDir, "git", "push", "origin", "feat-reclone")

	// Set up refinery
	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a stale clone: clone the repo, then inject an alternates file
	// to mimic what older git versions or --shared clones produce.
	repoName := filepath.Base(devDir)
	staleClone := filepath.Join(wtDir, repoName)
	cmd := exec.Command("git", "clone", "--no-local", devDir, staleClone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	// Inject alternates file to simulate a stale clone with shared objects
	altDir := filepath.Join(staleClone, ".git", "objects", "info")
	os.MkdirAll(altDir, 0755)
	altFile := filepath.Join(altDir, "alternates")
	os.WriteFile(altFile, []byte(filepath.Join(devDir, ".git", "objects")+"\n"), 0644)

	// Verify alternates file exists (confirms the clone is "stale")
	if !hasAlternates(staleClone) {
		t.Fatal("expected stale clone to have alternates")
	}

	// Now call ensureWorktree — it should detect alternates and re-clone
	resultDir, err := r.ensureWorktree(devDir)
	if err != nil {
		t.Fatalf("ensureWorktree failed: %v", err)
	}
	if resultDir != staleClone {
		t.Fatalf("expected worktree at %s, got %s", staleClone, resultDir)
	}

	// The re-cloned repo should NOT have alternates
	if hasAlternates(resultDir) {
		t.Error("re-cloned worktree should not have alternates")
	}

	// Verify it's functional: submit and process an MR through it
	id, err := r.Submit(MergeRequest{
		RepoPath:  devDir,
		Branch:    "feat-reclone",
		TargetRef: "main",
		Author:    "test-reclone",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}
}

// TestOnSubmitCallback verifies that the OnSubmit callback fires when
// a merge request is submitted.
func TestOnSubmitCallback(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	originDir := initBareOrigin(t, "main")

	var submittedMR *MergeRequest
	r.SetOnSubmit(func(mr *MergeRequest) {
		submittedMR = mr
	})

	id, err := r.Submit(MergeRequest{
		RepoPath: originDir,
		Branch:   "feat-1",
		Author:   "cat-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	if submittedMR == nil {
		t.Fatal("OnSubmit callback was not fired")
	}
	if submittedMR.ID != id {
		t.Errorf("OnSubmit got ID %q, want %q", submittedMR.ID, id)
	}
	if submittedMR.Author != "cat-test" {
		t.Errorf("OnSubmit got author %q, want cat-test", submittedMR.Author)
	}
}

// TestRebaseReplaySucceedsWithoutAmbientGitIdentity reproduces the
// "Committer identity unknown" failure (ia-1428 / gh #7). The refinery's
// worktree clone has no local user.name/user.email. When it runs in an
// environment with no global/system git config and no usable GIT_*_NAME/EMAIL
// env vars (pogod under launchd, CI runners), a rebase that *replays* commits
// onto a moved target fails because git has no committer identity. The fix
// supplies a default identity from gitCmdOutput, making the refinery
// self-contained.
//
// To exercise the production code path the test strips every ambient identity
// source — global config, system config, and the GIT_*_NAME/EMAIL vars seeded
// by TestMain — leaving the gitCmdOutput-injected default as the only identity
// git can use. Without the fix the rebase fails; with it the MR merges and the
// replayed commit carries the refinery identity.
func TestRebaseReplaySucceedsWithoutAmbientGitIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Neutralize every ambient git-identity source. t.Setenv restores these
	// on cleanup (and forbids t.Parallel, which is what we want).
	emptyCfg := filepath.Join(t.TempDir(), "empty.gitconfig")
	if err := os.WriteFile(emptyCfg, nil, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", emptyCfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")

	// === Set up "origin" bare repo ===
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// === Working clone for the SETUP commits ===
	// The `run` helper supplies its own GIT_*_NAME/EMAIL ("Test") for these
	// commits, so the setup identity lives only in the produced commit objects
	// — it does not leak into the refinery's worktree clone or the ambient
	// environment the rebase-under-test runs in.
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")

	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", "main")

	// === Polecat branch forks from main and adds a commit ===
	run(t, workDir, "git", "checkout", "-b", "polecat-identity")
	os.WriteFile(filepath.Join(workDir, "feature.go"), []byte("package main\n// feature\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: add feature (identity)")
	run(t, workDir, "git", "push", "origin", "polecat-identity")

	// === Advance main on origin so the rebase must REPLAY the branch commit ===
	// (a no-op fast-forward rebase wouldn't create a commit and wouldn't need a
	// committer identity — we need a genuine replay to reproduce the bug).
	run(t, workDir, "git", "checkout", "main")
	os.WriteFile(filepath.Join(workDir, "other.go"), []byte("package main\n// moved main\n"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "chore: advance main")
	run(t, workDir, "git", "push", "origin", "main")

	// === Refinery ===
	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "polecat-identity",
		TargetRef: "main",
		Author:    "cat-identity",
	})
	if err != nil {
		t.Fatal(err)
	}

	r.processNext()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found after processing")
	}
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s, gate output: %s)", mr.Status, mr.Error, mr.GateOutput)
	}

	// === Verify the replayed commit carries the refinery committer identity ===
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "feature.go")); os.IsNotExist(err) {
		t.Error("feature.go not found on main after merge")
	}
	committer := strings.TrimSpace(runOut(t, verifyDir, "git", "log", "-1", "--format=%cn", "main"))
	if committer != refineryCommitterName {
		t.Errorf("replayed commit committer = %q, want %q", committer, refineryCommitterName)
	}
	// The original author ("Test", set by the run helper for the setup commit)
	// must be preserved by the rebase replay — the injected refinery identity
	// supplies the committer, not the author.
	author := strings.TrimSpace(runOut(t, verifyDir, "git", "log", "-1", "--format=%an", "main"))
	if author != "Test" {
		t.Errorf("replayed commit author = %q, want %q (rebase should preserve author, not overwrite with committer)", author, "Test")
	}
}

// TestSubmitRefusesHTTPSRemoteWithoutCredentials reproduces the mg-9e00
// first-touch failure: a repo whose origin is an HTTPS URL that pogod can't
// authenticate against. The refinery must refuse the MR with an actionable
// error rather than letting it run through the full pipeline only to fail
// opaquely at push.
//
// The test uses an httptest server that returns 401 on every request to
// simulate an HTTPS remote requiring credentials that aren't available in
// the launchd-spawned process environment.
func TestSubmitRefusesHTTPSRemoteWithoutCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		http.Error(w, "auth required", http.StatusUnauthorized)
	}))
	defer srv.Close()

	srcDir := t.TempDir()
	run(t, srcDir, "git", "init", "-b", "main")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("# Test"), 0644)
	run(t, srcDir, "git", "add", ".")
	run(t, srcDir, "git", "commit", "-m", "initial")
	// Origin URL points at a server that always returns 401, simulating
	// an HTTPS remote whose credentials aren't visible to pogod.
	run(t, srcDir, "git", "remote", "add", "origin", srv.URL)

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, submitErr := r.Submit(MergeRequest{
		RepoPath:  srcDir,
		Branch:    "feat",
		TargetRef: "main",
		Author:    "cat-auth",
	})
	if submitErr == nil {
		t.Fatal("Submit should have refused the MR for an unreachable HTTPS remote")
	}
	msg := submitErr.Error()

	// First three lines must name the failure and a concrete next step.
	first3 := strings.SplitN(msg, "\n", 4)
	if len(first3) < 3 {
		t.Fatalf("expected at least 3 lines in error, got %d:\n%s", len(first3), msg)
	}
	header := first3[0] + "\n" + first3[1] + "\n" + first3[2]
	if !strings.Contains(header, "could not authenticate") {
		t.Errorf("first 3 lines should name auth failure, got:\n%s", header)
	}

	// Actionable next-steps must be present.
	for _, phrase := range []string{
		"Switch the remote to SSH",
		"credential helper",
		"GIT_ASKPASS",
	} {
		if !strings.Contains(msg, phrase) {
			t.Errorf("error missing actionable phrase %q\nfull error:\n%s", phrase, msg)
		}
	}

	// Raw git output must be preserved further down for debugging.
	if !strings.Contains(msg, "git output:") {
		t.Errorf("error should include 'git output:' section with raw stderr, got:\n%s", msg)
	}
}

// TestSubmitWakesRunningLoop verifies that a Submit wakes the queue loop
// immediately instead of waiting out the poll interval, and that multiple
// submits drain back-to-back. The poll interval is one hour, so any pickup
// within the test timeout proves the wake path (gh #36).
func TestSubmitWakesRunningLoop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Origin with a passing quality gate on main.
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", "main")

	// Two feature branches, each with its own file.
	for _, name := range []string{"feature-a", "feature-b"} {
		run(t, workDir, "git", "checkout", "-b", name, "main")
		os.WriteFile(filepath.Join(workDir, name+".go"), []byte("package main\n"), 0644)
		run(t, workDir, "git", "add", ".")
		run(t, workDir, "git", "commit", "-m", "feat: "+name)
		run(t, workDir, "git", "push", "origin", name)
	}

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour, // pickup within the timeout proves the wake, not the tick
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	merged := make(chan string, 2)
	r.SetOnMerged(func(mr *MergeRequest) {
		merged <- mr.ID
	})
	r.SetOnFailed(func(mr *MergeRequest) {
		t.Errorf("unexpected merge failure: %s: %s", mr.ID, mr.Error)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		r.Start(ctx)
		close(loopDone)
	}()
	defer func() {
		cancel()
		<-loopDone
	}()

	// Let the loop pass its initial processNext and block in select.
	time.Sleep(100 * time.Millisecond)

	// Back-to-back submits: the first wake picks up feature-a; feature-b is
	// drained by the loop's re-arm (wakeIfActionable), not a poll tick.
	ids := make(map[string]bool)
	for _, name := range []string{"feature-a", "feature-b"} {
		id, err := r.Submit(MergeRequest{
			RepoPath: originDir,
			Branch:   name,
			Author:   "cat-" + name,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[id] = true
	}

	for i := 0; i < 2; i++ {
		select {
		case id := <-merged:
			if !ids[id] {
				t.Errorf("merged unknown MR %s", id)
			}
			delete(ids, id)
		case <-time.After(60 * time.Second):
			t.Fatalf("MR not merged before timeout — submit did not wake the loop (still waiting: %v)", ids)
		}
	}
}
