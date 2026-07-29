package config

import (
	"testing"
	"time"
)

// The defaults, asserted with NO config file present — the state every
// deployment is in until someone writes one. A default that is only ever
// exercised alongside an explicit override has not been tested.
//
// Bare literals on purpose: comparing against the Default* constants would make
// this test follow a future retune instead of catching it.
func TestAckWatchDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.AckWatch.Enabled {
		t.Error("ack-watch defaults off: the counters it reads already existed and " +
			"went unread for the whole of mg-1935, which is the entire ticket")
	}
	if cfg.AckWatch.Interval != 30*time.Minute {
		t.Errorf("interval = %s, want 30m", cfg.AckWatch.Interval)
	}
	if cfg.AckWatch.RenotifyAfter != 6*time.Hour {
		t.Errorf("renotify_after = %s, want 6h", cfg.AckWatch.RenotifyAfter)
	}
	if cfg.AckWatch.NotifyTo != "mayor" {
		t.Errorf("notify_to = %q, want %q — the remedy (nudge --immediate, doctor restart) is coordination work",
			cfg.AckWatch.NotifyTo, "mayor")
	}
	// Shorter than gh-teardown's 72h on purpose: the coordinator is itself a
	// crew agent and can have the very defect being reported (mg-d385), so a
	// finding it does not clear needs to reach a person sooner.
	if cfg.AckWatch.EscalateAfter != 24*time.Hour {
		t.Errorf("escalate_after = %s, want 24h", cfg.AckWatch.EscalateAfter)
	}
}

func TestAckWatchOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[ack_watch]\ninterval = \"5m\"\nrenotify_after = \"1h\"\nnotify_to = \"pm-pogo\"\nescalate_after = \"12h\"\n")

	cfg := Load()

	if cfg.AckWatch.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.AckWatch.Interval)
	}
	if cfg.AckWatch.RenotifyAfter != time.Hour {
		t.Errorf("renotify_after = %s, want 1h", cfg.AckWatch.RenotifyAfter)
	}
	if cfg.AckWatch.NotifyTo != "pm-pogo" {
		t.Errorf("notify_to = %q, want %q", cfg.AckWatch.NotifyTo, "pm-pogo")
	}
	if cfg.AckWatch.EscalateAfter != 12*time.Hour {
		t.Errorf("escalate_after = %s, want 12h", cfg.AckWatch.EscalateAfter)
	}
}

// `enabled = false` must survive the merge. A bool cannot distinguish "unset"
// from "explicitly false" on its own, which is why parsedConfig tracks the key
// separately — without that, the off switch would be silently ignored.
func TestAckWatchDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[ack_watch]\nenabled = false\n")

	if cfg := Load(); cfg.AckWatch.Enabled {
		t.Error("enabled = false was dropped by the merge")
	}
}

// Escalation is disabled with a negative duration, because zero already means
// "unset, take the default". The merge must therefore accept a non-zero value
// rather than a positive one, or the off switch would be silently dropped and
// the config would appear to work while `human` kept being copied.
func TestAckWatchEscalationOffSwitchSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[ack_watch]\nescalate_after = \"-1s\"\n")

	if cfg := Load(); cfg.AckWatch.EscalateAfter >= 0 {
		t.Errorf("escalate_after = %s, want the negative off switch to survive", cfg.AckWatch.EscalateAfter)
	}
}
