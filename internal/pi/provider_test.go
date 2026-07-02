package pi

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

func TestProviderIdentity(t *testing.T) {
	if Provider.ID != "pi" {
		t.Errorf("Provider.ID = %q, want \"pi\"", Provider.ID)
	}
	if Provider.Binary != "pi" {
		t.Errorf("Provider.Binary = %q, want \"pi\"", Provider.Binary)
	}
}

// TestProviderCommandHasApproveFlag guards the autonomy contract: the pi
// command template must keep --approve so polecats in a fresh worktree of a
// repo that carries project-local .pi/ resources are never blocked by pi's
// "Trust project folder?" dialog.
func TestProviderCommandHasApproveFlag(t *testing.T) {
	if !strings.Contains(Provider.CommandTemplate, "--approve") {
		t.Fatal("pi.Provider.CommandTemplate must include --approve so the " +
			"project-trust dialog never blocks unattended startup")
	}
}

func TestProviderNonInteractiveFlags(t *testing.T) {
	if len(Provider.NonInteractiveFlags) == 0 {
		t.Fatal("pi.Provider must declare at least one non-interactive flag")
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

// TestProviderPromptInjection pins the AppendFlag strategy: pi's
// --append-system-prompt accepts a file path and reads its contents into the
// system prompt, so the persona is injected Claude-style — no context file is
// written into the worktree and the repo's own AGENTS.md keeps loading as
// project context.
func TestProviderPromptInjection(t *testing.T) {
	if Provider.PromptInjection.Kind != agent.InjectAppendFlag {
		t.Errorf("PromptInjection.Kind = %d, want InjectAppendFlag",
			Provider.PromptInjection.Kind)
	}
	if Provider.PromptInjection.Flag != "--append-system-prompt" {
		t.Errorf("PromptInjection.Flag = %q, want \"--append-system-prompt\"",
			Provider.PromptInjection.Flag)
	}
	// The flag must actually appear in the default template, carrying the
	// prompt file.
	if !strings.Contains(Provider.CommandTemplate, "--append-system-prompt {{.PromptFile}}") {
		t.Errorf("CommandTemplate %q does not deliver the prompt file via --append-system-prompt",
			Provider.CommandTemplate)
	}
}

// TestProviderNudgeProfile pins the empirically-calibrated nudge dialect. The
// values were measured against a live pi 0.80.3 — see
// docs/investigations/pi-nudge-calibration.md — and deliberately differ from
// Claude's: pi must NOT inherit DefaultNudgeProfile blindly.
func TestProviderNudgeProfile(t *testing.T) {
	n := Provider.Nudge

	// The pi TUI opens at an empty composer; the task must be nudged in.
	if !n.NeedsInitialNudge {
		t.Error("pi needs an initial nudge to deliver the task into the composer")
	}
	// A carriage return submits the composer.
	if n.SubmitTerminator != "\r" {
		t.Errorf("SubmitTerminator = %q, want carriage return", n.SubmitTerminator)
	}
	if n.SubmitDelay <= 0 {
		t.Errorf("SubmitDelay = %v, want a non-zero split-write gap (uniform Agent.Nudge path)", n.SubmitDelay)
	}
	if n.IdleThreshold <= 0 {
		t.Errorf("IdleThreshold = %v, want a positive idle window", n.IdleThreshold)
	}
	if n.InitialNudgeTimeout <= 0 {
		t.Errorf("InitialNudgeTimeout = %v, want a positive timeout", n.InitialNudgeTimeout)
	}

	// The profile is calibrated for pi, not inherited from Claude: pi's ~1.5s
	// TUI render gives a shorter InitialNudgeTimeout than Claude/Ink's 60s.
	if n == agent.DefaultNudgeProfile {
		t.Error("pi.Provider.Nudge must be calibrated for pi, " +
			"not equal to the Claude-tuned DefaultNudgeProfile")
	}
	if n.InitialNudgeTimeout >= agent.DefaultNudgeProfile.InitialNudgeTimeout {
		t.Errorf("InitialNudgeTimeout = %v; pi's fast TUI render should give "+
			"a shorter timeout than Claude's %v", n.InitialNudgeTimeout,
			agent.DefaultNudgeProfile.InitialNudgeTimeout)
	}
	// pi's pre-TUI startup is silent, so a quiescence-only initial-nudge gate
	// would fire before the input loop exists (mg-ce61) — a sentinel is
	// required.
	if n.PromptReadySentinel == "" {
		t.Error("pi.Provider.Nudge.PromptReadySentinel must be set: pi's " +
			"~1.5s of silent pre-TUI startup defeats a quiescence-only gate")
	}
	// And it must not be Claude's sentinel: pi renders no "? for shortcuts".
	if n.PromptReadySentinel == agent.DefaultNudgeProfile.PromptReadySentinel {
		t.Error("pi.Provider.Nudge.PromptReadySentinel must be pi's own hint " +
			"text, not Claude's")
	}
}

// TestProviderHooks pins pi's hook wiring: no PostSpawnHook (--approve
// suppresses the project-trust dialog before it can render) and no SessionHook
// (pi has no mid-session modal — errors render inline and tools run without
// approval prompts).
func TestProviderHooks(t *testing.T) {
	if Provider.PostSpawnHook != nil {
		t.Error("pi.Provider.PostSpawnHook must be nil — --approve suppresses " +
			"the trust dialog, so there is nothing to dismiss")
	}
	if Provider.SessionHook != nil {
		t.Error("pi.Provider.SessionHook must be nil — no mid-session modal to dismiss")
	}
}

// TestProviderNudgeValues pins the exact calibrated values so a regression in
// the measured profile is caught. See docs/investigations/pi-nudge-calibration.md.
func TestProviderNudgeValues(t *testing.T) {
	want := agent.NudgeProfile{
		NeedsInitialNudge:   true,
		InitialNudgeTimeout: 30 * time.Second,
		SubmitTerminator:    "\r",
		SubmitDelay:         50 * time.Millisecond,
		IdleThreshold:       2 * time.Second,
		PromptReadySentinel: "/ commands · ! bash",
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
