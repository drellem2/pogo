package codex_test

// Integration coverage for the Codex provider's persona-injection contract
// (mg-150a, hardening the mg-c146 review from mg-b9e0).
//
// TestSpawnWritesPlainContextFile drives pogo's real Registry.Spawn against the
// real codex.Provider descriptor with a stub binary in place of `codex`. Spawn
// writes the context file before it execs, so the injection contract is
// asserted without needing the Codex CLI or a network.
//
// It exists because the shared writeContextFilePrompt now supports an opt-in
// ContextFileHeader (added for Cursor's `alwaysApply: true` frontmatter). A unit
// test in internal/agent builds its own Provider literal to check "no header
// leaves the persona byte-identical" — but that literal cannot catch a header
// accidentally landing on the REAL codex.Provider descriptor. This test pins the
// real descriptor: if a ContextFileHeader is ever set on codex.Provider, Codex's
// AGENTS.override.md would gain a preamble and this assertion fails.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
	"github.com/drellem2/pogo/internal/codex"
)

// stubCommand is a long-lived no-op process standing in for the Codex CLI.
// Spawn writes the persona context file before exec, so the injection contract
// is observable without the real harness.
func stubCommand(t *testing.T) []string {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary to stand in for the codex CLI: %v", err)
	}
	return []string{sleep, "30"}
}

func TestSpawnWritesPlainContextFile(t *testing.T) {
	base := t.TempDir()
	workDir := filepath.Join(base, "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	const persona = "# pogo polecat persona\n\nPOGO-CODEX-PERSONA-MARKER\n"
	promptFile := filepath.Join(base, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(5 * time.Second) })
	reg.RegisterProvider(&codex.Provider)

	if _, err := reg.Spawn(agent.SpawnRequest{
		Name:       "inject",
		Type:       agent.TypePolecat,
		Command:    stubCommand(t),
		PromptFile: promptFile,
		Dir:        workDir,
		Provider:   &codex.Provider,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// The real codex.Provider writes the persona to AGENTS.override.md with no
	// header prepended — byte-identical to the prompt file. A ContextFileHeader
	// creeping onto the real descriptor would break this.
	got, err := os.ReadFile(filepath.Join(workDir, codex.Provider.PromptInjection.ContextFile))
	if err != nil {
		t.Fatalf("context file not written by Spawn: %v", err)
	}
	if string(got) != persona {
		t.Errorf("codex context file = %q, want the persona verbatim %q (no header on the real descriptor)", got, persona)
	}
}
