package refinery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupRepoWithDeploy creates a bare origin repo plus a polecat-style branch
// pushed to it. The build script and the deploy command (if any) are committed
// onto main so the refinery's clone picks them up. Returns the origin repo
// path and the branch name that was pushed.
func setupRepoWithDeploy(t *testing.T, deployTOML string) (originDir, branch string) {
	t.Helper()
	originDir = initBareOrigin(t, "main")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", originDir, ".")
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	// Passing build gate so the merge itself succeeds.
	os.WriteFile(filepath.Join(workDir, "build.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)

	if deployTOML != "" {
		os.MkdirAll(filepath.Join(workDir, ".pogo"), 0755)
		os.WriteFile(filepath.Join(workDir, ".pogo", "refinery.toml"), []byte(deployTOML), 0644)
	}

	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "init build + config")
	run(t, workDir, "git", "push", "origin", "main")

	branch = "polecat-mg-deploy"
	run(t, workDir, "git", "checkout", "-b", branch)
	os.WriteFile(filepath.Join(workDir, "feat.txt"), []byte("feat"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat")
	run(t, workDir, "git", "push", "origin", branch)
	return originDir, branch
}

// TestRunDeployHookOnSuccessfulMerge verifies that after a clean merge, the
// configured deploy command runs in the refinery's worktree (its side effect
// is observable in the worktree, not the source repo) and DeployError is empty.
func TestRunDeployHookOnSuccessfulMerge(t *testing.T) {
	logPath := useTempEventLog(t)

	// Marker file is created by the deploy script *inside whatever directory
	// the script runs in*. The refinery's clone lives under wtDir/<repo basename>,
	// so checking that subdir confirms the command ran in the refinery's
	// worktree (not the source repo).
	deployTOML := `
[deploy]
command = "touch deploy_marker"
`
	originDir, branch := setupRepoWithDeploy(t, deployTOML)

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
		Branch:    branch,
		TargetRef: "main",
		Author:    "mg-deploy",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr == nil || mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %+v", mr)
	}
	if mr.DeployError != "" {
		t.Errorf("expected empty DeployError, got %q", mr.DeployError)
	}

	// The marker file should exist in the refinery's clone (wtDir/<repo basename>),
	// confirming the deploy ran in the refinery's worktree.
	repoName := filepath.Base(originDir)
	markerPath := filepath.Join(wtDir, repoName, "deploy_marker")
	if _, err := os.Stat(markerPath); err != nil {
		t.Errorf("expected deploy marker at %s, got: %v", markerPath, err)
	}

	// Source repo (bare origin) must NOT have the marker — confirms the
	// deploy did not run against mr.RepoPath.
	if _, err := os.Stat(filepath.Join(originDir, "deploy_marker")); err == nil {
		t.Error("deploy marker leaked into source repo — should only exist in refinery worktree")
	}

	// Events: deploy_attempted + deployed, no deploy_failed.
	all := readEvents(t, logPath)
	attempted := filterEvents(all, "refinery_deploy_attempted")
	deployed := filterEvents(all, "refinery_deployed")
	failed := filterEvents(all, "refinery_deploy_failed")

	if len(attempted) != 1 {
		t.Errorf("expected 1 refinery_deploy_attempted, got %d", len(attempted))
	}
	if len(deployed) != 1 {
		t.Errorf("expected 1 refinery_deployed, got %d", len(deployed))
	}
	if len(failed) != 0 {
		t.Errorf("expected 0 refinery_deploy_failed, got %d", len(failed))
	}

	if len(attempted) == 1 {
		a := attempted[0]
		if a.Agent != "refinery" {
			t.Errorf("attempted.agent = %q, want refinery", a.Agent)
		}
		if a.WorkItemID != "mg-deploy" {
			t.Errorf("attempted.work_item_id = %q, want mg-deploy", a.WorkItemID)
		}
		if cmd, _ := a.Details["command"].(string); cmd != "touch deploy_marker" {
			t.Errorf("attempted.details.command = %q, want %q", cmd, "touch deploy_marker")
		}
		if got := a.Details["merge_request_id"]; got != id {
			t.Errorf("attempted.details.merge_request_id = %v, want %s", got, id)
		}
	}

	if len(deployed) == 1 {
		d := deployed[0]
		if _, ok := d.Details["duration_seconds"].(float64); !ok {
			t.Errorf("deployed.details.duration_seconds missing or wrong type: %v", d.Details["duration_seconds"])
		}
	}
}

// TestNoDeployConfiguredIsClean verifies a merge with no [deploy] section
// emits no deploy events and leaves DeployError empty.
func TestNoDeployConfiguredIsClean(t *testing.T) {
	logPath := useTempEventLog(t)

	originDir, branch := setupRepoWithDeploy(t, "") // no .pogo/refinery.toml

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
		Branch:    branch,
		TargetRef: "main",
		Author:    "mg-nodeploy",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.processNext()

	mr := r.Get(id)
	if mr == nil || mr.Status != StatusMerged {
		t.Fatalf("expected merged, got %+v", mr)
	}
	if mr.DeployError != "" {
		t.Errorf("expected empty DeployError when no deploy configured, got %q", mr.DeployError)
	}

	all := readEvents(t, logPath)
	deploy := filterEvents(all,
		"refinery_deploy_attempted",
		"refinery_deployed",
		"refinery_deploy_failed",
	)
	if len(deploy) != 0 {
		t.Errorf("expected 0 deploy events when no deploy configured, got %d", len(deploy))
	}
}

// TestDeployFailureKeepsMergeButRecordsError verifies that a deploy command
// exiting non-zero leaves the merge intact (Status=merged) but populates
// DeployError and emits refinery_deploy_failed (and no refinery_deployed).
func TestDeployFailureKeepsMergeButRecordsError(t *testing.T) {
	logPath := useTempEventLog(t)

	deployTOML := `
[deploy]
command = "false"
`
	originDir, branch := setupRepoWithDeploy(t, deployTOML)

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
		Branch:    branch,
		TargetRef: "main",
		Author:    "mg-baddeploy",
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
		t.Errorf("expected merged (deploy failure must not unwind), got %s (error=%q)", mr.Status, mr.Error)
	}
	if mr.DeployError == "" {
		t.Error("expected non-empty DeployError after failing deploy command")
	}

	all := readEvents(t, logPath)
	attempted := filterEvents(all, "refinery_deploy_attempted")
	deployed := filterEvents(all, "refinery_deployed")
	failed := filterEvents(all, "refinery_deploy_failed")

	if len(attempted) != 1 {
		t.Errorf("expected 1 refinery_deploy_attempted, got %d", len(attempted))
	}
	if len(deployed) != 0 {
		t.Errorf("expected 0 refinery_deployed on failure, got %d", len(deployed))
	}
	if len(failed) != 1 {
		t.Fatalf("expected 1 refinery_deploy_failed, got %d", len(failed))
	}
	f := failed[0]
	if f.Agent != "refinery" {
		t.Errorf("failed.agent = %q, want refinery", f.Agent)
	}
	if f.WorkItemID != "mg-baddeploy" {
		t.Errorf("failed.work_item_id = %q, want mg-baddeploy", f.WorkItemID)
	}
	if reason, _ := f.Details["reason"].(string); reason == "" {
		t.Error("failed.details.reason is empty")
	}
	if got := f.Details["merge_request_id"]; got != id {
		t.Errorf("failed.details.merge_request_id = %v, want %s", got, id)
	}
}

// TestDeployFailedEventTruncatesOutput verifies that a deploy hook with very
// large output gets emitted with output_truncated capped at gateOutputCap.
func TestDeployFailedEventTruncatesOutput(t *testing.T) {
	logPath := useTempEventLog(t)

	// Print > gateOutputCap bytes then exit non-zero.
	deployTOML := `
[deploy]
command = "yes x | head -c 5000; exit 1"
`
	originDir, branch := setupRepoWithDeploy(t, deployTOML)

	wtDir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  wtDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Submit(MergeRequest{
		RepoPath:  originDir,
		Branch:    branch,
		TargetRef: "main",
		Author:    "mg-truncate",
	}); err != nil {
		t.Fatal(err)
	}
	r.processNext()

	all := readEvents(t, logPath)
	failed := filterEvents(all, "refinery_deploy_failed")
	if len(failed) != 1 {
		t.Fatalf("expected 1 refinery_deploy_failed, got %d", len(failed))
	}
	out, _ := failed[0].Details["output_truncated"].(string)
	if len(out) > gateOutputCap {
		t.Errorf("output_truncated len = %d, expected ≤ %d", len(out), gateOutputCap)
	}
	if !strings.Contains(out, "x") {
		t.Errorf("output_truncated missing expected content: %q", out)
	}
}

// TestRunDeployHookEnv ensures POGO_REFINERY=1 is exported so deploy scripts
// can detect they're running under the refinery vs. a developer shell.
func TestRunDeployHookEnv(t *testing.T) {
	dir := t.TempDir()
	output, err := runDeployHook(dir, `printf "%s" "$POGO_REFINERY"`)
	if err != nil {
		t.Fatalf("runDeployHook: %v (output=%q)", err, output)
	}
	if output != "1" {
		t.Errorf("POGO_REFINERY = %q, want 1", output)
	}
}

// TestRunDeployHookCwd ensures the deploy command runs with cmd.Dir = wtDir,
// not the caller's cwd.
func TestRunDeployHookCwd(t *testing.T) {
	dir := t.TempDir()
	output, err := runDeployHook(dir, "pwd")
	if err != nil {
		t.Fatalf("runDeployHook: %v", err)
	}
	got := strings.TrimSpace(output)
	// On macOS, /var and /private/var alias each other, so resolve symlinks
	// before comparing.
	gotResolved, _ := filepath.EvalSymlinks(got)
	wantResolved, _ := filepath.EvalSymlinks(dir)
	if gotResolved != wantResolved {
		t.Errorf("pwd = %q (resolved %q), want %q (resolved %q)", got, gotResolved, dir, wantResolved)
	}
}

