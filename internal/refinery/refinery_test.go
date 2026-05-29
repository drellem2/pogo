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
