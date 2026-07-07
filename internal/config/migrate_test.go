package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox points POGO_HOME and XDG_CONFIG_HOME at fresh temp dirs so config /
// prompt state is fully isolated, and returns the resolved config.toml path.
func sandbox(t *testing.T) (home string, configPath string) {
	t.Helper()
	home = t.TempDir()
	xdg := t.TempDir()
	t.Setenv("POGO_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	return home, ConfigFilePath()
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeStampedPrompt(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "<!-- pogo-prompt: embed=sha256:abc body=sha256:def -->\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsExistingInstall_Fresh(t *testing.T) {
	sandbox(t)
	if IsExistingInstall() {
		t.Error("fresh sandbox (no config, no prompts) should not read as existing")
	}
}

func TestIsExistingInstall_ConfigFile(t *testing.T) {
	_, path := sandbox(t)
	writeConfig(t, path, "[server]\nport = 8080\n")
	if !IsExistingInstall() {
		t.Error("a present config file should mark an existing install")
	}
}

func TestIsExistingInstall_StampedPromptFallback(t *testing.T) {
	home, _ := sandbox(t)
	writeStampedPrompt(t, home, "mayor.md")
	if !IsExistingInstall() {
		t.Error("a stamped prompt under agents/ should mark an existing install")
	}
}

func TestIsExistingInstall_UnstampedPromptIgnored(t *testing.T) {
	home, _ := sandbox(t)
	dir := filepath.Join(home, "agents")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# just a file\n"), 0o644)
	if IsExistingInstall() {
		t.Error("an unstamped file under agents/ must not mark an existing install")
	}
}

func TestPin_FreshInstallNoOp(t *testing.T) {
	_, path := sandbox(t)
	res, err := PinRoleDefaultsIfExistingInstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pinned) != 0 {
		t.Errorf("fresh install should pin nothing, got %v", res.Pinned)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("fresh install must not create config.toml")
	}
}

func TestPin_ExistingNoConfigCreatesFile(t *testing.T) {
	_, path := sandbox(t)
	res, err := PinRoleDefaultsIfExistingInstall(true)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Pinned, ","); got != "coordinator,worker" {
		t.Errorf("expected both roles pinned, got %q", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config.toml should have been created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `coordinator = "mayor"`) {
		t.Errorf("coordinator not pinned to mayor:\n%s", content)
	}
	if !strings.Contains(content, `worker = "polecat"`) {
		t.Errorf("worker not pinned to polecat:\n%s", content)
	}
	// Round-trip: the loader reads the pinned coordinator back.
	if cfg := Load(); cfg.Agents.CoordinatorName() != "mayor" {
		t.Errorf("Load() after pin: coordinator = %q, want mayor", cfg.Agents.CoordinatorName())
	}
}

func TestPin_Idempotent(t *testing.T) {
	_, path := sandbox(t)
	if _, err := PinRoleDefaultsIfExistingInstall(true); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := PinRoleDefaultsIfExistingInstall(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pinned) != 0 {
		t.Errorf("second run should pin nothing, got %v", res.Pinned)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("second run rewrote config.toml:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestPin_PreservesExistingCoordinator(t *testing.T) {
	_, path := sandbox(t)
	writeConfig(t, path, "[agents]\ncoordinator = \"ringmaster\"\nprovider = \"claude\"\n")
	res, err := PinRoleDefaultsIfExistingInstall(true)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Pinned, ","); got != "worker" {
		t.Errorf("only worker should be pinned when coordinator is already set, got %q", got)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, `coordinator = "ringmaster"`) {
		t.Errorf("operator coordinator value must be preserved:\n%s", content)
	}
	if strings.Contains(content, `coordinator = "mayor"`) {
		t.Errorf("guard must not overwrite an operator-set coordinator:\n%s", content)
	}
	if !strings.Contains(content, `provider = "claude"`) {
		t.Errorf("existing [agents] keys must be preserved:\n%s", content)
	}
	if !strings.Contains(content, `worker = "polecat"`) {
		t.Errorf("worker should be pinned:\n%s", content)
	}
	// The pinned key lands inside the [agents] table, not in a duplicate one.
	if strings.Count(content, "[agents]") != 1 {
		t.Errorf("must not create a second [agents] table:\n%s", content)
	}
}

func TestPin_InsertsIntoExistingAgentsSection(t *testing.T) {
	_, path := sandbox(t)
	// [agents.polecat] sub-table present but no top-level role keys.
	writeConfig(t, path, "[server]\nport = 9000\n\n[agents]\nprovider = \"claude\"\n\n[agents.polecat]\nprovider = \"pi\"\n")
	if _, err := PinRoleDefaultsIfExistingInstall(true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if strings.Count(content, "[agents]") != 1 {
		t.Errorf("must not add a duplicate [agents] table:\n%s", content)
	}
	// The sub-table's provider override must survive untouched.
	if !strings.Contains(content, "[agents.polecat]\nprovider = \"pi\"") {
		t.Errorf("[agents.polecat] override must be preserved:\n%s", content)
	}
	// Round-trip through the real loader: pinned coordinator is read, and the
	// polecat sub-table override still parses.
	cfg := Load()
	if cfg.Agents.CoordinatorName() != "mayor" {
		t.Errorf("coordinator = %q, want mayor", cfg.Agents.CoordinatorName())
	}
	if cfg.Agents.AgentProvider("polecat") != "pi" {
		t.Errorf("polecat provider override lost: %q", cfg.Agents.AgentProvider("polecat"))
	}
}

func TestPin_StampedPromptOnlyInstall(t *testing.T) {
	home, path := sandbox(t)
	writeStampedPrompt(t, home, "mayor.md")
	res, err := PinRoleDefaultsIfExistingInstall(IsExistingInstall())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pinned) != 2 {
		t.Errorf("stamped-prompt-only install should pin both roles, got %v", res.Pinned)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config.toml should have been created for the stamped-prompt install: %v", err)
	}
}

// TestGuard_ExistingKeepsOldDefault_FreshGetsNew is the WI-5 statement of the
// migration-guard contract (mg-6a24 §3): a future default-flip must leave
// existing installs on their old role names while fresh installs adopt the new
// ones. We can't flip the compile-time const inside a test, so we assert the
// mechanism that makes the flip safe — where each install's resolved name comes
// FROM:
//
//   - Existing install → the guard writes worker="polecat" to config.toml, and
//     Load() reads that DISK value. A later const change is inert: the name is
//     pinned on disk, not derived from the const.
//   - Fresh install    → no config is written, so Load() falls back to the
//     DefaultWorker CONST. Flip the const and this install follows it — exactly
//     the "fresh gets the new default" path.
func TestGuard_ExistingKeepsOldDefault_FreshGetsNew(t *testing.T) {
	t.Run("existing install pins the current name to disk", func(t *testing.T) {
		_, path := sandbox(t)
		if _, err := PinRoleDefaultsIfExistingInstall(true); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("guard should have written config.toml: %v", err)
		}
		// The literal string on disk — not the const — is what survives a flip.
		if !strings.Contains(string(data), `worker = "polecat"`) {
			t.Errorf("worker not pinned to its current name on disk:\n%s", data)
		}
		// Load() reads the pinned disk value, so the name is decoupled from any
		// future DefaultWorker change.
		if got := Load().Agents.WorkerName(); got != "polecat" {
			t.Errorf("existing install worker = %q, want the pinned polecat", got)
		}
	})

	t.Run("fresh install stays const-driven so a flip reaches it", func(t *testing.T) {
		_, path := sandbox(t)
		res, err := PinRoleDefaultsIfExistingInstall(false)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Pinned) != 0 {
			t.Errorf("fresh install must pin nothing, got %v", res.Pinned)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("fresh install must not write config.toml (would freeze the default)")
		}
		// With no pinned key, WorkerName() resolves from the const — today that
		// is DefaultWorker, and a flip of that const would flow straight through.
		if got := Load().Agents.WorkerName(); got != DefaultWorker {
			t.Errorf("fresh install worker = %q, want the const DefaultWorker %q", got, DefaultWorker)
		}
	})
}

// TestPinRoleDefaults_FrozenLiterals is the regression that catches a post-flip
// leak: the guard must pin the FROZEN historical role names, not whatever the
// live Default* consts resolve to today. The expectations here are BARE literals
// ("mayor" / "polecat") and deliberately do NOT reference DefaultCoordinator /
// DefaultWorker — that decoupling is the whole point. When the gated
// flavor-rename flip (mg-ce47) changes the Default* consts, a guard that (wrongly)
// pinned the live const would write the NEW name and this test would fail, while
// a guard reading the frozen literals keeps writing the historical name and stays
// green.
func TestPinRoleDefaults_FrozenLiterals(t *testing.T) {
	// Freeze the legacy consts themselves: these must stay the historical role
	// names regardless of any future Default* flip. Bare literals on purpose.
	if legacyCoordinatorDefault != "mayor" {
		t.Errorf("legacyCoordinatorDefault = %q, want frozen literal %q", legacyCoordinatorDefault, "mayor")
	}
	if legacyWorkerDefault != "polecat" {
		t.Errorf("legacyWorkerDefault = %q, want frozen literal %q", legacyWorkerDefault, "polecat")
	}

	_, path := sandbox(t)
	if _, err := PinRoleDefaultsIfExistingInstall(true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("guard should have written config.toml: %v", err)
	}
	got := string(data)
	// The pinned config must contain the bare historical literals. Comparing
	// against DefaultCoordinator / DefaultWorker would make this test follow a
	// flip instead of catching it, so the expectations are hard-coded strings.
	if !strings.Contains(got, `coordinator = "mayor"`) {
		t.Errorf("guard did not pin the frozen coordinator literal:\n%s", got)
	}
	if !strings.Contains(got, `worker = "polecat"`) {
		t.Errorf("guard did not pin the frozen worker literal:\n%s", got)
	}
}
