package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The companion guard — that the four watcher packages' own DefaultEscalateTo
// constants still agree with DefaultEscalationBox — lives in
// cmd/pogod/escalationbox_test.go. It cannot live here: the watcher packages
// reach internal/events, which imports this package, so importing them from a
// test in package config is an import cycle.

// TestEscalationBoxDefaultsToHuman pins the property that makes this seam safe
// to add to a fleet that has never heard of it: an install with no
// `escalation_box` line escalates exactly where it escalated before — `human`.
//
// The seam exists so a deployment that puts a RELAY in front of `human` can
// route escalations past it (mg-65d2). Every install that has not done that
// must be untouched, because the four watchers this feeds are the fleet's
// last-resort channel and a default that moved under them would silently
// redirect the one class of mail nobody is watching for.
func TestEscalationBoxDefaultsToHuman(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("POGO_HOME", filepath.Join(dir, ".pogo"))

	cfg := Load()
	if cfg.Agents.EscalationBox != DefaultEscalationBox {
		t.Errorf("escalation_box = %q, want %q", cfg.Agents.EscalationBox, DefaultEscalationBox)
	}
	if DefaultEscalationBox != "human" {
		t.Errorf("DefaultEscalationBox = %q — the fleet's write target is %q and an "+
			"install with no relay must escalate there", DefaultEscalationBox, "human")
	}
}

// TestEscalationBoxFromConfig checks the one line an operator writes when they
// install a relay in front of `human`.
func TestEscalationBoxFromConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("POGO_HOME", filepath.Join(dir, ".pogo"))

	confDir := filepath.Join(dir, ".pogo")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "[agents]\nescalation_box = \"operator\"\n"
	if err := os.WriteFile(filepath.Join(confDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := Load()
	if cfg.Agents.EscalationBox != "operator" {
		t.Errorf("escalation_box = %q, want %q", cfg.Agents.EscalationBox, "operator")
	}
	// The coordinator must not follow it. These are different questions —
	// "which fleet agent coordinates" and "which box does a person read" — and
	// the whole point of the relay design is that they diverge.
	if cfg.Agents.CoordinatorName() != DefaultCoordinator {
		t.Errorf("coordinator = %q, want the escalation box to leave it alone",
			cfg.Agents.CoordinatorName())
	}
}

// TestEscalationBoxNameNeverEmpty covers the callers that never ran Load():
// tests, and anything holding a hand-built AgentsConfig. A blank recipient
// reaches `mg mail send` as a missing argument at the exact moment the fleet
// has already failed to clear a finding, and mg files mail for an unrecognized
// name into a fresh maildir rather than refusing (mg-f04b) — so neither a blank
// nor a typo reports itself. The accessor is the only place that can guarantee
// it, so it is the one every consumer is told to use.
func TestEscalationBoxNameNeverEmpty(t *testing.T) {
	var zero AgentsConfig
	if got := zero.EscalationBoxName(); got != DefaultEscalationBox {
		t.Errorf("zero-value EscalationBoxName() = %q, want %q", got, DefaultEscalationBox)
	}
	var nilCfg *AgentsConfig
	if got := nilCfg.EscalationBoxName(); got != DefaultEscalationBox {
		t.Errorf("nil EscalationBoxName() = %q, want %q", got, DefaultEscalationBox)
	}
	set := AgentsConfig{EscalationBox: "operator"}
	if got := set.EscalationBoxName(); got != "operator" {
		t.Errorf("EscalationBoxName() = %q, want the configured name", got)
	}
}
