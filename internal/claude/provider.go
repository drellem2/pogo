package claude

import (
	"path/filepath"
	"strings"

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

	// Claude Code writes one JSONL session transcript per session under
	// ~/.claude/projects/<slug-of-cwd>/. internal/synthfail reads it to
	// distinguish a wedged agent from one failing every turn locally.
	SessionTranscriptGlob: SessionTranscriptGlob,
}

// SessionTranscriptGlob returns the home-relative glob matching the Claude Code
// session transcripts for an agent whose working directory is workdir, or ""
// when workdir is unknown.
//
// The slug encoding is Claude Code's, verified against this machine's project
// dirs on 2026-07-23: every byte outside [A-Za-z0-9] becomes '-', with no
// collapsing of runs, so /Users/daniel/.pogo/agents/pm-pogo becomes
// -Users-daniel--pogo-agents-pm-pogo (the doubled '-' is the '/' plus the '.').
//
// This is harness-internal and pogo does not own it. It is declared here, not
// in synthfail, precisely so that when Claude Code changes it the blast radius
// is this function — and the failure mode is synthfail finding no files, which
// is StateUnavailable and degrades to pogo's pre-detector behaviour rather than
// reporting a false all-clear.
func SessionTranscriptGlob(workdir string) string {
	if workdir == "" {
		return ""
	}
	return filepath.Join(".claude", "projects", projectSlug(workdir), "*.jsonl")
}

// projectSlug applies Claude Code's path-to-directory-name encoding.
func projectSlug(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
