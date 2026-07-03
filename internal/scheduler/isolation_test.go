package scheduler

import (
	"path/filepath"
	"testing"
)

// TestDefaultPathHonorsPogoHome locks the mg-3dc3 contract: an isolated
// daemon (POGO_HOME overridden) must not read or write the real user's
// schedules.json.
func TestDefaultPathHonorsPogoHome(t *testing.T) {
	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(pogoHome, "schedules.json"); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}
