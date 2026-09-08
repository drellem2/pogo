package config

import (
	"testing"
	"time"
)

// The defaults, asserted with NO config file present — the state every
// deployment is in until someone writes one.
//
// This one carries the same weight its hand-edit sibling does, for a sharper
// reason: mg-385f's finding is that internal/staleness has answered this
// question correctly since mg-dd49 and nothing ran it. A detector that shipped
// with `enabled` defaulting to false would be that defect one level down —
// present in the binary, absent in the fleet — which is the whole class this
// ticket documents.
func TestPromptStaleDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.PromptStale.Enabled {
		t.Error("the prompt staleness detector is off by default: a correct detector that must " +
			"be switched on by hand is exactly the unread instrument mg-385f exists to remove")
	}
	if cfg.PromptStale.Interval != 6*time.Hour {
		t.Errorf("interval = %s, want 6h", cfg.PromptStale.Interval)
	}
	if cfg.PromptStale.RenotifyAfter != 24*time.Hour {
		t.Errorf("renotify_after = %s, want 24h — SHORTER than the hand-edit detector's 72h "+
			"because a stale prompt is nobody's decision and clears on the next successful "+
			"nightly, so a notice that outlives one nightly is news",
			cfg.PromptStale.RenotifyAfter)
	}
	if cfg.PromptStale.Ref != "origin/main" {
		t.Errorf("ref = %q, want origin/main", cfg.PromptStale.Ref)
	}
	if cfg.PromptStale.SkipRemote {
		t.Error("skip_remote defaults to true: the reference's own position against the live " +
			"remote is what separates 'matches what shipped' from 'matches what was deployed'")
	}
}

func TestPromptStaleOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_stale]\ninterval = \"15m\"\nrenotify_after = \"2h\"\nref = \"origin/release\"\n")

	cfg := Load()

	if cfg.PromptStale.Interval != 15*time.Minute {
		t.Errorf("interval = %s, want 15m", cfg.PromptStale.Interval)
	}
	if cfg.PromptStale.RenotifyAfter != 2*time.Hour {
		t.Errorf("renotify_after = %s, want 2h", cfg.PromptStale.RenotifyAfter)
	}
	if cfg.PromptStale.Ref != "origin/release" {
		t.Errorf("ref = %q, want origin/release", cfg.PromptStale.Ref)
	}
	if !cfg.PromptStale.Enabled {
		t.Error("an override of unrelated keys turned the detector off")
	}
}

// The off switch has to reach the merged config. `enabled = false` is the zero
// value of a bool, so a merge testing truthiness rather than "was this key set"
// would silently drop it and the detector would keep running against an
// operator's explicit instruction.
func TestPromptStaleDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_stale]\nenabled = false\n")

	cfg := Load()

	if cfg.PromptStale.Enabled {
		t.Error("`enabled = false` did not survive the merge — the zero value of a bool is " +
			"indistinguishable from an unset key without the explicit set-flag")
	}
}

// skip_remote is the OTHER bool whose shipped default is false, and it gets its
// own set-flag for the same reason. `skip_remote = false` written explicitly is
// harmless today because it matches the default; it stops being harmless the
// moment the default flips, and a merge that cannot represent it is where that
// would silently break.
func TestPromptStaleSkipRemoteSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_stale]\nskip_remote = true\n")

	cfg := Load()

	if !cfg.PromptStale.SkipRemote {
		t.Error("`skip_remote = true` did not survive the merge")
	}
	if !cfg.PromptStale.Enabled {
		t.Error("setting skip_remote turned the detector off")
	}
}

// There is deliberately no notify_to and no repo. Findings are addressed
// per-file through agent.PromptAddressee, because the agent reading a superseded
// prompt is the party harmed; and the reference REPO is the deploy checkout
// rather than a config key, because a configurable path is a path somebody
// points at a working tree they are mid-edit in.
func TestPromptStaleHasNoNotifyToAndNoRepo(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[prompt_stale]\nnotify_to = \"human\"\nrepo = \"/tmp/whatever\"\n")

	cfg := Load()

	if !cfg.PromptStale.Enabled {
		t.Fatal("an unknown key in the section disabled the detector")
	}
	if cfg.PromptStale.Ref != "origin/main" {
		t.Errorf("ref = %q — an unknown key perturbed a neighbouring one", cfg.PromptStale.Ref)
	}
	// The rest of the assertion is structural: PromptStaleConfig has neither
	// field, so this test would not compile if one were added without revisiting
	// the argument above.
}
