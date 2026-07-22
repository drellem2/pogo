// Package pi is pogo's pi coding agent harness provider — the third harness
// provider after Claude Code (internal/claude) and Codex (internal/codex).
//
// pi (https://pi.dev, earendil-works/pi-mono) is a minimal BYOK multi-provider
// terminal coding agent. It exports a single agent.Provider value, pi.Provider,
// capturing every pi-specific spawn decision: the command template, the
// --append-system-prompt prompt-injection flag, the --approve non-interactive
// flag, argv delivery of the initial task (gh #26), and the PTY nudge dialect.
//
// The nudge dialect was measured against a live pi 0.80.3 rather than copied
// from Claude or Codex — pi's TUI is its own differential-rendering framework
// (pi-tui), not Claude's Node/Ink or Codex's Rust/ratatui. See
// docs/investigations/pi-nudge-calibration.md.
package pi

import (
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// Provider is the pi coding agent harness descriptor.
//
// Prompt injection uses the AppendFlag strategy: pi's --append-system-prompt
// flag accepts either literal text or a file path — when the value names an
// existing file, pi reads the file's contents into the system prompt (verified
// against pi 0.80.3; see the calibration doc). This is Claude's injection shape
// and sidesteps the AGENTS.md-collision wrinkle entirely: the persona never
// touches the worktree, and pi still loads the repo's own AGENTS.md / CLAUDE.md
// as project context — exactly like Claude reading a repo's CLAUDE.md. (The
// ContextFile alternative, .pi/APPEND_SYSTEM.md, was rejected: pi only loads
// project-local .pi/ resources from a TRUSTED project, which would drag the
// trust system into persona delivery.)
//
// PostSpawnHook is nil: pi's "Trust project folder?" dialog appears only when
// the repo carries trust-requiring .pi/ resources (settings.json, extensions,
// skills, prompts, themes, SYSTEM.md, APPEND_SYSTEM.md) AND no trust decision
// is saved — and --approve suppresses it unconditionally by overriding the
// trust resolution before any prompt. SessionHook is nil: pi surfaces no
// mid-session modal (errors render inline; tool calls run without approval
// prompts — pi has no built-in permission system).
//
// pi selects its model via its own settings or --provider/--model; the default
// template deliberately pins neither, so the user's pi configuration (or a
// [agents] command override with explicit --provider/--model flags) decides.
// Auth comes from provider env keys (ANTHROPIC_API_KEY, OPENAI_API_KEY, …) or
// pi's own auth store (~/.pi/agent/auth.json, seeded via pi's /login).
var Provider = agent.Provider{
	ID:     "pi",
	Binary: "pi",

	// --approve pre-trusts the worktree (pi's equivalent of Claude's trust
	// dialog auto-accept): it suppresses the blocking "Trust project folder?"
	// dialog that would otherwise appear when the target repo carries
	// project-local .pi/ resources. Polecats run unattended in fresh
	// worktrees, so the dialog must never block startup.
	CommandTemplate: "pi --approve --append-system-prompt {{.PromptFile}}",

	PromptInjection: agent.PromptInjection{
		Kind: agent.InjectAppendFlag,
		Flag: "--append-system-prompt",
	},

	NonInteractiveFlags: []string{"--approve"},

	// pi accepts the initial task as a trailing positional argv element
	// (`pi [messages...]`), so Spawn appends it to the command instead of
	// typing it into the composer. The typed initial nudge was unreliable
	// here: pi-tui's differential renderer can emit near-continuous PTY
	// writes under load (worst with concurrent PTY activity, e.g. a mayor
	// dispatching several polecats), the idle window the nudge waits for
	// never opens, and the polecat silently sits taskless forever (gh #26).
	InitialPromptViaArgv: true,

	// pi's pi-tui nudge dialect — every value measured against a live pi
	// 0.80.3 at pogo's default 200×50 winsize, NOT copied from Claude or
	// Codex. See docs/investigations/pi-nudge-calibration.md for the raw
	// measurements.
	Nudge: agent.NudgeProfile{
		// The initial task arrives via argv (InitialPromptViaArgv above), not
		// as a typed nudge — see gh #26. The persona arrives via the
		// --append-system-prompt flag.
		NeedsInitialNudge: false,

		// Unused while NeedsInitialNudge is false; retained as the measured
		// calibration record should the nudge path ever be needed again. pi
		// is a Node CLI that renders its TUI in ~1.5s from spawn — slower
		// than Codex's native binary but far faster than Claude/Ink's cold
		// start. The bound is generous because pi's first-ever run also
		// downloads its fd/ripgrep helper binaries into ~/.pi/agent/bin.
		InitialNudgeTimeout: 30 * time.Second,

		// A carriage return submits the pi composer.
		SubmitTerminator: "\r",

		// Unlike Claude's Ink and Codex's ratatui, pi's composer submits even
		// when body+"\r" arrive as a single PTY chunk (measured: a combined
		// write triggers a turn; literal "\n" bytes inside the body stay
		// literal newlines, so multi-line nudges arrive intact as one
		// message). The 50ms split-write gap is therefore not load-bearing
		// for pi — it is kept equal to Claude's and Codex's value so
		// Agent.Nudge's split-write path stays uniform across providers.
		SubmitDelay: 50 * time.Millisecond,

		// A settled pi composer emits zero PTY output (differential
		// rendering), so steady-state idle would tolerate a sub-second
		// threshold. There is also no silent blocking dialog to race against
		// (--approve suppresses the only one). 2s simply keeps the idle
		// window uniform with Claude and Codex.
		IdleThreshold: 2 * time.Second,

		// pi renders a keybinding hint line under the composer once its input
		// loop is up — "escape interrupt · ctrl+c/ctrl+d clear/exit ·
		// / commands · ! bash · ctrl+o more". Unused for the initial prompt
		// while NeedsInitialNudge is false (argv delivery has no ready gate to
		// wait for); retained as the measured "input loop ready" marker for
		// any future wait-ready use. The "/" and "!" are keybinding names, so
		// a custom ~/.pi/agent keybinding config could reword the hint — in
		// that case WaitForReady degrades to best-effort wait-idle delivery
		// rather than dropping the nudge.
		PromptReadySentinel: "/ commands · ! bash",
	},

	// No hooks: --approve suppresses the trust dialog pre-spawn, and pi has no
	// mid-session modal to dismiss — see the Provider doc above.
	PostSpawnHook: nil,
	SessionHook:   nil,

	// PTYSize nil — pi renders correctly at pogo's default 200×50 winsize.

	// MemoryIndexGlobs nil: pi ships no MEMORY.md auto-memory index. ~/.pi
	// holds only the agent auth/settings store (measured 2026-07-21). If pi
	// ships one, declare its glob here — memcheck needs no change.
	MemoryIndexGlobs: nil,

	// SessionTranscriptGlob nil: pi does write per-workdir session transcripts
	// at ~/.pi/agent/sessions/<slug>/*.jsonl (measured 2026-07-23), but its
	// slug encoding and its failure-turn record shape have not been
	// characterised, and synthfail's structural test is written against a
	// record that marks a locally-answered turn. Declaring a glob whose records
	// never match would produce a confident StateQuiet — a false all-clear,
	// which is the exact error this detector exists to prevent. nil keeps pi on
	// today's behaviour until the record shape is measured.
	SessionTranscriptGlob: nil,
}
