package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPort(t *testing.T) {
	// Clear env to ensure defaults
	os.Unsetenv("POGO_PORT")
	os.Unsetenv("XDG_CONFIG_HOME")

	// Point XDG to a nonexistent dir so no config file is read
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if cfg.Port != DefaultPort {
		t.Errorf("expected default port %d, got %d", DefaultPort, cfg.Port)
	}
}

func TestEnvOverride(t *testing.T) {
	os.Setenv("POGO_PORT", "9999")
	defer os.Unsetenv("POGO_PORT")

	cfg := Load()
	if cfg.Port != 9999 {
		t.Errorf("expected port 9999, got %d", cfg.Port)
	}
}

func TestEnvInvalidIgnored(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	os.Setenv("POGO_PORT", "notanumber")
	defer os.Unsetenv("POGO_PORT")

	cfg := Load()
	if cfg.Port != DefaultPort {
		t.Errorf("expected default port %d for invalid env, got %d", DefaultPort, cfg.Port)
	}
}

func TestConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_PORT")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
# Pogo configuration

[server]
port = 8080
`), 0644)

	cfg := Load()
	if cfg.Port != 8080 {
		t.Errorf("expected port 8080 from config file, got %d", cfg.Port)
	}
}

func TestEnvOverridesConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[server]
port = 8080
`), 0644)

	os.Setenv("POGO_PORT", "7777")
	defer os.Unsetenv("POGO_PORT")

	cfg := Load()
	if cfg.Port != 7777 {
		t.Errorf("expected env port 7777 to override config file, got %d", cfg.Port)
	}
}

func TestServerURL(t *testing.T) {
	cfg := &Config{Port: 12345}
	if got := cfg.ServerURL(); got != "http://localhost:12345" {
		t.Errorf("expected http://localhost:12345, got %s", got)
	}
}

func TestListenAddr(t *testing.T) {
	cfg := &Config{Port: 12345}
	if got := cfg.ListenAddr(); got != ":12345" {
		t.Errorf("expected :12345, got %s", got)
	}
}
