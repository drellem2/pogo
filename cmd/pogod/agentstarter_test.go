package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestAgentStarterHonoursAutoStartDisabled: a mode round-trip must not become a
// side door around the boot gate. A daemon running with [agents] autostart =
// false refuses to spawn a fleet at boot; stop-orchestration followed by
// start-orchestration must refuse the same way — and say it did.
func TestAgentStarterHonoursAutoStartDisabled(t *testing.T) {
	swept := false
	start := agentStarterFor(
		func() bool { return true },
		func() bool { return false },
		func() []agent.AutoStartResult {
			swept = true
			return []agent.AutoStartResult{{Name: "mayor", Status: agent.AutoStartStatusStarted}}
		},
	)

	out := start()
	if swept {
		t.Error("the auto-start sweep ran despite [agents] autostart = false")
	}
	if out.Skipped != autoStartDisabledReason {
		t.Errorf("Skipped = %q, want %q — a silent zero is the shape of the bug being fixed",
			out.Skipped, autoStartDisabledReason)
	}
	if len(out.Results) != 0 {
		t.Errorf("Results = %+v, want none", out.Results)
	}
}

// TestAgentStarterRefusesOnAnUnconfiguredDaemon is the second gate, and it
// became load-bearing when this sweep moved onto the common `pogo server start`
// path (mg-060c). pogod's boot skips prompt install and auto-start entirely when
// no config file exists, so an isolated daemon (a test, CI, a POGO_HOME sandbox)
// cannot put an unrequested fleet on the machine. A CLI-driven sweep that
// ignored that check would be exactly the side door the boot path closes.
func TestAgentStarterRefusesOnAnUnconfiguredDaemon(t *testing.T) {
	swept := false
	start := agentStarterFor(
		func() bool { return false },
		func() bool { return true },
		func() []agent.AutoStartResult {
			swept = true
			return []agent.AutoStartResult{{Name: "mayor", Status: agent.AutoStartStatusStarted}}
		},
	)

	out := start()
	if swept {
		t.Error("the auto-start sweep ran on a daemon with no config file")
	}
	if out.Skipped != unconfiguredReason {
		t.Errorf("Skipped = %q, want %q", out.Skipped, unconfiguredReason)
	}
	if len(out.Results) != 0 {
		t.Errorf("Results = %+v, want none", out.Results)
	}
}

func TestAgentStarterSweepsWhenEnabled(t *testing.T) {
	start := agentStarterFor(
		func() bool { return true },
		func() bool { return true },
		func() []agent.AutoStartResult {
			return []agent.AutoStartResult{{Name: "mayor", Status: agent.AutoStartStatusStarted}}
		},
	)

	out := start()
	if out.Skipped != "" {
		t.Errorf("Skipped = %q, want empty", out.Skipped)
	}
	if len(out.Results) != 1 || out.Results[0].Name != "mayor" {
		t.Fatalf("Results = %+v, want the sweep's own results", out.Results)
	}
}

// The gate is read at call time, not captured at wiring time, so a transition
// reflects the config the daemon is running under when it happens.
func TestAgentStarterReadsTheGateAtCallTime(t *testing.T) {
	enabled := false
	start := agentStarterFor(
		func() bool { return true },
		func() bool { return enabled },
		func() []agent.AutoStartResult { return nil },
	)

	if out := start(); out.Skipped == "" {
		t.Fatal("expected the disabled gate to skip the sweep")
	}
	enabled = true
	if out := start(); out.Skipped != "" {
		t.Errorf("Skipped = %q after the gate was enabled; the gate was captured, not read", out.Skipped)
	}
}
