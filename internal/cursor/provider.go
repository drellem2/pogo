// Package cursor is pogo's Cursor CLI harness provider — the fourth harness
// provider after Claude Code (internal/claude), Codex (internal/codex) and pi
// (internal/pi).
//
// Cursor (https://cursor.com) ships a terminal coding agent whose command was
// renamed from `cursor-agent` to `agent` in 2026. It exports a single
// agent.Provider value, cursor.Provider, capturing every Cursor-specific spawn
// decision: the command template, the .cursor/rules context-file
// prompt-injection strategy, the --force non-interactive flag, argv delivery of
// the initial task, the workspace-trust PostSpawnHook, and the PTY nudge
// dialect.
//
// Every value here was measured against a live Cursor CLI 2026.07.09-a3815c0
// rather than copied from another provider — Cursor's TUI is its own renderer,
// and it differs from all three predecessors in at least one load-bearing way
// (see the SubmitDelay note below). See docs/investigations/cursor-nudge-calibration.md.
package cursor

import (
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// personaRuleFile is where the pogo persona is written inside the agent's
// working directory. Cursor loads every `.cursor/rules/**/*.mdc` in the
// workspace, so this is a namespace the repo does not own by convention —
// unlike AGENTS.md, which repos commonly check in and which pogo must not
// clobber.
const personaRuleFile = ".cursor/rules/pogo-persona.mdc"

// personaRuleHeader is the YAML frontmatter prepended to the persona.
//
// It is not decoration. A `.mdc` with no frontmatter is silently ignored by
// Cursor (measured: 0/3 runs saw the persona), and a rule with
// `alwaysApply: false` is only *sometimes* attached — Cursor decides from the
// description, so persona delivery would depend on the model's judgement.
// `alwaysApply: true` is the documented "Always" rule type and was injected in
// 3/3 runs. See docs/investigations/cursor-nudge-calibration.md.
const personaRuleHeader = "---\n" +
	"description: pogo agent persona\n" +
	"alwaysApply: true\n" +
	"---\n\n"

// Provider is the Cursor CLI harness descriptor.
//
// Prompt injection uses the ContextFile strategy, because Cursor has no
// --append-system-prompt equivalent (the only prompt-ish flag on the CLI is
// --no-prompt). The persona is written to .cursor/rules/pogo-persona.mdc in
// the agent's working directory, carrying `alwaysApply: true` frontmatter.
//
// That path is the escape hatch for the AGENTS.md-collision wrinkle the ticket
// flagged. Cursor loads AGENTS.md, CLAUDE.md, .cursorrules and
// .cursor/rules/**/*.mdc as rules. Injecting through AGENTS.md would clobber a
// file most repos own; injecting through a uniquely-named rule file does not.
// Both were measured to load together: with a repo AGENTS.md and a pogo rule
// present, a behavioural probe saw directives from *both*, and the repo's
// AGENTS.md was left byte-identical. Cursor offers no AGENTS.override.md-style
// precedence file (Codex's answer), so a separate rules namespace is the only
// additive injection point.
//
// PostSpawnHook is TrustDialogHook: Cursor blocks a fresh worktree behind a
// "Workspace Trust Required" dialog, and every polecat runs in a fresh
// worktree. Neither non-interactive flag suppresses it — --force governs
// command approval, not workspace trust, and --trust is rejected outright
// outside --print/headless mode ("Error: --trust can only be used with
// --print/headless mode"). So the dialog must be dismissed from the PTY, the
// way Claude and Codex dismiss theirs. SessionHook is nil: with --force,
// Cursor surfaces no mid-session modal — tool calls run without approval
// prompts and errors render inline.
//
// Cursor selects its model via its own config (~/.cursor/cli-config.json,
// default "auto") or --model; the template deliberately pins neither, so the
// user's Cursor configuration (or an [agents] command override with an explicit
// --model) decides. Auth comes from CURSOR_API_KEY or Cursor's own auth store
// (seeded by `agent login`); billing draws on the account's Cursor plan credits.
var Provider = agent.Provider{
	ID: "cursor",

	// The CLI binary is `agent` — renamed from `cursor-agent` in 2026. The
	// installer keeps a `cursor-agent` symlink beside it, but `agent` is the
	// documented command and the one `curl cursor.com/install` puts on PATH.
	// Note this is a generic name: ValidateCommandBinary's PATH check cannot
	// tell Cursor's `agent` from an unrelated binary of the same name.
	Binary: "agent",

	// --force ("Run Everything", aliased --yolo) is Cursor's equivalent of
	// Claude's --dangerously-skip-permissions: it allows every tool call that
	// is not explicitly denied, which polecats need to execute autonomously.
	//
	// --trust is deliberately absent: it is the workspace-trust bypass, but
	// Cursor rejects it outside --print mode and the CLI exits non-zero before
	// the TUI renders. Workspace trust is handled by TrustDialogHook instead.
	CommandTemplate: "agent --force",

	// ContextFile injection: the persona is written to
	// .cursor/rules/pogo-persona.mdc (created under the agent's working
	// directory) behind `alwaysApply: true` frontmatter. Cursor loads it
	// alongside — not instead of — the repo's own AGENTS.md, which pogo never
	// touches.
	PromptInjection: agent.PromptInjection{
		Kind:              agent.InjectContextFile,
		ContextFile:       personaRuleFile,
		ContextFileHeader: personaRuleHeader,
	},

	NonInteractiveFlags: []string{"--force"},

	// Cursor accepts the initial task as a trailing positional argv element
	// (`agent [options] [prompt...]`), so Spawn appends it to the command
	// instead of typing it into the composer.
	//
	// This is the gh #26 posture, adopted here for a second, Cursor-specific
	// reason: the workspace-trust dialog. A typed initial nudge would have to
	// wait out TrustDialogHook's dismissal, and the dialog is *silent* once
	// rendered — it reads as "idle" within ~0.5s, exactly the race that made
	// Codex's nudge type its task into the trust dialog. Argv delivery removes
	// the race rather than tuning against it: Cursor reads the prompt before
	// the TUI starts and runs it once the workspace is trusted (measured 3/3).
	InitialPromptViaArgv: true,

	// Cursor's TUI nudge dialect — every value measured against a live Cursor
	// CLI 2026.07.09-a3815c0 at pogo's default 200×50 winsize, NOT copied from
	// Claude, Codex or pi. See docs/investigations/cursor-nudge-calibration.md.
	Nudge: agent.NudgeProfile{
		// The initial task arrives via argv (InitialPromptViaArgv above), not
		// as a typed nudge. The persona arrives via the .cursor/rules file.
		NeedsInitialNudge: false,

		// Unused while NeedsInitialNudge is false; retained as the measured
		// calibration record should the nudge path ever be needed again.
		// Cursor is a Node CLI: the trust dialog renders ~0.7s from spawn and
		// the composer settles ~3.0s. 30s is a generous upper bound.
		InitialNudgeTimeout: 30 * time.Second,

		// A carriage return submits the Cursor composer.
		SubmitTerminator: "\r",

		// LOAD-BEARING, unlike pi's. Cursor's composer has paste-burst
		// detection: body+"\r" written as a single PTY chunk is absorbed as a
		// literal newline and does NOT submit (measured — the turn never ran,
		// the test timed out at 95s). The nudge body and terminator must arrive
		// in separate read()s. 50ms matches Claude's and Codex's value, so
		// Agent.Nudge's split-write path stays uniform across providers.
		SubmitDelay: 50 * time.Millisecond,

		// A settled Cursor composer emits zero PTY output (measured: 0 bytes
		// over a 10s idle watch) — a differential renderer like Codex's ratatui
		// and pi's pi-tui, unlike Claude's continuously-repainting Ink. Steady
		// state would therefore tolerate a sub-second threshold. The binding
		// constraint is the spawn-time race: the workspace-trust dialog is
		// itself dead silent once drawn, so it reads as "idle" almost
		// immediately. Argv delivery keeps the *initial* task clear of that
		// race, and 2s keeps mid-session wait-idle nudges uniform with the
		// other three providers.
		IdleThreshold: 2 * time.Second,

		// Cursor renders its composer placeholder — "→ Plan, search, build
		// anything" — once the input loop is up; it is absent during the
		// loading banner and the trust dialog, which makes it a precise "input
		// loop ready" marker. Unused for the initial prompt while
		// NeedsInitialNudge is false (argv delivery has no ready gate to wait
		// for); retained as the measured marker for any future wait-ready use,
		// and asserted by the e2e so a Cursor UI change is caught. It rendered
		// in 3/3 argv-delivered spawns before the turn replaced it with "Add a
		// follow-up".
		PromptReadySentinel: "Plan, search, build anything",
	},

	// PostSpawnHook auto-accepts Cursor's workspace-trust dialog; SessionHook is
	// nil — see the Provider doc above.
	PostSpawnHook: TrustDialogHook,
	SessionHook:   nil,

	// PTYSize nil — Cursor renders correctly at pogo's default 200×50 winsize.
}
