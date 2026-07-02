//go:build !windows

package service

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// TestRotatePogodLogEndToEnd simulates the launchd environment without
// touching the live daemon: it points this process's fds 1/2 at an
// oversized pogod.log exactly the way launchd's StandardOutPath redirect
// does, runs the startup rotation, and verifies (a) the old content moved
// to pogod.log.1 and (b) subsequent stderr writes land in the fresh
// pogod.log.
func TestRotatePogodLogEndToEnd(t *testing.T) {
	logPath := setTestHome(t)
	oldContent := bytes.Repeat([]byte("prior-run evidence\n"), maxPogodLogSize/19+1)
	if err := os.WriteFile(logPath, oldContent, 0644); err != nil {
		t.Fatalf("write big log: %v", err)
	}

	// Save the real stdout/stderr and guarantee restoration before any
	// assertion output is written.
	savedOut, err := unix.Dup(1)
	if err != nil {
		t.Fatalf("dup(1): %v", err)
	}
	savedErr, err := unix.Dup(2)
	if err != nil {
		t.Fatalf("dup(2): %v", err)
	}
	restore := func() {
		if savedOut >= 0 {
			dupFd(savedOut, 1)
			unix.Close(savedOut)
			savedOut = -1
		}
		if savedErr >= 0 {
			dupFd(savedErr, 2)
			unix.Close(savedErr)
			savedErr = -1
		}
	}
	defer restore()

	// Simulate launchd: open the log in append mode and dup2 it over 1/2.
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if err := dupFd(int(f.Fd()), 1); err != nil {
		f.Close()
		restore()
		t.Fatalf("dup2 onto stdout: %v", err)
	}
	if err := dupFd(int(f.Fd()), 2); err != nil {
		f.Close()
		restore()
		t.Fatalf("dup2 onto stderr: %v", err)
	}
	f.Close()

	rotated, gotPath, rotErr := RotatePogodLogIfNeeded()
	// Write a marker through os.Stderr (what the log package uses) while
	// still redirected, then restore fds before asserting.
	os.Stderr.WriteString("post-rotation marker\n")
	restore()

	if rotErr != nil {
		t.Fatalf("RotatePogodLogIfNeeded: %v", rotErr)
	}
	if !rotated {
		t.Fatal("rotated=false, want true (stderr was pogod.log and file exceeded the cap)")
	}
	if gotPath != logPath {
		t.Errorf("logPath = %q, want %q", gotPath, logPath)
	}

	rotatedContent := readFileT(t, logPath+".1")
	if !strings.HasPrefix(rotatedContent, "prior-run evidence") {
		t.Errorf("pogod.log.1 does not hold the prior run's content: %.40q", rotatedContent)
	}
	if int64(len(rotatedContent)) != int64(len(oldContent)) {
		t.Errorf("pogod.log.1 size = %d, want %d", len(rotatedContent), len(oldContent))
	}

	fresh := readFileT(t, logPath)
	if !strings.Contains(fresh, "post-rotation marker") {
		t.Errorf("fresh pogod.log missing post-rotation stderr write, got: %.80q", fresh)
	}
	if strings.Contains(fresh, "prior-run evidence") {
		t.Error("fresh pogod.log still contains prior-run content — rename did not happen")
	}
}
