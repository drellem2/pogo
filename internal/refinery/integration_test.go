package refinery

import (
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

// TestUnlinkWorktree verifies that UnlinkWorktree detaches a polecat's
// worktree from the source repo so the branch is no longer "checked out".
func TestUnlinkWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Set up a source repo
	srcDir := t.TempDir()
	run(t, srcDir, "git", "init", "-b", "main")
	run(t, srcDir, "git", "config", "user.email", "test@test.com")
	run(t, srcDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("# Test"), 0644)
	run(t, srcDir, "git", "add", ".")
	run(t, srcDir, "git", "commit", "-m", "initial")

	// Create a linked worktree (simulates polecat spawn)
	wtPath := filepath.Join(t.TempDir(), "polecat-wt")
	run(t, srcDir, "git", "worktree", "add", "-b", "polecat-branch", wtPath)

	// Verify the branch is tracked by git worktree list
	cmd := exec.Command("git", "-C", srcDir, "worktree", "list", "--porcelain")
	out, _ := cmd.Output()
	if !strings.Contains(string(out), wtPath) {
		t.Fatal("expected worktree to be listed")
	}

	// Unlink the worktree
	if err := UnlinkWorktree(srcDir, wtPath); err != nil {
		t.Fatalf("UnlinkWorktree: %v", err)
	}

	// Verify the worktree is no longer tracked
	cmd = exec.Command("git", "-C", srcDir, "worktree", "list", "--porcelain")
	out, _ = cmd.Output()
	if strings.Contains(string(out), wtPath) {
		t.Error("expected worktree to be unlinked from source repo")
	}

	// The directory should still exist (polecat process needs it)
	if _, err := os.Stat(wtPath); os.IsNotExist(err) {
		t.Error("expected worktree directory to still exist")
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
