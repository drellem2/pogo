package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestGitGCDefaults(t *testing.T) {
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if !cfg.GitGC.Enabled {
		t.Error("git GC should be enabled by default")
	}
	if cfg.GitGC.Interval != DefaultGitGCInterval {
		t.Errorf("git GC interval = %v, want %v", cfg.GitGC.Interval, DefaultGitGCInterval)
	}
}

func TestGitGCConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[gitgc]
enabled = false
interval = "15m"
repos = ["/home/u/dev/pogo", "/home/u/dev/other"]
`), 0644)

	cfg := Load()
	if cfg.GitGC.Enabled {
		t.Error("git GC should be disabled by config file")
	}
	if cfg.GitGC.Interval != 15*time.Minute {
		t.Errorf("git GC interval = %v, want 15m", cfg.GitGC.Interval)
	}
	if len(cfg.GitGC.Repos) != 2 || cfg.GitGC.Repos[0] != "/home/u/dev/pogo" {
		t.Errorf("git GC repos = %v, want 2 entries", cfg.GitGC.Repos)
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
	// With no explicit command configured, AgentCommand returns "" — the
	// signal for agent.Registry to fall back to the active provider's
	// CommandTemplate. The literal default no longer lives in config.
	cfg := &AgentsConfig{}
	if got := cfg.AgentCommand("crew"); got != "" {
		t.Errorf("expected empty (provider-default) command, got %q", got)
	}
	if got := cfg.AgentCommand("polecat"); got != "" {
		t.Errorf("expected empty (provider-default) command for polecat, got %q", got)
	}
}

func TestAgentProviderDefault(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_AGENT_PROVIDER")

	cfg := Load()
	if cfg.Agents.Provider != DefaultProvider {
		t.Errorf("expected default provider %q, got %q", DefaultProvider, cfg.Agents.Provider)
	}
	if cfg.Agents.Provider != "claude" {
		t.Errorf("default provider must be \"claude\" to keep existing deployments working, got %q", cfg.Agents.Provider)
	}
}

func TestAgentProviderEnvOverride(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	os.Setenv("POGO_AGENT_PROVIDER", "codex")
	defer os.Unsetenv("POGO_AGENT_PROVIDER")

	cfg := Load()
	if cfg.Agents.Provider != "codex" {
		t.Errorf("expected env override \"codex\", got %q", cfg.Agents.Provider)
	}
}

func TestAgentProviderConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_AGENT_PROVIDER")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[agents]
provider = "codex"
`), 0644)

	cfg := Load()
	if cfg.Agents.Provider != "codex" {
		t.Errorf("expected provider \"codex\" from file, got %q", cfg.Agents.Provider)
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

// TestAgentProviderMethodGlobal verifies AgentProvider returns the global
// [agents] provider for every type when no per-type override is set.
func TestAgentProviderMethodGlobal(t *testing.T) {
	cfg := &AgentsConfig{Provider: "codex"}
	for _, at := range []string{"crew", "polecat"} {
		if got := cfg.AgentProvider(at); got != "codex" {
			t.Errorf("AgentProvider(%q) = %q, want codex (global)", at, got)
		}
	}
}

// TestAgentProviderMethodPerType verifies a per-type [agents.<type>] provider
// overrides the global [agents] provider, while a type without an override
// inherits the global — the mixed-fleet selection from mg-b31b.
func TestAgentProviderMethodPerType(t *testing.T) {
	cfg := &AgentsConfig{
		Provider: "claude",
		Polecat:  AgentTypeConfig{Provider: "codex"},
	}
	if got := cfg.AgentProvider("polecat"); got != "codex" {
		t.Errorf("AgentProvider(polecat) = %q, want codex (per-type override)", got)
	}
	if got := cfg.AgentProvider("crew"); got != "claude" {
		t.Errorf("AgentProvider(crew) = %q, want claude (inherits global)", got)
	}
}

// TestAgentProviderPerTypeConfigFile verifies [agents.crew] provider and
// [agents.polecat] provider parse from the config file and that per-type beats
// the global [agents] provider — the headline mixed-fleet config (mg-b31b).
func TestAgentProviderPerTypeConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_PORT")
	os.Unsetenv("POGO_AGENT_PROVIDER")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[agents]
provider = "claude"

[agents.crew]
provider = "claude"

[agents.polecat]
provider = "codex"
`), 0644)

	cfg := Load()
	if cfg.Agents.Provider != "claude" {
		t.Errorf("global provider = %q, want claude", cfg.Agents.Provider)
	}
	if cfg.Agents.Crew.Provider != "claude" {
		t.Errorf("agents.crew.provider = %q, want claude", cfg.Agents.Crew.Provider)
	}
	if cfg.Agents.Polecat.Provider != "codex" {
		t.Errorf("agents.polecat.provider = %q, want codex", cfg.Agents.Polecat.Provider)
	}
	if got := cfg.Agents.AgentProvider("polecat"); got != "codex" {
		t.Errorf("AgentProvider(polecat) = %q, want codex", got)
	}
	if got := cfg.Agents.AgentProvider("crew"); got != "claude" {
		t.Errorf("AgentProvider(crew) = %q, want claude", got)
	}
}

// TestAgentProviderBackwardCompatConfigFile verifies a config with only the
// global [agents] provider set (no per-type sections) still resolves every
// type to that provider — the no-migration backward-compat guarantee (mg-b31b
// acceptance bar 7).
func TestAgentProviderBackwardCompatConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_PORT")
	os.Unsetenv("POGO_AGENT_PROVIDER")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[agents]
provider = "codex"
`), 0644)

	cfg := Load()
	if cfg.Agents.Crew.Provider != "" || cfg.Agents.Polecat.Provider != "" {
		t.Errorf("per-type providers should be empty, got crew=%q polecat=%q",
			cfg.Agents.Crew.Provider, cfg.Agents.Polecat.Provider)
	}
	for _, at := range []string{"crew", "polecat"} {
		if got := cfg.Agents.AgentProvider(at); got != "codex" {
			t.Errorf("AgentProvider(%q) = %q, want codex (global, no per-type)", at, got)
		}
	}
}

func TestDefaultIndexInterval(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if cfg.IndexInterval != DefaultIndexInterval {
		t.Errorf("expected default index interval %s, got %s", DefaultIndexInterval, cfg.IndexInterval)
	}
}

func TestIndexIntervalConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[search]
index_interval = "30s"
`), 0644)

	cfg := Load()
	if cfg.IndexInterval != 30*time.Second {
		t.Errorf("expected index interval 30s from config file, got %s", cfg.IndexInterval)
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

func TestHeartbeatConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[heartbeat]
interval = "15s"
jump_threshold = "45s"
`), 0644)

	cfg := Load()
	if cfg.Heartbeat.Interval != 15*time.Second {
		t.Errorf("expected heartbeat interval 15s, got %s", cfg.Heartbeat.Interval)
	}
	if cfg.Heartbeat.JumpThreshold != 45*time.Second {
		t.Errorf("expected heartbeat jump_threshold 45s, got %s", cfg.Heartbeat.JumpThreshold)
	}
}

func TestHeartbeatDefaultsWhenUnset(t *testing.T) {
	// With no [heartbeat] section, the Config zero values are returned and
	// the daemon's heartbeat.New() supplies its package-level defaults.
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[server]
port = 8080
`), 0644)

	cfg := Load()
	if cfg.Heartbeat.Interval != 0 {
		t.Errorf("expected zero heartbeat interval (defaults applied at use site), got %s", cfg.Heartbeat.Interval)
	}
	if cfg.Heartbeat.JumpThreshold != 0 {
		t.Errorf("expected zero heartbeat jump_threshold (defaults applied at use site), got %s", cfg.Heartbeat.JumpThreshold)
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

func TestDefaultMaxFilesPerTree(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_MAX_FILES_PER_TREE")

	cfg := Load()
	if cfg.MaxFilesPerTree != DefaultMaxFilesPerTree {
		t.Errorf("expected default max files per tree %d, got %d", DefaultMaxFilesPerTree, cfg.MaxFilesPerTree)
	}
}

func TestMaxFilesPerTreeEnvOverride(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Setenv("POGO_MAX_FILES_PER_TREE", "1234")
	defer os.Unsetenv("POGO_MAX_FILES_PER_TREE")

	cfg := Load()
	if cfg.MaxFilesPerTree != 1234 {
		t.Errorf("expected max files per tree 1234, got %d", cfg.MaxFilesPerTree)
	}
}

func TestMaxFilesPerTreeConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("POGO_MAX_FILES_PER_TREE")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[search]
max_files_per_tree = 9000
`), 0644)

	cfg := Load()
	if cfg.MaxFilesPerTree != 9000 {
		t.Errorf("expected max files per tree 9000 from config file, got %d", cfg.MaxFilesPerTree)
	}
}

func TestIndexRootsDefaultEmpty(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if len(cfg.IndexRoots) != 0 {
		t.Errorf("expected no index roots by default, got %v", cfg.IndexRoots)
	}
}

func TestIndexRootsConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[search]
index_roots = ["/home/user/dev", "/work/repos"]
`), 0644)

	cfg := Load()
	want := []string{"/home/user/dev", "/work/repos"}
	if len(cfg.IndexRoots) != len(want) {
		t.Fatalf("expected %d index roots, got %v", len(want), cfg.IndexRoots)
	}
	for i, w := range want {
		if cfg.IndexRoots[i] != w {
			t.Errorf("index root %d: expected %q, got %q", i, w, cfg.IndexRoots[i])
		}
	}
}

func TestStallWatchDefaults(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if !cfg.StallWatch.Enabled {
		t.Error("stall watch should be enabled by default")
	}
	if cfg.StallWatch.Agent != DefaultStallWatchAgent {
		t.Errorf("agent = %q, want %q", cfg.StallWatch.Agent, DefaultStallWatchAgent)
	}
	if cfg.StallWatch.UnclaimedItemAgeThreshold != DefaultUnclaimedItemAgeThreshold {
		t.Errorf("unclaimed threshold = %v, want %v", cfg.StallWatch.UnclaimedItemAgeThreshold, DefaultUnclaimedItemAgeThreshold)
	}
	if cfg.StallWatch.UnreadMailAgeThreshold != DefaultUnreadMailAgeThreshold {
		t.Errorf("mail age threshold = %v, want %v", cfg.StallWatch.UnreadMailAgeThreshold, DefaultUnreadMailAgeThreshold)
	}
	if cfg.StallWatch.MaxUnreadMailCount != DefaultMaxUnreadMailCount {
		t.Errorf("max unread = %d, want %d", cfg.StallWatch.MaxUnreadMailCount, DefaultMaxUnreadMailCount)
	}
	if cfg.StallWatch.NudgeCooldown != DefaultStallNudgeCooldown {
		t.Errorf("cooldown = %v, want %v", cfg.StallWatch.NudgeCooldown, DefaultStallNudgeCooldown)
	}
}

func TestStallWatchConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[stall_watch]
enabled = false
agent = "director"
unclaimed_item_age_threshold = "3m"
unread_mail_age_threshold = "4m"
max_unread_mail_count = 9
nudge_cooldown = "90s"
`), 0644)

	cfg := Load()
	if cfg.StallWatch.Enabled {
		t.Error("stall watch should be disabled by config file")
	}
	if cfg.StallWatch.Agent != "director" {
		t.Errorf("agent = %q, want director", cfg.StallWatch.Agent)
	}
	if cfg.StallWatch.UnclaimedItemAgeThreshold != 3*time.Minute {
		t.Errorf("unclaimed threshold = %v, want 3m", cfg.StallWatch.UnclaimedItemAgeThreshold)
	}
	if cfg.StallWatch.UnreadMailAgeThreshold != 4*time.Minute {
		t.Errorf("mail age threshold = %v, want 4m", cfg.StallWatch.UnreadMailAgeThreshold)
	}
	if cfg.StallWatch.MaxUnreadMailCount != 9 {
		t.Errorf("max unread = %d, want 9", cfg.StallWatch.MaxUnreadMailCount)
	}
	if cfg.StallWatch.NudgeCooldown != 90*time.Second {
		t.Errorf("cooldown = %v, want 90s", cfg.StallWatch.NudgeCooldown)
	}
}

// TestStallWatchUnrelatedKeysDontDisableIt mirrors the refinery regression: a
// [stall_watch] section that sets only a threshold must leave the default
// enabled=true intact (the enabledSet flag distinguishes unset from false).
func TestStallWatchUnrelatedKeysDontDisableIt(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[stall_watch]
nudge_cooldown = "2m"
`), 0644)

	cfg := Load()
	if !cfg.StallWatch.Enabled {
		t.Error("stall watch should remain enabled when only nudge_cooldown is set")
	}
	if cfg.StallWatch.NudgeCooldown != 2*time.Minute {
		t.Errorf("cooldown = %v, want 2m", cfg.StallWatch.NudgeCooldown)
	}
}

func TestParseStringArray(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{`["a", "b", "c"]`, []string{"a", "b", "c"}},
		{`[]`, nil},
		{`["only"]`, []string{"only"}},
		{`[ "spaced" , "out" ]`, []string{"spaced", "out"}},
	}
	for _, c := range cases {
		got := parseStringArray(c.input)
		if len(got) != len(c.want) {
			t.Errorf("parseStringArray(%q) = %v, want %v", c.input, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("parseStringArray(%q)[%d] = %q, want %q", c.input, i, got[i], c.want[i])
			}
		}
	}
}

// TestPogoHomeFromEnv verifies POGO_HOME takes precedence, so the singleton
// lockfile resolves to the same directory across the launchd domain, shells,
// and agents (#22).
func TestPogoHomeFromEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("POGO_HOME", home)
	if got := PogoHome(); got != home {
		t.Errorf("PogoHome() = %q, want %q", got, home)
	}
	if got, want := LockfilePath(), filepath.Join(home, "pogo.pid"); got != want {
		t.Errorf("LockfilePath() = %q, want %q", got, want)
	}
}

// TestPogoHomeDefaultNotTempDir verifies the fallback (POGO_HOME unset) is
// ~/.pogo and, critically, never os.TempDir — a TempDir-based path differs
// between the launchd domain and a shell/agent and would not share the lock.
func TestPogoHomeDefaultNotTempDir(t *testing.T) {
	t.Setenv("POGO_HOME", "")
	got := PogoHome()
	if home, err := os.UserHomeDir(); err == nil {
		if want := filepath.Join(home, ".pogo"); got != want {
			t.Errorf("PogoHome() = %q, want %q", got, want)
		}
	}
	if strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("PogoHome() = %q must not be under TempDir %q", got, os.TempDir())
	}
	if strings.HasPrefix(LockfilePath(), os.TempDir()) {
		t.Errorf("LockfilePath() = %q must not be under TempDir %q", LockfilePath(), os.TempDir())
	}
}

func TestDialAddr(t *testing.T) {
	cfg := &Config{Port: 10000}
	if got := cfg.DialAddr(); got != "127.0.0.1:10000" {
		t.Errorf("DialAddr() = %q, want 127.0.0.1:10000", got)
	}
}

func TestCoordinatorDefault(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	cfg := Load()
	if cfg.Agents.Coordinator != DefaultCoordinator {
		t.Errorf("coordinator = %q, want %q", cfg.Agents.Coordinator, DefaultCoordinator)
	}
	if got := cfg.Agents.CoordinatorName(); got != "mayor" {
		t.Errorf("CoordinatorName() = %q, want mayor", got)
	}
}

func TestCoordinatorConfigFile(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[agents]
coordinator = "boss"
`), 0644)

	cfg := Load()
	if cfg.Agents.Coordinator != "boss" {
		t.Errorf("coordinator = %q, want boss", cfg.Agents.Coordinator)
	}
	// The stall watcher follows the coordinator when [stall_watch] agent is unset.
	if cfg.StallWatch.Agent != "boss" {
		t.Errorf("stall watch agent = %q, want boss", cfg.StallWatch.Agent)
	}
}

func TestCoordinatorExplicitStallWatchAgentWins(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", dir)
	defer os.Unsetenv("XDG_CONFIG_HOME")

	pogoDir := filepath.Join(dir, "pogo")
	os.MkdirAll(pogoDir, 0755)
	os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[agents]
coordinator = "boss"

[stall_watch]
agent = "watched-elsewhere"
`), 0644)

	cfg := Load()
	if cfg.StallWatch.Agent != "watched-elsewhere" {
		t.Errorf("stall watch agent = %q, want watched-elsewhere", cfg.StallWatch.Agent)
	}
}

func TestCoordinatorNameZeroValue(t *testing.T) {
	var cfg AgentsConfig
	if got := cfg.CoordinatorName(); got != DefaultCoordinator {
		t.Errorf("CoordinatorName() = %q, want %q", got, DefaultCoordinator)
	}
	var nilCfg *AgentsConfig
	if got := nilCfg.CoordinatorName(); got != DefaultCoordinator {
		t.Errorf("nil CoordinatorName() = %q, want %q", got, DefaultCoordinator)
	}
}
