package refinery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initBareOrigin creates a bare git repo with an initial commit on the given branch.
// Returns the path to the bare repo directory.
func initBareOrigin(t *testing.T, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", branch)
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", branch)
	return originDir
}

func TestSubmitAndQueue(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour, // won't tick in this test
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	id, err := r.Submit(MergeRequest{
		RepoPath: originDir,
		Branch:   "feature-1",
		Author:   "cat-abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	queue := r.Queue()
	if len(queue) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(queue))
	}
	if queue[0].Branch != "feature-1" {
		t.Errorf("expected branch feature-1, got %s", queue[0].Branch)
	}
	if queue[0].Status != StatusQueued {
		t.Errorf("expected status queued, got %s", queue[0].Status)
	}
	if queue[0].TargetRef != "main" {
		t.Errorf("expected default target main, got %s", queue[0].TargetRef)
	}
}

func TestSubmitValidation(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Missing repo_path
	_, err = r.Submit(MergeRequest{Branch: "feature-1"})
	if err == nil {
		t.Error("expected error for missing repo_path")
	}

	// Missing branch
	_, err = r.Submit(MergeRequest{RepoPath: "/tmp/repo"})
	if err == nil {
		t.Error("expected error for missing branch")
	}
}

func TestGetMergeRequest(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	id, _ := r.Submit(MergeRequest{
		RepoPath: originDir,
		Branch:   "fix-bug",
	})

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("expected to find MR")
	}
	if mr.Branch != "fix-bug" {
		t.Errorf("expected branch fix-bug, got %s", mr.Branch)
	}

	// Not found
	if r.Get("nonexistent") != nil {
		t.Error("expected nil for nonexistent ID")
	}
}

func TestGetStatus(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: 10 * time.Second,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	status := r.GetStatus()
	if !status.Enabled {
		t.Error("expected enabled")
	}
	if status.QueueLen != 0 {
		t.Error("expected empty queue")
	}
}

func TestParseRefineryToml(t *testing.T) {
	dir := t.TempDir()

	// Test quality_gate key
	path := filepath.Join(dir, "simple.toml")
	os.WriteFile(path, []byte(`
quality_gate = "./build.sh"
`), 0644)

	gates := parseRefineryToml(path)
	if len(gates) != 1 || gates[0] != "./build.sh" {
		t.Errorf("expected [./build.sh], got %v", gates)
	}

	// Test [gates] section with commands array
	path2 := filepath.Join(dir, "array.toml")
	os.WriteFile(path2, []byte(`
[gates]
commands = ["./build.sh", "./test.sh"]
`), 0644)

	gates2 := parseRefineryToml(path2)
	if len(gates2) != 2 {
		t.Fatalf("expected 2 gates, got %d: %v", len(gates2), gates2)
	}
	if gates2[0] != "./build.sh" || gates2[1] != "./test.sh" {
		t.Errorf("unexpected gates: %v", gates2)
	}

	// Nonexistent file returns nil
	gates3 := parseRefineryToml("/nonexistent")
	if gates3 != nil {
		t.Error("expected nil for nonexistent file")
	}
}

func TestProcessMergeEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Create a bare "origin" repo with explicit main branch
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// Create a working clone, make an initial commit
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", "main")

	// Create a feature branch with changes
	run(t, workDir, "git", "checkout", "-b", "feature-1")
	os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("new feature"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add feature")
	run(t, workDir, "git", "push", "origin", "feature-1")

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

	// Submit and process
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-1",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Process the item directly
	r.processNext()

	// Check result
	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found")
	}
	if mr.Status != StatusMerged {
		t.Errorf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}

	// Verify the merge happened on origin
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "feature.txt")); os.IsNotExist(err) {
		t.Error("feature.txt not found on main after merge")
	}
}

func TestProcessMergeGateFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Create a bare "origin" repo with explicit main branch
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// Create a working clone, make an initial commit with a failing gate
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0644)
	// Create .pogo/refinery.toml with a gate that will fail
	os.MkdirAll(filepath.Join(workDir, ".pogo"), 0755)
	os.WriteFile(filepath.Join(workDir, ".pogo", "refinery.toml"), []byte(`
quality_gate = "exit 1"
`), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial with failing gate")
	run(t, workDir, "git", "push", "origin", "main")

	// Create feature branch
	run(t, workDir, "git", "checkout", "-b", "feature-fail")
	os.WriteFile(filepath.Join(workDir, "bad.txt"), []byte("bad"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "bad feature")
	run(t, workDir, "git", "push", "origin", "feature-fail")

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

	var failedMR *MergeRequest
	r.SetOnFailed(func(mr *MergeRequest) {
		failedMR = mr
	})

	id, _ := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-fail",
		TargetRef: "main",
		Author:    "test-cat",
	})

	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusFailed {
		t.Errorf("expected failed, got %s", mr.Status)
	}
	if mr.Error == "" {
		t.Error("expected error message")
	}
	if failedMR == nil {
		t.Error("expected onFailed callback to fire")
	}
}

func TestRefineryStartStop(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: 50 * time.Millisecond,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Start(ctx)
		close(done)
	}()

	// Let it tick a few times
	time.Sleep(200 * time.Millisecond)

	cancel()
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("refinery did not stop")
	}
}

func TestHistoryPruneByCount(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:       true,
		PollInterval:  time.Hour,
		WorktreeDir:   dir,
		MaxHistoryLen: 3,
		MaxHistoryAge: -1, // disable age pruning
	})
	if err != nil {
		t.Fatal(err)
	}

	// Manually add 5 entries to history.
	for i := 0; i < 5; i++ {
		mr := &MergeRequest{
			ID:       fmt.Sprintf("mr-%d", i),
			Status:   StatusMerged,
			DoneTime: time.Now(),
		}
		r.history = append(r.history, mr)
		r.byID[mr.ID] = mr
	}
	r.pruneHistoryLocked()

	if len(r.history) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(r.history))
	}
	// Should keep the last 3 (mr-2, mr-3, mr-4).
	if r.history[0].ID != "mr-2" {
		t.Errorf("expected first entry mr-2, got %s", r.history[0].ID)
	}
	// Pruned entries should be removed from byID.
	if r.Get("mr-0") != nil {
		t.Error("expected mr-0 to be pruned from byID")
	}
	if r.Get("mr-1") != nil {
		t.Error("expected mr-1 to be pruned from byID")
	}
	// Kept entries should still be in byID.
	if r.Get("mr-4") == nil {
		t.Error("expected mr-4 to still be in byID")
	}
}

func TestHistoryPruneByAge(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:       true,
		PollInterval:  time.Hour,
		WorktreeDir:   dir,
		MaxHistoryLen: -1, // disable count pruning
		MaxHistoryAge: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r.nowFunc = func() time.Time { return now }

	// Add entries at different ages.
	times := []time.Duration{
		-3 * time.Hour,             // old, should be pruned
		-2*time.Hour - time.Minute, // old, should be pruned
		-1 * time.Hour,             // recent, keep
		-30 * time.Minute,          // recent, keep
	}
	for i, offset := range times {
		mr := &MergeRequest{
			ID:       fmt.Sprintf("mr-%d", i),
			Status:   StatusMerged,
			DoneTime: now.Add(offset),
		}
		r.history = append(r.history, mr)
		r.byID[mr.ID] = mr
	}
	r.pruneHistoryLocked()

	if len(r.history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(r.history))
	}
	if r.history[0].ID != "mr-2" {
		t.Errorf("expected first entry mr-2, got %s", r.history[0].ID)
	}
	if r.Get("mr-0") != nil {
		t.Error("expected mr-0 to be pruned")
	}
	if r.Get("mr-3") == nil {
		t.Error("expected mr-3 to be kept")
	}
}

func TestHistoryPruneBothLimits(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:       true,
		PollInterval:  time.Hour,
		WorktreeDir:   dir,
		MaxHistoryLen: 5,
		MaxHistoryAge: 1 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r.nowFunc = func() time.Time { return now }

	// 8 entries: 3 old (>1h), 5 recent. Age prunes 3, then count prunes none (5 <= 5).
	for i := 0; i < 8; i++ {
		age := -30 * time.Minute
		if i < 3 {
			age = -2 * time.Hour
		}
		mr := &MergeRequest{
			ID:       fmt.Sprintf("mr-%d", i),
			Status:   StatusMerged,
			DoneTime: now.Add(age),
		}
		r.history = append(r.history, mr)
		r.byID[mr.ID] = mr
	}
	r.pruneHistoryLocked()

	if len(r.history) != 5 {
		t.Fatalf("expected 5 history entries, got %d", len(r.history))
	}
}

func TestHistoryDefaultLimits(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.MaxHistoryLen != DefaultMaxHistoryLen {
		t.Errorf("expected default MaxHistoryLen=%d, got %d", DefaultMaxHistoryLen, r.cfg.MaxHistoryLen)
	}
	if r.cfg.MaxHistoryAge != DefaultMaxHistoryAge {
		t.Errorf("expected default MaxHistoryAge=%s, got %s", DefaultMaxHistoryAge, r.cfg.MaxHistoryAge)
	}
}

func TestSubmitRejectsInvalidTargetRef(t *testing.T) {
	originDir := initBareOrigin(t, "main")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Submit with a nonexistent target ref should fail at submission time
	_, err = r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-1",
		TargetRef: "nonexistent-branch",
		Author:    "test-cat",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent target ref")
	}
	if !strings.Contains(err.Error(), "target_ref") || !strings.Contains(err.Error(), "nonexistent-branch") {
		t.Errorf("expected error mentioning target_ref and branch name, got: %s", err)
	}

	// Queue should be empty — the MR was rejected before queuing
	if len(r.Queue()) != 0 {
		t.Errorf("expected empty queue, got %d items", len(r.Queue()))
	}
}

// TestValidateTargetRefFallbackPaths exercises both branches of validateTargetRef:
//   - ls-remote unreachable (no origin configured) → falls back to local rev-parse.
//   - ls-remote reachable but empty output → hard-fails, even if a stale local
//     branch with the same name exists. Regression test for #10.
func TestValidateTargetRefFallbackPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	t.Run("ls-remote unreachable falls back to local rev-parse", func(t *testing.T) {
		// A bare repo has no "origin" remote configured, so ls-remote fails.
		// validateTargetRef must fall back to checking local branches.
		bareRepo := initBareOrigin(t, "main")

		// Existing local branch passes.
		if err := validateTargetRef(bareRepo, "main"); err != nil {
			t.Errorf("expected main to validate via local fallback, got: %v", err)
		}

		// Nonexistent local branch fails.
		if err := validateTargetRef(bareRepo, "nope"); err == nil {
			t.Error("expected error for nonexistent branch via local fallback")
		}
	})

	t.Run("ls-remote empty output hard-fails despite stale local branch", func(t *testing.T) {
		// Set up a bare origin with only "main"...
		originDir := initBareOrigin(t, "main")

		// ...and a working clone that has both "origin" configured and a
		// stale local branch "ghost" that does NOT exist on origin.
		workDir := t.TempDir()
		run(t, workDir, "git", "clone", originDir, ".")
		run(t, workDir, "git", "config", "user.email", "test@test.com")
		run(t, workDir, "git", "config", "user.name", "Test")
		run(t, workDir, "git", "branch", "ghost")

		// Sanity: ls-remote against this clone returns empty for "ghost"
		// and rev-parse against the local branch succeeds. This is exactly
		// the bug condition from #10.
		out, err := exec.Command("git", "-C", workDir, "ls-remote", "--heads", "origin", "ghost").CombinedOutput()
		if err != nil {
			t.Fatalf("ls-remote setup failed: %v: %s", err, out)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Fatalf("ls-remote setup invalid: expected empty output, got %q", string(out))
		}
		if err := exec.Command("git", "-C", workDir, "rev-parse", "--verify", "refs/heads/ghost").Run(); err != nil {
			t.Fatalf("local branch ghost should exist: %v", err)
		}

		// validateTargetRef must reject "ghost" — empty ls-remote is a
		// definitive "not found", local fallback must not save it.
		err = validateTargetRef(workDir, "ghost")
		if err == nil {
			t.Fatal("expected error for ref not on origin, even with stale local branch")
		}
		if !strings.Contains(err.Error(), "ghost") {
			t.Errorf("expected error to mention ref name, got: %v", err)
		}

		// "main" exists on origin and must still pass.
		if err := validateTargetRef(workDir, "main"); err != nil {
			t.Errorf("expected main to validate against origin, got: %v", err)
		}
	})
}

func TestIsRetryableWithInvalidUpstream(t *testing.T) {
	// A plain error is not retryable
	plainErr := fmt.Errorf("rebase onto main: some error: exit status 1")
	if isRetryable(plainErr) {
		t.Error("plain error should not be retryable")
	}

	// A retryableError wrapping "invalid upstream" should be retryable
	retryErr := &retryableError{fmt.Errorf("rebase onto main: invalid upstream 'origin/main': exit status 128")}
	if !isRetryable(retryErr) {
		t.Error("retryableError should be retryable")
	}
}

func TestFailureCountTracking(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Create a bare "origin" repo with a failing gate
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0644)
	os.MkdirAll(filepath.Join(workDir, ".pogo"), 0755)
	os.WriteFile(filepath.Join(workDir, ".pogo", "refinery.toml"), []byte(`
quality_gate = "exit 1"
`), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial")
	run(t, workDir, "git", "push", "origin", "main")

	// Set up refinery with threshold of 3
	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:          true,
		PollInterval:     time.Hour,
		WorktreeDir:      wtDir,
		FailureThreshold: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	var failedMRs []*MergeRequest
	r.SetOnFailed(func(mr *MergeRequest) {
		copy := *mr
		failedMRs = append(failedMRs, &copy)
	})

	// Submit and fail 3 times from the same author
	for i := 0; i < 3; i++ {
		branchName := fmt.Sprintf("feature-fail-%d", i)
		run(t, workDir, "git", "checkout", "main")
		run(t, workDir, "git", "checkout", "-b", branchName)
		os.WriteFile(filepath.Join(workDir, fmt.Sprintf("bad%d.txt", i)), []byte("bad"), 0644)
		run(t, workDir, "git", "add", ".")
		run(t, workDir, "git", "commit", "-m", fmt.Sprintf("bad feature %d", i))
		run(t, workDir, "git", "push", "origin", branchName)

		r.Submit(MergeRequest{
			RepoPath:  originDir,
			Branch:    branchName,
			TargetRef: "main",
			Author:    "test-cat",
		})
		r.processNext()
	}

	if len(failedMRs) != 3 {
		t.Fatalf("expected 3 failures, got %d", len(failedMRs))
	}

	// Check failure counts increment
	if failedMRs[0].FailureCount != 1 {
		t.Errorf("first failure: expected count 1, got %d", failedMRs[0].FailureCount)
	}
	if failedMRs[1].FailureCount != 2 {
		t.Errorf("second failure: expected count 2, got %d", failedMRs[1].FailureCount)
	}
	if failedMRs[2].FailureCount != 3 {
		t.Errorf("third failure: expected count 3, got %d", failedMRs[2].FailureCount)
	}

	// Threshold should only be reached on the third failure
	if failedMRs[0].ThresholdReached {
		t.Error("first failure should not reach threshold")
	}
	if failedMRs[1].ThresholdReached {
		t.Error("second failure should not reach threshold")
	}
	if !failedMRs[2].ThresholdReached {
		t.Error("third failure should reach threshold")
	}

	// AuthorFailureCount should reflect the count
	if count := r.AuthorFailureCount("test-cat"); count != 3 {
		t.Errorf("expected AuthorFailureCount=3, got %d", count)
	}
}

func TestFailureCountResetsOnSuccess(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Create a bare "origin" repo with a failing gate
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0644)
	os.MkdirAll(filepath.Join(workDir, ".pogo"), 0755)
	os.WriteFile(filepath.Join(workDir, ".pogo", "refinery.toml"), []byte(`
quality_gate = "exit 1"
`), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial")
	run(t, workDir, "git", "push", "origin", "main")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:          true,
		PollInterval:     time.Hour,
		WorktreeDir:      wtDir,
		FailureThreshold: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Fail twice
	for i := 0; i < 2; i++ {
		branchName := fmt.Sprintf("feature-fail-reset-%d", i)
		run(t, workDir, "git", "checkout", "main")
		run(t, workDir, "git", "checkout", "-b", branchName)
		os.WriteFile(filepath.Join(workDir, fmt.Sprintf("reset%d.txt", i)), []byte("bad"), 0644)
		run(t, workDir, "git", "add", ".")
		run(t, workDir, "git", "commit", "-m", fmt.Sprintf("bad %d", i))
		run(t, workDir, "git", "push", "origin", branchName)

		r.Submit(MergeRequest{
			RepoPath:  originDir,
			Branch:    branchName,
			TargetRef: "main",
			Author:    "reset-cat",
		})
		r.processNext()
	}

	if count := r.AuthorFailureCount("reset-cat"); count != 2 {
		t.Fatalf("expected 2 failures before success, got %d", count)
	}

	// Now succeed: remove the failing gate and submit a clean branch
	os.WriteFile(filepath.Join(workDir, ".pogo", "refinery.toml"), []byte(`
quality_gate = "true"
`), 0644)
	run(t, workDir, "git", "checkout", "main")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "fix gate")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "feature-success")
	os.WriteFile(filepath.Join(workDir, "good.txt"), []byte("good"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "good feature")
	run(t, workDir, "git", "push", "origin", "feature-success")

	r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-success",
		TargetRef: "main",
		Author:    "reset-cat",
	})
	r.processNext()

	// Failure count should be reset after success
	if count := r.AuthorFailureCount("reset-cat"); count != 0 {
		t.Errorf("expected failure count reset to 0 after success, got %d", count)
	}
}

func TestFailureCountDefaultThreshold(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.FailureThreshold != DefaultFailureThreshold {
		t.Errorf("expected default FailureThreshold=%d, got %d", DefaultFailureThreshold, r.cfg.FailureThreshold)
	}
}

func TestFailureCountDisabledThreshold(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:          true,
		PollInterval:     time.Hour,
		WorktreeDir:      dir,
		FailureThreshold: -1, // disabled
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.FailureThreshold != -1 {
		t.Errorf("expected FailureThreshold=-1, got %d", r.cfg.FailureThreshold)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}
