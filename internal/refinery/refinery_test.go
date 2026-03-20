package refinery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSubmitAndQueue(t *testing.T) {
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
		RepoPath: "/tmp/fakerepo",
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
		RepoPath: "/tmp/repo",
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
