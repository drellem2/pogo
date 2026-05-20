package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsIdle(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "idle-test",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Write something to trigger output
	a.Nudge("hello")
	time.Sleep(200 * time.Millisecond)

	// Should NOT be idle immediately after output
	if a.IsIdle(1 * time.Second) {
		t.Error("expected agent not idle immediately after output")
	}

	// Wait for quiescence
	time.Sleep(1200 * time.Millisecond)

	// Should be idle after quiet period
	if !a.IsIdle(1 * time.Second) {
		t.Error("expected agent to be idle after 1s of quiet")
	}
}

func TestIsIdleNoOutput(t *testing.T) {
	// Agent with no output yet should not be considered idle
	buf := NewRingBuffer(1024)
	a := &Agent{
		Name:      "no-output",
		outputBuf: buf,
		done:      make(chan struct{}),
	}
	if a.IsIdle(100 * time.Millisecond) {
		t.Error("agent with no output should not be idle")
	}
}

func TestNudgeWithModeImmediate(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "immediate-test",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Immediate nudge should work without waiting
	err = a.NudgeWithMode("immediate msg", NudgeImmediate, 5*time.Second)
	if err != nil {
		t.Fatalf("NudgeWithMode(immediate): %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	output := string(a.RecentOutput(1024))
	if !strings.Contains(output, "immediate msg") {
		t.Errorf("expected output to contain 'immediate msg', got %q", output)
	}
}

func TestNudgeWithModeWaitIdle(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "waitidle-test",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Generate some output first
	a.Nudge("warmup")
	time.Sleep(200 * time.Millisecond)

	// Wait-idle nudge — agent should become idle after output stops
	start := time.Now()
	err = a.NudgeWithMode("waited msg", NudgeWaitIdle, 10*time.Second)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("NudgeWithMode(wait-idle): %v", err)
	}

	// Should have waited at least the idle threshold (2s)
	if elapsed < DefaultNudgeProfile.IdleThreshold {
		t.Errorf("expected to wait at least %v, but only waited %v", DefaultNudgeProfile.IdleThreshold, elapsed)
	}

	time.Sleep(200 * time.Millisecond)
	output := string(a.RecentOutput(1024))
	if !strings.Contains(output, "waited msg") {
		t.Errorf("expected output to contain 'waited msg', got %q", output)
	}
}

// TestNudgeWithModeWaitIdleTimeoutOnBusy covers the S1 symptom from mg-8772:
// a nudge in wait-idle mode against an agent that never goes quiet must fail
// with a context-deadline error, not hang and not deliver. The agent here
// emits output continuously, so WaitIdle can never observe quiescence. The
// returned error must satisfy errors.Is(err, context.DeadlineExceeded) so
// callers can distinguish "agent busy" from other failures, and the message
// must name the busy/stuck condition for operator triage.
func TestNudgeWithModeWaitIdleTimeoutOnBusy(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// A shell that prints forever — the PTY is never quiet, so the agent
	// never satisfies the idle threshold.
	a, err := reg.Spawn(SpawnRequest{
		Name:    "busy-agent",
		Type:    TypePolecat,
		Command: []string{"sh", "-c", "while true; do printf x; sleep 0.05; done"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait until the agent has actually produced output, so we are testing
	// the busy path and not the just-spawned no-output path.
	deadline := time.Now().Add(2 * time.Second)
	for len(a.RecentOutput(16)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(a.RecentOutput(16)) == 0 {
		t.Fatal("busy agent never produced output")
	}

	start := time.Now()
	err = a.NudgeWithMode("should not deliver", NudgeWaitIdle, 1*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected wait-idle nudge to fail against a perpetually busy agent")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error must wrap context.DeadlineExceeded for callers to detect busy/timeout; got %v", err)
	}
	if !strings.Contains(err.Error(), "busy") && !strings.Contains(err.Error(), "still producing output") {
		t.Errorf("error message should name the busy/stuck condition; got %q", err.Error())
	}
	// The timeout was 1s; the call must return promptly around that, not hang.
	if elapsed > 5*time.Second {
		t.Errorf("nudge took %v to time out; expected ~1s", elapsed)
	}
}

func TestNudgeExitedAgent(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Spawn an agent that exits immediately
	a, err := reg.Spawn(SpawnRequest{
		Name:    "exit-nudge",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	// Wait-idle nudge should fail because the agent exited
	err = a.NudgeWithMode("hello", NudgeWaitIdle, 5*time.Second)
	if err == nil {
		t.Error("expected error nudging exited agent in wait-idle mode")
	}
}

func TestRingBufferLastWriteTime(t *testing.T) {
	buf := NewRingBuffer(1024)

	if !buf.LastWriteTime().IsZero() {
		t.Error("expected zero time before any writes")
	}

	buf.Write([]byte("data"))
	lw := buf.LastWriteTime()
	if lw.IsZero() {
		t.Error("expected non-zero time after write")
	}
	if time.Since(lw) > time.Second {
		t.Error("lastWrite should be recent")
	}
}
