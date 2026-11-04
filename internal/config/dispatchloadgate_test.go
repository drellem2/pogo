package config

import (
	"testing"

	"github.com/drellem2/pogo/internal/hostload"
)

// TestLoadGateShipsArmedAtTheHostloadMark: the shipped mark IS the constant the
// argument for it lives next to, not a copy of it (drellem2/pogo#202).
func TestLoadGateShipsArmedAtTheHostloadMark(t *testing.T) {
	d := DefaultDispatchLoadGateConfig()
	if !d.Armed() {
		t.Fatal("the shipped load gate is disarmed")
	}
	if d.FleetShare != hostload.FleetHeavyAt {
		t.Errorf("default FleetShare = %v, want hostload.FleetHeavyAt (%v)", d.FleetShare, hostload.FleetHeavyAt)
	}
	// A daemon with no config.toml still arms it.
	cfg := loadWithConfigDir(t, t.TempDir())
	if cfg.DispatchLoadGate.FleetShare != hostload.FleetHeavyAt {
		t.Errorf("unconfigured FleetShare = %v, want %v", cfg.DispatchLoadGate.FleetShare, hostload.FleetHeavyAt)
	}
}

// TestLoadGateZeroIsAValueNotAnAbsence is the merge bug the "was it set" flag
// prevents: `load_gate_fleet_share = 0` is how an operator disarms the gate,
// and a merge gated on `> 0` would restore 0.50 and leave them watching a
// daemon still refuse on load.
func TestLoadGateZeroIsAValueNotAnAbsence(t *testing.T) {
	dir := t.TempDir()
	writeCapConfig(t, dir, "[dispatch]\nload_gate_fleet_share = 0\n")
	cfg := loadWithConfigDir(t, dir)
	if cfg.DispatchLoadGate.Armed() {
		t.Fatalf("FleetShare = %v — an explicit disarm was overwritten by the default", cfg.DispatchLoadGate.FleetShare)
	}
	// Sibling keys keep their defaults: merged key by key.
	if !cfg.DispatchCap.Armed() || cfg.DispatchCap.MaxPolecatsPerRepo != DefaultMaxPolecatsPerRepo {
		t.Errorf("disarming the load gate touched the per-repo cap: %+v", cfg.DispatchCap)
	}
}

func TestLoadGateFleetShareParsing(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want float64
	}{
		{"0.7", 0.7},
		{"0.25", 0.25},
		{"1", 1},
		{"-0.3", 0},                         // negative clamps to disarmed, like max_polecats_per_repo
		{"3", 1},                            // above 1 clamps to 1: a fleet share cannot exceed 1
		{`"0.6"`, 0.6},                      // quoted is accepted, as for every other key
		{"lots", DefaultLoadGateFleetShare}, // unparseable keeps the default
		{"nan", DefaultLoadGateFleetShare},
	} {
		t.Run(tc.val, func(t *testing.T) {
			dir := t.TempDir()
			writeCapConfig(t, dir, "[dispatch]\nload_gate_fleet_share = "+tc.val+"\n")
			cfg := loadWithConfigDir(t, dir)
			if got := cfg.DispatchLoadGate.FleetShare; got != tc.want {
				t.Errorf("load_gate_fleet_share = %s → %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}
