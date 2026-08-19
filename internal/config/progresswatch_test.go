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
func TestProgressWatchDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.ProgressWatch.Enabled {
		t.Error("progress-watch defaults off: it is the ONLY instrument that asks whether the " +
			"fleet is getting anything done, and the state it detects — alive, not failing, " +
			"producing nothing — reads GREEN on every other one (mg-516e)")
	}
	if cfg.ProgressWatch.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.ProgressWatch.Interval)
	}
	if cfg.ProgressWatch.HoldDown != 10*time.Minute {
		t.Errorf("hold_down = %s, want 10m — two samples at the default interval, because the "+
			"CPU member is instantaneous and a whole fleet can be between things for one of them",
			cfg.ProgressWatch.HoldDown)
	}
	if cfg.ProgressWatch.RenotifyAfter != 2*time.Hour {
		t.Errorf("renotify_after = %s, want 2h", cfg.ProgressWatch.RenotifyAfter)
	}
	if cfg.ProgressWatch.NotifyTo != "mayor" {
		t.Errorf("notify_to = %q, want %q", cfg.ProgressWatch.NotifyTo, "mayor")
	}
	if cfg.ProgressWatch.EscalateAfter != 2*time.Hour {
		t.Errorf("escalate_after = %s, want 2h", cfg.ProgressWatch.EscalateAfter)
	}
}

func TestProgressWatchOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[progress_watch]\ninterval = \"90s\"\nhold_down = \"20m\"\n"+
		"renotify_after = \"45m\"\nnotify_to = \"pm-pogo\"\nescalate_after = \"6h\"\n")

	cfg := Load()

	if cfg.ProgressWatch.Interval != 90*time.Second {
		t.Errorf("interval = %s, want 90s", cfg.ProgressWatch.Interval)
	}
	if cfg.ProgressWatch.HoldDown != 20*time.Minute {
		t.Errorf("hold_down = %s, want 20m", cfg.ProgressWatch.HoldDown)
	}
	if cfg.ProgressWatch.RenotifyAfter != 45*time.Minute {
		t.Errorf("renotify_after = %s, want 45m", cfg.ProgressWatch.RenotifyAfter)
	}
	if cfg.ProgressWatch.NotifyTo != "pm-pogo" {
		t.Errorf("notify_to = %q, want pm-pogo", cfg.ProgressWatch.NotifyTo)
	}
	if cfg.ProgressWatch.EscalateAfter != 6*time.Hour {
		t.Errorf("escalate_after = %s, want 6h", cfg.ProgressWatch.EscalateAfter)
	}
	// An override of one key must not disturb the rest.
	if !cfg.ProgressWatch.Enabled {
		t.Error("a partial [progress_watch] section turned the detector off")
	}
}

// A deployment that deliberately silences this detector must STAY silenced. The
// enabled key is tracked separately from its zero value for the same reason
// every other detector's is: `false` and `absent` are different instructions,
// and merging on the zero value restores the shipped `true` over an operator's
// explicit `false`.
func TestProgressWatchCanBeDisabled(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[progress_watch]\nenabled = false\n")

	if cfg := Load(); cfg.ProgressWatch.Enabled {
		t.Error("enabled = false did not survive the merge")
	}
}

// NEGATIVE turns a wait off; ZERO means "unset, use the default". Merging the
// two would make a config that omits the key indistinguishable from one that
// deliberately disarmed the hold-down or the escalation.
func TestProgressWatchNegativesSurvive(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[progress_watch]\nhold_down = \"-1s\"\nescalate_after = \"-1s\"\n")

	cfg := Load()
	if cfg.ProgressWatch.HoldDown != -time.Second {
		t.Errorf("hold_down = %s, want -1s to survive as the disarm", cfg.ProgressWatch.HoldDown)
	}
	if cfg.ProgressWatch.EscalateAfter != -time.Second {
		t.Errorf("escalate_after = %s, want -1s to survive as the disarm", cfg.ProgressWatch.EscalateAfter)
	}
}
