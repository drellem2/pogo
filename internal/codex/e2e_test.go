package codex_test

// End-to-end verification for the Codex provider (Phase 3B, mg-7f76,
// acceptance bar 2). It drives a real `codex` process through pogo's actual
// agent.Registry and the resolved codex.Provider — the exact pipeline a
// `provider = "codex"` polecat takes:
//
//	providers.Resolve("codex")
//	  -> ExpandCommand(provider.CommandTemplate, ...)
//	  -> Registry.Spawn  (writes the persona to AGENTS.override.md)
//	  -> initial nudge   (the task, typed into the Codex composer)
//
// It is opt-in: it spawns a real Codex CLI and makes a real OpenAI request, so
// it is skipped unless POGO_CODEX_E2E=1 and a `codex` binary is on PATH. Run:
//
//	POGO_CODEX_E2E=1 go test ./internal/codex/ -run TestCodexEndToEnd -v
//
// The pogo-side pipeline (spawn, persona injection, nudge submission) is fully
// asserted. Whether Codex's model call then succeeds depends on the OpenAI
// account having quota — see docs/investigations/codex-nudge-calibration.md.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/providers"
)

func TestCodexEndToEnd(t *testing.T) {
	if os.Getenv("POGO_CODEX_E2E") != "1" {
		t.Skip("set POGO_CODEX_E2E=1 to run the live Codex end-to-end test")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex binary not on PATH")
	}

	base := t.TempDir()
	workDir := filepath.Join(base, "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	persona := "# pogo polecat persona\n\nYou are a pogo polecat agent. " +
		"Complete the assigned task precisely, then stop.\n"
	promptFile := filepath.Join(base, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	// Resolve the provider exactly as cmd/pogod does for provider = "codex".
	provider, ok := providers.Resolve("codex")
	if !ok || provider.ID != "codex" {
		t.Fatalf("providers.Resolve(\"codex\") = (%v, %v), want the codex provider", provider, ok)
	}

	reg, err := agent.NewRegistry(filepath.Join(base, "sock"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.RegisterProvider(provider)
	reg.SetDefaultProvider(provider.ID)
	defer reg.StopAll(3 * time.Second)

	cmd, err := agent.ExpandCommand(provider.CommandTemplate, agent.CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  "e2e",
		AgentType:  string(agent.TypePolecat),
		WorkDir:    workDir,
	})
	if err != nil {
		t.Fatalf("ExpandCommand: %v", err)
	}
	t.Logf("expanded command: %v", cmd)

	_, err = reg.Spawn(agent.SpawnRequest{
		Name:         "e2e",
		Type:         agent.TypePolecat,
		Command:      cmd,
		PromptFile:   promptFile,
		Dir:          workDir,
		InitialNudge: "Reply with exactly the word PONG and then stop.",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Acceptance: the persona is injected into AGENTS.override.md.
	got, err := os.ReadFile(filepath.Join(workDir, provider.PromptInjection.ContextFile))
	if err != nil {
		t.Fatalf("persona not injected into %s: %v", provider.PromptInjection.ContextFile, err)
	}
	if string(got) != persona {
		t.Fatalf("%s content mismatch", provider.PromptInjection.ContextFile)
	}
	t.Logf("persona injected into %s (%d bytes)", provider.PromptInjection.ContextFile, len(got))

	// Acceptance: TrustDialogHook dismisses the directory-trust dialog, the
	// initial nudge reaches the composer, and Codex runs a turn on it.
	//
	// "turn ran" is the end-to-end signal: for Codex to run a turn it must
	// have (a) passed the trust dialog — the PostSpawnHook worked — and (b)
	// received and submitted the nudged task — the composer accepted it. The
	// markers below are produced only by a real model turn, NOT by the
	// MCP-server boot spinner (which also prints "esc to interrupt").
	a := reg.Get("e2e")
	if a == nil {
		t.Fatal("agent not in registry after spawn")
	}
	// Markers that only a real model turn produces — NOT the MCP-server boot
	// spinner (which also prints "esc to interrupt"). "•PONG" is Codex's
	// answer bullet for this task; the rest cover quota errors / turn footers.
	turnMarkers := []string{"•PONG", "Quota", "tokensused", "Workedfor", "Reasoning"}
	deadline := time.Now().Add(90 * time.Second)
	var collapsed string
	turnRan := false
	for time.Now().Before(deadline) {
		text := string(agent.StripANSI(a.RecentOutput(65536)))
		// Codex renders glyph-by-glyph with cursor positioning; collapse all
		// whitespace so matches do not depend on how glyphs were positioned.
		collapsed = strings.Join(strings.Fields(text), "")
		for _, m := range turnMarkers {
			if strings.Contains(collapsed, m) {
				turnRan = true
			}
		}
		if turnRan {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("codex output tail:\n%s", tail(collapsed, 1200))

	if !strings.Contains(collapsed, "ReplywithexactlythewordPONG") {
		t.Error("nudged task text did not reach Codex intact (startup race?)")
	}
	// A completed turn is itself proof the trust dialog was dismissed: Codex
	// cannot run a turn while the modal trust dialog is up. (The dialog text
	// lingers in the PTY scrollback buffer, so scanning for it would false-
	// positive — turnRan is the reliable signal.)
	if !turnRan {
		t.Error("Codex never ran a turn on the nudged task — " +
			"trust dialog, persona injection, or nudge submission failed")
	}
}

// TestCodexEndToEndNonTrivial is the Phase 3D (mg-6599) end-to-end validation.
// Where TestCodexEndToEnd proves the pipeline mechanics with a one-word reply,
// this drives a real `codex` through pogo's actual agent.Registry + the
// resolved codex.Provider on a NON-TRIVIAL task: Codex must autonomously use
// its file-editing tool to create a Go source file in a fresh worktree. It is
// the "real workload" the 3D ticket green-lights, exercised through the exact
// pipeline a `provider = "codex"` polecat takes. It proves a Codex polecat can
// complete real, multi-step work end-to-end:
//
//   - the directory-trust dialog is dismissed (TrustDialogHook),
//   - the persona reaches Codex via AGENTS.override.md,
//   - the nudged task is submitted into the composer,
//   - Codex runs turns and invokes a file-mutating tool with NO approval
//     prompt — the --dangerously-bypass-approvals-and-sandbox flag holds,
//   - the requested file lands on disk with the correct content,
//   - the composer then returns to idle (NudgeProfile calibration holds),
//   - and the registry shuts the agent down cleanly.
//
// Like TestCodexEndToEnd it is opt-in (POGO_CODEX_E2E=1 + a codex binary) and
// makes a real OpenAI request. Run:
//
//	POGO_CODEX_E2E=1 go test ./internal/codex/ -run TestCodexEndToEndNonTrivial -v
func TestCodexEndToEndNonTrivial(t *testing.T) {
	if os.Getenv("POGO_CODEX_E2E") != "1" {
		t.Skip("set POGO_CODEX_E2E=1 to run the live Codex end-to-end test")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex binary not on PATH")
	}

	base := t.TempDir()
	workDir := filepath.Join(base, "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	persona := "# pogo polecat persona\n\nYou are a pogo polecat agent. " +
		"Complete the assigned task precisely using your tools, then stop.\n"
	promptFile := filepath.Join(base, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	// Resolve the provider exactly as cmd/pogod does for provider = "codex".
	provider, ok := providers.Resolve("codex")
	if !ok || provider.ID != "codex" {
		t.Fatalf("providers.Resolve(\"codex\") = (%v, %v), want the codex provider", provider, ok)
	}

	reg, err := agent.NewRegistry(filepath.Join(base, "sock"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.RegisterProvider(provider)
	reg.SetDefaultProvider(provider.ID)
	defer reg.StopAll(3 * time.Second) // safety net; explicit StopAll below

	cmd, err := agent.ExpandCommand(provider.CommandTemplate, agent.CommandTemplateVars{
		PromptFile: promptFile,
		AgentName:  "e2e-nt",
		AgentType:  string(agent.TypePolecat),
		WorkDir:    workDir,
	})
	if err != nil {
		t.Fatalf("ExpandCommand: %v", err)
	}
	t.Logf("expanded command: %v", cmd)

	// A non-trivial task: Codex must create a source file with specific
	// content — it has to run real tool calls, not just print a reply.
	const task = "In your current working directory, create a file named " +
		"add.go. It must begin with the line `package main` and contain a Go " +
		"function with the exact signature `func Add(a, b int) int` that " +
		"returns the sum a + b. Create only that one file, then stop."

	_, err = reg.Spawn(agent.SpawnRequest{
		Name:         "e2e-nt",
		Type:         agent.TypePolecat,
		Command:      cmd,
		PromptFile:   promptFile,
		Dir:          workDir,
		InitialNudge: task,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Acceptance: the persona is injected into AGENTS.override.md.
	injected, err := os.ReadFile(filepath.Join(workDir, provider.PromptInjection.ContextFile))
	if err != nil {
		t.Fatalf("persona not injected into %s: %v", provider.PromptInjection.ContextFile, err)
	}
	if string(injected) != persona {
		t.Fatalf("%s content mismatch", provider.PromptInjection.ContextFile)
	}
	t.Logf("persona injected into %s (%d bytes)", provider.PromptInjection.ContextFile, len(injected))

	a := reg.Get("e2e-nt")
	if a == nil {
		t.Fatal("agent not in registry after spawn")
	}

	// Acceptance: Codex completes the task — add.go appears on disk with the
	// requested content. The file landing is itself end-to-end proof: Codex
	// could only write it after the trust dialog was dismissed, the nudge
	// reached the composer, and a file-mutating tool call ran without an
	// approval prompt (--dangerously-bypass-approvals-and-sandbox held).
	addFile := filepath.Join(workDir, "add.go")
	deadline := time.Now().Add(180 * time.Second)
	var content string
	done := false
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(addFile); err == nil && len(b) > 0 {
			content = string(b)
			done = true
			break
		}
		time.Sleep(3 * time.Second)
	}
	if !done {
		out := string(agent.StripANSI(a.RecentOutput(65536)))
		t.Fatalf("Codex did not create add.go within the deadline — trust "+
			"dialog, persona injection, nudge submission, or the tool call "+
			"failed.\ncodex output tail:\n%s", tail(out, 2000))
	}

	// Collapse whitespace before asserting: Codex picks its own formatting
	// (gofmt, indentation); the checks are on structure, not layout.
	collapsed := strings.Join(strings.Fields(content), "")
	t.Logf("add.go (%d bytes):\n%s", len(content), content)
	if !strings.Contains(collapsed, "packagemain") {
		t.Error("add.go missing `package main`")
	}
	if !strings.Contains(collapsed, "funcAdd(a,bint)int") {
		t.Error("add.go missing the `func Add(a, b int) int` signature")
	}
	if !strings.Contains(collapsed, "a+b") {
		t.Error("add.go does not appear to return a + b")
	}

	// Acceptance (NudgeProfile calibration under a real dispatch): once the
	// task is done the ratatui composer returns to idle and emits no PTY
	// output — the calibration doc's "a settled composer emits zero bytes".
	// A still-growing buffer would mean Codex never settled.
	before := len(a.RecentOutput(1 << 20))
	time.Sleep(5 * time.Second)
	after := len(a.RecentOutput(1 << 20))
	t.Logf("idle check: PTY buffer %d -> %d bytes over 5s", before, after)
	if after-before > 4096 {
		t.Errorf("Codex composer did not settle after the task — buffer grew "+
			"%d bytes in 5s idle", after-before)
	}

	// Acceptance: the registry shuts the agent down cleanly. Codex's TUI does
	// not self-exit when idle, so "exits cleanly" means StopAll terminates it
	// gracefully within the timeout.
	stopped := make(chan struct{})
	go func() {
		reg.StopAll(10 * time.Second)
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Log("registry stopped the Codex agent cleanly")
	case <-time.After(15 * time.Second):
		t.Error("registry did not stop the Codex agent within 15s — unclean shutdown")
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
