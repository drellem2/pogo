package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
)

// sandboxHome redirects HOME, XDG_CONFIG_HOME and POGO_HOME at fresh temp dirs
// and returns the POGO_HOME path. Both the developer shell and launchd export
// POGO_HOME on some machines, so an unset alone would not isolate this.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	state := filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("POGO_HOME", state)
	return state
}

// restoreRoleNames resets the process-wide role names after a test mutates them.
// They are package globals in internal/agent, shared by every test in this
// binary's test process.
func restoreRoleNames(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		agent.SetCoordinatorName(agent.DefaultCoordinatorName)
		agent.SetWorkerName(agent.DefaultWorkerName)
	})
}

// The v0.3.0 -> v0.4.0 upgrade regression (mg-bc47): a config.toml that predates
// the [agents] role keys must resolve to the FROZEN legacy names on the very
// first boot of the flipped build, not on the second. Before the fix, pogod
// resolved ringmaster/pogocat from the live Default* consts and acted on them —
// auto-start, stall watcher, coordinator mail — while pinning mayor to disk in
// the same second.
//
// This is deliberately a test of cmd/pogod's own ordering, not of the guard:
// internal/config's guard tests passed throughout the bug because they exercise
// the guard in isolation.
func TestPinAndResolveRoles_V030ConfigResolvesLegacyNamesOnFirstBoot(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	// A genuine v0.3.0-era config: [agents] present, role keys absent.
	cfgPath := filepath.Join(state, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[agents]\nautostart = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Precondition — this is what main() sees before the pin runs.
	cfg := config.Load()
	if cfg.Agents.Coordinator != config.DefaultCoordinator {
		t.Fatalf("precondition: unpinned Load() coordinator = %q, want live default %q",
			cfg.Agents.Coordinator, config.DefaultCoordinator)
	}
	// Simulate the stale process-wide resolution the old ordering left behind.
	agent.SetCoordinatorName(cfg.Agents.Coordinator)
	agent.SetWorkerName(cfg.Agents.Worker)

	cfg = pinAndResolveRoles(cfg)

	// Bare literals throughout: comparing against Default* would make this test
	// follow a future flip instead of catching it.
	if cfg.Agents.Coordinator != "mayor" {
		t.Errorf("cfg coordinator = %q, want %q", cfg.Agents.Coordinator, "mayor")
	}
	if cfg.Agents.Worker != "polecat" {
		t.Errorf("cfg worker = %q, want %q", cfg.Agents.Worker, "polecat")
	}
	// The name AutoStartAgents and InstallPrompts read.
	if got := agent.CoordinatorName(); got != "mayor" {
		t.Errorf("process-wide coordinator name = %q, want %q — pogod would auto-start %q", got, "mayor", got)
	}
	if got := agent.WorkerName(); got != "polecat" {
		t.Errorf("process-wide worker name = %q, want %q", got, "polecat")
	}
	// The name the stall watcher arms on, and the one refinery mail is addressed to.
	if cfg.StallWatch.Agent != "mayor" {
		t.Errorf("stall watch agent = %q, want %q", cfg.StallWatch.Agent, "mayor")
	}
	if got := cfg.Agents.CoordinatorName(); got != "mayor" {
		t.Errorf("cfg.Agents.CoordinatorName() = %q, want %q", got, "mayor")
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `coordinator = "mayor"`) {
		t.Errorf("config.toml not pinned:\n%s", data)
	}
}

// Re-running the pin on an already-pinned config must be a no-op that still
// resolves the pinned names — the steady state on every boot after the first.
func TestPinAndResolveRoles_AlreadyPinnedIsIdempotent(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	cfgPath := filepath.Join(state, "config.toml")
	seed := "[agents]\ncoordinator = \"mayor\"\nworker = \"polecat\"\n"
	if err := os.WriteFile(cfgPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := pinAndResolveRoles(config.Load())
	if cfg.Agents.Coordinator != "mayor" || agent.CoordinatorName() != "mayor" {
		t.Errorf("coordinator = %q / %q, want mayor", cfg.Agents.Coordinator, agent.CoordinatorName())
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != seed {
		t.Errorf("already-pinned config rewritten:\ngot:\n%s\nwant:\n%s", data, seed)
	}
}

// A daemon with no config file must not gain one. pogod treats "no config" as
// "orchestration not opted into" (mg-3dc3) and skips prompt refresh and
// auto-start; writing a config.toml here would arm a fleet on the next boot of
// an isolated or sandboxed daemon. This is why pinAndResolveRoles keys off
// cfg.Source rather than config.IsExistingInstall, whose stamped-prompt fallback
// reads true for exactly that daemon.
func TestPinAndResolveRoles_NoConfigNeitherPinsNorCreatesOne(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	// A stamped prompt with no config.toml: IsExistingInstall() would say yes.
	agentsDir := filepath.Join(state, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamped := "<!-- pogo-prompt: embed=sha256:abc body=sha256:def -->\n# mayor\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "mayor.md"), []byte(stamped), 0o644); err != nil {
		t.Fatal(err)
	}
	if !config.IsExistingInstall() {
		t.Fatal("precondition: stamped prompt should make IsExistingInstall() true")
	}

	cfg := pinAndResolveRoles(config.Load())

	if cfg.Agents.Coordinator != config.DefaultCoordinator {
		t.Errorf("coordinator = %q, want the live default %q (no config to pin from)",
			cfg.Agents.Coordinator, config.DefaultCoordinator)
	}
	if _, err := os.Stat(filepath.Join(state, "config.toml")); !os.IsNotExist(err) {
		t.Error("pogod created config.toml with no config present; that opts an isolated daemon into orchestration")
	}
	if p := config.ConfigFilePath(); p != "" {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("pogod created %s with no config present", p)
		}
	}
}
