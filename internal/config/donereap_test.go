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
func TestDoneReapDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.DoneReap.Enabled {
		t.Error("done-reap defaults off: a polecat that completes without a merge would go back to " +
			"holding a concurrency slot until a coordinator noticed, which is the entire ticket (mg-56d1)")
	}
	if cfg.DoneReap.IdleGrace != 2*time.Minute {
		t.Errorf("idle_grace = %s, want 2m — long enough for the post-`done` tail work a polecat "+
			"actually does (one mail, one filing), short enough that the measured 7m16s leak is cut to under a quarter",
			cfg.DoneReap.IdleGrace)
	}
}

func TestDoneReapOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[done_reap]\nidle_grace = \"5m\"\n")

	if cfg := Load(); cfg.DoneReap.IdleGrace != 5*time.Minute {
		t.Errorf("idle_grace = %s, want 5m", cfg.DoneReap.IdleGrace)
	}
}

// `enabled = false` must survive the merge. A bool cannot distinguish "unset"
// from "explicitly false" on its own, which is why parsedConfig tracks the key
// separately — without that, the off switch would be silently ignored.
func TestDoneReapDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[done_reap]\nenabled = false\n")

	if cfg := Load(); cfg.DoneReap.Enabled {
		t.Error("enabled = false was dropped by the merge")
	}
}

// The off switch for the grace window is a NEGATIVE duration, because zero
// already means "unset, take the default". The merge must therefore accept a
// non-zero value rather than a positive one, or the switch would be silently
// dropped and the config would appear to work.
//
// It exists for tests only. A zero-grace reaper stops a done polecat the instant
// it is seen, including mid-mail, which is the one outcome strictly worse than
// the leak this reaper closes.
func TestDoneReapNegativeGraceSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[done_reap]\nidle_grace = \"-1s\"\n")

	if cfg := Load(); cfg.DoneReap.IdleGrace >= 0 {
		t.Errorf("idle_grace = %s, want the negative off switch to survive", cfg.DoneReap.IdleGrace)
	}
}
