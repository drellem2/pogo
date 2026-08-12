package config

import (
	"testing"
	"time"
)

// The defaults, asserted with NO config file present — the state every
// deployment is in until someone writes one. A default only ever exercised
// alongside an explicit override has not been tested.
func TestReviewDeclDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.ReviewDecl.Enabled {
		t.Error("the review-declaration detector is off by default: a detector for a silently-missing " +
			"guard that must be switched on by hand is a guard nobody has (mg-253e)")
	}
	if cfg.ReviewDecl.Interval != 30*time.Minute {
		t.Errorf("interval = %s, want 30m", cfg.ReviewDecl.Interval)
	}
	if cfg.ReviewDecl.RenotifyAfter != 24*time.Hour {
		t.Errorf("renotify_after = %s, want 24h", cfg.ReviewDecl.RenotifyAfter)
	}
	// A bare literal on purpose: comparing against DefaultReviewDeclNotifyTo would
	// make this test FOLLOW a future flip to `human` instead of catching it.
	if cfg.ReviewDecl.NotifyTo != "mayor" {
		t.Errorf("notify_to = %q, want %q — the coordinator is the agent that files review tickets "+
			"and therefore the only one that can write the missing line", cfg.ReviewDecl.NotifyTo, "mayor")
	}
	if cfg.ReviewDecl.NotifyTo == "human" {
		t.Error("review-declaration findings default to `human`. mg-253e priced this residual itself: " +
			"a missed write costs ONE recoverable round, and routing a defence-in-depth gap to a human " +
			"maildir spends a scarce reader and gets every sibling detector filtered alongside it")
	}
}

func TestReviewDeclOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[review_decl]\ninterval = \"5m\"\nrenotify_after = \"2h\"\nnotify_to = \"pm-pogo\"\n")

	cfg := Load()

	if cfg.ReviewDecl.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.ReviewDecl.Interval)
	}
	if cfg.ReviewDecl.RenotifyAfter != 2*time.Hour {
		t.Errorf("renotify_after = %s, want 2h", cfg.ReviewDecl.RenotifyAfter)
	}
	if cfg.ReviewDecl.NotifyTo != "pm-pogo" {
		t.Errorf("notify_to = %q, want pm-pogo", cfg.ReviewDecl.NotifyTo)
	}
	if !cfg.ReviewDecl.Enabled {
		t.Error("an override of unrelated keys turned the detector off")
	}
}

// The off switch has to actually reach the merged config. `enabled = false` is
// the zero value of a bool, so a merge testing truthiness rather than
// "was this key set" would silently drop it and the detector would keep running
// against an operator's explicit instruction.
func TestReviewDeclDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[review_decl]\nenabled = false\n")

	cfg := Load()

	if cfg.ReviewDecl.Enabled {
		t.Error("`enabled = false` did not survive the merge — the zero value of a bool is " +
			"indistinguishable from an unset key without the explicit set-flag")
	}
}
