package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// stallWatchArmed is the gate that fixes gh drellem2/pogo #75: an unconfigured
// daemon (no config file, cfg.Source empty) never auto-starts a coordinator, so
// the stall watcher must not arm — otherwise it nudges a "ringmaster" this
// process never launched. Both directions of the fix are asserted here as a pure
// unit test; TestStallWatchGate_BootDirections proves the same end-to-end against
// the real binary.
func TestStallWatchArmed_GatedOnSource(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		source  string
		want    bool
	}{
		// Direction 1 (the bug): enabled watcher on an UNCONFIGURED daemon must
		// stay disarmed — no coordinator was started, so nobody to nudge.
		{"enabled_no_config", true, "", false},
		// Direction 2 (no regression): enabled watcher on a CONFIGURED daemon
		// arms as before.
		{"enabled_with_config", true, "/some/config.toml", true},
		// Sanity: an explicitly disabled watcher never arms, config or not.
		{"disabled_with_config", false, "/some/config.toml", false},
		{"disabled_no_config", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Source: tc.source}
			cfg.StallWatch.Enabled = tc.enabled
			if got := stallWatchArmed(cfg); got != tc.want {
				t.Errorf("stallWatchArmed(enabled=%t source=%q) = %t, want %t",
					tc.enabled, tc.source, got, tc.want)
			}
		})
	}
}

// TestStallWatchGate_BootDirections boots the real pogod binary in both
// configurations and asserts the arming decision from its own logs. The unit
// test above pins the predicate; this proves main() actually threads it — the
// mg-bc47 class of bug (predicate correct in isolation, statement order wrong in
// main) is exactly why gh drellem2/pogo #75 needs an end-to-end check too.
func TestStallWatchGate_BootDirections(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real pogod; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	bin := filepath.Join(t.TempDir(), "pogod-under-test")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("building pogod: %v\n%s", err, out)
	}

	// Direction 1: no config file → cfg.Source empty → watcher must NOT arm.
	t.Run("unconfigured_does_not_arm", func(t *testing.T) {
		logs := bootPogod(t, bin, "")
		if !strings.Contains(logs, "stall watcher not armed") {
			t.Errorf("unconfigured daemon did not log the disarm decision\n--- log ---\n%s", logs)
		}
		if strings.Contains(logs, "stall watcher enabled (agent=") {
			t.Errorf("unconfigured daemon armed the stall watcher — it would nudge a coordinator it never started (gh #75)\n--- log ---\n%s", logs)
		}
	})

	// Direction 2: a config file present → cfg.Source set → watcher arms as
	// before. autostart = false keeps the test from spawning a real fleet; the
	// stall-watch gate is independent of autostart, so the watcher still arms.
	t.Run("configured_arms", func(t *testing.T) {
		logs := bootPogod(t, bin, "[agents]\nautostart = false\n")
		if !strings.Contains(logs, "stall watcher enabled (agent=") {
			t.Errorf("configured daemon did not arm the stall watcher — regression on the normal path (gh #75)\n--- log ---\n%s", logs)
		}
		if strings.Contains(logs, "stall watcher not armed") {
			t.Errorf("configured daemon logged the disarm decision — gate misfired\n--- log ---\n%s", logs)
		}
	})
}

// bootPogod starts the pogod binary in a fully sandboxed HOME/XDG/POGO_HOME,
// optionally writing cfgBody to config.toml (empty cfgBody = unconfigured
// daemon), waits until the stall-watch arming decision has been logged, then
// shuts it down and returns the full log. Every state path is private so the
// live daemon on :10000 is untouched.
func bootPogod(t *testing.T, bin, cfgBody string) string {
	t.Helper()

	sb := t.TempDir()
	state := filepath.Join(sb, "state")
	ws := filepath.Join(sb, "ws")
	for _, d := range []string{state, ws, filepath.Join(sb, ".config")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if cfgBody != "" {
		if err := os.WriteFile(filepath.Join(state, "config.toml"), []byte(cfgBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	logPath := filepath.Join(sb, "pogod.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin, "-port", strconv.Itoa(freePort(t)))
	cmd.Dir = ws
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"HOME="+sb,
		"XDG_CONFIG_HOME="+filepath.Join(sb, ".config"),
		"POGO_HOME="+state,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting pogod: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
	}()

	// Both boots log a stall-watch line early (before the HTTP serve): either
	// "stall watcher enabled (agent=" or "stall watcher not armed". Wait for
	// whichever lands so the decision is captured before shutdown.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(logPath); err == nil {
			s := string(data)
			if strings.Contains(s, "stall watcher enabled (agent=") || strings.Contains(s, "stall watcher not armed") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return readFile(t, logPath)
}
