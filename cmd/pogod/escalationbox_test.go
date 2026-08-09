package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/ackwatch"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/deafwatch"
	"github.com/drellem2/pogo/internal/ghintake"
	"github.com/drellem2/pogo/internal/ghteardown"
)

// TestWatcherEscalationDefaultsAgreeWithConfig pins the four watcher packages'
// own DefaultEscalateTo constants to config.DefaultEscalationBox.
//
// This file lives in cmd/pogod because that is the only package that imports
// both sides: the watcher packages reach internal/events, which imports
// internal/config, so the same assertion inside package config is an import
// cycle.
//
// pogod passes cfg.Agents.EscalationBoxName() to all four watchers (see
// escalationBox in main.go), so inside the daemon these constants are never
// consulted. They ARE consulted by anything constructing a watcher directly
// with an empty Options.EscalateTo — the watcher packages' own tests, and any
// future caller. If one drifts, that caller escalates somewhere the operator
// never configured, and the symptom is mail that is delivered, filed, and
// unread: the shape mg-f04b found fifteen times, on which no instrument
// distinguishes a working channel from a dead one.
//
// Same failure as mg-b201, where three artifacts declaring one schedule drifted
// apart because nothing asserted they agreed.
func TestWatcherEscalationDefaultsAgreeWithConfig(t *testing.T) {
	for _, w := range []struct {
		pkg     string
		escTo   string
		notifTo string
	}{
		{"deafwatch", deafwatch.DefaultEscalateTo, deafwatch.DefaultNotifyTo},
		{"ackwatch", ackwatch.DefaultEscalateTo, ackwatch.DefaultNotifyTo},
		{"ghintake", ghintake.DefaultEscalateTo, ghintake.DefaultNotifyTo},
		{"ghteardown", ghteardown.DefaultEscalateTo, ghteardown.DefaultNotifyTo},
	} {
		if w.escTo != config.DefaultEscalationBox {
			t.Errorf("%s.DefaultEscalateTo = %q, want config.DefaultEscalationBox (%q)",
				w.pkg, w.escTo, config.DefaultEscalationBox)
		}
		// And the escalation target must not equal the notify target. Every one
		// of these watchers drops the second recipient when the two are equal
		// (`escalateTo != notifyTo`), so a deployment that pointed both at one
		// box would lose the escalation entirely rather than double-send it —
		// escalation would read as configured and be a no-op.
		if w.escTo == w.notifTo {
			t.Errorf("%s: escalate target %q equals notify target — the watchers "+
				"suppress a duplicate recipient, so this silently disables escalation",
				w.pkg, w.escTo)
		}
	}
}

// TestEscalationBoxResolvesForWatcherWiring is the wiring-side half: whatever
// an operator writes, the value pogod hands the watchers is non-empty and is
// the configured one.
//
// The assertion looks trivial and is not. The four Options structs take
// EscalateTo as a plain string with `""` meaning "use my package default", so
// passing an unresolved cfg.Agents.EscalationBox (rather than the accessor)
// would compile, would pass every default-case test, and would quietly restore
// `human` on exactly the deployments that configured something else — the ones
// that installed a relay and most need the bypass.
func TestEscalationBoxResolvesForWatcherWiring(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.AgentsConfig
		want string
	}{
		{"unset falls back to the fleet's write target", config.AgentsConfig{}, config.DefaultEscalationBox},
		{"configured relay output box is used verbatim", config.AgentsConfig{EscalationBox: "operator"}, "operator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.EscalationBoxName()
			if got != tc.want {
				t.Errorf("EscalationBoxName() = %q, want %q", got, tc.want)
			}
			if got == "" {
				t.Error("resolved to the empty string: `mg mail send` would be " +
					"invoked with no recipient at the moment the fleet has already failed")
			}
		})
	}
}

// TestAckWatchBlackoutRoutingDefaultsAgree pins the ONE routing decision mg-e2a4
// turned on, across the two packages that each declare a piece of it.
//
// It lives beside the escalation-box assertion because it is the same class of
// bug — two artifacts declaring one policy and nothing asserting they agree
// (mg-b201) — and because cmd/pogod is again the only package that imports both
// sides.
//
// The failure it guards against is specific and silent. If config's blackout
// renotify drifts above ackwatch's, or above the sampling interval, a dead fleet
// stops re-announcing and the incident fits back inside one quiet period; and
// pogod would still log a value that looked configured.
func TestAckWatchBlackoutRoutingDefaultsAgree(t *testing.T) {
	if config.DefaultAckWatchBlackoutRenotify != ackwatch.DefaultBlackoutRenotifyAfter {
		t.Errorf("config says %s, ackwatch says %s — pogod passes the config value, so a "+
			"direct caller of ackwatch.New would renotify on a different clock",
			config.DefaultAckWatchBlackoutRenotify, ackwatch.DefaultBlackoutRenotifyAfter)
	}
	if config.DefaultAckWatchBlackoutRenotify >= config.DefaultAckWatchRenotify {
		t.Errorf("blackout renotify %s is not shorter than the ordinary %s: the fleet-outage "+
			"arm would inherit the 6-hour shadow that swallowed the 4.5-hour outage",
			config.DefaultAckWatchBlackoutRenotify, config.DefaultAckWatchRenotify)
	}
	if config.DefaultAckWatchBlackoutRenotify > config.DefaultAckWatchInterval {
		t.Errorf("blackout renotify %s exceeds the sampling interval %s: samples during a "+
			"dead fleet would be silently dropped",
			config.DefaultAckWatchBlackoutRenotify, config.DefaultAckWatchInterval)
	}
	// A blackout escalates on its first sample regardless of this, which is the
	// point — but the constant is still what gates the AGE-based escalation, and
	// a reader comparing the two needs them to be visibly different scales.
	if config.DefaultAckWatchEscalateAfter <= config.DefaultAckWatchBlackoutRenotify {
		t.Error("escalate_after has collapsed to the blackout cadence; the two conditions " +
			"are supposed to be on different clocks")
	}
}
