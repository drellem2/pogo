package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePogoBinary drops an executable named "pogo" next to the test binary so
// installReceiptHook can resolve one without depending on what is installed on
// the machine running the suite.
func fakePogoBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	path := filepath.Join(filepath.Dir(exe), "pogo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Skipf("cannot write next to the test binary: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// TestSpawnInstallsTheReceiptHookAndTellsTheAgentWhereToWrite is the wiring
// test: without it every part of confirmed delivery can be individually correct
// while no real agent ever gets a receipt.
func TestSpawnInstallsTheReceiptHookAndTellsTheAgentWhereToWrite(t *testing.T) {
	t.Setenv("POGO_HOME", t.TempDir())
	bin := fakePogoBinary(t)

	var installedDir, installedCmd string
	provider := &Provider{
		ID:    "fake",
		Nudge: DefaultNudgeProfile,
		SubmitReceiptHook: func(dir, hookCommand string) error {
			installedDir, installedCmd = dir, hookCommand
			return nil
		},
	}

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.RegisterProvider(provider)
	reg.SetDefaultProvider(provider.ID)

	workdir := t.TempDir()
	// `env` prints its environment and exits; the PTY output is the assertion.
	a, err := reg.Spawn(SpawnRequest{
		Name:    "wired",
		Type:    TypePolecat,
		Command: []string{"env"},
		Dir:     workdir,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if installedDir != workdir {
		t.Errorf("hook installed into %q, want the agent's working directory %q", installedDir, workdir)
	}
	if want := bin + " hook prompt-submit"; installedCmd != want {
		t.Errorf("hook command = %q, want %q", installedCmd, want)
	}

	wantReceipt := SubmitReceiptPath("wired")
	if a.receiptFile != wantReceipt {
		t.Errorf("agent receiptFile = %q, want %q", a.receiptFile, wantReceipt)
	}
	if !a.hasReceiptSignal() {
		t.Error("an agent whose hook installed cleanly must have a receipt signal")
	}

	// The hook process learns the path from the environment, so the spawned
	// process must actually carry it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(StripANSI(a.RecentOutput(64*1024))), "POGO_SUBMIT_RECEIPT="+wantReceipt) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("spawned process never saw POGO_SUBMIT_RECEIPT=%s in its environment:\n%s",
		wantReceipt, StripANSI(a.RecentOutput(64*1024)))
}

// TestSpawnWithoutAHookHasNoReceiptSignal: a provider that cannot report its
// own submissions is a supported answer, and its agents must fall back to the
// pre-existing behaviour rather than to an escalation firing against silence
// that means nothing.
func TestSpawnWithoutAHookHasNoReceiptSignal(t *testing.T) {
	t.Setenv("POGO_HOME", t.TempDir())

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.RegisterProvider(&Provider{ID: "silent", Nudge: DefaultNudgeProfile})
	reg.SetDefaultProvider("silent")

	a, err := reg.Spawn(SpawnRequest{
		Name:    "unwired",
		Type:    TypePolecat,
		Command: []string{"cat"},
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if a.hasReceiptSignal() {
		t.Fatal("a provider with no SubmitReceiptHook must leave the agent unconfirmable")
	}
}

// TestSpawnResetsAStaleReceipt: agent names are reused across runs, and a
// leftover count from a dead process is a number the live one can never move
// past — every nudge would escalate and then refuse.
func TestSpawnResetsAStaleReceipt(t *testing.T) {
	t.Setenv("POGO_HOME", t.TempDir())
	fakePogoBinary(t)

	stale := SubmitReceiptPath("recycled")
	for i := 0; i < 4; i++ {
		if err := RecordSubmit(stale); err != nil {
			t.Fatalf("RecordSubmit: %v", err)
		}
	}

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.RegisterProvider(&Provider{
		ID:                "fake",
		Nudge:             DefaultNudgeProfile,
		SubmitReceiptHook: func(dir, hookCommand string) error { return nil },
	})
	reg.SetDefaultProvider("fake")

	if _, err := reg.Spawn(SpawnRequest{
		Name:    "recycled",
		Type:    TypePolecat,
		Command: []string{"cat"},
		Dir:     t.TempDir(),
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	n, err := CountSubmits(stale)
	if err != nil {
		t.Fatalf("CountSubmits: %v", err)
	}
	if n != 0 {
		t.Fatalf("spawn left %d stale receipts behind", n)
	}
}

// TestInstallReceiptHookDeclinesWhenTheInstallerFails: a half-installed hook
// must degrade to "no signal", never to a signal that cannot move.
func TestInstallReceiptHookDeclinesWhenTheInstallerFails(t *testing.T) {
	t.Setenv("POGO_HOME", t.TempDir())
	fakePogoBinary(t)

	provider := &Provider{
		ID:                "fake",
		SubmitReceiptHook: func(dir, hookCommand string) error { return os.ErrPermission },
	}
	if got := installReceiptHook(provider, "declined", t.TempDir()); got != "" {
		t.Fatalf("a failed install must yield no receipt path, got %q", got)
	}
}
