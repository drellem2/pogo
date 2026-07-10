package config

import (
	"os"
	"path/filepath"
	"testing"
)

// layeredSandbox points HOME, XDG_CONFIG_HOME and POGO_HOME at fresh temp dirs
// and returns (xdgConfigFile, pogoHomeConfigFile) — the two layers Load reads,
// lowest precedence first. Neither file exists yet; a test writes the ones it
// needs.
func layeredSandbox(t *testing.T) (xdgPath, homePath string) {
	t.Helper()
	root := t.TempDir()
	xdg := filepath.Join(root, ".config")
	state := filepath.Join(root, "state")
	for _, d := range []string{filepath.Join(xdg, "pogo"), state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("POGO_HOME", state)
	return filepath.Join(xdg, "pogo", "config.toml"), filepath.Join(state, "config.toml")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The mg-cf9e footgun, exactly as reported. The [agents] coordinator/worker pin
// the default-migration guard wrote into ~/.config/pogo/config.toml is the only
// thing keeping an existing install off the flipped role defaults. Before the
// merge, a $POGO_HOME/config.toml — created by anything, saying anything —
// shadowed that file wholesale, the pin vanished from the resolved Config, and
// the next pogod boot auto-started a coordinator named "ringmaster" while the
// running "mayor" was left with an orphaned mailbox.
//
// Bare literals throughout: comparing against DefaultCoordinator would make this
// test follow a future flip instead of catching it.
func TestLoad_PartialPogoHomeConfigKeepsPinnedRoleNames(t *testing.T) {
	xdg, home := layeredSandbox(t)

	// The real user config, as the migration guard leaves it.
	write(t, xdg, "[agents]\ncoordinator = \"mayor\"\nworker = \"polecat\"\n")
	// A partial POGO_HOME config that says nothing about roles.
	write(t, home, "[server]\nport = 10001\n")

	cfg := Load()

	if cfg.Agents.Coordinator != "mayor" {
		t.Errorf("coordinator = %q, want %q — the POGO_HOME layer shadowed the pin", cfg.Agents.Coordinator, "mayor")
	}
	if cfg.Agents.Worker != "polecat" {
		t.Errorf("worker = %q, want %q", cfg.Agents.Worker, "polecat")
	}
	// The stall watcher (and refinery mail) address whatever the coordinator
	// resolves to; a dropped pin silently re-points both.
	if cfg.StallWatch.Agent != "mayor" {
		t.Errorf("stall watch agent = %q, want %q", cfg.StallWatch.Agent, "mayor")
	}
	// The override layer still wins for the key it actually sets.
	if cfg.Port != 10001 {
		t.Errorf("port = %d, want 10001 — the POGO_HOME layer must override", cfg.Port)
	}
	if got, want := cfg.Source, home; got != want {
		t.Errorf("Source = %q, want the highest-precedence layer %q", got, want)
	}
	if got := cfg.Sources; len(got) != 2 || got[0] != xdg || got[1] != home {
		t.Errorf("Sources = %v, want [%q %q]", got, xdg, home)
	}
}

// The override layer wins key by key, in both directions.
func TestLoad_PogoHomeOverridesNamedKeysOnly(t *testing.T) {
	xdg, home := layeredSandbox(t)

	write(t, xdg, `[server]
port = 10000
bind = "127.0.0.1"

[agents]
coordinator = "mayor"
worker = "polecat"
provider = "claude"

[refinery]
enabled = true
`)
	write(t, home, `[server]
port = 10002

[agents]
coordinator = "boss"
provider = "codex"

[refinery]
enabled = false
`)

	cfg := Load()

	checks := []struct {
		name      string
		got, want any
	}{
		{"port (overridden)", cfg.Port, 10002},
		{"bind (inherited)", cfg.Bind, "127.0.0.1"},
		{"coordinator (overridden)", cfg.Agents.Coordinator, "boss"},
		{"worker (inherited)", cfg.Agents.Worker, "polecat"},
		{"provider (overridden)", cfg.Agents.Provider, "codex"},
		{"refinery enabled (overridden to false)", cfg.Refinery.Enabled, false},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// An explicit `false` in the base layer survives an override layer that never
// mentions the key. The *EnabledSet bookkeeping that distinguishes "unset" from
// "set to false" has to span layers, not reset per file.
func TestLoad_BaseLayerExplicitFalseSurvivesPartialOverride(t *testing.T) {
	xdg, home := layeredSandbox(t)

	write(t, xdg, "[refinery]\nenabled = false\n\n[agents]\nautostart = false\n")
	write(t, home, "[server]\nport = 10003\n")

	cfg := Load()

	if cfg.Refinery.Enabled {
		t.Error("refinery enabled = true, want false — the base layer's explicit false was lost")
	}
	if cfg.Agents.AutoStart {
		t.Error("agents autostart = true, want false — the base layer's explicit false was lost")
	}
}

// Only the XDG layer present: the historical single-file layout, unchanged.
func TestLoad_XDGOnlyIsUnchanged(t *testing.T) {
	xdg, _ := layeredSandbox(t)
	write(t, xdg, "[server]\nport = 10004\n\n[agents]\ncoordinator = \"mayor\"\n")

	cfg := Load()
	if cfg.Port != 10004 || cfg.Agents.Coordinator != "mayor" {
		t.Errorf("port = %d, coordinator = %q; want 10004 / mayor", cfg.Port, cfg.Agents.Coordinator)
	}
	if cfg.Source != xdg {
		t.Errorf("Source = %q, want %q", cfg.Source, xdg)
	}
}

// No layer at all: no Source, so pogod still treats the daemon as unconfigured
// and skips crew auto-start (mg-3dc3).
func TestLoad_NoLayersLeavesSourceEmpty(t *testing.T) {
	layeredSandbox(t)

	cfg := Load()
	if cfg.Source != "" || len(cfg.Sources) != 0 {
		t.Errorf("Source = %q, Sources = %v; want empty — a daemon with no config must not read as configured", cfg.Source, cfg.Sources)
	}
}

// With POGO_HOME unset, ~/.pogo/config.toml is not a layer. It never has been,
// and promoting it now would hand a config file to every install that happens to
// have one lying around under the state dir.
func TestConfigFilePaths_PogoHomeUnsetYieldsOneLayer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	os.Unsetenv("POGO_HOME")

	paths := ConfigFilePaths()
	want := filepath.Join(root, ".config", "pogo", "config.toml")
	if len(paths) != 1 || paths[0] != want {
		t.Errorf("ConfigFilePaths() = %v, want [%q]", paths, want)
	}
}

// A POGO_HOME that resolves onto the XDG config directory must not be read
// twice; the dedupe keeps a single layer so nothing "overrides itself".
func TestConfigFilePaths_DedupesIdenticalLayers(t *testing.T) {
	root := t.TempDir()
	pogoDir := filepath.Join(root, ".config", "pogo")
	if err := os.MkdirAll(pogoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("POGO_HOME", pogoDir)

	if paths := ConfigFilePaths(); len(paths) != 1 {
		t.Errorf("ConfigFilePaths() = %v, want a single deduped layer", paths)
	}
}
