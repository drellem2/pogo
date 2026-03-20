package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpawnAndNudge(t *testing.T) {
	tmpDir := t.TempDir()
	socketDir := filepath.Join(tmpDir, "sockets")

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Spawn a simple cat process that echoes input
	agent, err := reg.Spawn(SpawnRequest{
		Name:    "test-agent",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if agent.PID == 0 {
		t.Error("expected non-zero PID")
	}
	if agent.Name != "test-agent" {
		t.Errorf("Name = %q, want %q", agent.Name, "test-agent")
	}
	if agent.Type != TypePolecat {
		t.Errorf("Type = %q, want %q", agent.Type, TypePolecat)
	}

	// Nudge: write to the agent's PTY
	err = agent.Nudge("hello\n")
	if err != nil {
		t.Fatalf("Nudge: %v", err)
	}

	// Give cat time to echo back through the PTY
	time.Sleep(200 * time.Millisecond)

	output := string(agent.RecentOutput(1024))
	if !strings.Contains(output, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", output)
	}
}

func TestSpawnDuplicate(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	_, err = reg.Spawn(SpawnRequest{
		Name:    "dup",
		Type:    TypeCrew,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("first Spawn: %v", err)
	}

	_, err = reg.Spawn(SpawnRequest{
		Name:    "dup",
		Type:    TypeCrew,
		Command: []string{"cat"},
	})
	if err == nil {
		t.Error("expected error spawning duplicate agent")
	}
}

func TestListAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	_, err = reg.Spawn(SpawnRequest{Name: "a1", Type: TypeCrew, Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reg.Spawn(SpawnRequest{Name: "a2", Type: TypePolecat, Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}

	agents := reg.List()
	if len(agents) != 2 {
		t.Errorf("List() returned %d agents, want 2", len(agents))
	}

	if reg.Get("a1") == nil {
		t.Error("Get(a1) returned nil")
	}
	if reg.Get("nonexistent") != nil {
		t.Error("Get(nonexistent) should return nil")
	}
}

func TestStopAgent(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	_, err = reg.Spawn(SpawnRequest{Name: "stopper", Type: TypePolecat, Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}

	err = reg.Stop("stopper", 2*time.Second)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if reg.Get("stopper") != nil {
		t.Error("agent should be removed after stop")
	}
}

func TestSocketPath(t *testing.T) {
	// Use /tmp directly to keep unix socket path under 108-char limit
	socketDir, err := os.MkdirTemp("/tmp", "pogo-test-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(socketDir)

	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	agent, err := reg.Spawn(SpawnRequest{Name: "s1", Type: TypeCrew, Command: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(socketDir, "s1.sock")
	if agent.SocketPath() != expected {
		t.Errorf("SocketPath() = %q, want %q", agent.SocketPath(), expected)
	}

	// Verify socket file exists
	if _, err := os.Stat(agent.SocketPath()); os.IsNotExist(err) {
		t.Error("socket file does not exist")
	}
}

func TestProcessExit(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Spawn a process that exits immediately
	agent, err := reg.Spawn(SpawnRequest{
		Name:    "short-lived",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for it to exit
	select {
	case <-agent.Done():
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit within 2 seconds")
	}
}
