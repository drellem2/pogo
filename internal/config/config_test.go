package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestAgentCommandDefault(t *testing.T) {
	cfg := &AgentsConfig{}
	if got := cfg.AgentCommand("crew"); got != DefaultAgentCommand {
		t.Errorf("expected default command, got %s", got)
	}
}

func TestAgentCommandGlobal(t *testing.T) {
	cfg := &AgentsConfig{Command: "myagent --flag {{.PromptFile}}"}
	if got := cfg.AgentCommand("crew"); got != "myagent --flag {{.PromptFile}}" {
		t.Errorf("expected global command, got %s", got)
	}
	if got := cfg.AgentCommand("polecat"); got != "myagent --flag {{.PromptFile}}" {
		t.Errorf("expected global command for polecat, got %s", got)
	}
}

func TestAgentCommandPerType(t *testing.T) {
	cfg := &AgentsConfig{
		Command: "default --flag {{.PromptFile}}",
		Crew:    AgentTypeConfig{Command: "crew-agent {{.PromptFile}}"},
		Polecat: AgentTypeConfig{Command: "polecat-agent {{.PromptFile}}"},
	}
	if got := cfg.AgentCommand("crew"); got != "crew-agent {{.PromptFile}}" {
		t.Errorf("expected crew override, got %s", got)
	}
	if got := cfg.AgentCommand("polecat"); got != "polecat-agent {{.PromptFile}}" {
		t.Errorf("expected polecat override, got %s", got)
	}
}

func TestAgentCommandEnvOverride(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_PORT")

	os.Setenv("POGO_AGENT_COMMAND", "custom-agent {{.PromptFile}}")
	defer os.Unsetenv("POGO_AGENT_COMMAND")

	cfg := Load()
	if cfg.Agents.Command != "custom-agent {{.PromptFile}}" {
		t.Errorf("expected env override, got %s", cfg.Agents.Command)
	}
}

func TestAgentCommandConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_PORT")
	os.Unsetenv("POGO_AGENT_COMMAND")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[agents]
command = "aider --model gpt-4o --read {{.PromptFile}}"

[agents.crew]
command = "crew-cmd {{.PromptFile}}"

[agents.polecat]
command = "polecat-cmd {{.PromptFile}}"
`), 0644)

	cfg := Load()
	if cfg.Agents.Command != "aider --model gpt-4o --read {{.PromptFile}}" {
		t.Errorf("expected agents.command from file, got %s", cfg.Agents.Command)
	}
	if cfg.Agents.Crew.Command != "crew-cmd {{.PromptFile}}" {
		t.Errorf("expected agents.crew.command from file, got %s", cfg.Agents.Crew.Command)
	}
	if cfg.Agents.Polecat.Command != "polecat-cmd {{.PromptFile}}" {
		t.Errorf("expected agents.polecat.command from file, got %s", cfg.Agents.Polecat.Command)
	}
}

func TestDefaultMaxWatchers(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_MAX_WATCHERS")

	cfg := Load()
	if cfg.MaxWatchers != DefaultMaxWatchers {
		t.Errorf("expected default max watchers %d, got %d", DefaultMaxWatchers, cfg.MaxWatchers)
	}
}

func TestMaxWatchersEnvOverride(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Setenv("POGO_MAX_WATCHERS", "2048")
	defer os.Unsetenv("POGO_MAX_WATCHERS")

	cfg := Load()
	if cfg.MaxWatchers != 2048 {
		t.Errorf("expected max watchers 2048, got %d", cfg.MaxWatchers)
	}
}

func TestMaxWatchersConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_MAX_WATCHERS")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[search]
max_watchers = 1024
`), 0644)

	cfg := Load()
	if cfg.MaxWatchers != 1024 {
		t.Errorf("expected max watchers 1024 from config file, got %d", cfg.MaxWatchers)
	}
}

func TestRefineryEnabledDefault(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if !cfg.Refinery.Enabled {
		t.Errorf("expected refinery to be enabled by default, got disabled")
	}
}

func TestRefineryDisabledViaConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[refinery]
enabled = false
`), 0644)

	cfg := Load()
	if cfg.Refinery.Enabled {
		t.Errorf("expected refinery to be disabled by config file, got enabled")
	}
}

func TestRefineryEnabledExplicitlyTrue(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[refinery]
enabled = true
`), 0644)

	cfg := Load()
	if !cfg.Refinery.Enabled {
		t.Errorf("expected refinery enabled = true to take effect, got disabled")
	}
}

func TestRefineryUnrelatedKeysDontDisableIt(t *testing.T) {
	// Regression: previously the parser cleared Enabled on any non-"true"
	// value because Load() didn't distinguish "unset" from "explicitly false".
	// A config file with [refinery] but no `enabled` key should leave the
	// default (true) intact.
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[refinery]
poll_interval = "15s"
`), 0644)

	cfg := Load()
	if !cfg.Refinery.Enabled {
		t.Errorf("expected refinery to remain enabled when only poll_interval is set, got disabled")
	}
	if cfg.Refinery.PollInterval != 15*time.Second {
		t.Errorf("expected poll_interval=15s from config file, got %s", cfg.Refinery.PollInterval)
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
