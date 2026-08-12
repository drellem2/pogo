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
func TestAbsentWatchDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.AbsentWatch.Enabled {
		t.Error("absent-watch defaults off: it is the ONLY detector whose population is the " +
			"configured set rather than the registry, and an agent that is not running is " +
			"invisible to every other one (mg-7d20)")
	}
	if cfg.AbsentWatch.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.AbsentWatch.Interval)
	}
	if cfg.AbsentWatch.HoldDown != 15*time.Minute {
		t.Errorf("hold_down = %s, want 15m — it must clear the window in which pogod's "+
			"boot-time auto-start sweep is still working through the crew", cfg.AbsentWatch.HoldDown)
	}
	if cfg.AbsentWatch.DormantAfter != 24*time.Hour {
		t.Errorf("dormant_after = %s, want 24h — an on-demand agent being off is its ordinary "+
			"state, and a detector that mails about it gets filtered", cfg.AbsentWatch.DormantAfter)
	}
	if cfg.AbsentWatch.RenotifyAfter != 12*time.Hour {
		t.Errorf("renotify_after = %s, want 12h", cfg.AbsentWatch.RenotifyAfter)
	}
	if cfg.AbsentWatch.NotifyTo != "mayor" {
		t.Errorf("notify_to = %q, want %q", cfg.AbsentWatch.NotifyTo, "mayor")
	}
	if cfg.AbsentWatch.EscalateAfter != 48*time.Hour {
		t.Errorf("escalate_after = %s, want 48h", cfg.AbsentWatch.EscalateAfter)
	}
}

// The two hold-downs are SEPARATE knobs. Tuning the supervised one must not
// silently retune the on-demand one, or a deployment that wants faster boot-gap
// detection would start mailing about every dormant agent.
func TestAbsentWatchOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[absent_watch]\ninterval = \"2m\"\nhold_down = \"45m\"\ndormant_after = \"6h\"\n"+
		"renotify_after = \"1h\"\nnotify_to = \"pm-pogo\"\nescalate_after = \"12h\"\n")

	cfg := Load()

	if cfg.AbsentWatch.Interval != 2*time.Minute {
		t.Errorf("interval = %s, want 2m", cfg.AbsentWatch.Interval)
	}
	if cfg.AbsentWatch.HoldDown != 45*time.Minute {
		t.Errorf("hold_down = %s, want 45m", cfg.AbsentWatch.HoldDown)
	}
	if cfg.AbsentWatch.DormantAfter != 6*time.Hour {
		t.Errorf("dormant_after = %s, want 6h", cfg.AbsentWatch.DormantAfter)
	}
	if cfg.AbsentWatch.RenotifyAfter != time.Hour {
		t.Errorf("renotify_after = %s, want 1h", cfg.AbsentWatch.RenotifyAfter)
	}
	if cfg.AbsentWatch.NotifyTo != "pm-pogo" {
		t.Errorf("notify_to = %q, want %q", cfg.AbsentWatch.NotifyTo, "pm-pogo")
	}
	if cfg.AbsentWatch.EscalateAfter != 12*time.Hour {
		t.Errorf("escalate_after = %s, want 12h", cfg.AbsentWatch.EscalateAfter)
	}
}

// Overriding ONE knob must leave the others at their defaults — the merge is
// key-by-key, and a section that reset its siblings would be worse than none.
func TestAbsentWatchPartialOverrideKeepsSiblings(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[absent_watch]\ndormant_after = \"72h\"\n")

	cfg := Load()
	if cfg.AbsentWatch.DormantAfter != 72*time.Hour {
		t.Errorf("dormant_after = %s, want 72h", cfg.AbsentWatch.DormantAfter)
	}
	if cfg.AbsentWatch.HoldDown != 15*time.Minute {
		t.Errorf("hold_down = %s, want the 15m default to survive a sibling override",
			cfg.AbsentWatch.HoldDown)
	}
	if !cfg.AbsentWatch.Enabled {
		t.Error("a partial override must not disarm the runner")
	}
}

// `enabled = false` must survive the merge. A bool cannot distinguish "unset"
// from "explicitly false" on its own, which is why parsedConfig tracks the key
// separately — without that, the off switch would be silently ignored.
func TestAbsentWatchDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[absent_watch]\nenabled = false\n")

	if cfg := Load(); cfg.AbsentWatch.Enabled {
		t.Error("enabled = false was dropped by the merge")
	}
}

// The off switches are NEGATIVE durations, because zero already means "unset,
// take the default". The merge must therefore accept a non-zero value rather
// than a positive one, or each switch would be silently dropped and the config
// would appear to work.
func TestAbsentWatchNegativeOffSwitchesSurviveTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[absent_watch]\nhold_down = \"-1s\"\ndormant_after = \"-1s\"\nescalate_after = \"-1s\"\n")

	cfg := Load()
	if cfg.AbsentWatch.HoldDown >= 0 {
		t.Errorf("hold_down = %s, want the negative off switch to survive", cfg.AbsentWatch.HoldDown)
	}
	if cfg.AbsentWatch.DormantAfter >= 0 {
		t.Errorf("dormant_after = %s, want the negative off switch to survive", cfg.AbsentWatch.DormantAfter)
	}
	if cfg.AbsentWatch.EscalateAfter >= 0 {
		t.Errorf("escalate_after = %s, want the negative off switch to survive", cfg.AbsentWatch.EscalateAfter)
	}
}
