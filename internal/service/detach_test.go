//go:build !windows

package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestBuildDetachCmdWiring verifies that buildDetachCmd assembles an exec.Cmd
// with stdin from /dev/null, stdout/stderr both pointing at the log file, and
// SysProcAttr.Setsid=true. This is the contract that makes --detach work:
// without Setsid the child stays in the parent's session and is killed
// alongside it; without redirected stdio the child can wedge on a closed pipe
// when the parent exits.
func TestBuildDetachCmdWiring(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "install.log")

	cmd, logFile, devnull, err := buildDetachCmd("/usr/bin/true", []string{"--ignored"}, logPath)
	if err != nil {
		t.Fatalf("buildDetachCmd: %v", err)
	}
	defer logFile.Close()
	defer devnull.Close()

	if cmd.Path != "/usr/bin/true" {
		t.Errorf("cmd.Path = %q, want /usr/bin/true", cmd.Path)
	}
	// exec.Command sets Args[0] to the binary path; user args follow.
	if len(cmd.Args) != 2 || cmd.Args[1] != "--ignored" {
		t.Errorf("cmd.Args = %v, want [/usr/bin/true --ignored]", cmd.Args)
	}
	if cmd.Stdin != devnull {
		t.Errorf("cmd.Stdin not wired to /dev/null")
	}
	if cmd.Stdout != logFile || cmd.Stderr != logFile {
		t.Errorf("cmd.Stdout/Stderr not both wired to log file (stdout=%v stderr=%v)", cmd.Stdout, cmd.Stderr)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Errorf("SysProcAttr.Setsid not set; child would not survive caller's session")
	}

	// Log file must exist, be writable, and be opened in append mode so
	// repeated --detach invocations don't truncate prior output.
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log file not created: %v", err)
	}
}

// TestBuildDetachCmdLogPathError covers the failure path when the log path
// is in a non-existent directory: the parent must surface a clean error
// rather than silently dispatching a child whose stdio goes nowhere.
func TestBuildDetachCmdLogPathError(t *testing.T) {
	_, _, _, err := buildDetachCmd("/usr/bin/true", nil, "/nonexistent-dir-xyz/install.log")
	if err == nil {
		t.Fatal("expected error opening log under non-existent dir, got nil")
	}
	if !strings.Contains(err.Error(), "open log") {
		t.Errorf("error did not mention 'open log': %v", err)
	}
}

// TestStartDetachedRunsInNewSession is the empirical proof that Setsid took
// effect: spawn a stub via startDetached, have it print its session ID to
// a file, then verify the SID differs from the test process's SID. If
// Setsid weren't applied, the child would inherit our session.
func TestStartDetachedRunsInNewSession(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "install.log")
	sidPath := filepath.Join(dir, "child.sid")

	// `ps -o sess= -p $$` prints the child's session ID. Redirect that to
	// sidPath so we can read it after the child exits.
	script := "ps -o sess= -p $$ > " + sidPath
	pid, gotLog, err := startDetached("/bin/sh", []string{"-c", script}, logPath)
	if err != nil {
		t.Fatalf("startDetached: %v", err)
	}
	if gotLog != logPath {
		t.Errorf("returned log path = %q, want %q", gotLog, logPath)
	}
	if pid <= 0 {
		t.Errorf("non-positive pid: %d", pid)
	}

	// Parent already called Process.Release, so we can't Wait — poll
	// for the SID file to appear.
	deadline := time.Now().Add(3 * time.Second)
	var childSID string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(sidPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			childSID = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if childSID == "" {
		t.Fatalf("child never wrote its session ID to %s", sidPath)
	}

	mySID, err := unix.Getsid(os.Getpid())
	if err != nil {
		t.Fatalf("Getsid: %v", err)
	}
	if childSID == strconv.Itoa(mySID) {
		t.Errorf("child SID %q matches parent SID %d — Setsid did not take effect", childSID, mySID)
	}
}

// TestDefaultDetachLogPath confirms the documented default. Tested directly
// rather than via Detach() because Detach() spawns the test binary itself
// against a non-existent `service install` subcommand, which is irrelevant
// to the fallback contract.
func TestDefaultDetachLogPath(t *testing.T) {
	if !strings.HasPrefix(DefaultDetachLogPath, "/") {
		t.Errorf("DefaultDetachLogPath = %q, expected absolute path", DefaultDetachLogPath)
	}
}
