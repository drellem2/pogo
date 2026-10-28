package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAgentsWakeConfigFile: [agents] wake is the install-wide wake-mode
// default (drellem2/pogo#232, #236). Parsed verbatim — validation is
// agent.SetDefaultWakeMode's, at pogod startup — and empty when unset, so the
// built-in mail-check applies rather than a value Load() invented.
func TestAgentsWakeConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)

	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte("[agents]\ncoordinator = \"boss\"\n"), 0644)
	if got := Load().Agents.Wake; got != "" {
		t.Errorf("wake unset = %q, want \"\"", got)
	}
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte("[agents]\nwake = \"pointer\"\n"), 0644)
	if got := Load().Agents.Wake; got != "pointer" {
		t.Errorf("wake = %q, want pointer", got)
	}
}
