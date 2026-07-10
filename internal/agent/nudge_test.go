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

// TestWaitForReadyGatesOnSentinel unit-tests the sentinel gate: the wait must
// not open until the sentinel has appeared in PTY output AND output has since
// gone quiet. Regression for mg-ce61, where the initial nudge fired on mere
// quiescence (true during pre-TUI startup) and piled into the kernel buffer.
func TestWaitForReadyGatesOnSentinel(t *testing.T) {
	const sentinel = "? for shortcuts"

	// No output yet: the sentinel can never appear, so the wait must time out
	// reporting seen=false.
	buf := NewRingBuffer(1024)
	a := &Agent{Name: "wfr-empty", outputBuf: buf, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	seen, err := a.WaitForReady(ctx, []string{sentinel}, 100*time.Millisecond)
	cancel()
	if seen {
		t.Error("sentinel reported seen with no output at all")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded with no sentinel; got %v", err)
	}

	// Sentinel present, then output goes quiet: the gate opens.
	buf.Write([]byte("\x1b[1mwelcome\x1b[0m\n? for shortcuts\n"))
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	seen, err = a.WaitForReady(ctx2, []string{sentinel}, 100*time.Millisecond)
	cancel2()
	if !seen {
		t.Error("sentinel present in output but not reported seen")
	}
	if err != nil {
		t.Errorf("WaitForReady should succeed once sentinel seen and idle; got %v", err)
	}
}

// TestWaitForReadySentinelSeenButNeverIdle covers the case where the sentinel
// has appeared but the harness keeps redrawing: the wait must report seen=true
// (so the caller can deliver best-effort) and a deadline error.
func TestWaitForReadySentinelSeenButNeverIdle(t *testing.T) {
	buf := NewRingBuffer(4096)
	a := &Agent{Name: "wfr-busy", outputBuf: buf, done: make(chan struct{})}

	// Sentinel is already present; a goroutine keeps writing so the buffer is
	// never quiet for the quiescence window.
	buf.Write([]byte("? for shortcuts\n"))
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				buf.Write([]byte("."))
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	defer close(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	seen, err := a.WaitForReady(ctx, []string{"? for shortcuts"}, 300*time.Millisecond)
	cancel()
	if !seen {
		t.Error("sentinel was present; expected seen=true even though never idle")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded when output never settles; got %v", err)
	}
}

// TestNudgeWaitReadyDeliversOnSentinel verifies the happy path: once the fake
// harness emits the prompt-ready sentinel and goes quiet, a wait-ready nudge
// delivers.
func TestNudgeWaitReadyDeliversOnSentinel(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "ready-test",
		Type:    TypePolecat,
		Command: []string{"bash", "-c", "echo '? for shortcuts'; cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.NudgeWithMode("go now", NudgeWaitReady, 10*time.Second); err != nil {
		t.Fatalf("NudgeWithMode(wait-ready): %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	output := string(a.RecentOutput(2048))
	if !strings.Contains(output, "go now") {
		t.Errorf("expected output to contain 'go now', got %q", output)
	}
}

// TestNudgeWaitReadyBestEffortAfterTimeout verifies the graceful-degradation
// contract: if the sentinel never appears (e.g. a harness UI change made it
// stale), the wait-ready nudge must NOT fire early (the pre-TUI-quiescence bug)
// but MUST still deliver best-effort once the timeout elapses, returning nil —
// degrading to no-worse-than the old wait-idle behavior rather than dropping
// the initial nudge.
func TestNudgeWaitReadyBestEffortAfterTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Emits output, but never the sentinel.
	a, err := reg.Spawn(SpawnRequest{
		Name:    "no-sentinel",
		Type:    TypePolecat,
		Command: []string{"bash", "-c", "echo other-banner; cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- a.NudgeWithMode("late msg", NudgeWaitReady, 3*time.Second) }()

	// Before the timeout the sentinel-gated nudge must not have delivered.
	time.Sleep(1500 * time.Millisecond)
	if strings.Contains(string(a.RecentOutput(2048)), "late msg") {
		t.Error("wait-ready nudge delivered before the sentinel appeared or timeout elapsed")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("best-effort delivery should return nil; got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NudgeWithMode(wait-ready) did not return after its timeout")
	}

	time.Sleep(200 * time.Millisecond)
	if !strings.Contains(string(a.RecentOutput(2048)), "late msg") {
		t.Error("expected best-effort delivery of 'late msg' after timeout")
	}
}

// TestNudgeWaitReadyFallsBackToWaitIdle verifies that a provider with no
// prompt-ready sentinel (e.g. Codex) gets pure wait-idle delivery from
// wait-ready mode: the nudge fires on quiescence alone, with no sentinel gate.
func TestNudgeWaitReadyFallsBackToWaitIdle(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "no-sentinel-profile",
		Type:    TypePolecat,
		Command: []string{"bash", "-c", "echo other-banner; cat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Clear the sentinel so wait-ready takes the fallback path.
	a.nudge.PromptReadySentinel = ""

	start := time.Now()
	if err := a.NudgeWithMode("idle msg", NudgeWaitReady, 10*time.Second); err != nil {
		t.Fatalf("NudgeWithMode(wait-ready, no sentinel): %v", err)
	}
	// Must have waited for quiescence (wait-idle semantics), not the full timeout.
	if elapsed := time.Since(start); elapsed < DefaultNudgeProfile.IdleThreshold {
		t.Errorf("expected to wait at least the idle threshold %v; waited %v",
			DefaultNudgeProfile.IdleThreshold, elapsed)
	}

	time.Sleep(200 * time.Millisecond)
	if !strings.Contains(string(a.RecentOutput(2048)), "idle msg") {
		t.Error("expected fallback wait-idle delivery of 'idle msg'")
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
