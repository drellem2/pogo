package agent

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// providerConfig is a test AgentCommandConfig with controllable per-type
// command and provider returns, for exercising the per-spawn provider
// resolution chain (mg-b31b). An empty field means "unset" — the resolver
// then falls through to the next precedence tier.
type providerConfig struct {
	crewCommand     string
	polecatCommand  string
	crewProvider    string
	polecatProvider string
}

func (c providerConfig) AgentCommand(agentType string) string {
	if agentType == "crew" {
		return c.crewCommand
	}
	return c.polecatCommand
}

func (c providerConfig) AgentProvider(agentType string) string {
	if agentType == "crew" {
		return c.crewProvider
	}
	return c.polecatProvider
}

// testProvider builds a minimal Provider with a distinctive nudge
// IdleThreshold so agents resolved to it can be told apart at spawn time.
func testProvider(id string, idle time.Duration) *Provider {
	return &Provider{
		ID:     id,
		Binary: id,
		Nudge:  NudgeProfile{SubmitTerminator: "\r", IdleThreshold: idle},
	}
}

// newResolutionRegistry returns a registry with claude + codex test providers
// registered (distinct nudge thresholds: claude 1s, codex 9s).
func newResolutionRegistry(t *testing.T) (reg *Registry, claudeP, codexP *Provider) {
	t.Helper()
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	claudeP = testProvider("claude", 1*time.Second)
	codexP = testProvider("codex", 9*time.Second)
	reg.RegisterProvider(claudeP)
	reg.RegisterProvider(codexP)
	return reg, claudeP, codexP
}

// recvWithin reads one value from ch or fails the test after d.
func recvWithin(t *testing.T, ch <-chan string, d time.Duration) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(d):
		t.Fatal("timed out waiting for channel value")
		return ""
	}
}

// TestResolveProviderPrecedence verifies the full per-spawn precedence chain
// (mg-b31b acceptance bars 1 + 4): flag > frontmatter > per-type config >
// global default > built-in default.
func TestResolveProviderPrecedence(t *testing.T) {
	t.Run("flag wins over everything", func(t *testing.T) {
		reg, _, codexP := newResolutionRegistry(t)
		reg.SetCommandConfig(providerConfig{polecatProvider: "claude"})
		reg.SetDefaultProvider("claude")
		p, err := reg.resolveProvider(TypePolecat, "codex", "claude")
		if err != nil {
			t.Fatal(err)
		}
		if p != codexP {
			t.Errorf("--provider flag not honored: got %q", p.ID)
		}
	})

	t.Run("frontmatter beats per-type config", func(t *testing.T) {
		reg, _, codexP := newResolutionRegistry(t)
		reg.SetCommandConfig(providerConfig{polecatProvider: "claude"})
		p, err := reg.resolveProvider(TypePolecat, "", "codex")
		if err != nil {
			t.Fatal(err)
		}
		if p != codexP {
			t.Errorf("provider: frontmatter not honored: got %q", p.ID)
		}
	})

	t.Run("per-type config beats global default", func(t *testing.T) {
		reg, claudeP, codexP := newResolutionRegistry(t)
		reg.SetCommandConfig(providerConfig{polecatProvider: "codex", crewProvider: "claude"})
		reg.SetDefaultProvider("claude")
		p, err := reg.resolveProvider(TypePolecat, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if p != codexP {
			t.Errorf("per-type [agents.polecat] provider not honored: got %q", p.ID)
		}
		// crew, with its own per-type value, resolves independently — proving
		// per-type selection is what enables a mixed fleet.
		cp, err := reg.resolveProvider(TypeCrew, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if cp != claudeP {
			t.Errorf("per-type [agents.crew] provider not honored: got %q", cp.ID)
		}
	})

	t.Run("global default when config empty", func(t *testing.T) {
		reg, _, codexP := newResolutionRegistry(t)
		reg.SetDefaultProvider("codex")
		p, err := reg.resolveProvider(TypePolecat, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if p != codexP {
			t.Errorf("global default not honored: got %q", p.ID)
		}
	})

	t.Run("built-in default when nothing is set", func(t *testing.T) {
		reg, claudeP, _ := newResolutionRegistry(t)
		p, err := reg.resolveProvider(TypePolecat, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if p != claudeP {
			t.Errorf("built-in default should be claude, got %q", p.ID)
		}
	})
}

// TestResolveProviderUnknownFlagFailsFast verifies an unknown --provider flag
// value is a hard error — never a silent wrong-provider spawn (bar 8).
func TestResolveProviderUnknownFlagFailsFast(t *testing.T) {
	reg, _, _ := newResolutionRegistry(t)
	if _, err := reg.resolveProvider(TypePolecat, "bogus", ""); err == nil {
		t.Fatal("expected a hard error for an unknown --provider flag value")
	}
}

// TestResolveProviderUnknownConfigFallsBack verifies an unknown provider id
// from config warns and falls back to the built-in default rather than
// erroring or silently spawning the wrong harness (bar 8).
func TestResolveProviderUnknownConfigFallsBack(t *testing.T) {
	reg, claudeP, _ := newResolutionRegistry(t)
	reg.SetCommandConfig(providerConfig{polecatProvider: "bogus"})
	p, err := reg.resolveProvider(TypePolecat, "", "")
	if err != nil {
		t.Fatalf("an unknown config value should warn+fall back, not error: %v", err)
	}
	if p != claudeP {
		t.Errorf("unknown config value should fall back to claude, got %q", p.ID)
	}
}

// TestResolveProviderUnknownFrontmatterFallsBack verifies an unknown provider
// id from prompt frontmatter warns and falls back, like config (bar 8).
func TestResolveProviderUnknownFrontmatterFallsBack(t *testing.T) {
	reg, claudeP, _ := newResolutionRegistry(t)
	p, err := reg.resolveProvider(TypePolecat, "", "bogus")
	if err != nil {
		t.Fatalf("an unknown frontmatter value should warn+fall back, not error: %v", err)
	}
	if p != claudeP {
		t.Errorf("unknown frontmatter value should fall back to claude, got %q", p.ID)
	}
}

// TestResolveProviderBareRegistry verifies a registry with no providers
// registered resolves to a nil provider — the degenerate unit-test path that
// lets Spawn fall back to pogo's built-in nudge/PTY defaults.
func TestResolveProviderBareRegistry(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	p, err := reg.resolveProvider(TypePolecat, "", "")
	if err != nil {
		t.Fatalf("bare-registry resolve should not error: %v", err)
	}
	if p != nil {
		t.Errorf("bare registry should resolve to a nil provider, got %q", p.ID)
	}
}

// TestSpawnResolvesProviderPerSpawn is the headline mixed-fleet check
// (mg-b31b acceptance bars 3 + 5): two polecats spawned back-to-back with
// different providers each get their own — no global caching, no
// cross-contamination — while the fleet default stays claude.
func TestSpawnResolvesProviderPerSpawn(t *testing.T) {
	reg, claudeP, codexP := newResolutionRegistry(t)
	defer reg.StopAll(2 * time.Second)
	reg.SetDefaultProvider("claude") // fleet default = claude

	a, err := reg.Spawn(SpawnRequest{
		Name: "p-claude", Type: TypePolecat, Command: []string{"cat"}, Provider: claudeP,
	})
	if err != nil {
		t.Fatalf("Spawn p-claude: %v", err)
	}
	b, err := reg.Spawn(SpawnRequest{
		Name: "p-codex", Type: TypePolecat, Command: []string{"cat"}, Provider: codexP,
	})
	if err != nil {
		t.Fatalf("Spawn p-codex: %v", err)
	}

	if a.provider != claudeP {
		t.Errorf("agent p-claude resolved to %v, want claude", a.provider)
	}
	if b.provider != codexP {
		t.Errorf("agent p-codex resolved to %v, want codex", b.provider)
	}
	// The nudge dialect travels with the resolved provider, per-spawn.
	if a.nudge.IdleThreshold != 1*time.Second {
		t.Errorf("p-claude nudge IdleThreshold = %v, want 1s", a.nudge.IdleThreshold)
	}
	if b.nudge.IdleThreshold != 9*time.Second {
		t.Errorf("p-codex nudge IdleThreshold = %v, want 9s", b.nudge.IdleThreshold)
	}
}

// TestSpawnRunsResolvedProviderHooks verifies PostSpawnHook and SessionHook
// run per-spawn off the agent's resolved provider, not a registry global
// (mg-b31b acceptance bar 6).
func TestSpawnRunsResolvedProviderHooks(t *testing.T) {
	reg, claudeP, codexP := newResolutionRegistry(t)
	defer reg.StopAll(2 * time.Second)

	postCh := make(chan string, 4)
	sessCh := make(chan string, 4)
	claudeP.PostSpawnHook = func(a *Agent) { postCh <- "claude:" + a.Name }
	claudeP.SessionHook = func(_ context.Context, a *Agent) { sessCh <- "claude:" + a.Name }
	codexP.PostSpawnHook = func(a *Agent) { postCh <- "codex:" + a.Name }
	codexP.SessionHook = func(_ context.Context, a *Agent) { sessCh <- "codex:" + a.Name }

	if _, err := reg.Spawn(SpawnRequest{
		Name: "h-codex", Type: TypePolecat, Command: []string{"cat"}, Provider: codexP,
	}); err != nil {
		t.Fatalf("Spawn h-codex: %v", err)
	}
	if got := recvWithin(t, postCh, 2*time.Second); got != "codex:h-codex" {
		t.Errorf("PostSpawnHook = %q, want codex:h-codex", got)
	}
	if got := recvWithin(t, sessCh, 2*time.Second); got != "codex:h-codex" {
		t.Errorf("SessionHook = %q, want codex:h-codex", got)
	}

	if _, err := reg.Spawn(SpawnRequest{
		Name: "h-claude", Type: TypePolecat, Command: []string{"cat"}, Provider: claudeP,
	}); err != nil {
		t.Fatalf("Spawn h-claude: %v", err)
	}
	if got := recvWithin(t, postCh, 2*time.Second); got != "claude:h-claude" {
		t.Errorf("PostSpawnHook = %q, want claude:h-claude", got)
	}
	if got := recvWithin(t, sessCh, 2*time.Second); got != "claude:h-claude" {
		t.Errorf("SessionHook = %q, want claude:h-claude", got)
	}
}

// TestSpawnNilProviderUsesBuiltinDefaults verifies a Spawn with no resolvable
// provider (bare registry) falls back to pogo's built-in nudge defaults —
// preserving pre-mg-b31b behavior for unit-test registries.
func TestSpawnNilProviderUsesBuiltinDefaults(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{Name: "bare", Type: TypePolecat, Command: []string{"cat"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if a.provider != nil {
		t.Errorf("bare-registry spawn should have a nil provider, got %q", a.provider.ID)
	}
	if !reflect.DeepEqual(a.nudge, DefaultNudgeProfile) {
		t.Errorf("bare-registry spawn should use DefaultNudgeProfile, got %+v", a.nudge)
	}
}

// TestRespawnKeepsResolvedProvider verifies an agent that restarts via the
// registry comes back with its OWN resolved provider and hooks, not the
// registry's global default (mg-b31b acceptance bar 9). The fleet default is
// claude; the agent was spawned as codex and must restart as codex.
func TestRespawnKeepsResolvedProvider(t *testing.T) {
	isolateParkState(t)
	reg, _, codexP := newResolutionRegistry(t)
	defer reg.StopAll(2 * time.Second)
	reg.SetDefaultProvider("claude") // fleet default is claude, NOT codex

	sessCh := make(chan string, 8)
	codexP.SessionHook = func(_ context.Context, a *Agent) { sessCh <- a.Name }

	// A codex agent whose process exits immediately.
	a, err := reg.Spawn(SpawnRequest{
		Name: "rs", Type: TypeCrew, Command: []string{"true"},
		Provider: codexP, RestartOnCrash: true,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-a.Done()
	if got := recvWithin(t, sessCh, 2*time.Second); got != "rs" {
		t.Errorf("session hook on first spawn = %q, want rs", got)
	}

	// Restart it with a long-lived command.
	a.Command = []string{"cat"}
	b, err := reg.Respawn("rs")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}

	if b.provider != codexP {
		t.Errorf("respawned agent provider = %v, want codex (its own, not the claude fleet default)", b.provider)
	}
	if b.nudge.IdleThreshold != 9*time.Second {
		t.Errorf("respawned nudge IdleThreshold = %v, want codex's 9s", b.nudge.IdleThreshold)
	}
	if got := recvWithin(t, sessCh, 2*time.Second); got != "rs" {
		t.Errorf("codex session hook did not re-arm on respawn: %q", got)
	}
}
