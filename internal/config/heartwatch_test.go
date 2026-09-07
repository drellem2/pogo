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
func TestHeartWatchDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.HeartWatch.Enabled {
		t.Error("heart-watch defaults off: the crew heartbeat check had exactly one executor — a " +
			"step in the coordinator's own loop — so it did not degrade when the coordinator " +
			"stopped, it stopped, and two PMs sat 14 days at ~168x T_restart (mg-d616)")
	}
	if cfg.HeartWatch.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.HeartWatch.Interval)
	}
	if cfg.HeartWatch.StallAfter != 90*time.Minute {
		t.Errorf("stall_after = %s, want 90m — mayor.md §3a's T_stall, carried over unchanged so "+
			"the prompt and the daemon cannot disagree about when an agent is late", cfg.HeartWatch.StallAfter)
	}
	if cfg.HeartWatch.RestartAfter != 120*time.Minute {
		t.Errorf("restart_after = %s, want 120m — mayor.md §3a's T_restart", cfg.HeartWatch.RestartAfter)
	}
	if cfg.HeartWatch.Grace != 30*time.Minute {
		t.Errorf("grace = %s, want 30m", cfg.HeartWatch.Grace)
	}
	if cfg.HeartWatch.HoldDown != 10*time.Minute {
		t.Errorf("hold_down = %s, want 10m", cfg.HeartWatch.HoldDown)
	}
	if cfg.HeartWatch.RenotifyAfter != 6*time.Hour {
		t.Errorf("renotify_after = %s, want 6h", cfg.HeartWatch.RenotifyAfter)
	}
}

func TestHeartWatchOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[heart_watch]\nenabled = false\ninterval = \"2m\"\nstall_after = \"45m\"\n"+
		"restart_after = \"75m\"\ngrace = \"5m\"\nhold_down = \"1m\"\nrenotify_after = \"1h\"\n")

	cfg := Load()

	if cfg.HeartWatch.Enabled {
		t.Error("enabled = true, want false")
	}
	if cfg.HeartWatch.Interval != 2*time.Minute {
		t.Errorf("interval = %s, want 2m", cfg.HeartWatch.Interval)
	}
	if cfg.HeartWatch.StallAfter != 45*time.Minute {
		t.Errorf("stall_after = %s, want 45m", cfg.HeartWatch.StallAfter)
	}
	if cfg.HeartWatch.RestartAfter != 75*time.Minute {
		t.Errorf("restart_after = %s, want 75m", cfg.HeartWatch.RestartAfter)
	}
	if cfg.HeartWatch.Grace != 5*time.Minute {
		t.Errorf("grace = %s, want 5m", cfg.HeartWatch.Grace)
	}
	if cfg.HeartWatch.HoldDown != time.Minute {
		t.Errorf("hold_down = %s, want 1m", cfg.HeartWatch.HoldDown)
	}
	if cfg.HeartWatch.RenotifyAfter != time.Hour {
		t.Errorf("renotify_after = %s, want 1h", cfg.HeartWatch.RenotifyAfter)
	}
}

// A partial override must not zero its siblings — the shape that has bitten
// this file before.
func TestHeartWatchPartialOverrideKeepsSiblings(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[heart_watch]\nstall_after = \"45m\"\n")

	cfg := Load()

	if cfg.HeartWatch.StallAfter != 45*time.Minute {
		t.Errorf("stall_after = %s, want 45m", cfg.HeartWatch.StallAfter)
	}
	if !cfg.HeartWatch.Enabled {
		t.Error("a partial override turned the detector off")
	}
	if cfg.HeartWatch.RestartAfter != 120*time.Minute {
		t.Errorf("restart_after = %s, want the 120m default to survive a sibling override",
			cfg.HeartWatch.RestartAfter)
	}
	if cfg.HeartWatch.HoldDown != 10*time.Minute {
		t.Errorf("hold_down = %s, want the 10m default to survive", cfg.HeartWatch.HoldDown)
	}
}

// A NEGATIVE window is a deliberate disable and must survive the merge, while a
// key the file simply omits must keep its default. If those two collapse, a
// config that says nothing silently turns a hold-down off.
func TestHeartWatchNegativeWindowsAreHonoured(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[heart_watch]\ngrace = \"-1s\"\nhold_down = \"-1s\"\n")

	cfg := Load()

	if cfg.HeartWatch.Grace != -time.Second {
		t.Errorf("grace = %s, want -1s carried through", cfg.HeartWatch.Grace)
	}
	if cfg.HeartWatch.HoldDown != -time.Second {
		t.Errorf("hold_down = %s, want -1s carried through", cfg.HeartWatch.HoldDown)
	}
}

func TestBlindWatchDefaults(t *testing.T) {
	layeredSandbox(t)

	cfg := Load()

	if !cfg.BlindWatch.Enabled {
		t.Error("blind-watch defaults off: wedge_watch_error had no consumer for 18 days and " +
			"2609 events, each saying out loud that an agent could NOT be judged (mg-d616)")
	}
	if cfg.BlindWatch.Interval != 15*time.Minute {
		t.Errorf("interval = %s, want 15m", cfg.BlindWatch.Interval)
	}
	if cfg.BlindWatch.HoldDown != 2*time.Hour {
		t.Errorf("hold_down = %s, want 2h", cfg.BlindWatch.HoldDown)
	}
	if cfg.BlindWatch.StaleAfter != time.Hour {
		t.Errorf("stale_after = %s, want 1h — the arm that keeps 'nothing blind' from meaning "+
			"'no detector'", cfg.BlindWatch.StaleAfter)
	}
	if cfg.BlindWatch.RenotifyAfter != 24*time.Hour {
		t.Errorf("renotify_after = %s, want 24h", cfg.BlindWatch.RenotifyAfter)
	}
}

func TestBlindWatchOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[blind_watch]\nenabled = false\ninterval = \"3m\"\nhold_down = \"30m\"\n"+
		"stale_after = \"10m\"\nrenotify_after = \"2h\"\n")

	cfg := Load()

	if cfg.BlindWatch.Enabled {
		t.Error("enabled = true, want false")
	}
	if cfg.BlindWatch.Interval != 3*time.Minute {
		t.Errorf("interval = %s, want 3m", cfg.BlindWatch.Interval)
	}
	if cfg.BlindWatch.HoldDown != 30*time.Minute {
		t.Errorf("hold_down = %s, want 30m", cfg.BlindWatch.HoldDown)
	}
	if cfg.BlindWatch.StaleAfter != 10*time.Minute {
		t.Errorf("stale_after = %s, want 10m", cfg.BlindWatch.StaleAfter)
	}
	if cfg.BlindWatch.RenotifyAfter != 2*time.Hour {
		t.Errorf("renotify_after = %s, want 2h", cfg.BlindWatch.RenotifyAfter)
	}
}

func TestBlindWatchPartialOverrideKeepsSiblings(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[blind_watch]\nhold_down = \"30m\"\n")

	cfg := Load()

	if cfg.BlindWatch.HoldDown != 30*time.Minute {
		t.Errorf("hold_down = %s, want 30m", cfg.BlindWatch.HoldDown)
	}
	if !cfg.BlindWatch.Enabled {
		t.Error("a partial override turned the detector off")
	}
	if cfg.BlindWatch.StaleAfter != time.Hour {
		t.Errorf("stale_after = %s, want the 1h default to survive a sibling override",
			cfg.BlindWatch.StaleAfter)
	}
}
