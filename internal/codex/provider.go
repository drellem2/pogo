// Package codex is pogo's OpenAI Codex CLI harness provider — the second
// harness provider after Claude Code (see internal/claude).
//
// It exports a single agent.Provider value, codex.Provider, capturing every
// Codex-specific spawn decision: the command template, the AGENTS.override.md
// context-file prompt-injection strategy, the --dangerously-bypass-approvals-
// and-sandbox non-interactive flag, and the PTY nudge dialect.
//
// The nudge dialect was measured against a live Codex CLI (0.132.0) rather
// than copied from Claude — Codex's TUI is Rust/ratatui, not Claude's
// Node/Ink, and the two differ materially. See docs/investigations/codex-nudge-calibration.md
// and docs/design/multi-provider-architecture-survey.md §3 Phase 3B.
package codex

import (
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// Provider is the OpenAI Codex CLI harness descriptor.
//
// Prompt injection uses the ContextFile strategy: pogo writes the persona
// prompt to AGENTS.override.md in the agent's working directory. Codex reads
// AGENTS.override.md natively and it takes precedence over a repo's own
// AGENTS.md — so the persona lands without an --append-system-prompt-file-style
// flag (Codex has none; experimental_instructions_file *replaces* Codex's
// tuned base instructions and must not be used). POGO_AGENT_PROMPT remains the
// env fallback. See agent.InjectContextFile and agent.writeContextFilePrompt.
//
// PostSpawnHook is TrustDialogHook: Codex shows a directory-trust dialog the
// first time it launches in an unknown directory, and every polecat runs in a
// fresh worktree. --dangerously-bypass-approvals-and-sandbox does NOT suppress
// that dialog (it governs command approvals and the sandbox, not project
// trust) — determined empirically; see docs/investigations/codex-nudge-calibration.md.
// SessionHook is nil: Codex surfaces no mid-session modal that needs
// dismissing (the quota/rate-limit notice is an inline message, and command
// approvals are bypassed by the command flag).
var Provider = agent.Provider{
	ID:     "codex",
	Binary: "codex",

	// --dangerously-bypass-approvals-and-sandbox is Codex's equivalent of
	// Claude's --dangerously-skip-permissions: it skips every approval prompt
	// and runs commands un-sandboxed, which polecats need to execute
	// autonomously in a fresh worktree.
	//
	// -c project_doc_max_bytes=1048576 lifts Codex's 32 KB default cap on
	// project-doc (AGENTS.md / AGENTS.override.md) content. The pogo persona
	// is delivered through AGENTS.override.md and can exceed 32 KB once a large
	// work-item body is templated in; without this override Codex silently
	// truncates the persona's tail (which carries the polecat protocol steps).
	CommandTemplate: "codex --dangerously-bypass-approvals-and-sandbox -c project_doc_max_bytes=1048576",

	// ContextFile injection: the persona is written to AGENTS.override.md in
	// the agent's working directory (a fresh worktree for polecats). Codex
	// loads it automatically and prefers it over any checked-in AGENTS.md, so
	// the repo's own AGENTS.md is never clobbered.
	PromptInjection: agent.PromptInjection{
		Kind:        agent.InjectContextFile,
		ContextFile: "AGENTS.override.md",
	},

	NonInteractiveFlags: []string{"--dangerously-bypass-approvals-and-sandbox"},

	// Codex's Rust/ratatui TUI nudge dialect — every value measured against a
	// live Codex CLI 0.132.0, NOT copied from Claude. See
	// docs/investigations/codex-nudge-calibration.md for the raw measurements.
	Nudge: agent.NudgeProfile{
		// The Codex TUI opens at an empty composer awaiting input; the task
		// must be typed and submitted, so an initial nudge is required (the
		// persona itself arrives via AGENTS.override.md, not the nudge).
		NeedsInitialNudge: true,

		// Codex is a native binary and renders its TUI to first-quiet in
		// ~0.2-0.3s — far faster than Claude/Ink's cold start. 30s is a
		// generous upper bound; the nudge actually fires at ~IdleThreshold
		// once the composer is idle.
		InitialNudgeTimeout: 30 * time.Second,

		// A carriage return submits the Codex composer (verified: a split
		// write ending in "\r" drives the TUI into its Working state).
		SubmitTerminator: "\r",

		// Codex's composer has paste-burst detection (cf. the disable_paste_
		// burst config key): a body+"\r" written as a single PTY chunk is
		// absorbed as a literal newline and does NOT submit. The nudge body
		// and terminator must arrive in separate read()s. A split write
		// submits at gaps as low as 10ms; 50ms keeps a 5x margin and matches
		// Claude's value, so Agent.Nudge's split-write path stays uniform.
		SubmitDelay: 50 * time.Millisecond,

		// A *settled* Codex composer emits zero PTY output, so steady-state
		// idle detection would tolerate a sub-second threshold. The binding
		// constraint is instead the spawn-time race: the directory-trust
		// dialog (dismissed by TrustDialogHook) is itself dead silent, so it
		// reads as "idle" within ~0.5s of rendering. If the initial nudge's
		// wait-idle fires during that gap it types the task into the trust
		// dialog, not the composer (observed: a 1s threshold dropped the
		// nudge's first word). 2s clears the dialog's quiet gap plus the
		// hook's worst-case dismiss latency (~1.3s), so the nudge reliably
		// lands in the ready composer. See docs/investigations/codex-nudge-calibration.md.
		IdleThreshold: 2 * time.Second,
	},

	// PostSpawnHook auto-accepts Codex's directory-trust dialog; SessionHook is
	// nil — see the Provider doc above.
	PostSpawnHook: TrustDialogHook,
	SessionHook:   nil,

	// PTYSize nil — Codex renders correctly at pogo's default 200x50 winsize.

	// MemoryIndexGlobs nil: Codex ships no MEMORY.md auto-memory index for
	// `pogo doctor` to size-check. ~/.codex/memories exists as a directory but
	// carries no index file of that shape (measured 2026-07-21). If Codex ships
	// one, declare its glob here — memcheck needs no change.
	MemoryIndexGlobs: nil,

	// SessionTranscriptGlob nil: Codex writes session rollouts to
	// ~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-*.jsonl, which is keyed by
	// START TIME, not by the agent's working directory (measured 2026-07-23) —
	// so there is no glob that maps one agent to its transcript, which is what
	// synthfail needs. Nor has a Codex zero-token failure-turn record been
	// characterised. Declaring nil keeps Codex agents on today's behaviour
	// (StateUnavailable) rather than guessing; if a workdir-addressable path or
	// a verified failure-turn shape turns up, declare it here — synthfail needs
	// no change.
	SessionTranscriptGlob: nil,
}
