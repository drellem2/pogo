package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentInfoLastActivity(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "activity-test",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Nudge to generate output
	if err := a.Nudge("hello"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	info := ExportInfo(a)
	if info.LastActivity == "" {
		t.Error("expected LastActivity to be set after output")
	}
	if !strings.Contains(info.LastActivity, "ago") && info.LastActivity != "just now" {
		t.Errorf("unexpected LastActivity format: %q", info.LastActivity)
	}
}

func TestAgentInfoLastActivityEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	reg, err := NewRegistry(filepath.Join(tmpDir, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	// Spawn a process that exits immediately without producing visible output
	a, err := reg.Spawn(SpawnRequest{
		Name:    "no-activity",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check info immediately — the ring buffer's lastWrite is zero before any PTY output
	// Note: PTY setup may produce some initial output, so we just verify the field
	// is either empty or a valid "ago" string.
	info := ExportInfo(a)
	if info.LastActivity != "" && !strings.Contains(info.LastActivity, "ago") && info.LastActivity != "just now" {
		t.Errorf("unexpected LastActivity format: %q", info.LastActivity)
	}
}

func TestFormatLastActivity(t *testing.T) {
	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"just now", 0, "just now"},
		{"seconds", 5 * time.Second, "5s ago"},
		{"minutes", 2*time.Minute + 30*time.Second, "2m30s ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLastActivity(time.Now().Add(-tt.ago))
			if got != tt.want {
				t.Errorf("formatLastActivity(-%v) = %q, want %q", tt.ago, got, tt.want)
			}
		})
	}
}
