//go:build !windows

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mg-a7a1 — pogod's own half of the fix: whoever spawned it, a pogod that
// finds its stdio on a pipe re-points it at the log before writing anything,
// so the pipe's reader exiting cannot kill it.
//
// The helper process below plays pogod: its stdout and stderr are pipes this
// test owns. The test closes its read ends — the spawner "exits" — and then
// tells the helper to write. With REPOINT=0 (the positive control, i.e. pogod
// before this change) the write kills it with SIGPIPE; with REPOINT=1 it must
// survive and the lines must be in the file.

const pipedHelperEnv = "POGO_TEST_PIPED_STDIO_HELPER"

func TestPipedStdioHelper(t *testing.T) {
	if os.Getenv(pipedHelperEnv) == "" {
		t.Skip("helper process only")
	}
	logPath := os.Getenv("LOGPATH")
	if os.Getenv("REPOINT") == "1" {
		p := ObservePipedStdio()
		if !p.Stdout || !p.Stderr {
			os.Exit(3) // the test handed us pipes; not seeing them is a failure
		}
		if err := RepointStdioAt(logPath); err != nil {
			os.Exit(4)
		}
	}
	// Wait for the parent to close its read ends — the spawner exiting.
	gate := os.Getenv("GOFILE")
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Stdout.WriteString("pogod-stdout-after-reader-gone\n")
	os.Stderr.WriteString("pogod-stderr-after-reader-gone\n")
	os.Exit(0)
}

func runPipedHelper(t *testing.T, repoint string) (exitErr error, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "logs", "pogod.log") // directory does not exist yet
	gate := filepath.Join(dir, "go")

	cmd := exec.Command(os.Args[0], "-test.run=^TestPipedStdioHelper$")
	cmd.Env = append(os.Environ(), pipedHelperEnv+"=1", "REPOINT="+repoint, "LOGPATH="+logPath, "GOFILE="+gate)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Give the helper time to observe and re-point, then drop every reader.
	time.Sleep(300 * time.Millisecond)
	stdout.Close()
	stderr.Close()
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return cmd.Wait(), logPath
}

func TestRepointStdioAt_PogodOutlivesThePipesReader(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	t.Run("control: without the re-point a write after the reader exits kills pogod", func(t *testing.T) {
		err, _ := runPipedHelper(t, "0")
		if err == nil {
			t.Fatal("helper survived writing to a pipe with no reader — this instrument cannot see the failure, so the re-point case proves nothing")
		}
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper failed for a reason other than its exit: %v", err)
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGPIPE {
			t.Fatalf("helper died, but not of SIGPIPE (%v) — the control is not measuring the outage's mechanism", err)
		}
	})
	t.Run("re-pointed at the log", func(t *testing.T) {
		err, logPath := runPipedHelper(t, "1")
		if err != nil {
			t.Fatalf("pogod stand-in died after its pipe's reader exited, despite RepointStdioAt: %v", err)
		}
		b, rerr := os.ReadFile(logPath)
		if rerr != nil {
			t.Fatalf("read log: %v", rerr)
		}
		for _, want := range []string{"pogod-stdout-after-reader-gone", "pogod-stderr-after-reader-gone"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("log lacks %q; got:\n%s", want, b)
			}
		}
	})
}

func TestObservePipedStdio_RegularFileIsNotAPipe(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isPipe(f) {
		t.Error("a regular file (launchd's redirect) read as a pipe — pogod would re-point a correctly-logging daemon")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if !isPipe(w) {
		t.Error("the write end of a pipe did not read as a pipe — the detector cannot fire")
	}
}
