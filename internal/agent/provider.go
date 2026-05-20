package agent

import "time"

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
// docs/multi-provider-architecture-survey.md §2 for the full design.
//
// Provider lives in package agent (rather than a standalone internal/provider
// package) because its hooks take *agent.Agent: a separate package would create
// the import cycle agent → provider → agent.
type Provider struct {
	// ID is the config-key identity ("claude", "codex", "gemini").
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
}
