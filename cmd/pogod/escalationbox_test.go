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
