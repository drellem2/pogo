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
func TestWedgeWatchDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()
	if !cfg.WedgeWatch.Enabled {
		t.Error("wedge-watch should be enabled by default — it is report-only, holds no mail seam, " +
			"and the fault it detects cost this box ~20 agent-hours across two nights")
	}
	if cfg.WedgeWatch.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.WedgeWatch.Interval)
	}
	if cfg.WedgeWatch.MarkerHoldDown != 10*time.Minute {
		t.Errorf("marker_hold_down = %s, want 10m — an agent merely WRITING about a dead-end "+
			"marker has it in its own PTY, so this must not be zero", cfg.WedgeWatch.MarkerHoldDown)
	}
	if cfg.WedgeWatch.FreezeHoldDown != 30*time.Minute {
		t.Errorf("freeze_hold_down = %s, want 30m — it must span at least two mail-check fires "+
			"at the fleet's 10-minute cadence", cfg.WedgeWatch.FreezeHoldDown)
	}
	if cfg.WedgeWatch.MinUptime != time.Hour {
		t.Errorf("min_uptime = %s, want 1h", cfg.WedgeWatch.MinUptime)
	}
	if cfg.WedgeWatch.Ratio != 20 {
		t.Errorf("ratio = %v, want 20", cfg.WedgeWatch.Ratio)
	}
	if cfg.WedgeWatch.CoincidenceWindow != 2*time.Hour {
		t.Errorf("coincidence_window = %s, want 2h — a short window reports an outage artifact "+
			"as a poisoned credential, which pages a human for a re-login that fixes nothing",
			cfg.WedgeWatch.CoincidenceWindow)
	}
	if cfg.WedgeWatch.RenotifyAfter != 6*time.Hour {
		t.Errorf("renotify_after = %s, want 6h", cfg.WedgeWatch.RenotifyAfter)
	}
}

func TestWedgeWatchOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[wedge_watch]\ninterval = \"2m\"\nmarker_hold_down = \"3m\"\n"+
		"freeze_hold_down = \"45m\"\nmin_uptime = \"20m\"\nratio = \"50\"\n"+
		"coincidence_window = \"4h\"\nrenotify_after = \"1h\"\n")

	cfg := Load()
	if cfg.WedgeWatch.Interval != 2*time.Minute {
		t.Errorf("interval = %s, want 2m", cfg.WedgeWatch.Interval)
	}
	if cfg.WedgeWatch.MarkerHoldDown != 3*time.Minute {
		t.Errorf("marker_hold_down = %s, want 3m", cfg.WedgeWatch.MarkerHoldDown)
	}
	if cfg.WedgeWatch.FreezeHoldDown != 45*time.Minute {
		t.Errorf("freeze_hold_down = %s, want 45m", cfg.WedgeWatch.FreezeHoldDown)
	}
	if cfg.WedgeWatch.MinUptime != 20*time.Minute {
		t.Errorf("min_uptime = %s, want 20m", cfg.WedgeWatch.MinUptime)
	}
	if cfg.WedgeWatch.Ratio != 50 {
		t.Errorf("ratio = %v, want 50", cfg.WedgeWatch.Ratio)
	}
	if cfg.WedgeWatch.CoincidenceWindow != 4*time.Hour {
		t.Errorf("coincidence_window = %s, want 4h", cfg.WedgeWatch.CoincidenceWindow)
	}
	if cfg.WedgeWatch.RenotifyAfter != time.Hour {
		t.Errorf("renotify_after = %s, want 1h", cfg.WedgeWatch.RenotifyAfter)
	}
}

// enabled=false must survive the file->default merge. The bug this guards is
// the one every *EnabledSet flag in this package exists for: `false` is also the
// zero value, so a naive merge silently discards the operator's off switch and
// leaves the runner armed.
func TestWedgeWatchDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[wedge_watch]\nenabled = false\n")

	if cfg := Load(); cfg.WedgeWatch.Enabled {
		t.Error("enabled = false was discarded by the merge")
	}
}

// The negative off switches must survive too. They are documented as the way to
// disable a hold-down, and a `> 0` merge would silently drop them — leaving a
// test or debugging session with the hold-down it explicitly turned off.
func TestWedgeWatchNegativeOffSwitchesSurviveTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[wedge_watch]\nmarker_hold_down = \"-1s\"\nfreeze_hold_down = \"-1s\"\nmin_uptime = \"-1s\"\n")

	cfg := Load()
	if cfg.WedgeWatch.MarkerHoldDown >= 0 {
		t.Errorf("marker_hold_down = %s, want the negative off switch to survive", cfg.WedgeWatch.MarkerHoldDown)
	}
	if cfg.WedgeWatch.FreezeHoldDown >= 0 {
		t.Errorf("freeze_hold_down = %s, want the negative off switch to survive", cfg.WedgeWatch.FreezeHoldDown)
	}
	if cfg.WedgeWatch.MinUptime >= 0 {
		t.Errorf("min_uptime = %s, want the negative off switch to survive", cfg.WedgeWatch.MinUptime)
	}
}
