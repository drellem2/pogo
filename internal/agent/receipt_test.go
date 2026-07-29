package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCountSubmitsCountsRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "a.submits")

	// A file that does not exist is zero, not an error: the receipt is created
	// by the harness's first submission, and pogod must be able to read "none
	// yet" without writing a file the harness owns.
	n, err := CountSubmits(path)
	if err != nil {
		t.Fatalf("CountSubmits on a missing file: %v", err)
	}
	if n != 0 {
		t.Fatalf("missing receipt file should count 0, got %d", n)
	}

	for i := 1; i <= 3; i++ {
		if err := RecordSubmit(path); err != nil {
			t.Fatalf("RecordSubmit %d: %v", i, err)
		}
		n, err := CountSubmits(path)
		if err != nil {
			t.Fatalf("CountSubmits: %v", err)
		}
		if n != i {
			t.Fatalf("after %d submits count is %d", i, n)
		}
	}
}

func TestResetReceiptClearsAStalePredecessor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.submits")
	if err := RecordSubmit(path); err != nil {
		t.Fatalf("RecordSubmit: %v", err)
	}

	if err := ResetReceipt(path); err != nil {
		t.Fatalf("ResetReceipt: %v", err)
	}
	n, err := CountSubmits(path)
	if err != nil {
		t.Fatalf("CountSubmits: %v", err)
	}
	if n != 0 {
		t.Fatalf("reset receipt should count 0, got %d", n)
	}

	// Resetting a receipt that is already gone is the normal case (a
	// first-ever spawn) and must not be an error.
	if err := ResetReceipt(path); err != nil {
		t.Fatalf("ResetReceipt on a missing file: %v", err)
	}
}

func TestSubmitReceiptPathIsUnderPogoHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("POGO_HOME", home)

	got := SubmitReceiptPath("pm-pogo")
	want := filepath.Join(home, "agents", "receipts", "pm-pogo.submits")
	if got != want {
		t.Fatalf("SubmitReceiptPath = %q, want %q", got, want)
	}
}

// TestPogoBinaryPathPrefersTheRunningBuild guards the reason the sibling is
// checked first: a daemon running out of a build directory must hand its agents
// that build's pogo, not whatever an older install left on PATH.
func TestPogoBinaryPathPrefersTheRunningBuild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	sibling := filepath.Join(filepath.Dir(exe), "pogo")
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Skipf("cannot write next to the test binary: %v", err)
	}
	defer os.Remove(sibling)

	if got := pogoBinaryPath(); got != sibling {
		t.Fatalf("pogoBinaryPath = %q, want the sibling %q", got, sibling)
	}
}
