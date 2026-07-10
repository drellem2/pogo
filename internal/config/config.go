package config

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
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
// coordinator is not configured. The name is policy, not mechanism: it decides
// the coordinator's agent name (and therefore its mg mailbox and schedule ids)
// and what prompts call the role. Existing installs are unaffected by a change
// to this value: the default-migration guard (see migrate.go) pins their
// historical coordinator name into config.toml, so this flip only sets the
// default for fresh installs. See mg-71ea, mg-ce47.
const DefaultCoordinator = "ringmaster"

// DefaultWorker (the worker role's display-name default, "pogocat") is
// declared in migrate.go alongside the role-default migration table that
// consumes it. The worker seam here — AgentsConfig.Worker, WorkerName(), the
// "worker" config key, and Load() defaulting — references it. See mg-ccec
// (design mg-6a24 §1.4).

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

// DefaultReaperInterval is how often the tier-1 heartbeat reaper sweeps its
// job list when [reaper] interval is unset.
const DefaultReaperInterval = 60 * time.Second

// DefaultReaperMaxKickstarts caps consecutive kickstarts of one job before the
// reaper gives up and escalates, when [reaper] max_kickstarts is unset.
const DefaultReaperMaxKickstarts = 3

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
	// DefaultHighPriorityWakeDelay is the minimum age a high-priority available
	// item must reach before the priority wake fires. Small enough to feel
	// immediate versus the old up-to-30-min idle-poll gap, large enough to let
	// a burst of enqueues settle so a batch produces one nudge rather than one
	// per item. See the priority-wake half of gh drellem2/pogo #61.
	DefaultHighPriorityWakeDelay = 30 * time.Second
	// DefaultHighPriorityWakeCooldown is the minimum gap between two
	// priority-wake nudges. It is deliberately shorter than the standard stall
	// cooldown (urgent work should recover fast) but long enough that a
	// high-priority item which stays available — e.g. the coordinator can't
	// dispatch it yet — does not re-nudge every heartbeat tick.
	DefaultHighPriorityWakeCooldown = 3 * time.Minute
)

// DefaultFastPriorities is the set of WorkItem.Priority values that trigger the
// priority wake. Just "high" today; extend it (e.g. add "critical") if the
// priority vocabulary grows. Kept as a var because a slice cannot be a const;
// treat it as read-only.
var DefaultFastPriorities = []string{"high"}

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
	Reaper     ReaperConfig
	Reconcile  ReconcileConfig
	// Source is the path of the highest-precedence config file Load read, or
	// "" when no config file was found and everything is defaults + env. pogod
	// uses this to gate crew auto-start: a daemon with no config file is
	// treated as an unconfigured/isolated instance and must not spawn agents
	// (mg-3dc3). When two layers exist, the values in the Config come from
	// both — see Sources.
	Source string
	// Sources lists every config file Load actually read, lowest precedence
	// first (~/.config/pogo/config.toml, then $POGO_HOME/config.toml). Empty
	// when no config file was found. Source is the last entry.
	Sources []string
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
	// DefaultStallWatchAgent ("ringmaster").
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

	// PriorityWakeEnabled turns on the priority-aware fast wake (gh
	// drellem2/pogo #61): a ready, watched, high-priority available item
	// bypasses UnclaimedItemAgeThreshold and is delivered promptly via the same
	// wait-idle nudge, so urgent work no longer waits out the idle-coordinator
	// polling gap. Because New() cannot distinguish an unset bool from an
	// explicit false, the production default (true) is applied by Load(), not
	// New(); a hand-built config must set this field to activate the wake.
	PriorityWakeEnabled bool
	// HighPriorityWakeDelay is the minimum age a high-priority available item
	// must reach before the priority wake fires (bypassing
	// UnclaimedItemAgeThreshold). Zero falls back to
	// DefaultHighPriorityWakeDelay.
	HighPriorityWakeDelay time.Duration
	// HighPriorityWakeCooldown is the minimum gap between two priority-wake
	// nudges — a dedicated cooldown so a high-priority item that stays available
	// does not re-nudge every tick. Zero falls back to
	// DefaultHighPriorityWakeCooldown.
	HighPriorityWakeCooldown time.Duration
	// FastPriorities lists the WorkItem.Priority values that trigger the
	// priority wake. Empty falls back to DefaultFastPriorities (["high"]).
	FastPriorities []string
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

// ReaperConfig configures pogod's tier-1 heartbeat reaper, which kickstarts
// declared launchd jobs whose heartbeat state file has gone stale. Liveness is
// heartbeat freshness, never process existence. See internal/reaper and
// docs/design/reaper-design.md.
type ReaperConfig struct {
	// Enabled turns the reaper loop on. Defaults to true; with no Jobs it is a
	// logged no-op.
	Enabled bool
	// Interval between sweeps. Zero falls back to DefaultReaperInterval.
	Interval time.Duration
	// MaxKickstarts caps consecutive kickstarts of one job before the reaper
	// gives up and escalates. Zero falls back to DefaultReaperMaxKickstarts.
	MaxKickstarts int
	// Jobs is the declared job list. Each entry is a single line of the form
	//   "<launchd-label>|<heartbeat-path>|<period>"
	// e.g. "com.pogo.watchdog|~/.pogo/health/watchdog.heartbeat|5m". A leading
	// ~ in the path is expanded to the user's home directory. period is a Go
	// duration. Malformed entries are dropped (and reported) at load.
	Jobs []ReaperJob
}

// ReaperJob is one parsed [reaper] jobs entry.
type ReaperJob struct {
	Label     string
	Heartbeat string
	Period    time.Duration
}

// ReconcileConfig declares the host-side artifacts that `pogo service
// reconcile` and `pogo service check-drift` manage (mg-be0c). Each mirror is a
// COPY of a generator/repo source — never a symlink into a checkout — so the
// repo/host boundary is preserved and drift is detectable. See
// internal/reconcile.
type ReconcileConfig struct {
	// Mirrors is the declared mirror list. Each entry is a single line of the
	// form
	//   "<name>|<source>|<target>[|<launchd-label>]"
	// e.g. "watchdog|~/dev/pogo-reminders/bin/watchdog.sh|~/.pogo/pogo-reminders/bin/watchdog.sh|com.pogo.watchdog".
	// A leading ~ in either path is expanded to the user's home directory. The
	// label is optional: omit it for a file that is not a running launchd job.
	// Malformed entries are dropped (and reported) at load.
	Mirrors []ReconcileMirror
}

// ReconcileMirror is one parsed [reconcile] mirrors entry.
type ReconcileMirror struct {
	Name   string
	Source string
	Target string
	Label  string
}

// AgentsConfig holds agent command configuration.
type AgentsConfig struct {
	// Provider selects the agent harness ("claude", "codex", "pi", "cursor"). Resolved
	// by cmd/pogod to an agent.Provider. Empty is treated as DefaultProvider;
	// Load() fills it in.
	Provider string
	// Coordinator is the coordinator agent's name ([agents] coordinator).
	// Empty is treated as DefaultCoordinator ("ringmaster"); Load() fills it in.
	// Prefer CoordinatorName() over reading the field so zero-value configs
	// (tests, callers that skip Load) still resolve to the default.
	Coordinator string
	// Worker is the worker role's display name ([agents] worker). Empty is
	// treated as DefaultWorker ("pogocat"); Load() fills it in. Prefer
	// WorkerName() over reading the field so zero-value configs still resolve
	// to the default. Display-only — it never renames an identifier.
	Worker string
	// AutoStart globally gates crew auto-start at pogod boot ([agents]
	// autostart). Defaults to true. Setting it false keeps a *configured*
	// daemon from spawning any crew agents, regardless of per-prompt
	// auto_start frontmatter — the switch for sandboxes and tests that need
	// a config file (e.g. for an [agents] command override) but no fleet
	// (mg-9a1c). Complements the mg-3dc3 gate, which only covers daemons
	// with no config file at all. POGO_AGENT_AUTOSTART overrides. Note: the
	// zero value is false — read this via a Load()ed Config, not a
	// hand-built AgentsConfig.
	AutoStart bool
	// Command is the default command template for all agent types. When empty,
	// the active provider's CommandTemplate is used instead.
	// Supports Go template variables: {{.PromptFile}}, {{.AgentName}}, {{.AgentType}}, {{.WorkDir}}
	Command string
	// ExtraPath lists directories to prepend to pogod's PATH — and therefore
	// to every spawned child's PATH — beyond the automatic repair in
	// internal/pathenv. Use it for harness runtimes in locations the daemon
	// cannot discover on its own (e.g. a nonstandard Node install for pi; see
	// gh #25). Set via [agents] extra_path or POGO_EXTRA_PATH
	// (list-separator-joined, i.e. colon-separated on unix).
	ExtraPath []string
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
	// Provider overrides the harness provider ("claude", "codex", "pi", "cursor") for
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
// to DefaultCoordinator ("ringmaster") when unset. Safe on a zero-value AgentsConfig.
func (c *AgentsConfig) CoordinatorName() string {
	if c != nil && c.Coordinator != "" {
		return c.Coordinator
	}
	return DefaultCoordinator
}

// WorkerName returns the configured worker display name, falling back to
// DefaultWorker ("pogocat") when unset. Safe on a zero-value AgentsConfig.
func (c *AgentsConfig) WorkerName() string {
	if c != nil && c.Worker != "" {
		return c.Worker
	}
	return DefaultWorker
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

// parsedConfig is the intermediate result of reading the config layers.
// It tracks which fields were explicitly set so Load() can distinguish
// "unset" from "set to a zero value" (e.g. enabled = false).
//
// One parsedConfig is filled by every layer in turn (lowest precedence first),
// which is what makes the merge key-by-key: parseConfigFileInto only assigns a
// field when its key appears on a line, so a higher layer overrides exactly the
// keys it names and leaves the rest of the lower layer's values in place.
type parsedConfig struct {
	Config
	refineryEnabledSet     bool
	gitgcEnabledSet        bool
	stallWatchEnabledSet   bool
	priorityWakeEnabledSet bool
	agentsAutoStartSet     bool
	reaperEnabledSet       bool
	// sources are the files that were read, lowest precedence first.
	sources []string
}

// Load reads configuration from (in priority order):
//  1. Environment variables (POGO_PORT, POGO_AGENT_COMMAND, …)
//  2. $POGO_HOME/config.toml, key by key
//  3. ~/.config/pogo/config.toml, key by key
//  4. Compiled-in defaults
//
// The two config files LAYER: a key set in $POGO_HOME/config.toml overrides the
// same key in ~/.config/pogo/config.toml, and every key it does not set keeps
// the ~/.config value. See loadConfigFiles for why whole-file precedence was a
// footgun (mg-cf9e).
func Load() *Config {
	cfg := &Config{
		Port:            DefaultPort,
		Bind:            DefaultBind,
		MaxFilesPerTree: DefaultMaxFilesPerTree,
		IndexInterval:   DefaultIndexInterval,
		Agents: AgentsConfig{
			AutoStart: true,
		},
		Refinery: RefineryConfig{
			Enabled:      true,
			PollInterval: 30 * time.Second,
		},
		GitGC: GitGCConfig{
			Enabled:  true,
			Interval: DefaultGitGCInterval,
		},
		Reaper: ReaperConfig{
			Enabled:       true,
			Interval:      DefaultReaperInterval,
			MaxKickstarts: DefaultReaperMaxKickstarts,
		},
		StallWatch: StallWatchConfig{
			Enabled: true,
			// Agent is resolved at the end of Load: explicit [stall_watch]
			// agent wins, otherwise it follows the [agents] coordinator.
			UnclaimedItemAgeThreshold: DefaultUnclaimedItemAgeThreshold,
			UnreadMailAgeThreshold:    DefaultUnreadMailAgeThreshold,
			MaxUnreadMailCount:        DefaultMaxUnreadMailCount,
			NudgeCooldown:             DefaultStallNudgeCooldown,
			// Priority wake is default-on for the watched coordinator (gh #61).
			PriorityWakeEnabled:      true,
			HighPriorityWakeDelay:    DefaultHighPriorityWakeDelay,
			HighPriorityWakeCooldown: DefaultHighPriorityWakeCooldown,
			FastPriorities:           DefaultFastPriorities,
		},
	}

	// Try config files first (lowest priority, overridden by env)
	if fileCfg, err := loadConfigFiles(); err == nil {
		cfg.Sources = fileCfg.sources
		cfg.Source = fileCfg.sources[len(fileCfg.sources)-1]
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
		if !fileCfg.agentsAutoStartSet {
			// The wholesale Agents copy above clobbers the default; restore
			// it unless the file set [agents] autostart explicitly.
			cfg.Agents.AutoStart = true
		}
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
		if fileCfg.reaperEnabledSet {
			cfg.Reaper.Enabled = fileCfg.Reaper.Enabled
		}
		if fileCfg.Reaper.Interval > 0 {
			cfg.Reaper.Interval = fileCfg.Reaper.Interval
		}
		if fileCfg.Reaper.MaxKickstarts > 0 {
			cfg.Reaper.MaxKickstarts = fileCfg.Reaper.MaxKickstarts
		}
		if len(fileCfg.Reaper.Jobs) > 0 {
			cfg.Reaper.Jobs = fileCfg.Reaper.Jobs
		}
		if len(fileCfg.Reconcile.Mirrors) > 0 {
			cfg.Reconcile.Mirrors = fileCfg.Reconcile.Mirrors
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
		if fileCfg.priorityWakeEnabledSet {
			cfg.StallWatch.PriorityWakeEnabled = fileCfg.StallWatch.PriorityWakeEnabled
		}
		if fileCfg.StallWatch.HighPriorityWakeDelay > 0 {
			cfg.StallWatch.HighPriorityWakeDelay = fileCfg.StallWatch.HighPriorityWakeDelay
		}
		if fileCfg.StallWatch.HighPriorityWakeCooldown > 0 {
			cfg.StallWatch.HighPriorityWakeCooldown = fileCfg.StallWatch.HighPriorityWakeCooldown
		}
		if len(fileCfg.StallWatch.FastPriorities) > 0 {
			cfg.StallWatch.FastPriorities = fileCfg.StallWatch.FastPriorities
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

	// POGO_EXTRA_PATH overrides [agents] extra_path from the config file.
	if extra := os.Getenv("POGO_EXTRA_PATH"); extra != "" {
		cfg.Agents.ExtraPath = filepath.SplitList(extra)
	}

	// POGO_AGENT_AUTOSTART overrides [agents] autostart from the config file.
	if v := os.Getenv("POGO_AGENT_AUTOSTART"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Agents.AutoStart = b
		}
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
	// Default the worker display name so existing deployments work with no
	// config change. Display-only; touches no identifier.
	if cfg.Agents.Worker == "" {
		cfg.Agents.Worker = DefaultWorker
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

// ConfigFilePath returns the path pogo WRITES config to, and the path whose
// existence answers "is this an install with a config file?".
//
// When POGO_HOME is set and $POGO_HOME/config.toml exists, that file wins so
// an isolated daemon (tests, CI) writes its own config instead of the real
// user's (mg-3dc3). Otherwise the XDG path from ConfigDir applies. The
// POGO_HOME probe is existence-gated rather than unconditional so
// deployments that set POGO_HOME but keep config.toml in ~/.config/pogo
// (the historical layout) are unaffected.
//
// It is NOT the whole read path: Load reads every layer ConfigFilePaths
// returns and merges them key by key. Callers that want "where did this value
// come from" should read Config.Sources.
func ConfigFilePath() string {
	if os.Getenv("POGO_HOME") != "" {
		p := filepath.Join(PogoHome(), "config.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.toml")
}

// ConfigFilePaths returns the config file layers Load reads, LOWEST precedence
// first: the XDG file (~/.config/pogo/config.toml), then — when POGO_HOME is
// set — $POGO_HOME/config.toml. Paths are returned whether or not they exist;
// Load skips the missing ones.
//
// The POGO_HOME layer is gated on the env var, not on PogoHome(), so an install
// that never sets POGO_HOME keeps reading exactly one file (~/.pogo/config.toml
// has never been consulted in that case, and starting now would be a surprise).
// A POGO_HOME that resolves onto the XDG directory yields one layer, not two.
func ConfigFilePaths() []string {
	var paths []string
	if dir := ConfigDir(); dir != "" {
		paths = append(paths, filepath.Join(dir, "config.toml"))
	}
	if os.Getenv("POGO_HOME") != "" {
		p := filepath.Join(PogoHome(), "config.toml")
		if len(paths) == 0 || filepath.Clean(paths[0]) != filepath.Clean(p) {
			paths = append(paths, p)
		}
	}
	return paths
}

// PogoHome returns the pogo state directory: $POGO_HOME, or ~/.pogo when
// unset. It deliberately never falls back to os.TempDir(): $TMPDIR differs
// between the launchd domain and an interactive shell/agent, so a
// TempDir-based path is not shared across domains. The singleton daemon
// lockfile (see LockfilePath) must resolve to the SAME path from launchd,
// shells, and agents, otherwise a second pogod acquires its own lock and
// displaces the running daemon (the :10000 race in #22).
//
// Every pogo state path (refinery-state.json, schedules.json, agents/,
// polecats/, events.log, recovery/, projects.json, plugin/) derives from this
// function, so overriding POGO_HOME (or HOME, via the default) fully isolates
// a daemon's state (mg-3dc3).
//
// Legacy normalization: an old shell integration exported POGO_HOME=$HOME
// ("where the dotfiles live"), and that value survives in existing zshrc
// copies and launchd plists. Honoring it literally would scatter agents/,
// refinery-state.json, etc. across the home directory root, so a POGO_HOME
// equal to the user's home dir is normalized to $HOME/.pogo — the documented
// default, and where all of that state already lives on such machines.
func PogoHome() string {
	if h := os.Getenv("POGO_HOME"); h != "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" &&
			filepath.Clean(h) == filepath.Clean(home) {
			return filepath.Join(h, ".pogo")
		}
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

// maxUnixSocketPathLen is the longest bindable AF_UNIX path. sockaddr_un's
// sun_path field is 104 bytes on darwin and 108 on linux, both counting the NUL
// terminator. We budget against the smaller (darwin) figure on every platform so
// that one POGO_HOME resolves to the same socket dir regardless of GOOS.
const maxUnixSocketPathLen = 103

// MaxAgentNameLen is the longest agent name whose attach socket is guaranteed to
// bind under AgentSocketDir. Real names are far shorter — "pm-dealdesk" (11) is
// the longest crew name, and a polecat is named for its work item ("8532") — so
// 24 bytes leaves better than 2x headroom.
//
// The reservation is a fixed constant rather than a function of the agent being
// bound: every agent under one POGO_HOME must agree on one socket dir, so the
// dir cannot depend on which agent binds first.
//
// agent.ValidateAgentName enforces this at spawn, so it is a promise and not
// merely a ceiling: a longer name is refused (HTTP 400) under every POGO_HOME,
// shallow or deep, rather than spawning an agent that runs fine but can never be
// attached to. Enforcing it unconditionally is the point — a name's fate must
// not depend on how deep the operator's root happens to be.
//
// The promise holds only because AgentSocketDir never returns a directory with
// less than agentSocketLeafBudget bytes of headroom, on any root and any TMPDIR;
// TestAgentSocketDirAlwaysFits pins that. Should this arithmetic ever drift from
// the real sun_path limit anyway, agent.Spawn treats a permanent bind failure as
// fatal, so the failure is loud rather than silent either way (mg-ef80).
const MaxAgentNameLen = 24

// agentSocketLeafBudget reserves room for the "/<agent name>.sock" leaf that
// callers append to AgentSocketDir.
const agentSocketLeafBudget = len("/") + MaxAgentNameLen + len(".sock")

// AgentSocketDir returns the directory holding the per-agent unix domain sockets
// that back `pogo agent attach`, and whether that directory lives inside
// PogoHome. Callers that want to report the fallback should use the returned
// bool rather than re-deriving it by inspecting the path: a POGO_HOME of "/"
// makes any prefix test lie.
//
// The directory derives from PogoHome() so two daemons on distinct POGO_HOME
// roots never share a socket path. Deriving it from os.TempDir() instead — as
// pogod did before mg-8532 — gave identically-named agents under different roots
// a single shared socket file, because $TMPDIR is per-user, not per-POGO_HOME.
// The singleton lockfile bars two pogods on the *same* root, but nothing stopped
// two on *different* roots from colliding here. The old symptom was quiet:
// whichever daemon bound last owned the path and the other silently lost attach.
// Once the mg-d216 attach supervisor shipped, it turned loud — each daemon
// observes the other's bind as its own socket being replaced, unlinks that live
// socket and rebinds, forever, on a 30s ticker.
//
// The sun_path limit forces one wrinkle. A sufficiently deep POGO_HOME (a
// t.TempDir() under /var/folders on darwin, say) leaves no room for the socket
// leaf, and bind would fail with EINVAL. Such a root falls back to a short
// directory named for a hash of the root — so the per-root distinctness this
// function exists to guarantee survives the fallback. The hash is taken over the
// cleaned root so that "/a/b" and "/a/b/" — which the lockfile already treats as
// one daemon — agree on one socket dir too.
//
// The returned directory always leaves room for the reserved MaxAgentNameLen
// leaf; every caller, and MaxAgentNameLen's promise to agent.ValidateAgentName,
// depends on that. The fallback therefore prefers os.TempDir() — per-user on
// darwin, and where these sockets already live — but only when it fits, because
// TMPDIR is itself unbounded: a TMPDIR over ~52 bytes leaves a directory in which
// no legal agent name could bind, which agent.Spawn treats as a fatal error
// rather than the silent attach loss it used to be. "/tmp" is the last resort;
// at 4 bytes it fits under any budget these constants could grow to. If it is
// not writable, NewRegistry's MkdirAll fails and pogod exits loudly at startup,
// which is the honest outcome (mg-ef80).
func AgentSocketDir() (dir string, insidePogoHome bool) {
	if dir := filepath.Join(PogoHome(), "agents", "sockets"); agentSocketDirFits(dir) {
		return dir, true
	}
	sum := sha256.Sum256([]byte(filepath.Clean(PogoHome())))
	leaf := "pogo-agents-" + hex.EncodeToString(sum[:4])
	if dir := filepath.Join(os.TempDir(), leaf); agentSocketDirFits(dir) {
		return dir, false
	}
	return filepath.Join("/tmp", leaf), false
}

// agentSocketDirFits reports whether dir leaves room to bind an agent socket
// beneath it without exceeding sun_path.
func agentSocketDirFits(dir string) bool {
	return len(dir)+agentSocketLeafBudget <= maxUnixSocketPathLen
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

// loadConfigFiles reads every config layer in ConfigFilePaths and merges them
// KEY BY KEY into one parsedConfig, lowest precedence first. It returns an
// error when no layer exists at all, which is Load's signal to stay on
// defaults + env.
//
// Key-by-key is the whole point. $POGO_HOME/config.toml used to shadow
// ~/.config/pogo/config.toml wholesale: whichever file ConfigFilePath picked
// was the only file read. That made the file a trapdoor — anything that
// created a partial $POGO_HOME/config.toml (a sandbox script, a test fixture,
// an operator pinning a port) silently dropped every key the real config
// carried, including the [agents] coordinator/worker pin the default-migration
// guard writes there. Dropping the pin re-arms the role-default flip (mg-ce47)
// against a deployment that was explicitly protected from it. Layering keeps
// the unnamed keys and overrides only what the higher file actually says
// (mg-cf9e).
func loadConfigFiles() (*parsedConfig, error) {
	paths := ConfigFilePaths()
	if len(paths) == 0 {
		return nil, fmt.Errorf("no config path")
	}

	cfg := &parsedConfig{}
	var firstErr error
	for _, path := range paths {
		switch err := parseConfigFileInto(cfg, path); {
		case err == nil:
			cfg.sources = append(cfg.sources, path)
		case os.IsNotExist(err):
			// A missing layer is the normal case, not an error.
		case firstErr == nil:
			firstErr = err
		}
	}
	if len(cfg.sources) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, os.ErrNotExist
	}
	return cfg, nil
}

// parseConfigFileInto parses one TOML config file into cfg, overwriting only
// the fields whose keys the file names. Only the minimal subset pogo needs is
// understood; unknown sections and keys are ignored.
func parseConfigFileInto(cfg *parsedConfig, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

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
		unquotedVal := unquote(val)

		switch currentSection {
		case "server":
			switch key {
			case "port":
				if port, err := strconv.Atoi(val); err == nil && port > 0 && port <= 65535 {
					cfg.Port = port
				}
			case "bind":
				cfg.Bind = unquotedVal
			}
		case "refinery":
			switch key {
			case "enabled":
				cfg.Refinery.Enabled = val == "true"
				cfg.refineryEnabledSet = true
			case "poll_interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
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
			case "priority_wake_enabled":
				cfg.StallWatch.PriorityWakeEnabled = val == "true"
				cfg.priorityWakeEnabledSet = true
			case "high_priority_wake_delay":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.HighPriorityWakeDelay = d
				}
			case "high_priority_wake_cooldown":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.StallWatch.HighPriorityWakeCooldown = d
				}
			case "fast_priorities":
				cfg.StallWatch.FastPriorities = parseStringArray(val)
			}
		case "reaper":
			switch key {
			case "enabled":
				cfg.Reaper.Enabled = val == "true"
				cfg.reaperEnabledSet = true
			case "interval":
				if d, err := time.ParseDuration(unquotedVal); err == nil {
					cfg.Reaper.Interval = d
				}
			case "max_kickstarts":
				if n, err := strconv.Atoi(unquotedVal); err == nil && n > 0 {
					cfg.Reaper.MaxKickstarts = n
				}
			case "jobs":
				cfg.Reaper.Jobs = parseReaperJobs(parseStringArray(val))
			}
		case "reconcile":
			switch key {
			case "mirrors":
				cfg.Reconcile.Mirrors = parseReconcileMirrors(parseStringArray(val))
			}
		case "agents":
			switch key {
			case "autostart":
				cfg.Agents.AutoStart = val == "true"
				cfg.agentsAutoStartSet = true
			case "command":
				cfg.Agents.Command = unquotedVal
			case "provider":
				cfg.Agents.Provider = unquotedVal
			case "coordinator":
				cfg.Agents.Coordinator = unquotedVal
			case "worker":
				cfg.Agents.Worker = unquotedVal
			case "extra_path":
				cfg.Agents.ExtraPath = parseStringArray(val)
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

	return scanner.Err()
}

// unquote strips one matched pair of surrounding TOML string quotes — basic
// ("...") or literal ('...') — from val. Values without a matched pair are
// returned unchanged, so a bare (technically invalid, but historically
// accepted) value like `bind = 127.0.0.1` keeps working, and interior quotes
// are never eaten. This is the regression from mg-a616: `bind = "127.0.0.1"`
// used to keep its quotes and produce an unusable listen address.
func unquote(val string) string {
	if len(val) >= 2 {
		first, last := val[0], val[len(val)-1]
		if first == last && (first == '"' || first == '\'') {
			return val[1 : len(val)-1]
		}
	}
	return val
}

// parseReaperJobs turns raw "<label>|<heartbeat-path>|<period>" entries into
// ReaperJob values. Malformed entries (wrong field count, empty label/path, or
// an unparseable period) are dropped with a log line rather than failing the
// whole config load — a typo in one job should not take the reaper (or pogod)
// down. The flat single-line encoding is deliberate: pogo's config is
// hand-parsed flat TOML with no table-array support (see the [stall_watch]
// note), so a per-field nested block is not available.
func parseReaperJobs(entries []string) []ReaperJob {
	var out []ReaperJob
	for _, e := range entries {
		parts := strings.Split(e, "|")
		if len(parts) != 3 {
			log.Printf("config: [reaper] ignoring malformed job %q (want label|path|period)", e)
			continue
		}
		label := strings.TrimSpace(parts[0])
		path := strings.TrimSpace(parts[1])
		period, err := time.ParseDuration(strings.TrimSpace(parts[2]))
		if label == "" || path == "" || err != nil || period <= 0 {
			log.Printf("config: [reaper] ignoring invalid job %q", e)
			continue
		}
		out = append(out, ReaperJob{Label: label, Heartbeat: path, Period: period})
	}
	return out
}

// parseReconcileMirrors turns raw "<name>|<source>|<target>[|<label>]" entries
// into ReconcileMirror values. The label is optional (three or four fields).
// A leading ~ in source/target is expanded to the home directory so config can
// be written portably. Malformed entries (wrong field count or an empty
// name/source/target) are dropped with a log line rather than failing the whole
// config load — a typo in one mirror should not take reconcile (or pogod) down.
// The flat single-line encoding matches [reaper] jobs: pogo's config is
// hand-parsed flat TOML with no table-array support.
func parseReconcileMirrors(entries []string) []ReconcileMirror {
	var out []ReconcileMirror
	for _, e := range entries {
		parts := strings.Split(e, "|")
		if len(parts) != 3 && len(parts) != 4 {
			log.Printf("config: [reconcile] ignoring malformed mirror %q (want name|source|target[|label])", e)
			continue
		}
		name := strings.TrimSpace(parts[0])
		source := expandTildePath(strings.TrimSpace(parts[1]))
		target := expandTildePath(strings.TrimSpace(parts[2]))
		label := ""
		if len(parts) == 4 {
			label = strings.TrimSpace(parts[3])
		}
		if name == "" || source == "" || target == "" {
			log.Printf("config: [reconcile] ignoring invalid mirror %q", e)
			continue
		}
		out = append(out, ReconcileMirror{Name: name, Source: source, Target: target, Label: label})
	}
	return out
}

// expandTildePath expands a leading ~ to the user's home directory. A bare ~ or
// ~/... only; ~user is left untouched (unsupported). Mirrors the reaper's
// expandHome so config paths are written portably.
func expandTildePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
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
