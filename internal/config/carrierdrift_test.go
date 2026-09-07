package config

import (
	"strings"
	"testing"
	"time"
)

// The defaults, asserted with NO config file present — the state every
// deployment is in until someone writes one. A default only ever exercised
// alongside an explicit override has not been tested.
func TestCarrierDriftDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.CarrierDrift.Enabled {
		t.Error("the carrier re-read is off by default. All three founding instances were found " +
			"by accident while every scheduled check read clean; a detector for that which must " +
			"be switched on by hand is a detector nobody has (mg-5d9d)")
	}
	if cfg.CarrierDrift.Interval != time.Hour {
		t.Errorf("interval = %s, want 1h", cfg.CarrierDrift.Interval)
	}
	if cfg.CarrierDrift.RenotifyAfter != 24*time.Hour {
		t.Errorf("renotify_after = %s, want 24h", cfg.CarrierDrift.RenotifyAfter)
	}
	if cfg.CarrierDrift.EscalateAfter != 72*time.Hour {
		t.Errorf("escalate_after = %s, want 72h", cfg.CarrierDrift.EscalateAfter)
	}
	// A bare literal on purpose: comparing against DefaultCarrierDriftNotifyTo
	// would make this test FOLLOW a future flip instead of catching it.
	if cfg.CarrierDrift.NotifyTo != "mayor" {
		t.Errorf("notify_to = %q, want mayor — the coordinator is the only agent that can resolve "+
			"a carrier, dispatch the triage that acknowledges a reporter, or move a stage",
			cfg.CarrierDrift.NotifyTo)
	}
	// The three windows default to ZERO here and are resolved inside
	// carrierdrift, deliberately: a concrete duration copied into the config
	// package would freeze the package default at whatever it was the day this
	// was written, and the two would then drift apart silently.
	if cfg.CarrierDrift.AckWindow != 0 || cfg.CarrierDrift.StageWindow != 0 || cfg.CarrierDrift.ClosedGrace != 0 {
		t.Errorf("windows are pre-resolved in config (%s/%s/%s); they must stay zero so "+
			"internal/carrierdrift owns the defaults",
			cfg.CarrierDrift.AckWindow, cfg.CarrierDrift.StageWindow, cfg.CarrierDrift.ClosedGrace)
	}
	if cfg.CarrierDrift.IncludeShelved {
		t.Error("shelved carriers are scanned by default; that boundary is meant to be opt-in")
	}
}

func TestCarrierDriftOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[carrier_drift]\ninterval = \"15m\"\nack_window = \"4h\"\n"+
		"stage_window = \"48h\"\nclosed_grace = \"30m\"\nrenotify_after = \"2h\"\n"+
		"notify_to = \"pm-pogo\"\nescalate_after = \"12h\"\ninclude_shelved = true\n"+
		"stages = [\"triage\", \"gated\"]\n")

	cfg := Load()

	if cfg.CarrierDrift.Interval != 15*time.Minute {
		t.Errorf("interval = %s, want 15m", cfg.CarrierDrift.Interval)
	}
	if cfg.CarrierDrift.AckWindow != 4*time.Hour {
		t.Errorf("ack_window = %s, want 4h", cfg.CarrierDrift.AckWindow)
	}
	if cfg.CarrierDrift.StageWindow != 48*time.Hour {
		t.Errorf("stage_window = %s, want 48h", cfg.CarrierDrift.StageWindow)
	}
	if cfg.CarrierDrift.ClosedGrace != 30*time.Minute {
		t.Errorf("closed_grace = %s, want 30m", cfg.CarrierDrift.ClosedGrace)
	}
	if cfg.CarrierDrift.RenotifyAfter != 2*time.Hour {
		t.Errorf("renotify_after = %s, want 2h", cfg.CarrierDrift.RenotifyAfter)
	}
	if cfg.CarrierDrift.NotifyTo != "pm-pogo" {
		t.Errorf("notify_to = %q, want pm-pogo", cfg.CarrierDrift.NotifyTo)
	}
	if cfg.CarrierDrift.EscalateAfter != 12*time.Hour {
		t.Errorf("escalate_after = %s, want 12h", cfg.CarrierDrift.EscalateAfter)
	}
	if !cfg.CarrierDrift.IncludeShelved {
		t.Error("include_shelved = true did not survive")
	}
	if got := strings.Join(cfg.CarrierDrift.Stages, ","); got != "triage,gated" {
		t.Errorf("stages = %q, want triage,gated", got)
	}
	if !cfg.CarrierDrift.Enabled {
		t.Error("an override of unrelated keys turned the detector off")
	}
}

// The off switch has to actually reach the merged config. `enabled = false` is
// the zero value of a bool, so a merge testing truthiness rather than "was this
// key set" would silently drop it and the detector would keep running against an
// operator's explicit instruction.
func TestCarrierDriftDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[carrier_drift]\nenabled = false\n")

	if Load().CarrierDrift.Enabled {
		t.Error("`enabled = false` did not survive the merge — the zero value of a bool is " +
			"indistinguishable from an unset key without a was-set flag")
	}
}

// A NEGATIVE window is the documented way to turn ONE of the three checks off,
// and it must survive a merge that only accepts positives. Merging `> 0` would
// restore the default and leave an operator who deliberately silenced a check
// still receiving it — which is worse than never offering the switch.
func TestCarrierDriftNegativeWindowsSurviveTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[carrier_drift]\nack_window = \"-1s\"\nstage_window = \"-1s\"\nclosed_grace = \"-1s\"\n")

	cfg := Load()

	for name, got := range map[string]time.Duration{
		"ack_window":   cfg.CarrierDrift.AckWindow,
		"stage_window": cfg.CarrierDrift.StageWindow,
		"closed_grace": cfg.CarrierDrift.ClosedGrace,
	} {
		if got >= 0 {
			t.Errorf("%s = %s — a negative override was merged away, so the check the operator "+
				"turned off is still running", name, got)
		}
	}
	// And a negative escalate_after must disable escalation for the same reason.
	_, home2 := layeredSandbox(t)
	write(t, home2, "[carrier_drift]\nescalate_after = \"-1s\"\n")
	if Load().CarrierDrift.EscalateAfter >= 0 {
		t.Error("a negative escalate_after was merged away")
	}
}
