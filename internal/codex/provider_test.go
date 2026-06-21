package codex

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

func TestProviderIdentity(t *testing.T) {
	if Provider.ID != "codex" {
		t.Errorf("Provider.ID = %q, want \"codex\"", Provider.ID)
	}
	if Provider.Binary != "codex" {
		t.Errorf("Provider.Binary = %q, want \"codex\"", Provider.Binary)
	}
}

// TestProviderCommandHasBypassFlag guards the autonomy contract: the Codex
// command template must keep --dangerously-bypass-approvals-and-sandbox so
// polecats run unattended in a fresh worktree without an approval prompt.
func TestProviderCommandHasBypassFlag(t *testing.T) {
	if !strings.Contains(Provider.CommandTemplate, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatal("codex.Provider.CommandTemplate must include " +
			"--dangerously-bypass-approvals-and-sandbox for autonomous execution")
	}
}

// TestProviderCommandLiftsProjectDocCap guards against silent persona
// truncation: Codex caps project-doc content at 32 KB by default, and the
// pogo persona delivered through AGENTS.override.md can exceed that. The
// command template must raise project_doc_max_bytes.
func TestProviderCommandLiftsProjectDocCap(t *testing.T) {
	if !strings.Contains(Provider.CommandTemplate, "project_doc_max_bytes=") {
		t.Fatal("codex.Provider.CommandTemplate must override project_doc_max_bytes " +
			"so a large persona is not truncated at Codex's 32 KB default")
	}
}

func TestProviderNonInteractiveFlags(t *testing.T) {
	if len(Provider.NonInteractiveFlags) == 0 {
		t.Fatal("codex.Provider must declare at least one non-interactive flag")
	}
	// Every declared non-interactive flag must appear in the command template,
	// otherwise ValidatePolecatCommand warns on the provider's own default.
	for _, f := range Provider.NonInteractiveFlags {
		if !strings.Contains(Provider.CommandTemplate, f) {
			t.Errorf("CommandTemplate %q is missing declared non-interactive flag %q",
				Provider.CommandTemplate, f)
		}
	}
}

// TestProviderPromptInjection pins the ContextFile strategy: Codex has no
// append-system-prompt flag, so the persona is delivered via AGENTS.override.md.
func TestProviderPromptInjection(t *testing.T) {
	if Provider.PromptInjection.Kind != agent.InjectContextFile {
		t.Errorf("PromptInjection.Kind = %d, want InjectContextFile",
			Provider.PromptInjection.Kind)
	}
	if Provider.PromptInjection.ContextFile != "AGENTS.override.md" {
		t.Errorf("PromptInjection.ContextFile = %q, want \"AGENTS.override.md\"",
			Provider.PromptInjection.ContextFile)
	}
}

// TestProviderNudgeProfile pins the empirically-calibrated nudge dialect. The
// values were measured against a live Codex CLI 0.132.0 — see
// docs/investigations/codex-nudge-calibration.md — and deliberately differ from Claude's:
// codex must NOT inherit DefaultNudgeProfile blindly.
func TestProviderNudgeProfile(t *testing.T) {
	n := Provider.Nudge

	// The Codex TUI opens at an empty composer; the task must be nudged in.
	if !n.NeedsInitialNudge {
		t.Error("Codex needs an initial nudge to deliver the task into the composer")
	}
	// A carriage return submits the composer.
	if n.SubmitTerminator != "\r" {
		t.Errorf("SubmitTerminator = %q, want carriage return", n.SubmitTerminator)
	}
	// Paste-burst detection means a combined body+terminator write fails to
	// submit; the split-write delay must be non-zero.
	if n.SubmitDelay <= 0 {
		t.Errorf("SubmitDelay = %v, want a non-zero split-write gap (paste-burst)", n.SubmitDelay)
	}
	if n.IdleThreshold <= 0 {
		t.Errorf("IdleThreshold = %v, want a positive idle window", n.IdleThreshold)
	}
	if n.InitialNudgeTimeout <= 0 {
		t.Errorf("InitialNudgeTimeout = %v, want a positive timeout", n.InitialNudgeTimeout)
	}

	// The profile is calibrated for Codex, not inherited from Claude: Codex's
	// faster cold start gives a shorter InitialNudgeTimeout than Claude's 60s.
	if n == agent.DefaultNudgeProfile {
		t.Error("codex.Provider.Nudge must be calibrated for Codex, " +
			"not equal to the Claude-tuned DefaultNudgeProfile")
	}
	if n.InitialNudgeTimeout >= agent.DefaultNudgeProfile.InitialNudgeTimeout {
		t.Errorf("InitialNudgeTimeout = %v; Codex's fast cold start should give "+
			"a shorter timeout than Claude's %v", n.InitialNudgeTimeout,
			agent.DefaultNudgeProfile.InitialNudgeTimeout)
	}
}

// TestProviderHooks pins Codex's hook wiring: a PostSpawnHook is required to
// dismiss the directory-trust dialog (the bypass flag does not suppress it),
// and no SessionHook is needed (Codex has no mid-session modal to dismiss).
func TestProviderHooks(t *testing.T) {
	if Provider.PostSpawnHook == nil {
		t.Error("codex.Provider.PostSpawnHook must be set (TrustDialogHook) — " +
			"--dangerously-bypass-approvals-and-sandbox does not suppress the trust dialog")
	}
	if Provider.SessionHook != nil {
		t.Error("codex.Provider.SessionHook must be nil — no mid-session modal to dismiss")
	}
}

// TestProviderNudgeValues pins the exact calibrated values so a regression in
// the measured profile is caught. See docs/investigations/codex-nudge-calibration.md.
func TestProviderNudgeValues(t *testing.T) {
	want := agent.NudgeProfile{
		NeedsInitialNudge:   true,
		InitialNudgeTimeout: 30 * time.Second,
		SubmitTerminator:    "\r",
		SubmitDelay:         50 * time.Millisecond,
		IdleThreshold:       2 * time.Second,
	}
	if Provider.Nudge != want {
		t.Errorf("Provider.Nudge = %+v, want %+v", Provider.Nudge, want)
	}
}

func TestProviderPTYSizeDefault(t *testing.T) {
	if Provider.PTYSize != nil {
		t.Errorf("Provider.PTYSize = %+v, want nil (pogo default winsize)", Provider.PTYSize)
	}
}
