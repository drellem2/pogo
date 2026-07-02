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

// DefaultCoordinator is the coordinator agent's name used when [agents]
// coordinator is not configured. Keeping this "mayor" means existing
// deployments work with no config change. The name is policy, not mechanism:
// it decides the coordinator's agent name (and therefore its mg mailbox and
// schedule ids) and what prompts call the role; the coordinator's prompt file
// stays at ~/.pogo/agents/mayor.md regardless. See mg-71ea.
const DefaultCoordinator = "mayor"

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
// docs/design/indexing-strategy.md and mg-5b0d.
const DefaultIndexInterval = 2 * time.Minute

// DefaultGitGCInterval is how often pogod runs the polecat git garbage
// collector (stale `polecat-*` branch + leaked worktree cleanup). Hourly is
// deliberately conservative: the GC is a backstop for the per-exit cleanup,
// not a hot path. See internal/gitgc and mg-30d5.
const DefaultGitGCInterval = time.Hour

// Stall-watch defaults. The stall watcher is the pogod-side third leg of the
// wedge-response triad (gh drellem2/macguffin #12): it rides pogod's heartbeat
// loop and nudges the mayor when work piles up behaviorally (process healthy
// but items unclaimed / mail unread). Because it runs in pogod's
// guaranteed-independent heartbeat — not in the mayor's own loop — it catches
// the one failure mode an Ocean-side watcher can't: the mayor's loop silently
// dropping its check-work / check-mail steps. See internal/stallwatch and
// docs/design/stall-watch-design.md.
const (
	// DefaultStallWatchAgent is the agent the watcher monitors. Only the
	// coordinator is in scope today (it is the sole behavioral-stall target),
	// but the name is configurable so a deployment can point it elsewhere.
	// When [stall_watch] agent is unset, Load() resolves it to the configured
	// [agents] coordinator, so a renamed coordinator is watched under its
	// configured name without extra config.
	DefaultStallWatchAgent = DefaultCoordinator
	// DefaultUnclaimedItemAgeThreshold is how long an available work item
	// assigned to (or pickup-expected by) the watched agent may sit before the
	// watcher nudges. Mirrors the gh #12 spec's 600s.
	DefaultUnclaimedItemAgeThreshold = 10 * time.Minute
	// DefaultUnreadMailAgeThreshold is how old a message in the watched agent's
	// new/ maildir may get before the watcher nudges. Mirrors gh #12's 600s.
	DefaultUnreadMailAgeThreshold = 10 * time.Minute
	// DefaultMaxUnreadMailCount is the unread-count ceiling above which the
	// watcher nudges regardless of message age. Mirrors gh #12's 5.
	DefaultMaxUnreadMailCount = 5
	// DefaultStallNudgeCooldown is the minimum gap between two nudges for the
	// same threshold category, so a persistent backlog produces one nudge per
	// cooldown rather than one per heartbeat tick. Mirrors gh #12's 300s.
	DefaultStallNudgeCooldown = 5 * time.Minute
)

// Config holds pogo daemon configuration.
type Config struct {
	Port            int
	Bind            string
	MaxFilesPerTree int
	// IndexInterval is how often the timer-driven incremental indexer
	// re-walks every registered project. Zero falls back to
	// DefaultIndexInterval. See docs/design/indexing-strategy.md.
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
	StallWatch StallWatchConfig
}

// StallWatchConfig configures pogod's passive stall watcher, which rides the
// heartbeat loop and nudges the watched agent (the mayor) when work piles up.
// See internal/stallwatch and docs/design/stall-watch-design.md.
//
// Note on shape: gh drellem2/macguffin #12 sketched this as a nested JSON
// stall_watch.agents.mayor.* block. pogo's config is flat single-line TOML
// (parsed by loadConfigFile), and the mayor is the only behavioral-stall
// target, so this is implemented as a single flat [stall_watch] section with a
// configurable `agent` key rather than a per-agent map. The thresholds carry
// the same meaning as the spec's *_seconds fields, expressed as Go durations.
type StallWatchConfig struct {
	// Enabled turns the watcher on. Defaults to true.
	Enabled bool
	// Agent is the macguffin agent name to watch. Empty falls back to
	// DefaultStallWatchAgent ("mayor").
	Agent string
	// UnclaimedItemAgeThreshold is how long an available work item assigned to
	// (or unassigned and pickup-expected by) Agent may sit before a nudge.
	// Zero falls back to DefaultUnclaimedItemAgeThreshold.
	UnclaimedItemAgeThreshold time.Duration
	// UnreadMailAgeThreshold is how old a message in Agent's new/ maildir may
	// get before a nudge. Zero falls back to DefaultUnreadMailAgeThreshold.
	UnreadMailAgeThreshold time.Duration
	// MaxUnreadMailCount is the unread-count ceiling above which a nudge fires
	// regardless of age. Zero falls back to DefaultMaxUnreadMailCount.
	MaxUnreadMailCount int
	// NudgeCooldown is the minimum gap between two nudges for the same
	// threshold category. Zero falls back to DefaultStallNudgeCooldown.
	NudgeCooldown time.Duration
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
	// Provider selects the agent harness ("claude", "codex", "pi"). Resolved
	// by cmd/pogod to an agent.Provider. Empty is treated as DefaultProvider;
	// Load() fills it in.
	Provider string
	// Coordinator is the coordinator agent's name ([agents] coordinator).
	// Empty is treated as DefaultCoordinator ("mayor"); Load() fills it in.
	// Prefer CoordinatorName() over reading the field so zero-value configs
	// (tests, callers that skip Load) still resolve to the default.
	Coordinator string
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
	// Provider overrides the harness provider ("claude", "codex", "pi") for
	// this agent type. Empty means inherit the global [agents] provider. This
	// is what lets a mixed fleet run — e.g. [agents.polecat] provider = "pi"
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

// CoordinatorName returns the configured coordinator agent name, falling back
// to DefaultCoordinator ("mayor") when unset. Safe on a zero-value AgentsConfig.
func (c *AgentsConfig) CoordinatorName() string {
	if c != nil && c.Coordinator != "" {
		return c.Coordinator
	}
	return DefaultCoordinator
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
	refineryEnabledSet   bool
	gitgcEnabledSet      bool
	stallWatchEnabledSet bool
}

// Load reads configuration from (in priority order):
//  1. POGO_PORT environment variable
//  2. ~/.config/pogo/config.toml [server] port field
//  3. Default (10000)
func Load() *Config {
	cfg := &Config{
		Port:            DefaultPort,
		Bind:            DefaultBind,
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
		StallWatch: StallWatchConfig{
			Enabled: true,
			// Agent is resolved at the end of Load: explicit [stall_watch]
			// agent wins, otherwise it follows the [agents] coordinator.
			UnclaimedItemAgeThreshold: DefaultUnclaimedItemAgeThreshold,
			UnreadMailAgeThreshold:    DefaultUnreadMailAgeThreshold,
			MaxUnreadMailCount:        DefaultMaxUnreadMailCount,
			NudgeCooldown:             DefaultStallNudgeCooldown,
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
		if fileCfg.stallWatchEnabledSet {
			cfg.StallWatch.Enabled = fileCfg.StallWatch.Enabled
		}
		if fileCfg.StallWatch.Agent != "" {
			cfg.StallWatch.Agent = fileCfg.StallWatch.Agent
		}
		if fileCfg.StallWatch.UnclaimedItemAgeThreshold > 0 {
			cfg.StallWatch.UnclaimedItemAgeThreshold = fileCfg.StallWatch.UnclaimedItemAgeThreshold
		}
		if fileCfg.StallWatch.UnreadMailAgeThreshold > 0 {
			cfg.StallWatch.UnreadMailAgeThreshold = fileCfg.StallWatch.UnreadMailAgeThreshold
		}
		if fileCfg.StallWatch.MaxUnreadMailCount > 0 {
			cfg.StallWatch.MaxUnreadMailCount = fileCfg.StallWatch.MaxUnreadMailCount
		}
		if fileCfg.StallWatch.NudgeCooldown > 0 {
			cfg.StallWatch.NudgeCooldown = fileCfg.StallWatch.NudgeCooldown
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

	// Default the coordinator name so existing deployments work with no
	// config change, then let the stall watcher follow it unless an explicit
	// [stall_watch] agent was configured.
	if cfg.Agents.Coordinator == "" {
		cfg.Agents.Coordinator = DefaultCoordinator
	}
	if cfg.StallWatch.Agent == "" {
		cfg.StallWatch.Agent = cfg.Agents.Coordinator
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

// PogoHome returns the pogo state directory: $POGO_HOME, or ~/.pogo when
// unset. It deliberately never falls back to os.TempDir(): $TMPDIR differs
// between the launchd domain and an interactive shell/agent, so a
// TempDir-based path is not shared across domains. The singleton daemon
// lockfile (see LockfilePath) must resolve to the SAME path from launchd,
// shells, and agents, otherwise a second pogod acquires its own lock and
// displaces the running daemon (the :10000 race in #22). This mirrors the
// existing POGO_HOME resolution in internal/service, internal/project, and
// internal/driver.
func PogoHome() string {
	if h := os.Getenv("POGO_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pogo")
}

// LockfilePath returns the deterministic path of the pogod singleton lockfile
// (pogo.pid) under PogoHome. Because PogoHome is domain-independent, the lock
// is shared across the launchd-managed pogod, shells, and agents, so a second
// pogod's TryLock fails instead of racing the live daemon for :10000 (#22).
func LockfilePath() string {
	return filepath.Join(PogoHome(), "pogo.pid")
}

// DialAddr returns a loopback TCP address (127.0.0.1:<port>) for probing
// whether a pogod is already bound to the daemon port. It targets 127.0.0.1
// explicitly rather than the raw Bind so a wildcard bind (0.0.0.0/::) is still
// probed on a concrete loopback address, and so the probe never races
// IPv6-vs-IPv4 resolution of "localhost". Callers use this to avoid spawning a
// rival pogod when the port is already held (#22).
func (c *Config) DialAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", c.Port)
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
		case "stall_watch":
			switch key {
			case "enabled":
				cfg.StallWatch.Enabled = val == "true"
				cfg.stallWatchEnabledSet = true
			case "agent":
				cfg.StallWatch.Agent = unquotedVal
			case "unclaimed_item_age_threshold":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.UnclaimedItemAgeThreshold = d
				}
			case "unread_mail_age_threshold":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.UnreadMailAgeThreshold = d
				}
			case "max_unread_mail_count":
				if n, err := strconv.Atoi(unquotedVal); err == nil && n > 0 {
					cfg.StallWatch.MaxUnreadMailCount = n
				}
			case "nudge_cooldown":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.NudgeCooldown = d
				}
			}
		case "agents":
			switch key {
			case "command":
				cfg.Agents.Command = unquotedVal
			case "provider":
				cfg.Agents.Provider = unquotedVal
			case "coordinator":
				cfg.Agents.Coordinator = unquotedVal
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
