package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
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
