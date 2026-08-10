package config

import (
	"testing"
	"time"
)

// The defaults, with NO config file present — the state every deployment is in
// until someone writes one, and the state the box was in on 2026-08-08.
//
// Bare literals on purpose: comparing against the Default* constants would make
// this test follow a future retune instead of catching it.
func TestOrchestrationResumeDefaults(t *testing.T) {
	layeredSandbox(t)

	cfg := Load()

	if !cfg.OrchestrationResume.Enabled {
		t.Error("the resume deadline defaults OFF, which means a fleet stopped by a procedure " +
			"that then dies stays stopped until a human notices — the 33-hour shape of mg-56ac. " +
			"This mechanism is worth nothing unarmed (mg-5af1)")
	}
	if cfg.OrchestrationResume.Grace != 15*time.Minute {
		t.Errorf("grace = %s, want 15m — comfortably longer than any legitimate stop/restart "+
			"cycle (so it cannot fight an ordinary deploy) and irrelevantly shorter than the "+
			"33-hour outage it exists to bound", cfg.OrchestrationResume.Grace)
	}
	if cfg.OrchestrationResume.Retry != time.Minute {
		t.Errorf("retry = %s, want 1m", cfg.OrchestrationResume.Retry)
	}
}

func TestOrchestrationResumeOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[orchestration_resume]\ngrace = \"45m\"\nretry = \"5m\"\n")

	cfg := Load()
	if cfg.OrchestrationResume.Grace != 45*time.Minute {
		t.Errorf("grace = %s, want 45m", cfg.OrchestrationResume.Grace)
	}
	if cfg.OrchestrationResume.Retry != 5*time.Minute {
		t.Errorf("retry = %s, want 5m", cfg.OrchestrationResume.Retry)
	}
}

// `enabled = false` must survive the merge. A bool cannot distinguish "unset"
// from "explicitly false" on its own — without the tracked key the off switch
// would be silently ignored, which for a mechanism that RESTARTS THE FLEET is
// the wrong direction to fail in: an operator who turned it off and was not
// obeyed finds out when it restarts something.
func TestOrchestrationResumeDisableSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[orchestration_resume]\nenabled = false\n")

	if cfg := Load(); cfg.OrchestrationResume.Enabled {
		t.Error("enabled = false was dropped by the merge")
	}
}

// The off switch for the deadline alone is a NEGATIVE grace, because zero
// already means "unset, take the default". Same shape as done_reap's negative
// idle_grace and for the same reason.
func TestOrchestrationResumeNegativeGraceSurvivesTheMerge(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[orchestration_resume]\ngrace = \"-1s\"\n")

	if cfg := Load(); cfg.OrchestrationResume.Grace >= 0 {
		t.Errorf("grace = %s, want the negative off switch to survive", cfg.OrchestrationResume.Grace)
	}
}
