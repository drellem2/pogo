package config

import "testing"

// drellem2/pogo#185: synthwatch, refusalwatch and turnwatch had no config key at
// all. These tests pin the three switches that now exist.

// The defaults, with NO config file present — the state every deployment is in
// until someone writes one. All three must stay armed: adding the keys was not
// meant to change behaviour for anyone who does not use them.
func TestDetectorSwitchDefaultsAreOn(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()
	if !cfg.SynthWatch.Enabled {
		t.Error("[synth_watch] enabled should default to true")
	}
	if !cfg.RefusalWatch.Enabled {
		t.Error("[refusal_watch] enabled should default to true")
	}
	if !cfg.TurnWatch.Enabled {
		t.Error("[turn_watch] enabled should default to true")
	}
}

// enabled = false must survive the file->default merge. `false` is also the
// zero value, so a merge on truthiness would silently discard the off switch.
func TestDetectorSwitchDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[synth_watch]\nenabled = false\n\n[refusal_watch]\nenabled = false\n\n[turn_watch]\nenabled = false\n")

	cfg := Load()
	if cfg.SynthWatch.Enabled {
		t.Error("[synth_watch] enabled = false was discarded by the merge")
	}
	if cfg.RefusalWatch.Enabled {
		t.Error("[refusal_watch] enabled = false was discarded by the merge")
	}
	if cfg.TurnWatch.Enabled {
		t.Error("[turn_watch] enabled = false was discarded by the merge")
	}
}

// The switches are independent: turning one off must not touch the others.
func TestDetectorSwitchesAreIndependent(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[refusal_watch]\nenabled = false\n")

	cfg := Load()
	if cfg.RefusalWatch.Enabled {
		t.Error("[refusal_watch] enabled = false was discarded")
	}
	if !cfg.SynthWatch.Enabled || !cfg.TurnWatch.Enabled {
		t.Errorf("synth_watch=%v turn_watch=%v, want both still true", cfg.SynthWatch.Enabled, cfg.TurnWatch.Enabled)
	}
}

// Layering, both directions. A higher-precedence file that OMITS the key must
// not re-arm (or disarm) what a lower layer set, and one that sets it wins.
func TestDetectorSwitchLayering(t *testing.T) {
	t.Run("omitted key in the override layer keeps the lower layer's false", func(t *testing.T) {
		xdg, home := layeredSandbox(t)
		write(t, xdg, "[synth_watch]\nenabled = false\n")
		write(t, home, "[server]\nport = 10001\n")

		if cfg := Load(); cfg.SynthWatch.Enabled {
			t.Error("a POGO_HOME config that says nothing about [synth_watch] re-armed it")
		}
	})
	t.Run("the override layer's explicit true wins", func(t *testing.T) {
		xdg, home := layeredSandbox(t)
		write(t, xdg, "[turn_watch]\nenabled = false\n")
		write(t, home, "[turn_watch]\nenabled = true\n")

		if cfg := Load(); !cfg.TurnWatch.Enabled {
			t.Error("the higher-precedence enabled = true did not win")
		}
	})
}
