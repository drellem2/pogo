package config

import (
	"testing"
	"time"
)

// The defaults asserted with NO config file present — the state every deployment
// is in until someone writes one (mg-7024). A default that is only ever
// exercised alongside an explicit override has not been tested.
//
// Bare literals on purpose: comparing against the Default* consts would make
// this test follow a future change instead of catching it.
func TestCredExpiryDefaultsToArmed(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	// Default-ON is the load-bearing assertion. The warner self-disarms on any
	// host with no readable credential, so leaving it on is safe everywhere —
	// and a fleet-wide auth outage that costs ~24h of output must not require
	// someone to have opted in beforehand.
	if !cfg.CredExpiry.Enabled {
		t.Error("cred_expiry defaults to DISABLED: the fleet-wide auth outage this " +
			"predicts has happened twice, both times unnoticed for ~24h, and a " +
			"warner nobody enabled is indistinguishable from one that does not exist")
	}
	if cfg.CredExpiry.Interval != 15*time.Minute {
		t.Errorf("interval = %s, want 15m", cfg.CredExpiry.Interval)
	}
	if cfg.CredExpiry.BlindRenotify != 24*time.Hour {
		t.Errorf("blind_renotify = %s, want 24h", cfg.CredExpiry.BlindRenotify)
	}
}

func TestCredExpiryOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[cred_expiry]\nenabled = false\ninterval = \"1h\"\nblind_renotify = \"6h\"\n")

	cfg := Load()

	if cfg.CredExpiry.Enabled {
		t.Error("enabled = true, want false — the off switch does not work")
	}
	if cfg.CredExpiry.Interval != time.Hour {
		t.Errorf("interval = %s, want 1h", cfg.CredExpiry.Interval)
	}
	if cfg.CredExpiry.BlindRenotify != 6*time.Hour {
		t.Errorf("blind_renotify = %s, want 6h", cfg.CredExpiry.BlindRenotify)
	}
}

// A config that sets only ONE cred_expiry key must not silently zero the others.
// The enabled-set flag exists precisely so `enabled = false` is distinguishable
// from "absent", and the interval fallbacks are gated on > 0.
func TestCredExpiryPartialConfigKeepsOtherDefaults(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[cred_expiry]\ninterval = \"5m\"\n")

	cfg := Load()

	if !cfg.CredExpiry.Enabled {
		t.Error("setting only `interval` disabled the warner")
	}
	if cfg.CredExpiry.Interval != 5*time.Minute {
		t.Errorf("interval = %s, want 5m", cfg.CredExpiry.Interval)
	}
	if cfg.CredExpiry.BlindRenotify != 24*time.Hour {
		t.Errorf("blind_renotify = %s, want the 24h default", cfg.CredExpiry.BlindRenotify)
	}
}
