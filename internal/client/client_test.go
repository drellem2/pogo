package client

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alwaysFailingHealth simulates a daemon that never becomes healthy.
func alwaysFailingHealth() error {
	return errors.New("connection refused")
}

// TestStartServerCmd_BindFailureSurfacedInError simulates the mg-71e6 bug:
// a "pogod" process that exits before binding (e.g. "address already in
// use"). The fix must (a) detect that pogod never became healthy and
// (b) surface the captured output in the returned error so the user can
// see *why* the install failed.
func TestStartServerCmd_BindFailureSurfacedInError(t *testing.T) {
	// Spawn a fake "pogod" that prints a bind-failure-style message to
	// stderr and exits non-zero. This is the scenario where the daemon
	// can't bind to its port.
	cmd := exec.Command("sh", "-c", "echo 'listen tcp 127.0.0.1:6060: bind: address already in use' >&2; exit 1")

	start := time.Now()
	err := startServerCmd(cmd, alwaysFailingHealth, 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when pogod exits before binding, got nil")
	}
	// Must surface the underlying stderr message so the user can see why.
	if !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("expected error to contain captured stderr ('address already in use'), got: %v", err)
	}
	// Premature-exit path should return promptly via the cmd.Wait
	// goroutine — we should NOT spend the full 5s deadline waiting.
	if elapsed > 2*time.Second {
		t.Errorf("expected fast-fail on premature exit, took %v", elapsed)
	}
}

// TestStartServerCmd_StdoutFailureSurfacedInError covers the case where
// pogod writes its early-exit error to stdout instead of stderr — which
// is what real pogod does for lockfile failures (fmt.Printf rather than
// log.Fatal). Capturing stdout too is part of the fix.
func TestStartServerCmd_StdoutFailureSurfacedInError(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 'Cannot get lock \"/tmp/pogo.pid\", reason: Locked by other process'; exit 1")

	err := startServerCmd(cmd, alwaysFailingHealth, 5*time.Second)
	if err == nil {
		t.Fatal("expected error when pogod exits with stdout-only message, got nil")
	}
	if !strings.Contains(err.Error(), "Cannot get lock") {
		t.Errorf("expected error to surface stdout message, got: %v", err)
	}
}

// TestStartServerCmd_TimeoutWithoutHealth covers the case where pogod
// keeps running (doesn't crash) but never serves /health — e.g. because
// some other process is squatting the port and pogod is wedged retrying.
// The fix must time out cleanly with an actionable error.
func TestStartServerCmd_TimeoutWithoutHealth(t *testing.T) {
	// Fake pogod that runs but never responds to health checks. We
	// also write to stderr so the timeout path can surface it. Use
	// "exec sleep" so the shell replaces itself with sleep — that
	// way SIGKILL on the spawned process actually closes the stderr
	// pipe, instead of orphaning a child that holds the FD open.
	cmd := exec.Command("sh", "-c", "echo 'pogod: still trying to bind...' >&2; exec sleep 30")

	start := time.Now()
	err := startServerCmd(cmd, alwaysFailingHealth, 500*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when pogod never becomes healthy, got nil")
	}
	if !strings.Contains(err.Error(), "did not become healthy") {
		t.Errorf("expected timeout error message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "still trying to bind") {
		t.Errorf("expected error to surface captured stderr, got: %v", err)
	}
	if elapsed < 500*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("expected ~500ms timeout, took %v", elapsed)
	}
}

// TestStartServerCmd_HealthySucceeds is the happy path: healthCheck
// starts returning nil after a brief warm-up. StartServer should
// return success well under the timeout.
func TestStartServerCmd_HealthySucceeds(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exec sleep 30")

	calls := 0
	healthCheck := func() error {
		calls++
		if calls >= 2 {
			return nil
		}
		return errors.New("not yet")
	}

	start := time.Now()
	err := startServerCmd(cmd, healthCheck, 5*time.Second)
	elapsed := time.Since(start)

	// Always clean up the spawned process, regardless of test outcome.
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	if err != nil {
		t.Fatalf("expected success when health check passes, got: %v", err)
	}
	// Happy path target from acceptance criteria: <1s.
	if elapsed > time.Second {
		t.Errorf("happy path took too long: %v (target: <1s)", elapsed)
	}
}

// TestStartServerCmd_SpawnFailure covers the case where exec.Cmd.Start
// itself fails (e.g. binary not found). The error should be surfaced
// directly without trying to poll a nonexistent process.
func TestStartServerCmd_SpawnFailure(t *testing.T) {
	cmd := exec.Command("/nonexistent/path/to/pogod-binary-mg71e6")
	err := startServerCmd(cmd, alwaysFailingHealth, 5*time.Second)
	if err == nil {
		t.Fatal("expected error when binary doesn't exist, got nil")
	}
	if !strings.Contains(err.Error(), "failed to spawn pogod") {
		t.Errorf("expected spawn-failure error, got: %v", err)
	}
}

// TestNewServerCmd_SessionIsolation is the regression guard for the gh #22
// SIGTERM cascade. An auto-started pogod must be spawned into its own
// session — otherwise it joins the invoking CLI's process group, and a
// Ctrl-C at that terminal or a harness tearing down the CLI's process group
// SIGTERMs pogod (LastExitStatus=15), whose shutdown then stops every agent.
func TestNewServerCmd_SessionIsolation(t *testing.T) {
	cmd := newServerCmd()
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("StartServer must spawn pogod with SysProcAttr.Setsid so the daemon detaches from the CLI's process group and controlling terminal")
	}

	// Behavioral check: the same SysProcAttr, applied to a stand-in daemon,
	// actually lands the child in its own process group.
	fake := exec.Command("sh", "-c", "exec sleep 30")
	fake.SysProcAttr = cmd.SysProcAttr
	if err := startServerCmd(fake, func() error { return nil }, 5*time.Second); err != nil {
		t.Fatalf("startServerCmd: %v", err)
	}
	defer func() {
		_ = fake.Process.Kill()
	}()

	childPgid, err := syscall.Getpgid(fake.Process.Pid)
	if err != nil {
		t.Fatalf("Getpgid(child): %v", err)
	}
	selfPgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid(self): %v", err)
	}
	if childPgid == selfPgid {
		t.Fatalf("spawned daemon shares the CLI's process group (pgid=%d); group-wide signals would cascade into pogod", childPgid)
	}
	if childPgid != fake.Process.Pid {
		t.Errorf("spawned daemon pgid = %d, want %d (daemon should lead its own session/process group)", childPgid, fake.Process.Pid)
	}
}
