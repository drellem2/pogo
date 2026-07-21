package config

import (
	"testing"
	"time"
)

// The routing default, asserted with NO config file present — the state every
// deployment is in until someone writes one (mg-b586). A default that is only
// ever exercised alongside an explicit override has not been tested.
//
// Bare literals on purpose: comparing against DefaultGHTeardownNotifyTo would
// make this test follow a future flip back to `human` instead of catching it.
func TestGHTeardownDefaultsToAFleetMailbox(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if cfg.GHTeardown.NotifyTo != "pm-pogo" {
		t.Errorf("notify_to = %q, want %q", cfg.GHTeardown.NotifyTo, "pm-pogo")
	}
	if cfg.GHTeardown.NotifyTo == "human" {
		t.Error("teardown findings default to `human`: a gh-issue teardown miss is a " +
			"fleet workflow failure, and an hourly detector mailing a human directly " +
			"bypasses the urgent-plus-daily-digest mail contract")
	}
	if cfg.GHTeardown.EscalateAfter != 72*time.Hour {
		t.Errorf("escalate_after = %s, want 72h", cfg.GHTeardown.EscalateAfter)
	}
}

func TestGHTeardownNotifyToOverride(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[gh_teardown]\nnotify_to = \"mayor\"\nescalate_after = \"12h\"\n")

	cfg := Load()

	if cfg.GHTeardown.NotifyTo != "mayor" {
		t.Errorf("notify_to = %q, want %q", cfg.GHTeardown.NotifyTo, "mayor")
	}
	if cfg.GHTeardown.EscalateAfter != 12*time.Hour {
		t.Errorf("escalate_after = %s, want 12h", cfg.GHTeardown.EscalateAfter)
	}
}

// Escalation is disabled with a negative duration, because zero already means
// "unset, take the default". The merge must therefore accept a non-zero value
// rather than a positive one, or the off switch would be silently dropped and
// the config would appear to work while `human` kept being copied.
func TestGHTeardownEscalationOffSwitchSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[gh_teardown]\nescalate_after = \"-1s\"\n")

	cfg := Load()

	if cfg.GHTeardown.EscalateAfter >= 0 {
		t.Errorf("escalate_after = %s, want the negative off switch to survive", cfg.GHTeardown.EscalateAfter)
	}
}
