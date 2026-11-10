package config

import (
	"testing"
	"time"
)

// pogo#296: [turn_watch] max_age and hold_down. Zero in the struct means
// "unset" (pogod then applies turnlog.DefaultMaxAge / turnwatch.DefaultHoldDown,
// pinned in cmd/pogod/turnwatchcfg_test.go); only a positive duration is stored.

func TestTurnWatchWindowsUnsetWhenAbsent(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[turn_watch]\nenabled = true\n")

	tw := Load().TurnWatch
	if tw.MaxAge != 0 || tw.HoldDown != 0 || tw.MaxAgeInvalid != "" || tw.HoldDownInvalid != "" {
		t.Errorf("absent keys produced %+v, want all zero (so the defaults stay in force)", tw)
	}
}

func TestTurnWatchWindowsParse(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[turn_watch]\nmax_age = \"45m\"\nhold_down = \"5m\"\n")

	tw := Load().TurnWatch
	if tw.MaxAge != 45*time.Minute || tw.HoldDown != 5*time.Minute {
		t.Errorf("max_age=%s hold_down=%s, want 45m/5m", tw.MaxAge, tw.HoldDown)
	}
	if !tw.Enabled {
		t.Error("setting the windows disarmed the reader")
	}
}

func TestTurnWatchWindowsRejectNonsense(t *testing.T) {
	for _, tc := range []struct{ raw string }{{`"0s"`}, {`"-1h"`}, {`"soon"`}, {`0`}} {
		t.Run(tc.raw, func(t *testing.T) {
			_, home := layeredSandbox(t)
			write(t, home, "[turn_watch]\nmax_age = "+tc.raw+"\nhold_down = "+tc.raw+"\n")

			tw := Load().TurnWatch
			if tw.MaxAge != 0 || tw.HoldDown != 0 {
				t.Errorf("nonsense %s was stored: max_age=%s hold_down=%s", tc.raw, tw.MaxAge, tw.HoldDown)
			}
			if tw.MaxAgeInvalid == "" || tw.HoldDownInvalid == "" {
				t.Errorf("nonsense %s was dropped silently: %+v", tc.raw, tw)
			}
		})
	}
}

// A valid value in the override layer wins over, and clears, a rejected one
// below it; a value set only in the lower layer survives the merge.
func TestTurnWatchWindowsLayering(t *testing.T) {
	xdg, home := layeredSandbox(t)
	write(t, xdg, "[turn_watch]\nmax_age = \"-5m\"\nhold_down = \"10m\"\n")
	write(t, home, "[turn_watch]\nmax_age = \"1h\"\n")

	tw := Load().TurnWatch
	if tw.MaxAge != time.Hour || tw.MaxAgeInvalid != "" {
		t.Errorf("max_age=%s invalid=%q, want 1h and no rejection", tw.MaxAge, tw.MaxAgeInvalid)
	}
	if tw.HoldDown != 10*time.Minute {
		t.Errorf("hold_down=%s, want the lower layer's 10m", tw.HoldDown)
	}
}
