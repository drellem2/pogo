package config

import (
	"reflect"
	"testing"
	"time"
)

// Bare literals on purpose, as in heartwatch_test.go: a retune of the
// Default* constants must show up here, not be followed silently.
func TestCrewResetDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.CrewReset.Enabled {
		t.Error("[crew_reset] defaults off; mg-5b58d ships it on (see CrewResetConfig for why)")
	}
	if cfg.CrewReset.After != 4*time.Hour {
		t.Errorf("after = %s, want 4h", cfg.CrewReset.After)
	}
	if cfg.CrewReset.RenoticeAfter != time.Hour {
		t.Errorf("renotice_after = %s, want 1h", cfg.CrewReset.RenoticeAfter)
	}
	if cfg.CrewReset.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.CrewReset.Interval)
	}
	if len(cfg.CrewReset.Exclude) != 0 {
		t.Errorf("exclude = %v, want empty", cfg.CrewReset.Exclude)
	}
}

func TestCrewResetOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[crew_reset]\nenabled = false\nafter = \"3h\"\nrenotice_after = \"30m\"\n"+
		"interval = \"1m\"\nexclude = [\"mayor\", \"architect\"]\n")

	cfg := Load()

	if cfg.CrewReset.Enabled {
		t.Error("enabled = true, want false")
	}
	if cfg.CrewReset.After != 3*time.Hour {
		t.Errorf("after = %s, want 3h", cfg.CrewReset.After)
	}
	if cfg.CrewReset.RenoticeAfter != 30*time.Minute {
		t.Errorf("renotice_after = %s, want 30m", cfg.CrewReset.RenoticeAfter)
	}
	if cfg.CrewReset.Interval != time.Minute {
		t.Errorf("interval = %s, want 1m", cfg.CrewReset.Interval)
	}
	if want := []string{"mayor", "architect"}; !reflect.DeepEqual(cfg.CrewReset.Exclude, want) {
		t.Errorf("exclude = %v, want %v", cfg.CrewReset.Exclude, want)
	}
}

// A partial override keeps its siblings, and an explicit `exclude = []` in the
// override layer clears a list the base layer set.
func TestCrewResetLayering(t *testing.T) {
	xdg, home := layeredSandbox(t)
	write(t, xdg, "[crew_reset]\nexclude = [\"mayor\"]\nafter = \"5h\"\n")
	write(t, home, "[crew_reset]\nexclude = []\n")

	cfg := Load()

	if !cfg.CrewReset.Enabled {
		t.Error("a layer that omits enabled disarmed the notice")
	}
	if cfg.CrewReset.After != 5*time.Hour {
		t.Errorf("after = %s, want the base layer's 5h", cfg.CrewReset.After)
	}
	if len(cfg.CrewReset.Exclude) != 0 {
		t.Errorf("exclude = %v, want the override layer's empty list", cfg.CrewReset.Exclude)
	}
}
