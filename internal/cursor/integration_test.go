package cursor_test

// Integration coverage for the Cursor provider's config-resolution path and
// its persona-injection contract (mg-c146).
//
// TestPolecatProviderConfigPath drives the exact composition cmd/pogod runs at
// startup for a `[agents.polecat] provider = "cursor"` deployment — TOML bytes
// on disk through config.Load(), the per-type AgentProvider precedence, and
// providers.Resolve() — and asserts the result is the real cursor descriptor
// and that its command template expands to the argv a cursor polecat is
// spawned with.
//
// TestSpawnPersonaInjectionIsAdditive is the offline half of the
// AGENTS.md-collision regression. It drives pogo's real Registry.Spawn — the
// same call path a live cursor polecat takes — against a worktree carrying its
// own AGENTS.md, but with a stub binary in place of `agent`. Spawn writes the
// context file before it execs, so the injection contract is asserted without
// needing a Cursor CLI, an account, or a network: the persona lands in
// .cursor/rules/pogo-persona.mdc behind `alwaysApply: true` frontmatter, and
// the repo's AGENTS.md survives byte-identical. Whether Cursor then *reads*
// both files is asserted live in e2e_test.go.
//
// Neither test needs an `agent` binary or an env gate: both run in every plain
// `go test ./...`, and therefore in the refinery gates and GitHub CI.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/cursor"
	"github.com/drellem2/pogo/internal/providers"
)

func TestPolecatProviderConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// Neutralize env overrides that would shadow the config file.
	t.Setenv("POGO_AGENT_PROVIDER", "")
	t.Setenv("POGO_AGENT_COMMAND", "")

	pogoDir := filepath.Join(dir, "pogo")
	if err := os.MkdirAll(pogoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pogoDir, "config.toml"), []byte(`
[agents]
provider = "claude"

[agents.polecat]
provider = "cursor"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Load()

	// Per-type precedence: polecats get cursor, crew inherit the global claude.
	if got := cfg.Agents.AgentProvider("polecat"); got != "cursor" {
		t.Fatalf("AgentProvider(polecat) = %q, want cursor", got)
	}
	if got := cfg.Agents.AgentProvider("crew"); got != "claude" {
		t.Errorf("AgentProvider(crew) = %q, want claude (global)", got)
	}

	// The id maps to the real cursor descriptor — same composition as pogod's
	// resolveAgentProvider(cfg.Agents.AgentProvider(agentType)).
	p, ok := providers.Resolve(cfg.Agents.AgentProvider("polecat"))
	if !ok {
		t.Fatal("providers.Resolve returned ok=false for the configured polecat provider")
	}
	if p != &cursor.Provider {
		t.Fatalf("Resolve returned %q (%p), want the cursor.Provider descriptor (%p)",
			p.ID, p, &cursor.Provider)
	}

	// No explicit [agents] command is configured, so the provider's template
	// supplies the spawn argv.
	if got := cfg.Agents.AgentCommand("polecat"); got != "" {
		t.Fatalf("AgentCommand(polecat) = %q, want \"\" (provider template should win)", got)
	}
	argv, err := agent.ExpandCommand(p.CommandTemplate, agent.CommandTemplateVars{
		PromptFile: "/tmp/prompt.md",
		AgentName:  "cfg-test",
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

// stubCommand is a long-lived no-op process standing in for the Cursor CLI.
// Spawn writes the persona context file before exec, so the injection contract
// is observable without the real harness.
func stubCommand(t *testing.T) []string {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary to stand in for the cursor CLI: %v", err)
	}
	return []string{sleep, "30"}
}

// shortSocketDir returns a directory path short enough that "<dir>/<name>.sock"
// fits inside AF_UNIX's sun_path limit (104 bytes on darwin, 108 on linux).
// t.TempDir() on darwin returns /var/folders/... paths that exceed it on their
// own, and since mg-ef80 an unbindable attach socket is fatal to the spawn.
// Mirrors internal/agent's own unexported helper of the same name.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pogo-cursor-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// spawnWithCursorProvider runs Registry.Spawn against workDir with the real
// cursor.Provider descriptor and a stub binary, and returns the registry so the
// caller can stop it.
func spawnWithCursorProvider(t *testing.T, workDir, promptFile, name string) *agent.Registry {
	t.Helper()
	reg, err := agent.NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(5 * time.Second) })

	reg.RegisterProvider(&cursor.Provider)
	if _, err := reg.Spawn(agent.SpawnRequest{
		Name:       name,
		Type:       agent.TypePolecat,
		Command:    stubCommand(t),
		PromptFile: promptFile,
		Dir:        workDir,
		Provider:   &cursor.Provider,
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return reg
}

func TestSpawnPersonaInjectionIsAdditive(t *testing.T) {
	base := t.TempDir()
	workDir := filepath.Join(base, "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	// The worktree carries its own AGENTS.md — the file pogo must not clobber.
	const repoMarker = "POGO-CURSOR-REPO-AGENTS-MD-MARKER"
	repoAgentsMD := []byte("# repo context\n\n" + repoMarker + "\n")
	agentsMDPath := filepath.Join(workDir, "AGENTS.md")
	if err := os.WriteFile(agentsMDPath, repoAgentsMD, 0644); err != nil {
		t.Fatal(err)
	}

	const personaMarker = "POGO-CURSOR-PERSONA-MARKER"
	persona := "# pogo polecat persona\n\n" + personaMarker + "\n"
	promptFile := filepath.Join(base, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatal(err)
	}

	spawnWithCursorProvider(t, workDir, promptFile, "inject")

	// The persona landed at the nested rules path — whose parent directories
	// did not exist before the spawn.
	rulePath := filepath.Join(workDir, ".cursor", "rules", "pogo-persona.mdc")
	got, err := os.ReadFile(rulePath)
	if err != nil {
		t.Fatalf("persona rule file not written by Spawn: %v", err)
	}
	body := string(got)

	// Frontmatter first, then the persona verbatim.
	if !strings.HasPrefix(body, "---\n") {
		t.Errorf("rule file must open with YAML frontmatter, got:\n%s", body)
	}
	if !strings.Contains(body, "alwaysApply: true") {
		t.Error("rule file must declare `alwaysApply: true`, or Cursor will not " +
			"reliably fold the persona into the system prompt")
	}
	if !strings.HasSuffix(body, persona) {
		t.Errorf("rule file must end with the persona verbatim; got:\n%s", body)
	}
	if !strings.Contains(body, personaMarker) {
		t.Errorf("persona marker missing from rule file:\n%s", body)
	}

	// The repo's own AGENTS.md is untouched, byte for byte.
	after, err := os.ReadFile(agentsMDPath)
	if err != nil {
		t.Fatalf("repo AGENTS.md unreadable after spawn: %v", err)
	}
	if !bytes.Equal(after, repoAgentsMD) {
		t.Errorf("repo AGENTS.md was clobbered by persona injection; contents now:\n%s", after)
	}

	// Spawn wrote nothing into the worktree root beyond the repo's own file and
	// the .cursor tree.
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 {
		t.Errorf("persona injection wrote unexpected entries into the worktree root: %v", names)
	}
}

// TestRespawnRewritesPersona guards the respawn path: Registry.Respawn
// re-delivers the context-file persona into a worktree where .cursor/rules and
// the rule file already exist. The second write must succeed (MkdirAll on an
// existing tree) and must not double the frontmatter block.
func TestRespawnRewritesPersona(t *testing.T) {
	base := t.TempDir()
	workDir := filepath.Join(base, "worktree")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}
	promptFile := filepath.Join(base, "prompt.md")
	persona := "# persona\n\nbody\n"
	if err := os.WriteFile(promptFile, []byte(persona), 0644); err != nil {
		t.Fatal(err)
	}

	// Two spawns into the same worktree stand in for spawn-then-respawn: both
	// go through the same writeContextFilePrompt call inside Spawn.
	spawnWithCursorProvider(t, workDir, promptFile, "respawn-1")
	spawnWithCursorProvider(t, workDir, promptFile, "respawn-2")

	got, err := os.ReadFile(filepath.Join(workDir, ".cursor", "rules", "pogo-persona.mdc"))
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one frontmatter block: one opening and one closing delimiter.
	if n := strings.Count(string(got), "---\n"); n != 2 {
		t.Errorf("re-delivery produced %d frontmatter delimiters, want 2 (one block):\n%s", n, got)
	}
	if n := strings.Count(string(got), "alwaysApply: true"); n != 1 {
		t.Errorf("re-delivery produced %d alwaysApply lines, want 1", n)
	}
	if !strings.HasSuffix(string(got), persona) {
		t.Errorf("re-delivery must leave the persona verbatim at the tail:\n%s", got)
	}
}
