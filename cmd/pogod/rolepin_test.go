package main

import (
	"os"
	"os/exec"
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

// The mg-cf9e scenario, end to end through pogod's own boot ordering. The
// [agents] pin lives in ~/.config/pogo/config.toml, where the migration guard
// wrote it. Something — a sandbox script, a test fixture, an operator pinning a
// port — creates a $POGO_HOME/config.toml that says nothing about roles. That
// file used to shadow the pinned one wholesale, so this boot resolved the flipped
// default, auto-started a coordinator named "ringmaster", armed the stall watcher
// on it, and left the running "mayor" holding an orphaned mailbox.
//
// Bare literals: comparing against Default* would follow a future flip.
func TestPinAndResolveRoles_PartialPogoHomeConfigDoesNotDropThePin(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	xdgPogo := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pogo")
	if err := os.MkdirAll(xdgPogo, 0o755); err != nil {
		t.Fatal(err)
	}
	xdgCfg := filepath.Join(xdgPogo, "config.toml")
	pinned := "[agents]\ncoordinator = \"mayor\"\nworker = \"polecat\"\n"
	if err := os.WriteFile(xdgCfg, []byte(pinned), 0o644); err != nil {
		t.Fatal(err)
	}
	// The trapdoor: a POGO_HOME config with no [agents] section at all.
	homeCfg := filepath.Join(state, "config.toml")
	if err := os.WriteFile(homeCfg, []byte("[server]\nport = 10001\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := pinAndResolveRoles(config.Load())

	if cfg.Agents.Coordinator != "mayor" {
		t.Errorf("cfg coordinator = %q, want mayor — pogod would auto-start %q and kill the running mayor",
			cfg.Agents.Coordinator, cfg.Agents.Coordinator)
	}
	if cfg.Agents.Worker != "polecat" {
		t.Errorf("cfg worker = %q, want polecat", cfg.Agents.Worker)
	}
	if got := agent.CoordinatorName(); got != "mayor" {
		t.Errorf("process-wide coordinator name = %q, want mayor — this is the name AutoStartAgents spawns", got)
	}
	if cfg.StallWatch.Agent != "mayor" {
		t.Errorf("stall watch agent = %q, want mayor", cfg.StallWatch.Agent)
	}
	if cfg.Port != 10001 {
		t.Errorf("port = %d, want 10001 — the POGO_HOME layer still overrides the keys it sets", cfg.Port)
	}
	// The pin is already satisfied by the XDG layer; pogod must not re-pin it
	// into the POGO_HOME layer, where it would override that operator's file.
	data, err := os.ReadFile(homeCfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "coordinator") {
		t.Errorf("pin leaked into the POGO_HOME layer; a key set in any layer is already pinned:\n%s", data)
	}
}

// Even with the pin gone from every layer — the worst case, an install the
// migration guard never reached — a coordinator that is RUNNING is not renamed.
// This is what makes the config pin stop being load-bearing (mg-cf9e).
func TestPinAndResolveRoles_RefusesToRenameARunningCoordinator(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	// Config says "ringmaster" outright — the worst case. The key is present, so
	// the migration guard is a no-op and cannot be what saves us here: only the
	// rename refusal can.
	if err := os.WriteFile(filepath.Join(state, "config.toml"),
		[]byte("[agents]\ncoordinator = \"ringmaster\"\nworker = \"pogocat\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A live coordinator named "mayor" — an orphan of a SIGKILLed pogod, say.
	sleep := exec.Command("sleep", "600")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	if err := config.RecordRunningCoordinator("mayor", sleep.Process.Pid); err != nil {
		t.Fatal(err)
	}

	if got := config.Load().Agents.Coordinator; got != "ringmaster" {
		t.Fatalf("precondition: Load() coordinator = %q, want the configured %q", got, "ringmaster")
	}

	cfg := pinAndResolveRoles(config.Load())

	if cfg.Agents.Coordinator != "mayor" {
		t.Errorf("cfg coordinator = %q, want mayor — a running coordinator must never be renamed", cfg.Agents.Coordinator)
	}
	if got := agent.CoordinatorName(); got != "mayor" {
		t.Errorf("process-wide coordinator name = %q, want mayor", got)
	}
	if cfg.StallWatch.Agent != "mayor" {
		t.Errorf("stall watch agent = %q, want mayor", cfg.StallWatch.Agent)
	}
}

// A coordinator that is not running may still be renamed — the documented way to
// rename the role. The guard freezes live processes, not config files.
func TestPinAndResolveRoles_StoppedCoordinatorIsRenamable(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	if err := os.WriteFile(filepath.Join(state, "config.toml"),
		[]byte("[agents]\ncoordinator = \"boss\"\nworker = \"polecat\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A record left by a coordinator that has since exited.
	done := exec.Command("true")
	if err := done.Run(); err != nil {
		t.Fatal(err)
	}
	if err := config.RecordRunningCoordinator("mayor", done.Process.Pid); err != nil {
		t.Fatal(err)
	}

	cfg := pinAndResolveRoles(config.Load())

	if cfg.Agents.Coordinator != "boss" {
		t.Errorf("cfg coordinator = %q, want boss — a stopped coordinator must remain renamable", cfg.Agents.Coordinator)
	}
	if got := agent.CoordinatorName(); got != "boss" {
		t.Errorf("process-wide coordinator name = %q, want boss", got)
	}
}
