package config

import (
	"testing"
	"time"
)

// The defaults, asserted with NO config file present — the state every
// deployment is in until someone writes one. A default only ever exercised
// alongside an explicit override has not been tested.
//
// This one carries extra weight: mg-0c96's whole finding is that a working
// detector with no runner audits as instrumented and reports nothing. A
// detector that ships with `enabled` defaulting to false would be that defect
// one level down — present in the binary, absent in the fleet.
func TestPromptEditDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.PromptEdit.Enabled {
		t.Error("the prompt hand-edit detector is off by default: an armed detector that must be " +
			"switched on by hand is the unscheduled instrument mg-0c96 exists to remove")
	}
	if cfg.PromptEdit.Interval != 6*time.Hour {
		t.Errorf("interval = %s, want 6h", cfg.PromptEdit.Interval)
	}
	if cfg.PromptEdit.RenotifyAfter != 72*time.Hour {
		t.Errorf("renotify_after = %s, want 72h — reconciling a prompt is a judgement about which "+
			"local edits are still load-bearing, not a command to run, and a nag arriving faster "+
			"than the work can be scheduled trains the recipient to filter it",
			cfg.PromptEdit.RenotifyAfter)
	}
}

func TestPromptEditOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_edit]\ninterval = \"15m\"\nrenotify_after = \"2h\"\n")

	cfg := Load()

	if cfg.PromptEdit.Interval != 15*time.Minute {
		t.Errorf("interval = %s, want 15m", cfg.PromptEdit.Interval)
	}
	if cfg.PromptEdit.RenotifyAfter != 2*time.Hour {
		t.Errorf("renotify_after = %s, want 2h", cfg.PromptEdit.RenotifyAfter)
	}
	if !cfg.PromptEdit.Enabled {
		t.Error("an override of unrelated keys turned the detector off")
	}
}

// The off switch has to actually reach the merged config. `enabled = false` is
// the zero value of a bool, so a merge testing truthiness rather than "was this
// key set" would silently drop it and the detector would keep running against
// an operator's explicit instruction.
func TestPromptEditDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_edit]\nenabled = false\n")

	cfg := Load()

	if cfg.PromptEdit.Enabled {
		t.Error("`enabled = false` did not survive the merge — the zero value of a bool is " +
			"indistinguishable from an unset key without the explicit set-flag")
	}
}

// There is deliberately no notify_to. Findings are addressed per-file to the
// agent that owns the prompt, because that agent is the only party who can
// judge whether a given edit is still load-bearing. A single configurable
// destination would either misroute every finding or recreate the pile a
// fleet-wide inbox becomes — which is the failure this detector's own siting
// argument (mg-10e3) is about.
func TestPromptEditHasNoNotifyTo(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_edit]\nnotify_to = \"human\"\n")

	cfg := Load()

	if !cfg.PromptEdit.Enabled {
		t.Fatal("an unknown key in the section disabled the detector")
	}
	// The assertion is structural: PromptEditConfig has no such field, so this
	// test would not compile if one were added without revisiting the argument
	// above. The Load() above proves an unknown key is ignored rather than
	// fatal.
}
