package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// useTempEventLog redirects events.Emit to a temp file for the duration of
// the test and returns the path.
func useTempEventLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	events.SetLogPathForTesting(path)
	t.Cleanup(func() { events.SetLogPathForTesting("") })
	return path
}

func readEventLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		var m map[string]any
		if err := json.Unmarshal(s.Bytes(), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", s.Text(), err)
		}
		out = append(out, m)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func findEvent(events []map[string]any, eventType, agent string) map[string]any {
	for _, e := range events {
		if e["event_type"] == eventType && e["agent"] == agent {
			return e
		}
	}
	return nil
}

// waitForEvent polls the log file up to timeout for an event of the given type
// and agent. Returns the matched event or nil if not seen.
func waitForEvent(t *testing.T, path, eventType, agent string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			if ev := findEvent(readEventLines(t, path), eventType, agent); ev != nil {
				return ev
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

func TestEmitsAgentSpawned(t *testing.T) {
	path := useTempEventLog(t)
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:        "spawned-emit",
		Type:        TypePolecat,
		Command:     []string{"cat"},
		PromptFile:  "/tmp/prompt.md",
		WorktreeDir: "/tmp/worktree",
		SourceRepo:  "/tmp/repo",
	})
	if err != nil {
		t.Fatal(err)
	}

	ev := waitForEvent(t, path, "agent_spawned", "cat-spawned-emit", 2*time.Second)
	if ev == nil {
		t.Fatal("no agent_spawned event recorded")
	}
	if ev["repo"] != "/tmp/repo" {
		t.Errorf("repo: want /tmp/repo, got %v", ev["repo"])
	}
	d := ev["details"].(map[string]any)
	if d["agent_type"] != "polecat" {
		t.Errorf("agent_type: want polecat, got %v", d["agent_type"])
	}
	if int(d["pid"].(float64)) != a.PID {
		t.Errorf("pid: want %d, got %v", a.PID, d["pid"])
	}
	if d["prompt_file"] != "/tmp/prompt.md" {
		t.Errorf("prompt_file: want /tmp/prompt.md, got %v", d["prompt_file"])
	}
	if d["worktree"] != "/tmp/worktree" {
		t.Errorf("worktree: want /tmp/worktree, got %v", d["worktree"])
	}
}

func TestEmitsAgentStoppedOnCleanExit(t *testing.T) {
	path := useTempEventLog(t)
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "clean-exit",
		Type:    TypePolecat,
		Command: []string{"true"}, // exits immediately with 0
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	ev := waitForEvent(t, path, "agent_stopped", "cat-clean-exit", 2*time.Second)
	if ev == nil {
		t.Fatal("no agent_stopped event recorded")
	}
	d := ev["details"].(map[string]any)
	if int(d["exit_code"].(float64)) != 0 {
		t.Errorf("exit_code: want 0, got %v", d["exit_code"])
	}
	if d["reason"] != "task_complete" {
		t.Errorf("reason: want task_complete, got %v", d["reason"])
	}
	if _, ok := d["duration_seconds"].(float64); !ok {
		t.Errorf("duration_seconds missing or wrong type: %v", d["duration_seconds"])
	}
}

func TestEmitsAgentStoppedOnRequestedStop(t *testing.T) {
	path := useTempEventLog(t)
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = reg.Spawn(SpawnRequest{
		Name:    "requested-stop",
		Type:    TypeCrew,
		Command: []string{"sleep", "30"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := reg.Stop("requested-stop", 2*time.Second); err != nil {
		t.Fatal(err)
	}

	ev := waitForEvent(t, path, "agent_stopped", "crew-requested-stop", 3*time.Second)
	if ev == nil {
		t.Fatal("no agent_stopped event recorded")
	}
	d := ev["details"].(map[string]any)
	if d["reason"] != "requested" {
		t.Errorf("reason: want requested, got %v", d["reason"])
	}
}

func TestEmitsAgentCrashedOnNonZeroExit(t *testing.T) {
	path := useTempEventLog(t)
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "crashed-emit",
		Type:    TypePolecat,
		Command: []string{"false"}, // exits with status 1
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	ev := waitForEvent(t, path, "agent_crashed", "cat-crashed-emit", 2*time.Second)
	if ev == nil {
		t.Fatal("no agent_crashed event recorded")
	}
	d := ev["details"].(map[string]any)
	if int(d["exit_code"].(float64)) != 1 {
		t.Errorf("exit_code: want 1, got %v", d["exit_code"])
	}
	// agent_crashed must NOT have a "reason" field
	if _, ok := d["reason"]; ok {
		t.Errorf("agent_crashed must not include reason, got %v", d["reason"])
	}
}

func TestEmitsAgentRestartedOnRespawn(t *testing.T) {
	path := useTempEventLog(t)
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "restart-emit",
		Type:    TypeCrew,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prevPID := a.PID
	<-a.Done()

	// Switch to a long-lived command so the respawned process stays up.
	a.Command = []string{"sleep", "10"}
	b, err := reg.Respawn("restart-emit")
	if err != nil {
		t.Fatal(err)
	}

	ev := waitForEvent(t, path, "agent_restarted", "crew-restart-emit", 2*time.Second)
	if ev == nil {
		t.Fatal("no agent_restarted event recorded")
	}
	d := ev["details"].(map[string]any)
	if int(d["previous_pid"].(float64)) != prevPID {
		t.Errorf("previous_pid: want %d, got %v", prevPID, d["previous_pid"])
	}
	if int(d["new_pid"].(float64)) != b.PID {
		t.Errorf("new_pid: want %d, got %v", b.PID, d["new_pid"])
	}
	if int(d["restart_count"].(float64)) != 1 {
		t.Errorf("restart_count: want 1, got %v", d["restart_count"])
	}
}

func TestEmitsNudgeSent(t *testing.T) {
	path := useTempEventLog(t)
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "nudge-emit",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.NudgeWithMode("hello", NudgeImmediate, time.Second); err != nil {
		t.Fatalf("NudgeWithMode: %v", err)
	}

	ev := waitForEvent(t, path, "nudge_sent", "pogod", 2*time.Second)
	if ev == nil {
		t.Fatal("no nudge_sent event recorded")
	}
	d := ev["details"].(map[string]any)
	if d["to"] != "cat-nudge-emit" {
		t.Errorf("to: want cat-nudge-emit, got %v", d["to"])
	}
	if d["message"] != "hello" {
		t.Errorf("message: want hello, got %v", d["message"])
	}
	if d["delivery"] != "pty" {
		t.Errorf("delivery: want pty, got %v", d["delivery"])
	}
	if d["mode"] != "immediate" {
		t.Errorf("mode: want immediate, got %v", d["mode"])
	}
}

// TestEventEnvelope verifies every emitted event carries schema_version,
// timestamp (RFC3339Nano UTC), event_type, agent, and details.
func TestEventEnvelope(t *testing.T) {
	path := useTempEventLog(t)
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "envelope",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	// Wait until at least the spawned + stopped events are flushed.
	deadline := time.Now().Add(2 * time.Second)
	var lines []map[string]any
	for time.Now().Before(deadline) {
		lines = readEventLines(t, path)
		if len(lines) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(lines) < 2 {
		t.Fatalf("expected ≥2 events, got %d", len(lines))
	}

	for i, ev := range lines {
		if int(ev["schema_version"].(float64)) != events.SchemaVersion {
			t.Errorf("line %d: schema_version=%v want %d", i, ev["schema_version"], events.SchemaVersion)
		}
		ts, _ := ev["timestamp"].(string)
		if !strings.HasSuffix(ts, "Z") {
			t.Errorf("line %d: timestamp not UTC: %q", i, ts)
		}
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Errorf("line %d: timestamp not RFC3339Nano: %q", i, ts)
		}
		if ev["event_type"] == "" {
			t.Errorf("line %d: empty event_type", i)
		}
		if ev["agent"] == "" {
			t.Errorf("line %d: empty agent", i)
		}
		if _, ok := ev["details"].(map[string]any); !ok {
			t.Errorf("line %d: details missing or wrong type: %v", i, ev["details"])
		}
	}
}
