package agent

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStallThresholdFor(t *testing.T) {
	if got := StallThresholdFor(TypeCrew); got != StallThresholdCrew {
		t.Errorf("StallThresholdFor(crew) = %v, want %v", got, StallThresholdCrew)
	}
	if got := StallThresholdFor(TypePolecat); got != StallThresholdPolecat {
		t.Errorf("StallThresholdFor(polecat) = %v, want %v", got, StallThresholdPolecat)
	}
}

func TestDiagnoseHealthy(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "diag-healthy",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Write some output so lastWrite is recent
	a.Nudge("hello")
	time.Sleep(200 * time.Millisecond)

	diag := diagnoseAgent(a)
	if diag.Health != "healthy" {
		t.Errorf("Health = %q, want %q", diag.Health, "healthy")
	}
	if diag.Stalled {
		t.Error("should not be stalled")
	}
	if !diag.ProcessAlive {
		t.Error("process should be alive")
	}
	if diag.LastActivity.IsZero() {
		t.Error("LastActivity should be set after output")
	}
	if diag.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", diag.Status, StatusRunning)
	}
}

func TestDiagnoseExited(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	a, err := reg.Spawn(SpawnRequest{
		Name:    "diag-exited",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	diag := diagnoseAgent(a)
	if diag.Health != "exited" {
		t.Errorf("Health = %q, want %q", diag.Health, "exited")
	}
	if diag.ProcessAlive {
		t.Error("process should not be alive after exit")
	}
}

func TestDiagnoseNoOutput(t *testing.T) {
	// An agent with no output yet should be healthy (not stalled).
	buf := NewRingBuffer(1024)
	a := &Agent{
		Name:      "diag-no-output",
		Type:      TypePolecat,
		PID:       0,
		Status:    StatusRunning,
		StartTime: time.Now(),
		outputBuf: buf,
		done:      make(chan struct{}),
	}
	diag := diagnoseAgent(a)
	if diag.Stalled {
		t.Error("agent with no output should not be stalled")
	}
	if diag.LastActivity != (time.Time{}) {
		t.Error("LastActivity should be zero when no output written")
	}
}

func TestDiagnoseStalled(t *testing.T) {
	// Simulate a stalled agent by setting lastWrite far in the past.
	buf := NewRingBuffer(1024)
	buf.Write([]byte("old data"))
	// Manually override lastWrite to simulate stall
	buf.mu.Lock()
	buf.lastWrite = time.Now().Add(-10 * time.Minute)
	buf.mu.Unlock()

	a := &Agent{
		Name:      "diag-stalled",
		Type:      TypePolecat,
		PID:       0,
		Status:    StatusRunning,
		StartTime: time.Now().Add(-15 * time.Minute),
		outputBuf: buf,
		done:      make(chan struct{}),
	}

	diag := diagnoseAgent(a)
	if !diag.Stalled {
		t.Error("agent should be stalled (idle > 5min threshold)")
	}
	if diag.Health != "stalled" {
		t.Errorf("Health = %q, want %q", diag.Health, "stalled")
	}
}

func TestDiagnoseIdle(t *testing.T) {
	// Simulate an idle agent — past half threshold but not stalled.
	buf := NewRingBuffer(1024)
	buf.Write([]byte("some data"))
	buf.mu.Lock()
	buf.lastWrite = time.Now().Add(-3 * time.Minute) // > 2.5min (half of 5min), < 5min
	buf.mu.Unlock()

	a := &Agent{
		Name:      "diag-idle",
		Type:      TypePolecat,
		PID:       0,
		Status:    StatusRunning,
		StartTime: time.Now().Add(-10 * time.Minute),
		outputBuf: buf,
		done:      make(chan struct{}),
	}

	diag := diagnoseAgent(a)
	if diag.Stalled {
		t.Error("agent should not be stalled yet")
	}
	if diag.Health != "idle" {
		t.Errorf("Health = %q, want %q", diag.Health, "idle")
	}
}

func TestDiagnoseRecentOutputTail(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "diag-tail",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	a.Nudge("tail-test-data")
	time.Sleep(200 * time.Millisecond)

	diag := diagnoseAgent(a)
	if diag.RecentOutputTail == "" {
		t.Error("RecentOutputTail should not be empty after output")
	}
}
