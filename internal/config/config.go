package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPort = 10000
	DefaultBind = "127.0.0.1"
)

// RunMode represents the operating mode of the pogo daemon.
type RunMode int

const (
	// ModeFull means everything is running: agents, refinery, indexing, HTTP.
	ModeFull RunMode = iota
	// ModeIndexOnly means only project indexing, search, and HTTP are running.
	// Agents and refinery are stopped.
	ModeIndexOnly
)

// String returns the human-readable name of the run mode.
func (m RunMode) String() string {
	switch m {
	case ModeFull:
		return "full"
	case ModeIndexOnly:
		return "index-only"
	default:
		return "unknown"
	}
}

// DefaultProvider is the agent harness provider used when none is configured.
// Keeping this "claude" means existing deployments work with no config change.
// The provider supplies the default agent command template; see
// internal/agent/provider.go and internal/claude/provider.go.
const DefaultProvider = "claude"

// DefaultMaxWatchers is the default cap on watched project roots.
//
// On darwin the watch backend is FSEvents (one recursive stream per tree, see
// internal/watch), so the unit watched is the tree and this cap finally bounds
// the right unit. Before mg-d205 the kqueue backend consumed one fd per file
// inside every watched directory, so the cap bounded directories rather than
// fds and could not prevent FD exhaustion. The default is a generous sanity
// backstop, not a tuning knob.
const DefaultMaxWatchers = 4096

// DefaultMaxFilesPerTree is the default per-tree file-count ceiling. A tree
// with more files than this is registered but marked skipped-too-large: it is
// not deep-walked. This bounds index cost (building the search index is
// O(files)) and catches pathological generated-data directories that no
// exclude list anticipated. See mg-d205.
const DefaultMaxFilesPerTree = 25000

// DefaultIndexInterval is how often the timer-driven incremental indexer
// re-walks every registered project. The re-index is incremental — a
// no-change tick costs one Lstat per file — so the interval only bounds how
// long a file change can take to surface in search results. Two minutes is a
// comfortable default given every index consumer is request-driven. See
// docs/indexing-strategy.md and mg-5b0d.
const DefaultIndexInterval = 2 * time.Minute

// DefaultGitGCInterval is how often pogod runs the polecat git garbage
// collector (stale `polecat-*` branch + leaked worktree cleanup). Hourly is
// deliberately conservative: the GC is a backstop for the per-exit cleanup,
// not a hot path. See internal/gitgc and mg-30d5.
const DefaultGitGCInterval = time.Hour

// Config holds pogo daemon configuration.
type Config struct {
	Port            int
	Bind            string
	MaxWatchers     int
	MaxFilesPerTree int
	// IndexInterval is how often the timer-driven incremental indexer
	// re-walks every registered project. Zero falls back to
	// DefaultIndexInterval. See docs/indexing-strategy.md.
	IndexInterval time.Duration
	// IndexRoots, when non-empty, restricts auto-registration to git repos
	// under one of these paths (opt-in strict mode). Empty means the default
	// zero-config behavior: any visited git repo may be auto-registered,
	// bounded by MaxFilesPerTree and the default-exclude patterns.
	IndexRoots []string
	Refinery   RefineryConfig
	Agents     AgentsConfig
	Heartbeat  HeartbeatConfig
	GitGC      GitGCConfig
}

// GitGCConfig configures pogod's periodic polecat git garbage collector.
// It deletes stale `polecat-*` branches and reclaims leaked worktrees once
// their work items have concluded. See internal/gitgc.
type GitGCConfig struct {
	// Enabled turns on the startup sweep and the periodic ticker.
	// Defaults to true.
	Enabled bool
	// Interval between periodic sweeps. Zero falls back to
	// DefaultGitGCInterval.
	Interval time.Duration
	// Repos lists git repositories to sweep. pogod also sweeps the source
	// repo of every registered agent, so this is mainly needed so the
	// startup sweep can reach a repo after a pogod crash that left no live
	// agents behind.
	Repos []string
}

// HeartbeatConfig configures pogod's clock-jump detector. Zero values fall
// back to internal/heartbeat defaults (30s tick, 60s jump threshold).
type HeartbeatConfig struct {
	Interval      time.Duration
	JumpThreshold time.Duration
}

// AgentsConfig holds agent command configuration.
type AgentsConfig struct {
	// Provider selects the agent harness ("claude", and in future "codex" /
	// "gemini"). Resolved by cmd/pogod to an agent.Provider. Empty is treated
	// as DefaultProvider; Load() fills it in.
	Provider string
	// Command is the default command template for all agent types. When empty,
	// the active provider's CommandTemplate is used instead.
	// Supports Go template variables: {{.PromptFile}}, {{.AgentName}}, {{.AgentType}}, {{.WorkDir}}
	Command string
	// Crew overrides the command template for crew agents.
	Crew AgentTypeConfig
	// Polecat overrides the command template for polecat agents.
	Polecat AgentTypeConfig
}

// AgentTypeConfig holds per-agent-type spawn configuration.
type AgentTypeConfig struct {
	// Command overrides the command template for this agent type. Empty means
	// inherit the global [agents] command (or the provider default).
	Command string
	// Provider overrides the harness provider ("claude", "codex") for this
	// agent type. Empty means inherit the global [agents] provider. This is
	// what lets a mixed fleet run — e.g. [agents.polecat] provider = "codex"
	// while crew agents stay on Claude. See mg-b31b.
	Provider string
}

// AgentCommand returns the explicitly-configured command template for a given
// agent type, or "" when none is set. An empty result is the signal for the
// caller (agent.Registry) to fall back to the active provider's default
// CommandTemplate. Precedence: per-type override > global [agents] command
// (which POGO_AGENT_COMMAND also feeds via Load).
func (c *AgentsConfig) AgentCommand(agentType string) string {
	switch agentType {
	case "crew":
		if c.Crew.Command != "" {
			return c.Crew.Command
		}
	case "polecat":
		if c.Polecat.Command != "" {
			return c.Polecat.Command
		}
	}
	return c.Command
}

// AgentProvider returns the configured harness provider id for a given agent
// type. Precedence: per-type [agents.<type>] provider > global [agents]
// provider. The global value is non-empty after Load() (it defaults to
// DefaultProvider), so a "crew" or "polecat" argument always yields a usable
// id. Mirrors AgentCommand; see mg-b31b for the mixed-fleet rationale.
func (c *AgentsConfig) AgentProvider(agentType string) string {
	switch agentType {
	case "crew":
		if c.Crew.Provider != "" {
			return c.Crew.Provider
		}
	case "polecat":
		if c.Polecat.Provider != "" {
			return c.Polecat.Provider
		}
	}
	return c.Provider
}

// RefineryConfig holds merge queue configuration.
type RefineryConfig struct {
	Enabled      bool
	PollInterval time.Duration
}

// parsedConfig is the intermediate result of reading config.toml.
// It tracks which fields were explicitly set so Load() can distinguish
// "unset" from "set to a zero value" (e.g. enabled = false).
type parsedConfig struct {
	Config
	refineryEnabledSet bool
	gitgcEnabledSet    bool
}

// Load reads configuration from (in priority order):
//  1. POGO_PORT environment variable
//  2. ~/.config/pogo/config.toml [server] port field
//  3. Default (10000)
func Load() *Config {
	cfg := &Config{
		Port:            DefaultPort,
		Bind:            DefaultBind,
		MaxWatchers:     DefaultMaxWatchers,
		MaxFilesPerTree: DefaultMaxFilesPerTree,
		IndexInterval:   DefaultIndexInterval,
		Refinery: RefineryConfig{
			Enabled:      true,
			PollInterval: 30 * time.Second,
		},
		GitGC: GitGCConfig{
			Enabled:  true,
			Interval: DefaultGitGCInterval,
		},
	}

	// Try config file first (lowest priority, overridden by env)
	if fileCfg, err := loadConfigFile(); err == nil {
		if fileCfg.Port != 0 {
			cfg.Port = fileCfg.Port
		}
		if fileCfg.Bind != "" {
			cfg.Bind = fileCfg.Bind
		}
		if fileCfg.MaxWatchers > 0 {
			cfg.MaxWatchers = fileCfg.MaxWatchers
		}
		if fileCfg.MaxFilesPerTree > 0 {
			cfg.MaxFilesPerTree = fileCfg.MaxFilesPerTree
		}
		if fileCfg.IndexInterval > 0 {
			cfg.IndexInterval = fileCfg.IndexInterval
		}
		if len(fileCfg.IndexRoots) > 0 {
			cfg.IndexRoots = fileCfg.IndexRoots
		}
		cfg.Agents = fileCfg.Agents
		if fileCfg.refineryEnabledSet {
			cfg.Refinery.Enabled = fileCfg.Refinery.Enabled
		}
		if fileCfg.Refinery.PollInterval > 0 {
			cfg.Refinery.PollInterval = fileCfg.Refinery.PollInterval
		}
		if fileCfg.Heartbeat.Interval > 0 {
			cfg.Heartbeat.Interval = fileCfg.Heartbeat.Interval
		}
		if fileCfg.Heartbeat.JumpThreshold > 0 {
			cfg.Heartbeat.JumpThreshold = fileCfg.Heartbeat.JumpThreshold
		}
		if fileCfg.gitgcEnabledSet {
			cfg.GitGC.Enabled = fileCfg.GitGC.Enabled
		}
		if fileCfg.GitGC.Interval > 0 {
			cfg.GitGC.Interval = fileCfg.GitGC.Interval
		}
		if len(fileCfg.GitGC.Repos) > 0 {
			cfg.GitGC.Repos = fileCfg.GitGC.Repos
		}
	}

	// Environment variables override config file
	if portStr := os.Getenv("POGO_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			cfg.Port = port
		}
	}
	if bind := os.Getenv("POGO_BIND"); bind != "" {
		cfg.Bind = bind
	}
	if mwStr := os.Getenv("POGO_MAX_WATCHERS"); mwStr != "" {
		if mw, err := strconv.Atoi(mwStr); err == nil && mw > 0 {
			cfg.MaxWatchers = mw
		}
	}
	if mfStr := os.Getenv("POGO_MAX_FILES_PER_TREE"); mfStr != "" {
		if mf, err := strconv.Atoi(mfStr); err == nil && mf > 0 {
			cfg.MaxFilesPerTree = mf
		}
	}

	// POGO_AGENT_COMMAND overrides the default agent command from config file
	if agentCmd := os.Getenv("POGO_AGENT_COMMAND"); agentCmd != "" {
		cfg.Agents.Command = agentCmd
	}

	// POGO_AGENT_PROVIDER overrides the [agents] provider from the config file.
	if provider := os.Getenv("POGO_AGENT_PROVIDER"); provider != "" {
		cfg.Agents.Provider = provider
	}
	// Default the provider so existing deployments work with no config change.
	if cfg.Agents.Provider == "" {
		cfg.Agents.Provider = DefaultProvider
	}

	return cfg
}

// ServerURL returns the base URL for connecting to the pogo daemon.
func (c *Config) ServerURL() string {
	return fmt.Sprintf("http://localhost:%d", c.Port)
}

// ListenAddr returns the address string for the server to listen on.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Bind, c.Port)
}

// ConfigDir returns the pogo configuration directory path.
func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "pogo")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "pogo")
}

// ConfigFilePath returns the path to the pogo config file.
func ConfigFilePath() string {
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// loadConfigFile reads port from the TOML config file.
// Only parses the minimal subset needed: [server] section with port key.
func loadConfigFile() (*parsedConfig, error) {
	path := ConfigFilePath()
	if path == "" {
		return nil, fmt.Errorf("no config path")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &parsedConfig{}
	currentSection := ""
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section headers
		if strings.HasPrefix(line, "[") {
			currentSection = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Strip surrounding quotes from values
		unquotedVal := strings.Trim(val, "\"")

		switch currentSection {
		case "server":
			switch key {
			case "port":
				if port, err := strconv.Atoi(val); err == nil && port > 0 && port <= 65535 {
					cfg.Port = port
				}
			case "bind":
				cfg.Bind = val
			}
		case "refinery":
			switch key {
			case "enabled":
				cfg.Refinery.Enabled = val == "true"
				cfg.refineryEnabledSet = true
			case "poll_interval":
				val = strings.Trim(val, "\"")
				if d, err := time.ParseDuration(val); err == nil {
					cfg.Refinery.PollInterval = d
				}
			}
		case "search":
			switch key {
			case "max_watchers":
				if mw, err := strconv.Atoi(val); err == nil && mw > 0 {
					cfg.MaxWatchers = mw
				}
			case "max_files_per_tree":
				if mf, err := strconv.Atoi(val); err == nil && mf > 0 {
					cfg.MaxFilesPerTree = mf
				}
			case "index_interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.IndexInterval = d
				}
			case "index_roots":
				cfg.IndexRoots = parseStringArray(val)
			}
		case "heartbeat":
			switch key {
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.Heartbeat.Interval = d
				}
			case "jump_threshold":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.Heartbeat.JumpThreshold = d
				}
			}
		case "gitgc":
			switch key {
			case "enabled":
				cfg.GitGC.Enabled = val == "true"
				cfg.gitgcEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.GitGC.Interval = d
				}
			case "repos":
				cfg.GitGC.Repos = parseStringArray(val)
			}
		case "agents":
			switch key {
			case "command":
				cfg.Agents.Command = unquotedVal
			case "provider":
				cfg.Agents.Provider = unquotedVal
			}
		case "agents.crew":
			switch key {
			case "command":
				cfg.Agents.Crew.Command = unquotedVal
			case "provider":
				cfg.Agents.Crew.Provider = unquotedVal
			}
		case "agents.polecat":
			switch key {
			case "command":
				cfg.Agents.Polecat.Command = unquotedVal
			case "provider":
				cfg.Agents.Polecat.Provider = unquotedVal
			}
		}
	}

	return cfg, scanner.Err()
}

// parseStringArray parses a minimal single-line TOML string array,
// e.g. `["/home/user/dev", "/work"]`, into a slice. Empty/blank entries are
// dropped. This is intentionally simple — it does not handle multi-line arrays.
func parseStringArray(val string) []string {
	val = strings.TrimSpace(val)
	val = strings.TrimPrefix(val, "[")
	val = strings.TrimSuffix(val, "]")
	var out []string
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "\"'")
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
