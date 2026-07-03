package refinery

import (
	"path/filepath"
	"testing"
)

// TestDefaultPathsHonorPogoHome locks the mg-3dc3 contract: an isolated
// daemon (POGO_HOME overridden) must never resolve the real user's
// refinery-state.json or worktree dir.
func TestDefaultPathsHonorPogoHome(t *testing.T) {
	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)

	got, err := DefaultStatePath()
	if err != nil {
		t.Fatalf("DefaultStatePath: %v", err)
	}
	if want := filepath.Join(pogoHome, "refinery-state.json"); got != want {
		t.Errorf("DefaultStatePath() = %q, want %q", got, want)
	}

	cfg := DefaultConfig()
	if want := filepath.Join(pogoHome, "refinery-state.json"); cfg.StatePath != want {
		t.Errorf("DefaultConfig().StatePath = %q, want %q", cfg.StatePath, want)
	}
	if want := filepath.Join(pogoHome, "refinery", "worktrees"); cfg.WorktreeDir != want {
		t.Errorf("DefaultConfig().WorktreeDir = %q, want %q", cfg.WorktreeDir, want)
	}
}
