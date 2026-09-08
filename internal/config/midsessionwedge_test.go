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
func TestMidSessionWedgeDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()
	if !cfg.MidSessionWedge.Enabled {
		t.Error("the mid-session wedge detector should be enabled by default — its whole " +
			"subject is a failure that nothing recovers, and mg-daf4's investigation " +
			"shipped without a detector at all")
	}
	if cfg.MidSessionWedge.ReportOnly {
		t.Error("report_only defaults true; a detector nobody wired to an action is what " +
			"mg-daf4 declined to ship, and the one action here is a bare return, which " +
			"carries no content and cannot duplicate a message that already landed")
	}
	if cfg.MidSessionWedge.Interval != 30*time.Second {
		t.Errorf("interval = %s, want 30s — the quiet run is measured from the last tick "+
			"at which the ring CHANGED, so the interval is the resolution of the "+
			"threshold and must stay far finer than it", cfg.MidSessionWedge.Interval)
	}
	if cfg.MidSessionWedge.NotifyTo != "mayor" {
		t.Errorf("notify_to = %q, want mayor", cfg.MidSessionWedge.NotifyTo)
	}
	// Quiescence and MaxAttempts are deliberately ZERO here. The measured
	// threshold lives in internal/midsessionwedge next to the measurement that
	// justifies it, and a mirrored default in this package would be a second
	// number to keep in step with a first — a drift shape this tree has been
	// bitten by before.
	if cfg.MidSessionWedge.Quiescence != 0 {
		t.Errorf("quiescence = %s, want 0 so the package default (which carries the "+
			"measurement) is what applies", cfg.MidSessionWedge.Quiescence)
	}
	if cfg.MidSessionWedge.MaxAttempts != 0 {
		t.Errorf("max_attempts = %d, want 0 so the package default applies", cfg.MidSessionWedge.MaxAttempts)
	}
	if cfg.MidSessionWedge.RenotifyAfter != 0 {
		t.Errorf("renotify_after = %s, want 0 so the package default applies", cfg.MidSessionWedge.RenotifyAfter)
	}
}

func TestMidSessionWedgeOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[midsession_wedge]\ninterval = \"10s\"\nquiescence = \"9m\"\n"+
		"max_attempts = \"5\"\nreport_only = true\nnotify_to = \"architect\"\n"+
		"renotify_after = \"90m\"\n")

	cfg := Load()
	if cfg.MidSessionWedge.Interval != 10*time.Second {
		t.Errorf("interval = %s, want 10s", cfg.MidSessionWedge.Interval)
	}
	if cfg.MidSessionWedge.Quiescence != 9*time.Minute {
		t.Errorf("quiescence = %s, want 9m", cfg.MidSessionWedge.Quiescence)
	}
	if cfg.MidSessionWedge.MaxAttempts != 5 {
		t.Errorf("max_attempts = %d, want 5", cfg.MidSessionWedge.MaxAttempts)
	}
	if !cfg.MidSessionWedge.ReportOnly {
		t.Error("report_only = true was not applied")
	}
	if cfg.MidSessionWedge.NotifyTo != "architect" {
		t.Errorf("notify_to = %q, want architect", cfg.MidSessionWedge.NotifyTo)
	}
	if cfg.MidSessionWedge.RenotifyAfter != 90*time.Minute {
		t.Errorf("renotify_after = %s, want 90m", cfg.MidSessionWedge.RenotifyAfter)
	}
}

// A NEGATIVE quiescence is the documented way to turn the requirement off, so it
// must survive the merge like any other override rather than being filtered out
// as "not greater than zero". Only tests should ever set it: a watcher with no
// quiescence requirement types a bare return into every agent that owes a
// submit the instant it owes one, which is the state every healthy mid-turn
// agent is in.
func TestMidSessionWedgeNegativeQuiescenceSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[midsession_wedge]\nquiescence = \"-1s\"\n")
	if got := Load().MidSessionWedge.Quiescence; got != -time.Second {
		t.Errorf("quiescence = %s, want -1s", got)
	}
}

// enabled = false and report_only = false must both be applied. Both are the
// zero value of a bool, so a merge that tests the value rather than "was it
// set" silently ignores the only two lines an operator would write to stand this
// detector down.
func TestMidSessionWedgeFalseValuesAreApplied(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[midsession_wedge]\nenabled = false\n")
	if Load().MidSessionWedge.Enabled {
		t.Error("enabled = false was ignored; an operator cannot stand the detector down")
	}
}
