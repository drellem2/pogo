package config

import "github.com/drellem2/pogo/internal/hostload"

// DispatchLoadGateConfig is the operator's lever on the host load gate
// (internal/agent/dispatchload.go), read from `[dispatch] load_gate_fleet_share`
// (drellem2/pogo#202).
//
// # Why this gate gets a key at all
//
// Every other refusal on `pogo agent spawn-polecat` already has a way past it —
// a per-item `mg edit`, a per-dispatch `--*-override` flag that records a
// reason, or a config key. The load gate had none: its mark was the compiled
// constant hostload.FleetHeavyAt, so a host the gate misjudged refused EVERY
// dispatch and the only remedy was a rebuild. A refusal gate's failure mode is
// that no work happens at all, and an operator must be able to end that without
// a compiler.
//
// # Why a threshold and not an on/off switch
//
// One number answers both questions: where the line is, and — at 0 — whether
// there is a line. That is the same shape as `max_polecats_per_repo = 0`.
//
// # What it deliberately does not cover
//
// The live-owner and claim checks have no key and get none: disabling them
// risks destroying a running worker's tree, and the rule is a key only where
// the failure is "no work happens", never where it is "work gets destroyed".
// There is no per-dispatch --load-override either: a misjudged host refuses
// every dispatch, so a reason typed on each spawn would be noise.
type DispatchLoadGateConfig struct {
	// FleetShare is the fraction of the host's cores the fleet must be holding
	// before a dispatch is refused. ZERO DISARMS the gate — no dispatch is ever
	// refused on load — and is a distinct value from "unset", which takes
	// DefaultLoadGateFleetShare. Values above 1 are clamped to 1 at parse time:
	// a fleet share cannot exceed 1, so a larger mark would be a disarm that
	// nothing announces.
	//
	// The worker budget (internal/agent/workerbudget.go) reads this SAME value
	// when no per-repo cap is armed, so the gate and the budget cannot drift.
	FleetShare float64
}

// DefaultLoadGateFleetShare is the shipped mark. It IS hostload.FleetHeavyAt —
// referenced, not copied, so the argument for the number stays in one place.
const DefaultLoadGateFleetShare = hostload.FleetHeavyAt

// DefaultDispatchLoadGateConfig is the shipped policy: armed at half the host.
func DefaultDispatchLoadGateConfig() DispatchLoadGateConfig {
	return DispatchLoadGateConfig{FleetShare: DefaultLoadGateFleetShare}
}

// Armed reports whether the gate refuses anything at all.
func (c DispatchLoadGateConfig) Armed() bool { return c.FleetShare > 0 }
