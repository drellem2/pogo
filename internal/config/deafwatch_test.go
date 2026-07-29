package config

import (
	"testing"
	"time"
)

// The defaults, asserted with NO config file present — the state every
// deployment is in until someone writes one.
//
// Bare literals on purpose: comparing against the Default* constants would make
// this test follow a future retune instead of catching it.
func TestDeafWatchDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.DeafWatch.Enabled {
		t.Error("deaf-watch defaults off: the judgement it announces already existed and " +
			"had exactly one reader — `pogo agent diagnose <name>` — for two tickets, " +
			"which is the entire ticket (mg-032b)")
	}
	if cfg.DeafWatch.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.DeafWatch.Interval)
	}
	if cfg.DeafWatch.HoldDown != 15*time.Minute {
		t.Errorf("hold_down = %s, want 15m — spawn and schedule registration are not simultaneous, "+
			"and a redeploy re-runs that gap for the whole fleet", cfg.DeafWatch.HoldDown)
	}
	if cfg.DeafWatch.RenotifyAfter != 6*time.Hour {
		t.Errorf("renotify_after = %s, want 6h", cfg.DeafWatch.RenotifyAfter)
	}
	if cfg.DeafWatch.NotifyTo != "mayor" {
		t.Errorf("notify_to = %q, want %q", cfg.DeafWatch.NotifyTo, "mayor")
	}
	if cfg.DeafWatch.EscalateAfter != 24*time.Hour {
		t.Errorf("escalate_after = %s, want 24h", cfg.DeafWatch.EscalateAfter)
	}
}

func TestDeafWatchOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[deaf_watch]\ninterval = \"2m\"\nhold_down = \"45m\"\nrenotify_after = \"1h\"\nnotify_to = \"pm-pogo\"\nescalate_after = \"12h\"\n")

	cfg := Load()

	if cfg.DeafWatch.Interval != 2*time.Minute {
		t.Errorf("interval = %s, want 2m", cfg.DeafWatch.Interval)
	}
	if cfg.DeafWatch.HoldDown != 45*time.Minute {
		t.Errorf("hold_down = %s, want 45m", cfg.DeafWatch.HoldDown)
	}
	if cfg.DeafWatch.RenotifyAfter != time.Hour {
		t.Errorf("renotify_after = %s, want 1h", cfg.DeafWatch.RenotifyAfter)
	}
	if cfg.DeafWatch.NotifyTo != "pm-pogo" {
		t.Errorf("notify_to = %q, want %q", cfg.DeafWatch.NotifyTo, "pm-pogo")
	}
	if cfg.DeafWatch.EscalateAfter != 12*time.Hour {
		t.Errorf("escalate_after = %s, want 12h", cfg.DeafWatch.EscalateAfter)
	}
}

// `enabled = false` must survive the merge. A bool cannot distinguish "unset"
// from "explicitly false" on its own, which is why parsedConfig tracks the key
// separately — without that, the off switch would be silently ignored.
func TestDeafWatchDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[deaf_watch]\nenabled = false\n")

	if cfg := Load(); cfg.DeafWatch.Enabled {
		t.Error("enabled = false was dropped by the merge")
	}
}

// Both off switches are NEGATIVE durations, because zero already means "unset,
// take the default". The merge must therefore accept a non-zero value rather
// than a positive one, or each switch would be silently dropped and the config
// would appear to work.
func TestDeafWatchNegativeOffSwitchesSurviveTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[deaf_watch]\nhold_down = \"-1s\"\nescalate_after = \"-1s\"\n")

	cfg := Load()
	if cfg.DeafWatch.HoldDown >= 0 {
		t.Errorf("hold_down = %s, want the negative off switch to survive", cfg.DeafWatch.HoldDown)
	}
	if cfg.DeafWatch.EscalateAfter >= 0 {
		t.Errorf("escalate_after = %s, want the negative off switch to survive", cfg.DeafWatch.EscalateAfter)
	}
}
