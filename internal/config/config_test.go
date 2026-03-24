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
	cfg := &Config{Port: 12345, Bind: "127.0.0.1"}
	if got := cfg.ListenAddr(); got != "127.0.0.1:12345" {
		t.Errorf("expected 127.0.0.1:12345, got %s", got)
	}
}

func TestDefaultBind(t *testing.T) {
	os.Unsetenv("POGO_PORT")
	os.Unsetenv("POGO_BIND")
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if cfg.Bind != DefaultBind {
		t.Errorf("expected default bind %s, got %s", DefaultBind, cfg.Bind)
	}
	if got := cfg.ListenAddr(); got != "127.0.0.1:10000" {
		t.Errorf("expected 127.0.0.1:10000, got %s", got)
	}
}

func TestBindEnvOverride(t *testing.T) {
	os.Setenv("POGO_BIND", "0.0.0.0")
	defer os.Unsetenv("POGO_BIND")
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if cfg.Bind != "0.0.0.0" {
		t.Errorf("expected bind 0.0.0.0, got %s", cfg.Bind)
	}
}

func TestDefaultMode(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_MODE")

	cfg := Load()
	if cfg.Mode != ModeLocal {
		t.Errorf("expected default mode %s, got %s", ModeLocal, cfg.Mode)
	}
	if cfg.IsCloud() {
		t.Error("expected IsCloud() to be false for default config")
	}
}

func TestCloudModeEnv(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Setenv("POGO_MODE", "cloud")
	defer os.Unsetenv("POGO_MODE")

	cfg := Load()
	if cfg.Mode != ModeCloud {
		t.Errorf("expected mode cloud, got %s", cfg.Mode)
	}
	if !cfg.IsCloud() {
		t.Error("expected IsCloud() to be true")
	}
	if cfg.WorkspaceDir != "/workspace/repos" {
		t.Errorf("expected default cloud workspace /workspace/repos, got %s", cfg.WorkspaceDir)
	}
}

func TestCloudModeConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_MODE")
	os.Unsetenv("POGO_GITHUB_TOKEN")
	os.Unsetenv("POGO_WORKSPACE_DIR")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[server]
mode = cloud
github_token = "ghp_test123"
workspace_dir = "/tmp/test-repos"
`), 0644)

	cfg := Load()
	if cfg.Mode != ModeCloud {
		t.Errorf("expected mode cloud from config file, got %s", cfg.Mode)
	}
	if cfg.GitHubToken != "ghp_test123" {
		t.Errorf("expected token ghp_test123, got %s", cfg.GitHubToken)
	}
	if cfg.WorkspaceDir != "/tmp/test-repos" {
		t.Errorf("expected workspace /tmp/test-repos, got %s", cfg.WorkspaceDir)
	}
}

func TestGitHubTokenEnvOverride(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[server]
github_token = "file_token"
`), 0644)

	os.Setenv("POGO_GITHUB_TOKEN", "env_token")
	defer os.Unsetenv("POGO_GITHUB_TOKEN")

	cfg := Load()
	if cfg.GitHubToken != "env_token" {
		t.Errorf("expected env token to override file token, got %s", cfg.GitHubToken)
	}
}

func TestWorkspaceDirEnvOverride(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Setenv("POGO_MODE", "cloud")
	defer os.Unsetenv("POGO_MODE")
	os.Setenv("POGO_WORKSPACE_DIR", "/custom/repos")
	defer os.Unsetenv("POGO_WORKSPACE_DIR")

	cfg := Load()
	if cfg.WorkspaceDir != "/custom/repos" {
		t.Errorf("expected /custom/repos, got %s", cfg.WorkspaceDir)
	}
}

func TestLocalModeNoDefaultWorkspace(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_MODE")
	os.Unsetenv("POGO_WORKSPACE_DIR")

	cfg := Load()
	if cfg.WorkspaceDir != "" {
		t.Errorf("expected empty workspace dir in local mode, got %s", cfg.WorkspaceDir)
	}
}

func TestBindConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_PORT")
	os.Unsetenv("POGO_BIND")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[server]
port = 8080
bind = 0.0.0.0
`), 0644)

	cfg := Load()
	if cfg.Bind != "0.0.0.0" {
		t.Errorf("expected bind 0.0.0.0 from config file, got %s", cfg.Bind)
	}
	if got := cfg.ListenAddr(); got != "0.0.0.0:8080" {
		t.Errorf("expected 0.0.0.0:8080, got %s", got)
	}
}
