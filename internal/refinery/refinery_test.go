package refinery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestParseRefineryTomlRetryKeys(t *testing.T) {
	dir := t.TempDir()

	// max_attempts under [gates]
	path := filepath.Join(dir, "max_attempts.toml")
	os.WriteFile(path, []byte(`
[gates]
commands = ["./build.sh"]
max_attempts = 9
`), 0644)
	cfg := parseRefineryConfig(path)
	if cfg.MaxAttempts != 9 {
		t.Errorf("expected MaxAttempts=9, got %d", cfg.MaxAttempts)
	}

	// skip_on_retry under [gates]
	path2 := filepath.Join(dir, "skip.toml")
	os.WriteFile(path2, []byte(`
[gates]
commands = ["./build.sh"]
skip_on_retry = true
`), 0644)
	cfg2 := parseRefineryConfig(path2)
	if !cfg2.SkipGatesOnRetry {
		t.Error("expected SkipGatesOnRetry=true")
	}

	// skip_on_retry = false leaves field zero-valued
	path3 := filepath.Join(dir, "skip_false.toml")
	os.WriteFile(path3, []byte(`
[gates]
skip_on_retry = false
`), 0644)
	cfg3 := parseRefineryConfig(path3)
	if cfg3.SkipGatesOnRetry {
		t.Error("expected SkipGatesOnRetry=false")
	}

	// Both keys + commands array coexist
	path4 := filepath.Join(dir, "all.toml")
	os.WriteFile(path4, []byte(`
[gates]
commands = ["./build.sh", "./test.sh"]
max_attempts = 12
skip_on_retry = true

[deploy]
command = "./deploy.sh"
`), 0644)
	cfg4 := parseRefineryConfig(path4)
	if cfg4.MaxAttempts != 12 {
		t.Errorf("expected MaxAttempts=12, got %d", cfg4.MaxAttempts)
	}
	if !cfg4.SkipGatesOnRetry {
		t.Error("expected SkipGatesOnRetry=true alongside other keys")
	}
	if len(cfg4.Gates) != 2 {
		t.Errorf("expected 2 gates alongside retry keys, got %v", cfg4.Gates)
	}
	if cfg4.DeployCommand != "./deploy.sh" {
		t.Errorf("expected deploy command preserved, got %q", cfg4.DeployCommand)
	}

	// Invalid max_attempts (non-numeric, zero, negative) leaves field zero
	for _, raw := range []string{`max_attempts = "abc"`, `max_attempts = 0`, `max_attempts = -1`} {
		p := filepath.Join(dir, "bad_"+strings.ReplaceAll(raw, " ", "")+".toml")
		os.WriteFile(p, []byte("[gates]\n"+raw+"\n"), 0644)
		c := parseRefineryConfig(p)
		if c.MaxAttempts != 0 {
			t.Errorf("expected zero MaxAttempts for %q, got %d", raw, c.MaxAttempts)
		}
	}

	// parseTomlBool truth table
	cases := map[string]bool{
		"true": true, "TRUE": true, "True": true, "1": true, "yes": true,
		"false": false, "0": false, "no": false, "": false, "garbage": false,
	}
	for in, want := range cases {
		if got := parseTomlBool(in); got != want {
			t.Errorf("parseTomlBool(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseRefineryTomlDeploy(t *testing.T) {
	dir := t.TempDir()

	// [deploy] command is parsed
	path := filepath.Join(dir, "deploy.toml")
	os.WriteFile(path, []byte(`
[deploy]
command = "./deploy.sh"
`), 0644)

	cfg := parseRefineryConfig(path)
	if cfg.DeployCommand != "./deploy.sh" {
		t.Errorf("expected deploy command ./deploy.sh, got %q", cfg.DeployCommand)
	}

	// Both [gates] and [deploy] coexist without interference
	mixedPath := filepath.Join(dir, "mixed.toml")
	os.WriteFile(mixedPath, []byte(`
[gates]
commands = ["./build.sh", "./test.sh"]

[deploy]
command = "./deploy.sh"
`), 0644)

	mixed := parseRefineryConfig(mixedPath)
	if mixed.DeployCommand != "./deploy.sh" {
		t.Errorf("expected deploy command ./deploy.sh, got %q", mixed.DeployCommand)
	}
	if len(mixed.Gates) != 2 || mixed.Gates[0] != "./build.sh" || mixed.Gates[1] != "./test.sh" {
		t.Errorf("expected gates [./build.sh ./test.sh], got %v", mixed.Gates)
	}

	// Missing [deploy] section returns empty string, not an error
	gatesOnlyPath := filepath.Join(dir, "gates_only.toml")
	os.WriteFile(gatesOnlyPath, []byte(`
[gates]
commands = ["./build.sh"]
`), 0644)

	gatesOnly := parseRefineryConfig(gatesOnlyPath)
	if gatesOnly.DeployCommand != "" {
		t.Errorf("expected empty deploy command, got %q", gatesOnly.DeployCommand)
	}

	// Nonexistent file returns zero-value config
	missing := parseRefineryConfig("/nonexistent")
	if missing.DeployCommand != "" || missing.Gates != nil {
		t.Errorf("expected zero-value config, got %+v", missing)
	}
}

func TestRefineryDeployCommand(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Repo with no .pogo/refinery.toml: empty string, no error
	emptyRepo := t.TempDir()
	if got := r.DeployCommand(emptyRepo); got != "" {
		t.Errorf("expected empty deploy command for repo without config, got %q", got)
	}

	// Repo with a [deploy] section
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".pogo"), 0755)
	os.WriteFile(filepath.Join(repo, ".pogo", "refinery.toml"), []byte(`
[deploy]
command = "./scripts/deploy.sh production"
`), 0644)

	if got := r.DeployCommand(repo); got != "./scripts/deploy.sh production" {
		t.Errorf("expected deploy command from refinery.toml, got %q", got)
	}

	// Repo with refinery.toml but no [deploy] section
	gatesOnlyRepo := t.TempDir()
	os.MkdirAll(filepath.Join(gatesOnlyRepo, ".pogo"), 0755)
	os.WriteFile(filepath.Join(gatesOnlyRepo, ".pogo", "refinery.toml"), []byte(`
quality_gate = "./build.sh"
`), 0644)

	if got := r.DeployCommand(gatesOnlyRepo); got != "" {
		t.Errorf("expected empty deploy command when only [gates] configured, got %q", got)
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

// TestProcessMergeFFRetryOnRace exercises the gh-issue #13 race: between
// the refinery's fetch and its ff-only push, another commit lands on
// origin/main (simulating CI's version-bump after every merge). The
// refinery must retry with a fresh fetch+rebase and succeed within the
// default maxAttempts budget.
func TestProcessMergeFFRetryOnRace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Bare origin.
	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	// Primary working clone — sets up main and the feature branch.
	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	// Sidecar clone the gate pushes from (stand-in for the CI auto-bump
	// process). Lives outside the refinery's worktree so the refinery
	// cannot reset it.
	sidecarDir := t.TempDir()
	run(t, sidecarDir, "git", "clone", originDir, ".")
	run(t, sidecarDir, "git", "config", "user.email", "ci@test.com")
	run(t, sidecarDir, "git", "config", "user.name", "CI")

	// Race-state files outside any worktree — the refinery resets the
	// worktree on each attempt, so state must persist elsewhere.
	stateDir := t.TempDir()
	raceFlag := filepath.Join(stateDir, "race_done")
	gateRuns := filepath.Join(stateDir, "gate_runs")

	// build.sh:
	//   - bumps gate-run counter on every invocation (so we can assert
	//     the gate ran on each attempt — i.e. skip_on_retry is OFF here)
	//   - on the first invocation only, pushes an empty commit to
	//     origin/main from the sidecar clone (the race injection)
	buildSh := fmt.Sprintf(`#!/bin/sh
set -e
RUNS=$(cat %s 2>/dev/null || echo 0)
RUNS=$((RUNS+1))
echo $RUNS > %s
if [ ! -f %s ]; then
    touch %s
    (cd %s && git fetch origin main >/dev/null 2>&1 && git reset --hard origin/main >/dev/null 2>&1 && git commit --allow-empty -m "ci: version bump" >/dev/null && git push origin main >/dev/null 2>&1)
fi
exit 0
`, gateRuns, gateRuns, raceFlag, raceFlag, sidecarDir)

	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte(buildSh), 0755)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial with race-injecting build")
	run(t, workDir, "git", "push", "origin", "main")

	// Feature branch carries the same build.sh.
	run(t, workDir, "git", "checkout", "-b", "feature-race")
	os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("feature"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add feature")
	run(t, workDir, "git", "push", "origin", "feature-race")

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
		Branch:    "feature-race",
		TargetRef: "main",
		Author:    "test-cat",
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
		t.Fatalf("expected merged after race retry, got %s\nerror: %s\ngate_output: %s",
			mr.Status, mr.Error, mr.GateOutput)
	}

	// Sanity: the race did happen — sidecar pushed something. The merged
	// HEAD on origin must include both the version bump and the feature.
	verifyDir := t.TempDir()
	run(t, verifyDir, "git", "clone", originDir, ".")
	if _, err := os.Stat(filepath.Join(verifyDir, "feature.txt")); os.IsNotExist(err) {
		t.Error("feature.txt missing on main after race retry")
	}

	// Gate ran on every attempt (skip_on_retry NOT set in this test).
	runsData, _ := os.ReadFile(gateRuns)
	runs, _ := strconv.Atoi(strings.TrimSpace(string(runsData)))
	if runs < 2 {
		t.Errorf("expected gate to run at least twice (race forces retry), got %d", runs)
	}
}

// TestProcessMergeSkipGatesOnRetry verifies the [gates] skip_on_retry
// knob: when set, the quality-gate phase is bypassed on attempts after
// the first. Pairs with the higher maxAttempts default to make retries
// cheap on fast-gate repos that race CI auto-bumps.
func TestProcessMergeSkipGatesOnRetry(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	sidecarDir := t.TempDir()
	run(t, sidecarDir, "git", "clone", originDir, ".")
	run(t, sidecarDir, "git", "config", "user.email", "ci@test.com")
	run(t, sidecarDir, "git", "config", "user.name", "CI")

	stateDir := t.TempDir()
	raceFlag := filepath.Join(stateDir, "race_done")
	gateRuns := filepath.Join(stateDir, "gate_runs")

	buildSh := fmt.Sprintf(`#!/bin/sh
set -e
RUNS=$(cat %s 2>/dev/null || echo 0)
RUNS=$((RUNS+1))
echo $RUNS > %s
if [ ! -f %s ]; then
    touch %s
    (cd %s && git fetch origin main >/dev/null 2>&1 && git reset --hard origin/main >/dev/null 2>&1 && git commit --allow-empty -m "ci: version bump" >/dev/null && git push origin main >/dev/null 2>&1)
fi
exit 0
`, gateRuns, gateRuns, raceFlag, raceFlag, sidecarDir)

	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte(buildSh), 0755)
	os.MkdirAll(filepath.Join(workDir, ".pogo"), 0755)
	os.WriteFile(filepath.Join(workDir, ".pogo", "refinery.toml"), []byte(`
[gates]
commands = ["./build.sh"]
skip_on_retry = true
`), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial with skip_on_retry config")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "feature-skip")
	os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("feature"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add feature")
	run(t, workDir, "git", "push", "origin", "feature-skip")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	id, _ := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-skip",
		TargetRef: "main",
		Author:    "test-cat",
	})

	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}

	// With skip_on_retry, the gate must run exactly once (attempt 1) even
	// though the race forces a retry. This is the cost-saving the knob
	// exists to deliver.
	runsData, _ := os.ReadFile(gateRuns)
	runs, _ := strconv.Atoi(strings.TrimSpace(string(runsData)))
	if runs != 1 {
		t.Errorf("expected gate to run exactly once with skip_on_retry=true, got %d", runs)
	}

	// The retry-attempt's gate output should explicitly mark itself as
	// skipped — useful for diagnostics.
	if !strings.Contains(mr.GateOutput, "skipped") {
		t.Errorf("expected GateOutput to mention gates were skipped, got: %s", mr.GateOutput)
	}
}

// TestProcessMergeMaxAttemptsConfigurable verifies that [gates]
// max_attempts overrides the built-in default. We use max_attempts=2
// with a perpetually-racing gate; the merge must fail after exactly
// two attempts (gate runs twice, then the loop gives up).
func TestProcessMergeMaxAttemptsConfigurable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir := t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	sidecarDir := t.TempDir()
	run(t, sidecarDir, "git", "clone", originDir, ".")
	run(t, sidecarDir, "git", "config", "user.email", "ci@test.com")
	run(t, sidecarDir, "git", "config", "user.name", "CI")

	stateDir := t.TempDir()
	gateRuns := filepath.Join(stateDir, "gate_runs")

	// build.sh increments the run counter and pushes a bump to
	// origin/main on every invocation — guarantees the ff-only merge
	// always fails. Counter tells us exactly how many attempts ran.
	buildSh := fmt.Sprintf(`#!/bin/sh
set -e
RUNS=$(cat %s 2>/dev/null || echo 0)
RUNS=$((RUNS+1))
echo $RUNS > %s
(cd %s && git fetch origin main >/dev/null 2>&1 && git reset --hard origin/main >/dev/null 2>&1 && git commit --allow-empty -m "perpetual bump" >/dev/null && git push origin main >/dev/null 2>&1)
exit 0
`, gateRuns, gateRuns, sidecarDir)

	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte(buildSh), 0755)
	os.MkdirAll(filepath.Join(workDir, ".pogo"), 0755)
	os.WriteFile(filepath.Join(workDir, ".pogo", "refinery.toml"), []byte(`
[gates]
commands = ["./build.sh"]
max_attempts = 2
`), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial with max_attempts=2")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "feature-perpetual")
	os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("f"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat")
	run(t, workDir, "git", "push", "origin", "feature-perpetual")

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	id, _ := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-perpetual",
		TargetRef: "main",
		Author:    "test-cat",
	})

	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusFailed {
		t.Fatalf("expected failed (perpetual race exhausts attempts), got %s", mr.Status)
	}

	// Must have exhausted exactly the configured 2 attempts — not the
	// default 7. The gate ran once per attempt (skip_on_retry not set).
	runsData, _ := os.ReadFile(gateRuns)
	runs, _ := strconv.Atoi(strings.TrimSpace(string(runsData)))
	if runs != 2 {
		t.Errorf("expected gate to run exactly max_attempts=2 times, got %d", runs)
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

func TestSubmitSignalsWake(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Submit(MergeRequest{
		RepoPath: originDir,
		Branch:   "feature-1",
		Author:   "cat-abc",
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-r.wakeCh:
		// OK — Submit left a wake pending for the loop.
	default:
		t.Fatal("expected a pending wake signal after Submit")
	}

	// Concurrent signals collapse: two more wakes leave exactly one pending.
	r.wake()
	r.wake()
	select {
	case <-r.wakeCh:
	default:
		t.Fatal("expected a pending wake signal")
	}
	select {
	case <-r.wakeCh:
		t.Fatal("wake signals should collapse into one")
	default:
	}
}

func TestWakeIfActionableSkipsHeld(t *testing.T) {
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Empty queue: no wake.
	r.wakeIfActionable()
	select {
	case <-r.wakeCh:
		t.Fatal("empty queue must not signal a wake")
	default:
	}

	// Held-only queue: no wake — waking would busy-loop the QA gate check.
	r.mu.Lock()
	r.queue = append(r.queue, &MergeRequest{ID: "held-1", Status: StatusHeld})
	r.mu.Unlock()
	r.wakeIfActionable()
	select {
	case <-r.wakeCh:
		t.Fatal("held-only queue must not signal a wake")
	default:
	}

	// A queued item behind the held one: wake.
	r.mu.Lock()
	r.queue = append(r.queue, &MergeRequest{ID: "queued-1", Status: StatusQueued})
	r.mu.Unlock()
	r.wakeIfActionable()
	select {
	case <-r.wakeCh:
	default:
		t.Fatal("expected a wake for the queued item")
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

func TestIsAuthFailure(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "could-not-read-username-prompt-disabled",
			out:  "fatal: could not read Username for 'https://github.com': terminal prompts disabled",
			want: true,
		},
		{
			name: "could-not-read-password",
			out:  "fatal: could not read Password for 'https://user@github.com': terminal prompts disabled",
			want: true,
		},
		{
			name: "authentication-failed",
			out:  "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/foo/bar.git/'",
			want: true,
		},
		{
			name: "github-deprecated-password-auth",
			out:  "remote: Support for password authentication was removed on August 13, 2021.",
			want: true,
		},
		{
			name: "case-insensitive",
			out:  "FATAL: Could Not Read Username for 'https://example.com'",
			want: true,
		},
		{
			name: "not-an-auth-error",
			out:  "fatal: refusing to update branch: non-fast-forward",
			want: false,
		},
		{
			name: "empty",
			out:  "",
			want: false,
		},
		{
			name: "dns-failure-not-auth",
			out:  "fatal: unable to access 'https://example.invalid/foo.git/': Could not resolve host: example.invalid",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAuthFailure(c.out); got != c.want {
				t.Errorf("isAuthFailure(%q) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}

func TestFormatPushAuthError(t *testing.T) {
	gitOut := "fatal: could not read Username for 'https://github.com': terminal prompts disabled"
	err := formatPushAuthError(gitOut)
	if err == nil {
		t.Fatal("formatPushAuthError returned nil")
	}
	msg := err.Error()

	// First three lines must name the failure mode and at least one concrete
	// next step (acceptance criterion).
	firstLines := strings.SplitN(msg, "\n", 4)
	if len(firstLines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d:\n%s", len(firstLines), msg)
	}
	if !strings.Contains(firstLines[0], "could not authenticate") {
		t.Errorf("line 1 should name failure mode, got: %q", firstLines[0])
	}

	// Required actionable phrases.
	for _, phrase := range []string{
		"Switch the remote to SSH",
		"credential helper",
		"GIT_ASKPASS",
		"git@github.com",
		"gh auth setup-git",
	} {
		if !strings.Contains(msg, phrase) {
			t.Errorf("error missing actionable phrase %q\nfull error:\n%s", phrase, msg)
		}
	}

	// Original git stderr must be preserved further down for debug.
	if !strings.Contains(msg, gitOut) {
		t.Errorf("error should preserve raw git output, got:\n%s", msg)
	}
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

// TestSubmitAutoCreateTargetRef covers the opt-in behaviour added for gh-issue
// #14. Default behaviour (auto-create off) must keep erroring out when the
// target ref doesn't exist; auto-create on must branch the missing ref off
// the repo's default branch and then accept the MR.
func TestSubmitAutoCreateTargetRef(t *testing.T) {
	t.Run("default off — missing target still errors", func(t *testing.T) {
		originDir := initBareOrigin(t, "main")
		r, err := New(Config{
			Enabled:      true,
			PollInterval: time.Hour,
			WorktreeDir:  t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Submit(MergeRequest{
			RepoPath:  originDir,
			Branch:    "feature-1",
			TargetRef: "fam-45",
			Author:    "test-cat",
		})
		if err == nil {
			t.Fatal("expected error for missing target_ref with auto-create off")
		}
		if !strings.Contains(err.Error(), "fam-45") {
			t.Errorf("expected error to mention target ref name, got: %v", err)
		}
	})

	t.Run("opt-in on bare repo — creates target from HEAD", func(t *testing.T) {
		originDir := initBareOrigin(t, "main")
		r, err := New(Config{
			Enabled:      true,
			PollInterval: time.Hour,
			WorktreeDir:  t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		id, err := r.Submit(MergeRequest{
			RepoPath:            originDir,
			Branch:              "feature-1",
			TargetRef:           "fam-45",
			Author:              "test-cat",
			AutoCreateTargetRef: true,
		})
		if err != nil {
			t.Fatalf("expected auto-create to succeed, got: %v", err)
		}
		if id == "" {
			t.Fatal("expected non-empty MR id")
		}
		// Sanity: the new ref should now resolve in the bare repo and point
		// at the same commit as main (the source branch).
		mainSha, err := exec.Command("git", "-C", originDir, "rev-parse", "refs/heads/main").CombinedOutput()
		if err != nil {
			t.Fatalf("rev-parse main: %v: %s", err, mainSha)
		}
		newSha, err := exec.Command("git", "-C", originDir, "rev-parse", "refs/heads/fam-45").CombinedOutput()
		if err != nil {
			t.Fatalf("rev-parse fam-45: %v: %s", err, newSha)
		}
		if strings.TrimSpace(string(mainSha)) != strings.TrimSpace(string(newSha)) {
			t.Errorf("auto-created ref points at %s, expected %s", strings.TrimSpace(string(newSha)), strings.TrimSpace(string(mainSha)))
		}
	})

	t.Run("opt-in on working clone — pushes target to origin", func(t *testing.T) {
		originDir := initBareOrigin(t, "main")
		workDir := t.TempDir()
		run(t, workDir, "git", "clone", originDir, ".")
		run(t, workDir, "git", "config", "user.email", "test@test.com")
		run(t, workDir, "git", "config", "user.name", "Test")

		r, err := New(Config{
			Enabled:      true,
			PollInterval: time.Hour,
			WorktreeDir:  t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Submit(MergeRequest{
			RepoPath:            workDir,
			Branch:              "feature-1",
			TargetRef:           "fam-45",
			Author:              "test-cat",
			AutoCreateTargetRef: true,
		})
		if err != nil {
			t.Fatalf("expected auto-create from working clone to succeed, got: %v", err)
		}
		// The new ref should now appear on origin via ls-remote.
		out, err := exec.Command("git", "-C", workDir, "ls-remote", "--heads", "origin", "fam-45").CombinedOutput()
		if err != nil {
			t.Fatalf("ls-remote: %v: %s", err, out)
		}
		if strings.TrimSpace(string(out)) == "" {
			t.Error("expected fam-45 to exist on origin after auto-create, ls-remote returned empty")
		}
	})

	t.Run("opt-in but default branch undetectable — still errors", func(t *testing.T) {
		// detectDefaultBranch falls back to "main" via HEAD in a bare repo
		// initialised with -b main, so to exercise the failure path we point
		// Submit at a path that isn't a git repo at all.
		notARepo := t.TempDir()
		r, err := New(Config{
			Enabled:      true,
			PollInterval: time.Hour,
			WorktreeDir:  t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.Submit(MergeRequest{
			RepoPath:            notARepo,
			Branch:              "feature-1",
			TargetRef:           "fam-45",
			Author:              "test-cat",
			AutoCreateTargetRef: true,
		})
		if err == nil {
			t.Fatal("expected error when default branch can't be detected")
		}
	})
}

// TestSubmitDeferDonePreserved covers gh drellem2/pogo #81: the DeferDone flag
// set on a submitted MR must survive onto the queued MergeRequest (and its
// persisted state) so pogod's OnMerged reap path can honour it at merge time.
func TestSubmitDeferDonePreserved(t *testing.T) {
	originDir := initBareOrigin(t, "main")
	statePath := filepath.Join(t.TempDir(), "refinery.json")
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		StatePath:    statePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-1",
		TargetRef: "main",
		Author:    "mg-1234",
		DeferDone: true,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := r.Get(id); got == nil || !got.DeferDone {
		t.Fatalf("expected queued MR to carry DeferDone=true, got %+v", got)
	}

	// Default (non-defer) submit must stay DeferDone=false.
	id2, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-2",
		TargetRef: "main",
		Author:    "mg-5678",
	})
	if err != nil {
		t.Fatalf("submit default: %v", err)
	}
	if got := r.Get(id2); got == nil || got.DeferDone {
		t.Fatalf("expected default MR to have DeferDone=false, got %+v", got)
	}

	// DeferDone must round-trip through the persisted state file: a fresh
	// Refinery loaded from the same StatePath sees the flag on recovery.
	r2, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		StatePath:    statePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Get(id); got == nil || !got.DeferDone {
		t.Fatalf("expected DeferDone=true to survive state reload, got %+v", got)
	}
}

func TestParseRefineryTomlPRMode(t *testing.T) {
	dir := t.TempDir()

	// Under [gates] (the design doc's spelling)
	path := filepath.Join(dir, "gates.toml")
	os.WriteFile(path, []byte(`
[gates]
commands = ["./build.sh"]
pr_mode = true
`), 0644)
	if !parseRefineryConfig(path).PRMode {
		t.Error("expected PRMode=true under [gates]")
	}

	// Top-level (the ticket's spelling)
	path2 := filepath.Join(dir, "toplevel.toml")
	os.WriteFile(path2, []byte(`
pr_mode = true
`), 0644)
	if !parseRefineryConfig(path2).PRMode {
		t.Error("expected PRMode=true at top level")
	}

	// Absent → false
	path3 := filepath.Join(dir, "absent.toml")
	os.WriteFile(path3, []byte(`
[gates]
commands = ["./build.sh"]
`), 0644)
	if parseRefineryConfig(path3).PRMode {
		t.Error("expected PRMode=false when key absent")
	}

	// Explicit false → false
	path4 := filepath.Join(dir, "false.toml")
	os.WriteFile(path4, []byte(`
[gates]
pr_mode = false
`), 0644)
	if parseRefineryConfig(path4).PRMode {
		t.Error("expected PRMode=false when explicitly false")
	}
}

// fakeGH installs a stub `gh` executable ahead of PATH with the given shell
// script body, so PR-mode tests control the PR lookup without a network or
// a real GitHub repo.
func fakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeGHLog installs a stub `gh` that appends each invocation's arguments to
// a log file (one space-joined line per call, e.g. `pr close 7 --comment ...`)
// before running script, so tests can assert on which gh subcommands the
// refinery actually issued. The log path is exported to script as $GH_LOG,
// letting a stub vary its answer by how many times it has been called — the
// PR state genuinely changes across a merge, so a static stub can't model it.
func fakeGHLog(t *testing.T, script string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "gh.log")
	path := filepath.Join(dir, "gh")
	body := fmt.Sprintf("#!/bin/sh\nGH_LOG=%q\necho \"$@\" >> \"$GH_LOG\"\n%s\n", logPath, script)
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// ghCalls returns the gh invocations recorded by fakeGHLog, in order.
func ghCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// ghCalled reports whether any recorded gh invocation starts with prefix.
func ghCalled(t *testing.T, logPath, prefix string) bool {
	t.Helper()
	for _, c := range ghCalls(t, logPath) {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// setupPRModeRepo builds a bare origin with pr_mode enabled. See setupPRRepo.
func setupPRModeRepo(t *testing.T) (originDir, branchSHA string) {
	t.Helper()
	return setupPRRepo(t, true)
}

// setupPRRepo builds a bare origin on main, a feature branch, and a later
// commit on main so the refinery's rebase actually rewrites the branch SHAs
// (the case PR-mode and the post-merge PR close both exist for). prMode sets
// [gates] pr_mode in the repo's refinery.toml. Returns the origin path and
// the feature branch's pre-merge tip SHA.
func setupPRRepo(t *testing.T, prMode bool) (originDir, branchSHA string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	originDir = t.TempDir()
	run(t, originDir, "git", "init", "--bare", "-b", "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")
	os.MkdirAll(filepath.Join(workDir, ".pogo"), 0755)
	os.WriteFile(filepath.Join(workDir, ".pogo", "refinery.toml"), []byte(fmt.Sprintf(`
[gates]
pr_mode = %t
`, prMode)), 0644)
	os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "initial commit")
	run(t, workDir, "git", "push", "origin", "main")

	// Feature branch forked from the initial commit.
	run(t, workDir, "git", "checkout", "-b", "feature-pr")
	os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("new feature"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add feature")
	run(t, workDir, "git", "push", "origin", "feature-pr")

	// Advance main so the rebase rewrites the branch's SHAs.
	run(t, workDir, "git", "checkout", "main")
	os.WriteFile(filepath.Join(workDir, "other.txt"), []byte("moved on"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "main moved on")
	run(t, workDir, "git", "push", "origin", "main")

	branchSHA = strings.TrimSpace(runOut(t, originDir, "git", "rev-parse", "feature-pr"))
	return originDir, branchSHA
}

// TestProcessMergePRModePushBack exercises the phase-2 PR-mode happy path
// (mg-b828): with pr_mode enabled and gh reporting an open PR, the refinery
// force-pushes the rebased branch back to origin, so after the ff-merge the
// branch tip on origin equals the target tip — the condition under which
// GitHub marks the PR merged.
// The post-merge close+reap (mg-f18c) deletes origin's branch, so the
// push-back's effect on origin can only be observed before that runs. The
// stub's post-merge `pr view` — issued after the ff-merge push and before the
// reap — snapshots origin's branch tip at exactly that moment.
func TestProcessMergePRModePushBack(t *testing.T) {
	originDir, oldSHA := setupPRModeRepo(t)
	snapshot := filepath.Join(t.TempDir(), "tip")
	fakeGHLog(t, fmt.Sprintf(`
views=$(grep -c '^pr view' "$GH_LOG")
if [ "$views" -le 1 ]; then
  # Pre-merge lookup: the PR is open, so the push-back should run.
  echo '{"state":"OPEN","number":7}'
else
  # Post-merge lookup: the realigned head landed, so GitHub has already
  # marked the PR merged. Snapshot origin's tip before the reap removes it.
  git --git-dir=%q rev-parse feature-pr > %q
  echo '{"state":"MERGED","number":7}'
fi`, originDir, snapshot))

	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-pr",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}

	mainSHA := strings.TrimSpace(runOut(t, originDir, "git", "rev-parse", "main"))
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatalf("no post-merge snapshot of origin's branch tip: %v", err)
	}
	branchSHA := strings.TrimSpace(string(data))
	if branchSHA == oldSHA {
		t.Error("expected origin branch to be force-pushed to the rebased SHAs, but tip is unchanged")
	}
	if branchSHA != mainSHA {
		t.Errorf("expected origin branch tip %s to equal merged main tip %s (PR would not read merged)", branchSHA, mainSHA)
	}
}

// TestProcessMergeClosesRebasedPRAndReapsBranch is direction (a) of mg-f18c:
// the refinery rebased the branch, so the landed SHA differs from the PR head
// and GitHub cannot auto-detect the merge. The PR must end CLOSED (with a
// comment naming the SHA it landed as) and the remote branch must be gone.
func TestProcessMergeClosesRebasedPRAndReapsBranch(t *testing.T) {
	originDir, oldSHA := setupPRRepo(t, false)
	ghLog := fakeGHLog(t, `
case "$1 $2" in
  "pr view")  echo '{"state":"OPEN","number":81}' ;;
  "pr close") echo "Closed pull request #81" ;;
esac`)

	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-pr",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}
	mainSHA := strings.TrimSpace(runOut(t, originDir, "git", "rev-parse", "main"))
	if mainSHA == oldSHA {
		t.Fatalf("main tip %s equals the pre-merge branch tip — no rebase happened, so this is not the dangling-PR case", mainSHA)
	}

	// (a1) the PR is closed out, with the landed SHA in the comment.
	var closeCall string
	for _, c := range ghCalls(t, ghLog) {
		if strings.HasPrefix(c, "pr close") {
			closeCall = c
		}
	}
	if closeCall == "" {
		t.Fatalf("expected `gh pr close` for the rebased branch's open PR; gh calls: %q", ghCalls(t, ghLog))
	}
	if !strings.Contains(closeCall, "pr close 81 --comment") {
		t.Errorf("expected close of PR #81 with a comment, got: %q", closeCall)
	}
	if !strings.Contains(closeCall, mainSHA) {
		t.Errorf("expected the close comment to name the merged SHA %s, got: %q", mainSHA, closeCall)
	}

	// (a2) the remote branch is reaped.
	if _, err := exec.Command("git", "--git-dir="+originDir, "rev-parse", "--verify", "feature-pr").Output(); err == nil {
		t.Error("expected origin's feature-pr branch to be reaped after the merge, but it still exists")
	}
}

// TestProcessMergeAutoClosedPRNotClosedAgain is direction (b) of mg-f18c: on
// the single/first-MR path GitHub auto-detects the merge and closes the PR
// itself. The post-merge step must not try to close an already-closed PR (and
// must still merge cleanly), but must still reap the branch.
func TestProcessMergeAutoClosedPRNotClosedAgain(t *testing.T) {
	originDir, _ := setupPRRepo(t, false)
	ghLog := fakeGHLog(t, `
case "$1 $2" in
  "pr view")  echo '{"state":"MERGED","number":85}' ;;
  "pr close") echo "gh: PR #85 is already closed" >&2; exit 1 ;;
esac`)

	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-pr",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	if mr := r.Get(id); mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}
	if ghCalled(t, ghLog, "pr close") {
		t.Errorf("expected no close of an already-merged PR; gh calls: %q", ghCalls(t, ghLog))
	}
	if _, err := exec.Command("git", "--git-dir="+originDir, "rev-parse", "--verify", "feature-pr").Output(); err == nil {
		t.Error("expected origin's feature-pr branch to be reaped even when GitHub auto-closed the PR")
	}
}

// TestProcessMergeNoPRLeavesBranch: branches with no GitHub PR (internal
// mg-track branches) have no PR loop to close, so the post-merge step must
// leave them — and their remote branch — alone. Reaping is PR hygiene, not a
// general branch-cleanup policy; gitgc owns the rest.
func TestProcessMergeNoPRLeavesBranch(t *testing.T) {
	originDir, _ := setupPRRepo(t, false)
	ghLog := fakeGHLog(t, `echo 'no pull requests found for branch "feature-pr"' >&2; exit 1`)

	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-pr",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	if mr := r.Get(id); mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %s (error: %s)", mr.Status, mr.Error)
	}
	if ghCalled(t, ghLog, "pr close") {
		t.Errorf("expected no close when the branch has no PR; gh calls: %q", ghCalls(t, ghLog))
	}
	if _, err := exec.Command("git", "--git-dir="+originDir, "rev-parse", "--verify", "feature-pr").Output(); err != nil {
		t.Error("expected origin's feature-pr branch to survive when it has no PR")
	}
}

// TestClosePRAndReapFailSoft: every failure mode of the post-merge step is
// non-fatal by construction (it returns nothing), but the merge that precedes
// it must survive a gh that is broken outright.
func TestProcessMergeClosePRFailSoft(t *testing.T) {
	originDir, _ := setupPRRepo(t, false)
	fakeGHLog(t, `echo "gh: network unreachable" >&2; exit 1`)

	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-pr",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	if mr := r.Get(id); mr.Status != StatusMerged {
		t.Fatalf("expected merged despite a broken gh, got %s (error: %s)", mr.Status, mr.Error)
	}
}

// TestLookupPR covers the gh output states lookupPR distinguishes; the state
// string drives whether closePRAndReap closes the PR or leaves it alone.
func TestLookupPR(t *testing.T) {
	dir := t.TempDir()

	fakeGH(t, `echo '{"state":"OPEN","number":42}'`)
	if n, state, err := lookupPR(dir, "b"); err != nil || n != 42 || state != "OPEN" {
		t.Errorf("open PR: expected (42, OPEN, nil), got (%d, %s, %v)", n, state, err)
	}

	fakeGH(t, `echo '{"state":"MERGED","number":42}'`)
	if n, state, err := lookupPR(dir, "b"); err != nil || n != 42 || state != "MERGED" {
		t.Errorf("merged PR: expected (42, MERGED, nil), got (%d, %s, %v)", n, state, err)
	}

	fakeGH(t, `echo 'no pull requests found for branch "b"' >&2; exit 1`)
	if n, state, err := lookupPR(dir, "b"); err != nil || n != 0 || state != "" {
		t.Errorf("no PR: expected (0, \"\", nil), got (%d, %s, %v)", n, state, err)
	}

	fakeGH(t, `echo "gh: could not determine base repo" >&2; exit 1`)
	if _, _, err := lookupPR(dir, "b"); err == nil {
		t.Error("hard failure: expected an error, got nil")
	}
}

// TestPRClosedComment: the comment must name the SHA the content landed as —
// that pointer is the whole reason to close explicitly rather than silently.
func TestPRClosedComment(t *testing.T) {
	mr := &MergeRequest{ID: "mr-1", Branch: "polecat-mg-f18c", TargetRef: "main"}

	got := prClosedComment(mr, "deadbeef\n")
	if !strings.Contains(got, "deadbeef") {
		t.Errorf("expected the merged SHA in the comment, got: %q", got)
	}
	if strings.Contains(got, "\n") && !strings.Contains(got, "mr-1") {
		t.Errorf("expected the MR ID in the comment, got: %q", got)
	}

	// A rev-parse failure leaves the SHA empty (the merge still landed) —
	// the comment must stay coherent rather than reading "Merged as  on".
	if got := prClosedComment(mr, ""); !strings.Contains(got, "the current main tip") {
		t.Errorf("expected a fallback pointer when the SHA is unknown, got: %q", got)
	}
}

// TestProcessMergePRModeFailSoft: when the gh PR lookup fails, PR mode must
// not block the merge (the push-back is cosmetic-only) and must not rewrite
// the branch on origin.
func TestProcessMergePRModeFailSoft(t *testing.T) {
	originDir, oldSHA := setupPRModeRepo(t)
	fakeGH(t, `echo "gh: network unreachable" >&2; exit 1`)

	r, err := New(Config{Enabled: true, PollInterval: time.Hour, WorktreeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    "feature-pr",
		TargetRef: "main",
		Author:    "test-cat",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr.Status != StatusMerged {
		t.Fatalf("expected merged despite gh failure, got %s (error: %s)", mr.Status, mr.Error)
	}
	if got := strings.TrimSpace(runOut(t, originDir, "git", "rev-parse", "feature-pr")); got != oldSHA {
		t.Errorf("expected origin branch untouched on gh failure, got %s (was %s)", got, oldSHA)
	}
}

// TestOpenPRNumber covers the gh output states openPRNumber distinguishes:
// open PR, non-open PR, no PR at all, and hard lookup failure.
func TestOpenPRNumber(t *testing.T) {
	dir := t.TempDir()

	fakeGH(t, `echo '{"state":"OPEN","number":42}'`)
	if n, err := openPRNumber(dir, "b"); err != nil || n != 42 {
		t.Errorf("open PR: expected (42, nil), got (%d, %v)", n, err)
	}

	fakeGH(t, `echo '{"state":"MERGED","number":42}'`)
	if n, err := openPRNumber(dir, "b"); err != nil || n != 0 {
		t.Errorf("merged PR: expected (0, nil), got (%d, %v)", n, err)
	}

	fakeGH(t, `echo 'no pull requests found for branch "b"' >&2; exit 1`)
	if n, err := openPRNumber(dir, "b"); err != nil || n != 0 {
		t.Errorf("no PR: expected (0, nil), got (%d, %v)", n, err)
	}

	fakeGH(t, `echo "gh: could not determine base repo" >&2; exit 1`)
	if _, err := openPRNumber(dir, "b"); err == nil {
		t.Error("hard failure: expected an error, got nil")
	}

	fakeGH(t, `echo 'not json'`)
	if _, err := openPRNumber(dir, "b"); err == nil {
		t.Error("bad JSON: expected an error, got nil")
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

// runOut is like run but returns the command's combined output. Useful for
// reading values back out of a repo (e.g. `git log --format=...`).
func runOut(t *testing.T, dir string, name string, args ...string) string {
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
	return string(out)
}
