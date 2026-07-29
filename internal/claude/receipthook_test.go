package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSettingsFile(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, settingsRelPath))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse settings: %v\n%s", err, data)
	}
	return m
}

// promptSubmitCommands returns every UserPromptSubmit hook command registered
// in the settings object, flattened across matcher groups.
func promptSubmitCommands(t *testing.T, settings map[string]any) []string {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["UserPromptSubmit"].([]any)
	var out []string
	for _, g := range groups {
		group, _ := g.(map[string]any)
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			if cmd, ok := entry["command"].(string); ok {
				out = append(out, cmd)
			}
		}
	}
	return out
}

func TestInstallSubmitReceiptHookCreatesSettings(t *testing.T) {
	dir := t.TempDir()
	if err := InstallSubmitReceiptHook(dir, "/usr/local/bin/pogo hook prompt-submit"); err != nil {
		t.Fatalf("InstallSubmitReceiptHook: %v", err)
	}

	got := promptSubmitCommands(t, readSettingsFile(t, dir))
	if len(got) != 1 || got[0] != "/usr/local/bin/pogo hook prompt-submit" {
		t.Fatalf("UserPromptSubmit commands = %v", got)
	}
}

// TestInstallSubmitReceiptHookPreservesExistingSettings is the one that matters
// operationally: an agent's working directory can be a real repository whose
// settings.local.json holds a human's permissions and hooks. Installing a
// delivery receipt must not cost them.
func TestInstallSubmitReceiptHookPreservesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{
	  "permissions": {"allow": ["Bash(go test:*)"]},
	  "hooks": {
	    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "audit.sh"}]}],
	    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "mine.sh"}]}]
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, settingsRelPath), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallSubmitReceiptHook(dir, "pogo hook prompt-submit"); err != nil {
		t.Fatalf("InstallSubmitReceiptHook: %v", err)
	}

	settings := readSettingsFile(t, dir)
	if _, ok := settings["permissions"]; !ok {
		t.Error("permissions block was dropped")
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Error("PreToolUse hooks were dropped")
	}

	cmds := promptSubmitCommands(t, settings)
	if len(cmds) != 2 {
		t.Fatalf("want the user's hook plus pogo's, got %v", cmds)
	}
	if cmds[0] != "mine.sh" {
		t.Errorf("the user's UserPromptSubmit hook was displaced: %v", cmds)
	}
	if cmds[1] != "pogo hook prompt-submit" {
		t.Errorf("pogo's hook was not appended: %v", cmds)
	}
}

// TestInstallSubmitReceiptHookIsIdempotent: a respawn re-installs, and a second
// copy of the hook would count every prompt twice — which reads as a delivery
// that was confirmed when it was not.
func TestInstallSubmitReceiptHookIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := InstallSubmitReceiptHook(dir, "pogo hook prompt-submit"); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	if cmds := promptSubmitCommands(t, readSettingsFile(t, dir)); len(cmds) != 1 {
		t.Fatalf("three installs left %d hooks: %v", len(cmds), cmds)
	}
}

// TestInstallSubmitReceiptHookRefreshesAMovedBinary: pogod hands over a fully
// resolved path, so a daemon that moved must update its entry rather than stack
// a second one pointing at a binary that may no longer exist.
func TestInstallSubmitReceiptHookRefreshesAMovedBinary(t *testing.T) {
	dir := t.TempDir()
	if err := InstallSubmitReceiptHook(dir, "/old/bin/pogo hook prompt-submit"); err != nil {
		t.Fatal(err)
	}
	if err := InstallSubmitReceiptHook(dir, "/new/bin/pogo hook prompt-submit"); err != nil {
		t.Fatal(err)
	}
	cmds := promptSubmitCommands(t, readSettingsFile(t, dir))
	if len(cmds) != 1 || cmds[0] != "/new/bin/pogo hook prompt-submit" {
		t.Fatalf("want a single refreshed entry, got %v", cmds)
	}
}

// TestInstallSubmitReceiptHookRefusesUnparseableSettings: the file belongs to a
// human. pogo does not get to decide that unreadable means disposable — it
// declines, and the caller degrades to unconfirmed delivery.
func TestInstallSubmitReceiptHookRefusesUnparseableSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("{ this is not json")
	if err := os.WriteFile(filepath.Join(dir, settingsRelPath), garbage, 0o644); err != nil {
		t.Fatal(err)
	}

	err := InstallSubmitReceiptHook(dir, "pogo hook prompt-submit")
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("error should say what it refused: %v", err)
	}

	after, readErr := os.ReadFile(filepath.Join(dir, settingsRelPath))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(garbage) {
		t.Errorf("the user's file was modified: %q", after)
	}
}

func TestInstallSubmitReceiptHookRequiresADirAndACommand(t *testing.T) {
	if err := InstallSubmitReceiptHook("", "pogo hook prompt-submit"); err == nil {
		t.Error("empty dir should be an error")
	}
	if err := InstallSubmitReceiptHook(t.TempDir(), ""); err == nil {
		t.Error("empty command should be an error")
	}
}

// TestProviderDeclaresTheReceiptHook keeps the wiring honest: everything above
// is dead code if the provider descriptor does not point at it.
func TestProviderDeclaresTheReceiptHook(t *testing.T) {
	if Provider.SubmitReceiptHook == nil {
		t.Fatal("claude provider declares no SubmitReceiptHook; nudges to Claude " +
			"agents can never be confirmed")
	}
	dir := t.TempDir()
	if err := Provider.SubmitReceiptHook(dir, "pogo hook prompt-submit"); err != nil {
		t.Fatalf("provider hook installer: %v", err)
	}
	if cmds := promptSubmitCommands(t, readSettingsFile(t, dir)); len(cmds) != 1 {
		t.Fatalf("provider hook installed %v", cmds)
	}
}
