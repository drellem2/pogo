package client

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mg-a7a1 — the regression the 2026-09 outage turned on: pogod must outlive
// the CLI that spawned it. StartServer is reached from every `lsp`, `pose` and
// `pogo visit` (the zsh chpwd hook), and each of those exits the moment pogod
// answers /health. When pogod's stdio was a pipe that CLI read, the CLI's exit
// left the pipe with no reader and pogod's next log line killed it.
//
// The shape is reproduced exactly: a helper process (standing in for the CLI)
// spawns a stand-in daemon and exits at once; the daemon writes to stdout and
// stderr a second later and only then drops a marker. The marker exists iff
// those writes did not kill it.
//
// The PIPE case is run too, as the positive control. Without it a green
// result would not distinguish "the fix works" from "this instrument cannot
// see a daemon die" — e.g. under an inherited SIG_IGN for SIGPIPE, where a
// broken-pipe write returns EPIPE instead of killing (the script's `set -e`
// turns that into the same death, so the control still fires).

const stdioHelperEnv = "POGO_TEST_STDIO_HELPER"

// daemonScript is the stand-in pogod. It records its pid, waits long enough
// for its spawner to be gone, writes one line to each stream, and marks that
// it survived.
const daemonScript = `set -e; echo $$ > "$PIDFILE"; sleep 1; echo daemon-stdout-line; echo daemon-stderr-line >&2; touch "$MARKER"`

// TestStdioHelperSpawnAndExit is not a test: it is the helper process the
// tests below re-exec. It spawns the stand-in daemon and exits immediately,
// exactly as a CLI invocation does after StartServer returns.
func TestStdioHelperSpawnAndExit(t *testing.T) {
	mode := os.Getenv(stdioHelperEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	cmd := exec.Command("sh", "-c", daemonScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	switch mode {
	case "startserver":
		if err := startServerCmd(cmd, os.Getenv("LOGPATH"), func() error { return nil }, 5*time.Second); err != nil {
			os.Stderr.WriteString("startServerCmd: " + err.Error() + "\n")
			os.Exit(2)
		}
	case "pipe":
		// The pre-mg-a7a1 capture: os/exec creates a pipe per stream and a
		// goroutine in THIS process reads it.
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Start(); err != nil {
			os.Stderr.WriteString("start: " + err.Error() + "\n")
			os.Exit(2)
		}
	}
	os.Exit(0)
}

// runSpawnAndExit runs the helper in mode and reports whether the stand-in
// daemon survived its spawner's exit, plus the log path it was given.
func runSpawnAndExit(t *testing.T, mode string) (survived bool, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "pogod.log")
	pidFile := filepath.Join(dir, "daemon.pid")
	marker := filepath.Join(dir, "survived")

	helper := exec.Command(os.Args[0], "-test.run=^TestStdioHelperSpawnAndExit$")
	helper.Env = append(os.Environ(),
		stdioHelperEnv+"="+mode, "LOGPATH="+logPath, "PIDFILE="+pidFile, "MARKER="+marker)
	if out, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("helper (%s) failed: %v\n%s", mode, err, out)
	}

	// The spawner is gone. Wait for the daemon to finish one way or the other.
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pid = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("stand-in daemon (%s) never recorded its pid", mode)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("stand-in daemon (%s) pid %d still running after 10s", mode, pid)
	}
	_, err := os.Stat(marker)
	return err == nil, logPath
}

func TestStartServerCmd_DaemonOutlivesItsSpawner(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes and waits on them")
	}

	t.Run("control: a pipe read by the spawner kills the daemon", func(t *testing.T) {
		if survived, _ := runSpawnAndExit(t, "pipe"); survived {
			t.Fatal("the stand-in daemon survived writing to a pipe whose reader had exited — this instrument cannot see the failure, so the startserver case below proves nothing")
		}
	})

	t.Run("startServerCmd: stdio on the log file", func(t *testing.T) {
		survived, logPath := runSpawnAndExit(t, "startserver")
		if !survived {
			t.Fatal("pogod stand-in died after its spawning CLI exited: startServerCmd gave it stdio its spawner owned (mg-a7a1 — the 2026-09 outage)")
		}
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		for _, want := range []string{"daemon-stdout-line", "daemon-stderr-line"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("log %s lacks %q — both streams must land in the log file; got:\n%s", logPath, want, b)
			}
		}
	})
}

// TestStartServerCmd_EarlyExitReadsOnlyThisSpawn guards the diagnostics now
// that they come from a shared, appended file: output from an earlier run must
// not be reported as this spawn's failure.
func TestStartServerCmd_EarlyExitReadsOnlyThisSpawn(t *testing.T) {
	logPath := testLogPath(t)
	if err := os.WriteFile(logPath, []byte("stale line from a previous pogod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "echo 'Cannot get lock' ; exit 1")
	err := startServerCmd(cmd, logPath, alwaysFailingHealth, 5*time.Second)
	if err == nil {
		t.Fatal("expected an error for a pogod that exits before binding")
	}
	if !strings.Contains(err.Error(), "Cannot get lock") {
		t.Errorf("error lacks this spawn's output: %v", err)
	}
	if strings.Contains(err.Error(), "stale line") {
		t.Errorf("error reports output from before this spawn: %v", err)
	}
}
