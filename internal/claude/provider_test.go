package claude

import (
	"reflect"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

func TestProviderIdentity(t *testing.T) {
	if Provider.ID != "claude" {
		t.Errorf("Provider.ID = %q, want \"claude\"", Provider.ID)
	}
	if Provider.Binary != "claude" {
		t.Errorf("Provider.Binary = %q, want \"claude\"", Provider.Binary)
	}
}

// TestProviderCommandHasPermissionsSkip is the relocated guard from the old
// agent.TestDefaultCommandHasPermissionsSkip: the Claude command template must
// keep --dangerously-skip-permissions so polecats run unattended in
// freshly-created worktree directories that Claude Code has never seen.
func TestProviderCommandHasPermissionsSkip(t *testing.T) {
	if !strings.Contains(Provider.CommandTemplate, "--dangerously-skip-permissions") {
		t.Fatal("claude.Provider.CommandTemplate must include --dangerously-skip-permissions " +
			"for autonomous polecat execution in new worktree directories")
	}
}

func TestProviderNonInteractiveFlags(t *testing.T) {
	if len(Provider.NonInteractiveFlags) == 0 {
		t.Fatal("claude.Provider must declare at least one non-interactive flag")
	}
	want := "--dangerously-skip-permissions"
	found := false
	for _, f := range Provider.NonInteractiveFlags {
		if f == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Provider.NonInteractiveFlags = %v, must include %q", Provider.NonInteractiveFlags, want)
	}
	// Every required non-interactive flag must actually appear in the default
	// command template — otherwise ValidatePolecatCommand would warn on the
	// provider's own default.
	for _, f := range Provider.NonInteractiveFlags {
		if !strings.Contains(Provider.CommandTemplate, f) {
			t.Errorf("CommandTemplate %q is missing declared non-interactive flag %q",
				Provider.CommandTemplate, f)
		}
	}
}

func TestProviderPromptInjection(t *testing.T) {
	if Provider.PromptInjection.Kind != agent.InjectAppendFlag {
		t.Errorf("PromptInjection.Kind = %d, want InjectAppendFlag", Provider.PromptInjection.Kind)
	}
	if Provider.PromptInjection.Flag != "--append-system-prompt-file" {
		t.Errorf("PromptInjection.Flag = %q, want \"--append-system-prompt-file\"",
			Provider.PromptInjection.Flag)
	}
	// The injection flag must match the command template's actual flag.
	if !strings.Contains(Provider.CommandTemplate, Provider.PromptInjection.Flag) {
		t.Errorf("CommandTemplate %q does not use PromptInjection.Flag %q",
			Provider.CommandTemplate, Provider.PromptInjection.Flag)
	}
}

// TestProviderNudgeProfile pins phase-3A's behavior-preservation contract: the
// Claude provider must carry exactly the nudge dialect pogo used before the
// Provider abstraction (DefaultNudgeProfile).
func TestProviderNudgeProfile(t *testing.T) {
	if !reflect.DeepEqual(Provider.Nudge, agent.DefaultNudgeProfile) {
		t.Errorf("Provider.Nudge = %+v, want DefaultNudgeProfile %+v",
			Provider.Nudge, agent.DefaultNudgeProfile)
	}
	if !Provider.Nudge.NeedsInitialNudge {
		t.Error("Claude needs an initial nudge to bypass its interactive prompt")
	}
	if Provider.Nudge.SubmitTerminator != "\r" {
		t.Errorf("SubmitTerminator = %q, want carriage return", Provider.Nudge.SubmitTerminator)
	}
}

func TestProviderHooks(t *testing.T) {
	if Provider.PostSpawnHook == nil {
		t.Error("Provider.PostSpawnHook must be set (TrustDialogHook)")
	}
	if Provider.SessionHook == nil {
		t.Error("Provider.SessionHook must be set (ModalHook)")
	}
}

// TestProviderPTYSizeDefault confirms Claude uses pogo's default winsize — a
// nil PTYSize — rather than overriding it.
func TestProviderPTYSizeDefault(t *testing.T) {
	if Provider.PTYSize != nil {
		t.Errorf("Provider.PTYSize = %+v, want nil (pogo default winsize)", Provider.PTYSize)
	}
}
