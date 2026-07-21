package claude

import (
	"path/filepath"

	"github.com/drellem2/pogo/internal/agent"
)

// Provider is the Claude Code harness descriptor — pogo's sole registered
// provider today. It captures, in one value, every Claude-specific spawn
// decision that was previously scattered across config, agent, claude, and
// cmd/pogod: the command template, prompt-injection flag, the
// --dangerously-skip-permissions non-interactive flag, the PTY nudge dialect,
// and the two lifecycle hooks.
//
// Phase 3A is a behavior-preserving refactor: this value reproduces today's
// exact behavior. See docs/design/multi-provider-architecture-survey.md §2–3A.
var Provider = agent.Provider{
	ID:     "claude",
	Binary: "claude",

	// --dangerously-skip-permissions is required for autonomous execution in
	// freshly-created worktree directories; --permission-mode bypassPermissions
	// does not work without additional setup. It does NOT suppress the
	// workspace trust dialog — TrustDialogHook handles that separately.
	CommandTemplate: "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}",

	PromptInjection: agent.PromptInjection{
		Kind: agent.InjectAppendFlag,
		Flag: "--append-system-prompt-file",
	},

	NonInteractiveFlags: []string{"--dangerously-skip-permissions"},

	// Claude's Ink/React TUI nudge dialect. pogo's DefaultNudgeProfile was
	// tuned against it, so the provider adopts it verbatim — keeping a single
	// source of truth for the {60s, "\r", 50ms, 2s} timings.
	Nudge: agent.DefaultNudgeProfile,

	// PostSpawnHook auto-accepts the workspace trust dialog; SessionHook is the
	// mid-session modal-dismissal watcher (rating dialog + rate-limit modal).
	PostSpawnHook: TrustDialogHook,
	SessionHook:   ModalHook,

	// PTYSize nil — Claude uses pogo's default 200×50 winsize.

	// Claude Code keeps a per-project auto-memory index at
	// ~/.claude/projects/<project-slug>/memory/MEMORY.md. This is a Claude Code
	// product feature, so its path is declared HERE rather than in the shared
	// memcheck package — that literal living in a neutral package made a
	// harness-agnostic check Claude-only in practice.
	MemoryIndexGlobs: []string{
		filepath.Join(".claude", "projects", "*", "memory", "MEMORY.md"),
	},
}
