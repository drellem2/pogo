package pi_test

// Integration coverage for the pi provider's config-resolution path (mg-3e7c).
//
// TestPolecatProviderConfigPath drives the exact composition cmd/pogod runs at
// startup for a `[agents.polecat] provider = "pi"` deployment — TOML bytes on
// disk through config.Load(), the per-type AgentProvider precedence, and
// providers.Resolve() — and asserts the result is the real pi descriptor and
// that its command template expands to the argv a pi polecat is spawned with.
//
// Unlike the live tests in e2e_test.go this needs no pi binary and no env
// gate: it runs in every `go test ./...` (and therefore in the refinery gates
// and GitHub CI). The registry side of the same chain — Registry.Spawn
// resolving pi through the config tier — needs a real spawn, so it is
// asserted in TestPiEndToEnd via Agent.ProviderID.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/pi"
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
provider = "pi"
`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Load()

	// Per-type precedence: polecats get pi, crew inherit the global claude.
	if got := cfg.Agents.AgentProvider("polecat"); got != "pi" {
		t.Fatalf("AgentProvider(polecat) = %q, want pi", got)
	}
	if got := cfg.Agents.AgentProvider("crew"); got != "claude" {
		t.Errorf("AgentProvider(crew) = %q, want claude (global)", got)
	}

	// The id maps to the real pi descriptor — same composition as pogod's
	// resolveAgentProvider(cfg.Agents.AgentProvider(agentType)).
	p, ok := providers.Resolve(cfg.Agents.AgentProvider("polecat"))
	if !ok {
		t.Fatal("providers.Resolve returned ok=false for the configured polecat provider")
	}
	if p != &pi.Provider {
		t.Fatalf("Resolve returned %q (%p), want the pi.Provider descriptor (%p)",
			p.ID, p, &pi.Provider)
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
	want := []string{"pi", "--approve", "--append-system-prompt", "/tmp/prompt.md"}
	if len(argv) != len(want) {
		t.Fatalf("expanded argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("expanded argv = %v, want %v", argv, want)
		}
	}
}
