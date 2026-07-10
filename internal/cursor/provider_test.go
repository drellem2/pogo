package cursor

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

func TestProviderIdentity(t *testing.T) {
	if Provider.ID != "cursor" {
		t.Errorf("Provider.ID = %q, want \"cursor\"", Provider.ID)
	}
	// The CLI was renamed cursor-agent -> agent in 2026. Pin the expectation:
	// a closed-source CLI that churns this fast should fail loudly here rather
	// than silently PATH-miss at spawn.
	if Provider.Binary != "agent" {
		t.Errorf("Provider.Binary = %q, want \"agent\" (renamed from cursor-agent in 2026)",
			Provider.Binary)
	}
}

// TestProviderCommandHasForceFlag guards the autonomy contract: the Cursor
// command template must keep --force so polecats in a fresh worktree are never
// blocked by a tool-approval prompt.
func TestProviderCommandHasForceFlag(t *testing.T) {
	if !strings.Contains(Provider.CommandTemplate, "--force") {
		t.Fatal("cursor.Provider.CommandTemplate must include --force so tool " +
			"approvals never block unattended execution")
	}
}

// TestProviderCommandOmitsTrustFlag pins a measured Cursor constraint: --trust
// is only accepted with --print/headless. Putting it in the interactive
// template makes the CLI exit non-zero before the TUI renders ("Error: --trust
// can only be used with --print/headless mode"), so every polecat would die at
// spawn. Workspace trust is handled by TrustDialogHook instead.
func TestProviderCommandOmitsTrustFlag(t *testing.T) {
	if strings.Contains(Provider.CommandTemplate, "--trust") {
		t.Fatal("cursor.Provider.CommandTemplate must NOT include --trust — " +
			"Cursor rejects it outside --print mode and exits before the TUI renders; " +
			"TrustDialogHook dismisses the dialog instead")
	}
}

func TestProviderNonInteractiveFlags(t *testing.T) {
	if len(Provider.NonInteractiveFlags) == 0 {
		t.Fatal("cursor.Provider must declare at least one non-interactive flag")
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

// TestProviderPromptInjection pins the ContextFile strategy and, critically,
// the rules-file escape hatch: Cursor has no --append-system-prompt equivalent,
// and injecting through AGENTS.md would clobber a file most repos own. The
// persona goes to a uniquely-named .cursor/rules/*.mdc instead.
func TestProviderPromptInjection(t *testing.T) {
	pi := Provider.PromptInjection
	if pi.Kind != agent.InjectContextFile {
		t.Errorf("PromptInjection.Kind = %d, want InjectContextFile", pi.Kind)
	}
	if pi.Flag != "" {
		t.Errorf("PromptInjection.Flag = %q, want empty — Cursor has no "+
			"--append-system-prompt equivalent", pi.Flag)
	}
	if pi.ContextFile != ".cursor/rules/pogo-persona.mdc" {
		t.Errorf("PromptInjection.ContextFile = %q, want .cursor/rules/pogo-persona.mdc",
			pi.ContextFile)
	}
	// The collision wrinkle: the persona must never be delivered through a file
	// the repo conventionally owns.
	for _, repoOwned := range []string{"AGENTS.md", "CLAUDE.md", "CLAUDE.local.md", ".cursorrules"} {
		if pi.ContextFile == repoOwned {
			t.Errorf("PromptInjection.ContextFile = %q — persona must not be "+
				"delivered through a repo-owned context file", repoOwned)
		}
	}
	// The template must carry no prompt-file flag: injection is out-of-band.
	if strings.Contains(Provider.CommandTemplate, "{{.PromptFile}}") {
		t.Errorf("CommandTemplate %q must not reference the prompt file — "+
			"Cursor injects via the rules context file", Provider.CommandTemplate)
	}
}

// TestProviderContextFileHeader pins the measured frontmatter contract: a .mdc
// with no frontmatter is silently ignored by Cursor, and only `alwaysApply:
// true` guarantees the rule reaches the system prompt. Losing this header is a
// silent persona-delivery failure, so it is asserted field by field.
func TestProviderContextFileHeader(t *testing.T) {
	h := Provider.PromptInjection.ContextFileHeader
	if h == "" {
		t.Fatal("cursor.Provider.PromptInjection.ContextFileHeader must be set — " +
			"a .mdc without YAML frontmatter is silently ignored by Cursor")
	}
	if !strings.HasPrefix(h, "---\n") {
		t.Errorf("ContextFileHeader must open a YAML frontmatter block, got %q", h)
	}
	if !strings.Contains(h, "alwaysApply: true") {
		t.Error("ContextFileHeader must declare `alwaysApply: true` — " +
			"`alwaysApply: false` leaves attachment to the model's judgement")
	}
	// The frontmatter block must be closed, and the persona body must start on
	// its own line after it.
	if strings.Count(h, "---\n") != 2 {
		t.Errorf("ContextFileHeader must open and close the frontmatter block, got %q", h)
	}
	if !strings.HasSuffix(h, "\n\n") {
		t.Errorf("ContextFileHeader must end with a blank line so the persona "+
			"body does not run into the frontmatter terminator, got %q", h)
	}
}

// TestProviderNudgeProfile pins the empirically-calibrated nudge dialect. The
// values were measured against a live Cursor CLI 2026.07.09-a3815c0 — see
// docs/investigations/cursor-nudge-calibration.md — and deliberately differ
// from Claude's: Cursor must NOT inherit DefaultNudgeProfile blindly.
func TestProviderNudgeProfile(t *testing.T) {
	n := Provider.Nudge

	// The initial task arrives via argv; a typed nudge would race the silent
	// workspace-trust dialog.
	if n.NeedsInitialNudge {
		t.Error("cursor must not take the PTY initial-nudge path — the silent " +
			"workspace-trust dialog reads as idle; the task is delivered via argv")
	}
	if n.SubmitTerminator != "\r" {
		t.Errorf("SubmitTerminator = %q, want carriage return", n.SubmitTerminator)
	}
	// Load-bearing for Cursor specifically: a combined body+"\r" write does not
	// submit (paste-burst detection), so the split-write gap must be non-zero.
	if n.SubmitDelay <= 0 {
		t.Errorf("SubmitDelay = %v, want a non-zero split-write gap — Cursor's "+
			"composer swallows a combined body+CR write", n.SubmitDelay)
	}
	if n.IdleThreshold <= 0 {
		t.Errorf("IdleThreshold = %v, want a positive idle window", n.IdleThreshold)
	}
	if n.InitialNudgeTimeout <= 0 {
		t.Errorf("InitialNudgeTimeout = %v, want a positive timeout", n.InitialNudgeTimeout)
	}

	// The profile is calibrated for Cursor, not inherited from Claude.
	if n == agent.DefaultNudgeProfile {
		t.Error("cursor.Provider.Nudge must be calibrated for Cursor, " +
			"not equal to the Claude-tuned DefaultNudgeProfile")
	}
	if n.InitialNudgeTimeout >= agent.DefaultNudgeProfile.InitialNudgeTimeout {
		t.Errorf("InitialNudgeTimeout = %v; Cursor's ~3s composer render should give "+
			"a shorter timeout than Claude's %v", n.InitialNudgeTimeout,
			agent.DefaultNudgeProfile.InitialNudgeTimeout)
	}
	// The sentinel is unused for the initial prompt (argv delivery) but
	// retained as the measured "input loop ready" marker.
	if n.PromptReadySentinel == "" {
		t.Error("cursor.Provider.Nudge.PromptReadySentinel must be set: it is the " +
			"measured input-loop-ready marker (see cursor-nudge-calibration.md)")
	}
	if n.PromptReadySentinel == agent.DefaultNudgeProfile.PromptReadySentinel {
		t.Error("cursor.Provider.Nudge.PromptReadySentinel must be Cursor's own " +
			"composer placeholder, not Claude's")
	}
}

// TestProviderNudgeValues pins the exact calibrated values so a regression in
// the measured profile is caught. See docs/investigations/cursor-nudge-calibration.md.
func TestProviderNudgeValues(t *testing.T) {
	want := agent.NudgeProfile{
		NeedsInitialNudge:   false, // initial task arrives via argv
		InitialNudgeTimeout: 30 * time.Second,
		SubmitTerminator:    "\r",
		SubmitDelay:         50 * time.Millisecond,
		IdleThreshold:       2 * time.Second,
		PromptReadySentinel: "Plan, search, build anything",
	}
	if Provider.Nudge != want {
		t.Errorf("Provider.Nudge = %+v, want %+v", Provider.Nudge, want)
	}
}

// TestProviderInitialPromptViaArgv pins argv delivery: Cursor accepts trailing
// positional prompts (`agent [options] [prompt...]`), so the initial task is
// appended to the spawn argv by Registry.Spawn instead of typed into the
// composer while the silent trust dialog still owns the screen. The two fields
// must stay paired: argv delivery on, nudge path off — both set would deliver
// the task twice, both unset would never deliver it at all.
func TestProviderInitialPromptViaArgv(t *testing.T) {
	if !Provider.InitialPromptViaArgv {
		t.Error("cursor.Provider.InitialPromptViaArgv must be true — a typed " +
			"initial nudge races the silent workspace-trust dialog")
	}
	if Provider.Nudge.NeedsInitialNudge {
		t.Error("NeedsInitialNudge must be false when InitialPromptViaArgv is " +
			"set, or the task would be delivered twice")
	}
}

// TestProviderHooks pins Cursor's hook wiring: a PostSpawnHook is REQUIRED
// (neither --force nor --trust suppresses the workspace-trust dialog in the
// TUI), and no SessionHook (with --force there is no mid-session modal).
func TestProviderHooks(t *testing.T) {
	if Provider.PostSpawnHook == nil {
		t.Error("cursor.Provider.PostSpawnHook must be set — --force does not " +
			"suppress the workspace-trust dialog and --trust is TUI-invalid")
	}
	if Provider.SessionHook != nil {
		t.Error("cursor.Provider.SessionHook must be nil — with --force there is " +
			"no mid-session modal to dismiss")
	}
}

func TestProviderPTYSizeDefault(t *testing.T) {
	if Provider.PTYSize != nil {
		t.Errorf("Provider.PTYSize = %+v, want nil (pogo default winsize)", Provider.PTYSize)
	}
}

// TestExpandedCommand pins the argv a Cursor polecat is actually spawned with.
func TestExpandedCommand(t *testing.T) {
	argv, err := agent.ExpandCommand(Provider.CommandTemplate, agent.CommandTemplateVars{
		PromptFile: "/tmp/prompt.md",
		AgentName:  "c1",
		AgentType:  string(agent.TypePolecat),
		WorkDir:    "/tmp/work",
	})
	if err != nil {
		t.Fatalf("ExpandCommand: %v", err)
	}
	want := []string{"agent", "--force"}
	if len(argv) != len(want) {
		t.Fatalf("expanded argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("expanded argv = %v, want %v", argv, want)
		}
	}
}
