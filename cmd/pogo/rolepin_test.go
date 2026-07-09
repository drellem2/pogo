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

func restoreRoleNames(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		agent.SetCoordinatorName(agent.DefaultCoordinatorName)
		agent.SetWorkerName(agent.DefaultWorkerName)
	})
}

// The v0.3.0 -> v0.4.0 upgrade regression (mg-bc47), `pogo install` side. main()
// resolves role names from config.Load() at startup, which fills a role-key-less
// [agents] with the live Default* consts. `pogo install` must pin the frozen
// legacy names and RE-resolve before it synthesizes prompts (which expand the
// role names into prose) or prints its "next steps".
func TestPinAndResolveRoles_ExistingInstallReResolvesAfterPin(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	cfgPath := filepath.Join(state, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[agents]\nautostart = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reproduce main()'s startup resolution against the unpinned config.
	cfg := resolveRoles()
	if cfg.Agents.Coordinator != config.DefaultCoordinator {
		t.Fatalf("precondition: startup resolved %q, want live default %q",
			cfg.Agents.Coordinator, config.DefaultCoordinator)
	}
	if agent.CoordinatorName() != config.DefaultCoordinator {
		t.Fatalf("precondition: process-wide name is %q, want the live default", agent.CoordinatorName())
	}

	res, err := pinAndResolveRoles(config.IsExistingInstall())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pinned) != 2 {
		t.Errorf("Pinned = %v, want both role keys", res.Pinned)
	}
	// Bare literals: comparing against Default* would follow a flip, not catch it.
	if got := agent.CoordinatorName(); got != "mayor" {
		t.Errorf("coordinator name = %q, want %q — install would print 'pogo agent start %s'", got, "mayor", got)
	}
	if got := agent.WorkerName(); got != "polecat" {
		t.Errorf("worker name = %q, want %q — installed prompts would name the wrong role", got, "polecat")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `coordinator = "mayor"`) {
		t.Errorf("config.toml not pinned:\n%s", data)
	}
}

// `pogo install` starts pogod first, and pogod now pins on boot. So by the time
// install runs its own pin the keys can already be present, making the pin a
// no-op with an empty PinResult while this process still holds the stale names
// read at startup. The re-resolve must therefore be unconditional.
func TestPinAndResolveRoles_ReResolvesWhenAnotherProcessAlreadyPinned(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	// Startup: config has no role keys, so the process resolves the new defaults.
	cfgPath := filepath.Join(state, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[agents]\nautostart = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolveRoles()
	if agent.CoordinatorName() != config.DefaultCoordinator {
		t.Fatalf("precondition: process-wide name is %q, want the live default", agent.CoordinatorName())
	}

	// pogod, spawned by install's step 1, pins the file behind our back.
	pinned := "[agents]\ncoordinator = \"mayor\"\nworker = \"polecat\"\nautostart = false\n"
	if err := os.WriteFile(cfgPath, []byte(pinned), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := pinAndResolveRoles(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pinned) != 0 {
		t.Errorf("Pinned = %v, want nothing — another process already pinned", res.Pinned)
	}
	if got := agent.CoordinatorName(); got != "mayor" {
		t.Errorf("coordinator name = %q, want %q; an empty PinResult must still re-resolve", got, "mayor")
	}
}

// A fresh install adopts the new defaults and writes no config.toml.
func TestPinAndResolveRoles_FreshInstallAdoptsNewDefaults(t *testing.T) {
	state := sandboxHome(t)
	restoreRoleNames(t)

	if config.IsExistingInstall() {
		t.Fatal("precondition: empty sandbox must not read as an existing install")
	}
	res, err := pinAndResolveRoles(config.IsExistingInstall())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pinned) != 0 {
		t.Errorf("Pinned = %v, want nothing on a fresh install", res.Pinned)
	}
	if got := agent.CoordinatorName(); got != config.DefaultCoordinator {
		t.Errorf("coordinator name = %q, want the live default %q", got, config.DefaultCoordinator)
	}
	if _, err := os.Stat(filepath.Join(state, "config.toml")); !os.IsNotExist(err) {
		t.Error("fresh install wrote config.toml; the guard must be a no-op")
	}
}
