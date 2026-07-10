package agent

import "time"

// DefaultProviderID is the built-in fallback harness provider — the final tier
// of the per-spawn resolution chain (see Registry.resolveProvider). It is kept
// equal to config.DefaultProvider; the agent package cannot import config
// (config has no agent dependency and the value is a plain string), so the
// literal is repeated here as the resolution floor.
const DefaultProviderID = "claude"

// Provider describes a terminal-native agentic harness that pogo can (a) launch
// as a long-running interactive TUI, (b) inject a persona prompt into, (c) run
// fully unattended, and (d) nudge via stdin. It is mostly a data descriptor,
// with two behavior fields (PostSpawnHook, SessionHook) that match pogo's
// existing func(*Agent) hook style.
//
// A Provider is NOT a model endpoint — it is the harness process itself. Claude
// Code is the sole provider today (see internal/claude); the type exists so
// every Claude-specific spawn decision is forced behind one explicit seam
// instead of being scattered across config, agent, claude, and cmd/pogod. See
// docs/design/multi-provider-architecture-survey.md §2 for the full design.
//
// Provider lives in package agent (rather than a standalone internal/provider
// package) because its hooks take *agent.Agent: a separate package would create
// the import cycle agent → provider → agent.
type Provider struct {
	// ID is the config-key identity ("claude", "codex", "pi", "cursor").
	ID string

	// Binary is the executable name — used by `pogo doctor` and PATH checks.
	Binary string

	// CommandTemplate is the default Go-template spawn command. It is only the
	// default: an explicit [agents] command (config file or POGO_AGENT_COMMAND
	// env) still overrides it. Template vars are CommandTemplateVars fields.
	CommandTemplate string

	// PromptInjection describes how the persona prompt is delivered to the
	// harness.
	PromptInjection PromptInjection

	// NonInteractiveFlags are the flags a command template must carry for the
	// harness to run unattended (no permission or trust prompts).
	// ValidatePolecatCommand checks all of these are present.
	NonInteractiveFlags []string

	// InitialPromptViaArgv is true when the harness accepts its initial task
	// message as a trailing positional argv element (pi: `pi [messages...]`).
	// Spawn then appends SpawnRequest.InitialNudge to the spawn command as a
	// single argument and skips the PTY initial-nudge path entirely. This is
	// the reliable delivery for a differential-render TUI that redraws
	// near-continuously: such a TUI can hold the PTY busy indefinitely, the
	// idle window the typed initial nudge waits for never opens, and the agent
	// sits taskless forever while showing "running" (gh #26). Providers whose
	// harness has no argv message support leave this false and take the
	// NeedsInitialNudge nudge path.
	InitialPromptViaArgv bool

	// Nudge is the PTY-input dialect pogo uses to drive the harness.
	Nudge NudgeProfile

	// PostSpawnHook runs once, in a goroutine, after an agent is registered.
	// nil = no hook. (Claude: auto-dismiss the workspace trust dialog.)
	PostSpawnHook func(a *Agent)

	// SessionHook runs for the agent's whole lifetime. nil = no hook.
	// (Claude: the mid-session modal-dismissal watcher.)
	SessionHook SessionHookFunc

	// PTYSize overrides pogo's default PTY winsize. nil = pogo default
	// (defaultPTYCols × defaultPTYRows).
	PTYSize *PTYSize
}

// PromptInjectionKind enumerates the strategies for delivering a persona prompt
// to a harness.
type PromptInjectionKind int

const (
	// InjectAppendFlag passes the prompt file via a command-line flag
	// (Claude: --append-system-prompt-file).
	InjectAppendFlag PromptInjectionKind = iota

	// InjectContextFile writes the persona into a context file the harness
	// reads on startup (e.g. Codex's AGENTS.override.md).
	InjectContextFile

	// InjectEnvOnly relies solely on the POGO_AGENT_PROMPT env var that pogo
	// already injects at spawn.
	InjectEnvOnly
)

// PromptInjection describes how a provider receives its persona prompt.
type PromptInjection struct {
	Kind        PromptInjectionKind
	Flag        string // InjectAppendFlag: e.g. "--append-system-prompt-file"
	ContextFile string // InjectContextFile: e.g. "AGENTS.override.md"

	// ContextFileHeader is prepended verbatim to the persona when the context
	// file is written. It exists for harnesses whose context files are only
	// honored when they carry a machine-readable preamble: Cursor ignores a
	// .cursor/rules/*.mdc that has no YAML frontmatter, and only a rule
	// declaring `alwaysApply: true` is guaranteed to reach the system prompt
	// (measured — see docs/investigations/cursor-nudge-calibration.md). Empty
	// for Codex, whose AGENTS.override.md is plain markdown.
	//
	// ContextFile may name a nested path ("dir/file.md"); the parent
	// directories are created under the agent's working directory.
	ContextFileHeader string
}

// NudgeProfile is a provider's PTY-input dialect: the timings and terminator
// pogo uses to deliver nudges and detect idleness. The Claude-tuned values
// live in DefaultNudgeProfile.
type NudgeProfile struct {
	// NeedsInitialNudge is true when the harness starts at an interactive
	// prompt that must be bypassed with a post-spawn nudge; false when the
	// persona prompt is passed as a command-line arg and no nudge is needed.
	NeedsInitialNudge bool

	// InitialNudgeTimeout bounds the wait for the PTY to go idle before the
	// post-spawn nudge is delivered. Generous because harness startup can be
	// slow on a cold cache or with a large prompt file.
	InitialNudgeTimeout time.Duration

	// SubmitTerminator is written after a nudge body to submit it
	// (Claude: "\r").
	SubmitTerminator string

	// SubmitDelay is the gap between writing the nudge body and the submit
	// terminator, so the receiver reads them in separate read() calls — see
	// Agent.Nudge for the paste-detection rationale (Claude: 50ms).
	SubmitDelay time.Duration

	// IdleThreshold is how long PTY output must be quiet before the agent is
	// considered idle for wait-idle nudge delivery (Claude: 2s).
	IdleThreshold time.Duration

	// PromptReadySentinel, when non-empty, is a substring of the harness's
	// PTY output (after ANSI stripping) that proves the interactive input
	// loop has rendered and is ready to accept a submitted nudge. The initial
	// nudge waits for this marker to appear before delivering, rather than
	// relying on output quiescence alone — quiescence is ALSO true during
	// pre-TUI startup, so a quiescence-only gate can fire the nudge before
	// Ink's input loop exists. The bytes then pile in the kernel input buffer
	// and Ink reads the whole spawn-cluster as one paste-mode block that the
	// submit never re-tokenizes, wedging the agent (mg-ce61). An empty
	// sentinel falls back to pure wait-idle behavior (e.g. Codex, whose
	// ratatui composer has no equivalent stable marker).
	PromptReadySentinel string

	// PromptReadyAlternates are additional accepted ready-markers: WaitForReady
	// opens its gate when the primary sentinel OR any alternate appears. It
	// exists because a single exact-string sentinel is brittle — when the
	// harness's TUI changes the exact hint text (as Claude Code did between the
	// "? for shortcuts" era and v2.1.x, which shows a "shift+tab to cycle" mode
	// bar and a rotating `Try "…"` placeholder instead), the lone sentinel stops
	// matching and EVERY spawn silently pays the full InitialNudgeTimeout as dead
	// time before the best-effort delivery. Listing several stable markers keeps
	// readiness detection working across harness versions instead of regressing
	// to a flat per-spawn timeout tax (mg-ce61 follow-up).
	PromptReadyAlternates []string
}

// PTYSize is an explicit PTY winsize a provider can request in place of pogo's
// default. A nil Provider.PTYSize means "use defaultPTYCols × defaultPTYRows".
type PTYSize struct {
	Cols uint16
	Rows uint16
}

// DefaultNudgeProfile is pogo's default PTY-input dialect. The values were
// tuned against Claude Code's Ink/React TUI — a 50ms paste-detection gap, a
// 60s cold-start budget, a 2s idle window, and a carriage-return submit — and
// serve as the fallback used when no provider is registered on a Registry.
// claude.Provider adopts this profile verbatim, so it doubles as the single
// source of truth for Claude's nudge timings.
var DefaultNudgeProfile = NudgeProfile{
	NeedsInitialNudge:   true,
	InitialNudgeTimeout: 60 * time.Second,
	SubmitTerminator:    "\r",
	SubmitDelay:         50 * time.Millisecond,
	IdleThreshold:       2 * time.Second,

	// Claude Code's empty Ink composer renders a "? for shortcuts" hint in its
	// input box once the interactive input loop is up and idle. The hint is
	// absent during the loading spinner and the workspace-trust dialog, so its
	// appearance is a precise "input loop ready" signal — exactly the gate the
	// initial nudge needs (mg-ce61). Verified stable across Claude Code
	// versions; if it ever changes, WaitForReady degrades to best-effort
	// wait-idle delivery rather than dropping the nudge.
	PromptReadySentinel: "? for shortcuts",

	// Alternates keep readiness detection working after Claude Code's composer
	// dropped the "? for shortcuts" hint (v2.1.x). Two markers render only once
	// the interactive input loop is up: the mode bar's mode-cycle hint and the
	// empty composer's rotating placeholder. NOTE the missing spaces: v2.1.x
	// renders the footer with per-word cursor-column moves (ESC[<n>G), so after
	// ANSI stripping the words concatenate — the live PTY yields
	// "accepteditson(shift+tabtocycle)…" and `❯ Try"…"`, not the spaced forms.
	// "shift+tabtocycle" is mode-invariant (shown for every permission mode);
	// `Try"` marks the empty composer a fresh spawn always starts at. Without
	// these, v2.1.x spawns never match the primary sentinel and burn the full
	// 60s InitialNudgeTimeout as dead time on every worker.
	PromptReadyAlternates: []string{"shift+tabtocycle", "Try\""},
}
