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
// account having quota — see docs/codex-nudge-calibration.md.

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
	reg.SetProvider(provider)
	reg.SetPostSpawnHook(provider.PostSpawnHook)
	reg.SetSessionHook(provider.SessionHook)
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

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
