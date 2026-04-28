//go:build !windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// DefaultDetachLogPath is where stdout+stderr of the detached install land
// when no override is provided. /tmp is world-writable, survives the pogod
// restart, and is the same location the README and prior nohup-setsid recipe
// targeted, so existing tooling that tails the log keeps working.
const DefaultDetachLogPath = "/tmp/pogo-service-install.log"

// Detach re-execs the current binary running `service install` (without
// --detach, to avoid recursion) in a new session. The child outlives the
// caller's session and survives the pogod restart that the install
// performs, while the parent returns immediately with the child's PID.
//
// If logPath is empty, DefaultDetachLogPath is used. The log file is
// opened in append mode; consecutive --detach invocations accumulate.
//
// This replaces the prior `nohup setsid pogo service install &` recipe,
// which is not portable to macOS where setsid does not exist in base or
// via Homebrew.
func Detach(logPath string) (pid int, resolvedLog string, err error) {
	if logPath == "" {
		logPath = DefaultDetachLogPath
	}
	self, err := os.Executable()
	if err != nil {
		return 0, "", fmt.Errorf("resolve own binary: %w", err)
	}
	return startDetached(self, []string{"service", "install"}, logPath)
}

// buildDetachCmd is the testable seam for Detach: it assembles the
// exec.Cmd with stdio redirected to logPath and SysProcAttr.Setsid set,
// but does not start it. The returned files (log, devnull) must be
// closed by the caller after Start (or instead of Start, in a test).
func buildDetachCmd(bin string, args []string, logPath string) (*exec.Cmd, *os.File, *os.File, error) {
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open log %s: %w", logPath, err)
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		logFile.Close()
		return nil, nil, nil, fmt.Errorf("open /dev/null: %w", err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, logFile, devnull, nil
}

func startDetached(bin string, args []string, logPath string) (int, string, error) {
	cmd, logFile, devnull, err := buildDetachCmd(bin, args, logPath)
	if err != nil {
		return 0, "", err
	}
	defer logFile.Close()
	defer devnull.Close()

	if err := cmd.Start(); err != nil {
		return 0, "", fmt.Errorf("start detached process: %w", err)
	}
	// Capture the pid before Release: os.Process.Release zeroes (sets to
	// -1) p.Pid so callers can't accidentally signal a recycled pid.
	pid := cmd.Process.Pid
	// Release the os.Process so the child becomes a true orphan reaped
	// by init/launchd. Without Release the Go runtime keeps a wait4
	// outstanding and the parent can't cleanly forget about the child.
	if err := cmd.Process.Release(); err != nil {
		return 0, "", fmt.Errorf("release detached process: %w", err)
	}
	return pid, logPath, nil
}
