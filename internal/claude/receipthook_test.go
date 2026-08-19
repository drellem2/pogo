package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/hookarm"
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

// postToolUseGroups returns the PostToolUse matcher groups as (matcher,
// commands) pairs.
func postToolUseGroups(t *testing.T, settings map[string]any) map[string][]string {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["PostToolUse"].([]any)
	out := map[string][]string{}
	for _, g := range groups {
		group, _ := g.(map[string]any)
		matcher, _ := group["matcher"].(string)
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			if cmd, ok := entry["command"].(string); ok {
				out[matcher] = append(out[matcher], cmd)
			}
		}
	}
	return out
}

func TestInstallMailRecipientHookRegistersOnBash(t *testing.T) {
	dir := t.TempDir()
	if err := InstallMailRecipientHook(dir, "/usr/local/bin/pogo hook mail-recipient"); err != nil {
		t.Fatalf("InstallMailRecipientHook: %v", err)
	}
	got := postToolUseGroups(t, readSettingsFile(t, dir))
	cmds := got["Bash"]
	if len(cmds) != 1 || !strings.HasSuffix(cmds[0], "hook mail-recipient") {
		t.Fatalf("PostToolUse/Bash hooks = %v", got)
	}
}

// TestInstallMailRecipientHookIsIdempotentAcrossAMovedBinary: an agent respawns
// often, and a second copy of this hook would print every warning twice — which
// is how a warning stops being read.
func TestInstallMailRecipientHookIsIdempotentAcrossAMovedBinary(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"/old/pogo", "/old/pogo", "/new/pogo"} {
		if err := InstallMailRecipientHook(dir, bin+" hook mail-recipient"); err != nil {
			t.Fatalf("InstallMailRecipientHook(%s): %v", bin, err)
		}
	}
	cmds := postToolUseGroups(t, readSettingsFile(t, dir))["Bash"]
	if len(cmds) != 1 {
		t.Fatalf("want exactly one entry after three installs, got %v", cmds)
	}
	if cmds[0] != "/new/pogo hook mail-recipient" {
		t.Fatalf("stale command survived a move: %q", cmds[0])
	}
}

// TestBothHooksCoexist: they are installed by the same spawn into the same
// file, and one must not eat the other.
func TestBothHooksCoexist(t *testing.T) {
	dir := t.TempDir()
	if err := InstallSubmitReceiptHook(dir, "/bin/pogo hook prompt-submit"); err != nil {
		t.Fatalf("InstallSubmitReceiptHook: %v", err)
	}
	if err := InstallMailRecipientHook(dir, "/bin/pogo hook mail-recipient"); err != nil {
		t.Fatalf("InstallMailRecipientHook: %v", err)
	}
	settings := readSettingsFile(t, dir)
	if got := promptSubmitCommands(t, settings); len(got) != 1 {
		t.Errorf("UserPromptSubmit hooks = %v", got)
	}
	if got := postToolUseGroups(t, settings)["Bash"]; len(got) != 1 {
		t.Errorf("PostToolUse/Bash hooks = %v", got)
	}
}

// TestInstallMailRecipientHookPreservesAHumansOwnPostToolUseHooks: the agent's
// working directory can be a real repository whose settings.local.json belongs
// to a person.
func TestInstallMailRecipientHookPreservesAHumansOwnPostToolUseHooks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, settingsRelPath)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `{
	  "permissions": {"allow": ["Bash(go test:*)"]},
	  "hooks": {"PostToolUse": [{"matcher": "Write", "hooks": [{"type": "command", "command": "prettier"}]}]}
	}`
	if err := os.WriteFile(filepath.Join(dir, settingsRelPath), []byte(existing), 0o644); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := InstallMailRecipientHook(dir, "/bin/pogo hook mail-recipient"); err != nil {
		t.Fatalf("InstallMailRecipientHook: %v", err)
	}
	settings := readSettingsFile(t, dir)
	groups := postToolUseGroups(t, settings)
	if got := groups["Write"]; len(got) != 1 || got[0] != "prettier" {
		t.Errorf("the human's Write hook was disturbed: %v", groups)
	}
	if got := groups["Bash"]; len(got) != 1 {
		t.Errorf("pogo's hook not registered: %v", groups)
	}
	if _, ok := settings["permissions"]; !ok {
		t.Error("permissions block was dropped")
	}
}

// TestProviderCarriesBothHooks: the agent-side wiring tests use a fake
// provider, so the REAL one is asserted here. A Provider literal that forgot
// the field would pass every other test on either side.
func TestProviderCarriesBothHooks(t *testing.T) {
	if Provider.SubmitReceiptHook == nil {
		t.Error("the Claude provider lost its SubmitReceiptHook")
	}
	if Provider.MailRecipientHook == nil {
		t.Error("the Claude provider has no MailRecipientHook: no agent will ever be warned " +
			"that it just mailed a stopped agent (mg-d924)")
	}
}

// TestTheInstallerWritesWhatTheReaderRecognises is the seam between the two
// halves of the arming report: this package WRITES the registration, and pogod
// READS it through internal/hookarm to answer "which running agents are armed?"
// They are different binaries and can be at different revisions, so a drift
// between them would report a whole fleet as unarmed while every agent was
// perfectly armed — or, worse, the reverse.
//
// The chain was also observed live on 2026-08-20 (mg-503d): this installer
// wrote the settings, a real Claude Code session loaded them, the real
// `pogo hook mail-recipient` binary ran and stamped, and Resolve answered
// "armed". This test is what keeps that true.
func TestTheInstallerWritesWhatTheReaderRecognises(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()

	if state, _ := hookarm.Resolve(dir, start); state != hookarm.StateOff {
		t.Fatalf("before install: state = %q, want %q", state, hookarm.StateOff)
	}

	if err := InstallMailRecipientHook(dir, "/opt/pogo/bin/pogo hook mail-recipient"); err != nil {
		t.Fatalf("InstallMailRecipientHook: %v", err)
	}
	state, why := hookarm.Resolve(dir, start)
	if state != hookarm.StatePending {
		t.Fatalf("after install: state = %q, want %q — the reader did not recognise what the installer wrote (%s)",
			state, hookarm.StatePending, why)
	}

	if err := hookarm.RecordFire(dir); err != nil {
		t.Fatal(err)
	}
	future := start.Add(time.Minute)
	if err := os.Chtimes(hookarm.StampPath(dir), future, future); err != nil {
		t.Fatal(err)
	}
	if state, why := hookarm.Resolve(dir, start); state != hookarm.StateArmed {
		t.Fatalf("after a firing: state = %q, want %q (%s)", state, hookarm.StateArmed, why)
	}
}

// TestReinstallingDoesNotStackASecondEntry: the reader takes the first match,
// so a duplicate would be harmless to it — but a second copy of the hook would
// print every warning twice, and the marker-based upsert is what prevents it.
// Asserted through the reader so both halves are exercised together.
func TestReinstallingDoesNotStackASecondEntry(t *testing.T) {
	dir := t.TempDir()
	if err := InstallMailRecipientHook(dir, "/old/path/pogo hook mail-recipient"); err != nil {
		t.Fatal(err)
	}
	if err := InstallMailRecipientHook(dir, "/new/path/pogo hook mail-recipient"); err != nil {
		t.Fatal(err)
	}
	cmd, ok, err := hookarm.Registered(dir)
	if err != nil || !ok {
		t.Fatalf("Registered = (%q, %v, %v)", cmd, ok, err)
	}
	if cmd != "/new/path/pogo hook mail-recipient" {
		t.Errorf("the moved binary's path did not replace the old one: %q", cmd)
	}
}
