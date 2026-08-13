package hostload

import (
	"fmt"
	"sync"
)

// SaturatedAt is the fraction of a host's cores that must be in use before the
// host counts as saturated — the condition under which a long step's
// wall-clock stops reflecting the step and starts reflecting the queue.
//
// It is a judgement, like the gate timeout it sits next to, and like that one
// it ships with the evidence that makes a wrong value diagnosable rather than
// mysterious. Measured for mg-1b8c on the fleet's 10-core host: a fixed CPU
// sweep took 31.3s with the host at roughly 4.5 cores in use and 48.7s with it
// pinned near 10 — the wall-clock of identical work moved by half again once
// demand reached capacity, and kept climbing past it.
//
// Note what this threshold can and cannot see. It is computed from CPU time
// actually consumed, which is bounded by the core count: a host with twice as
// many runnable tasks as cores reads 1.0, exactly as a host with one task per
// core does. Saturation therefore detects "the host is full" and cannot
// measure how far past full it is. It is also blind to a step degraded by I/O
// rather than CPU. Both limits are stated in the report this produces.
const SaturatedAt = 0.85

// FleetHeavyAt is the fraction of a host's cores the fleet must be holding
// before starting more fleet work is judged to make things worse.
//
// This is a threshold on a marginal decision made without knowing the new
// work's cost, so it cannot be exactly right. What it can be is arguable and
// self-reporting: every refusal prints the measurement it acted on.
//
// Keyed on the FLEET's share deliberately. A threshold on total host CPU would
// let an unrelated process — the VPN client measured at ~0.9 cores, the system
// indexer at ~0.3 — veto the fleet's own dispatch, and pausing our work would
// not give those cores back. This number only ever answers "is the fleet
// already using most of this box".
//
// The line is HALF THE HOST, and the argument is about what dispatch can
// control. Below half, the host's spare capacity and non-fleet work dominate,
// and holding a dispatch would be gating on something pausing our own work
// cannot fix. Above half, the fleet's own work is the dominant consumer, so
// the marginal worker competes chiefly with fleet work — which is exactly and
// only what dispatch has any say over.
//
// It was 0.60 first, calibrated directly to the live instance: ONE worker at
// ~5.7 cores of 10, ~6.2 with the rest of the fleet. A live check of the guard
// then measured seven compute processes at 5.8 cores — 58%, the same condition
// — and 0.60 did not fire. Calibrating a threshold to sit just under a single
// measurement leaves it one point of jitter from missing the case it was built
// for, so the number now follows from an argument instead of from that
// instance.
//
// The idle case is untouched either way: five workers doing ordinary work held
// well under 1 core of 10 in every sample taken here.
const FleetHeavyAt = 0.50

// Summary is what a sequence of samples says about a step's run conditions.
// It is attached to a long step's record so that "this took 40 minutes" can be
// read alongside "and the host was full for 38 of them" — the distinction
// between a slow change and a slow host, which nothing on this fleet could
// previously make.
type Summary struct {
	Samples int `json:"samples"`
	Cores   int `json:"cores"`

	MeanFleetCores    float64 `json:"mean_fleet_cores"`
	PeakFleetCores    float64 `json:"peak_fleet_cores"`
	MeanExternalCores float64 `json:"mean_external_cores"`
	PeakFleetProcs    int     `json:"peak_fleet_procs"`

	// SaturatedSamples counts samples in which the whole host was at or above
	// SaturatedAt. Reported as a count rather than a mean so a brief spike and
	// a sustained pin are distinguishable.
	SaturatedSamples int `json:"saturated_samples"`

	// PeakLoadAvg1 is the highest 1-minute load average seen. CONTEXT ONLY: it
	// is reported because it is what makes a human look, and it is never a
	// term in any judgement below.
	PeakLoadAvg1 float64 `json:"peak_load_avg_1"`

	// Attributed is false when no fleet root was ever found, in which case
	// the fleet figures are zero for want of attribution, not for want of
	// load.
	Attributed bool `json:"attributed"`
}

// MeanUsedCores is mean total CPU in use across the samples.
func (s Summary) MeanUsedCores() float64 { return s.MeanFleetCores + s.MeanExternalCores }

// SaturatedFraction is the share of samples taken while the host was full.
func (s Summary) SaturatedFraction() float64 {
	if s.Samples == 0 {
		return 0
	}
	return float64(s.SaturatedSamples) / float64(s.Samples)
}

// Contended reports whether the host was full for most of the step. This is
// the claim worth making about a slow gate: not "the host was busy at some
// point" but "the host had no spare capacity for most of this run".
func (s Summary) Contended() bool {
	return s.Samples > 0 && s.SaturatedFraction() >= 0.5
}

// FleetShare is the fraction of in-use CPU the fleet was responsible for, 0
// when nothing was in use. It answers whether pausing fleet work would have
// helped.
func (s Summary) FleetShare() float64 {
	used := s.MeanUsedCores()
	if used <= 0 {
		return 0
	}
	return s.MeanFleetCores / used
}

// Report renders the summary as an operator-facing sentence.
//
// It states the limits of the measurement inline rather than in a footnote,
// because the failure this exists to prevent is a reader taking a number for
// more than it says: a load average of 214 was read as a busy CPU when it was
// mostly processes waiting on I/O.
func (s Summary) Report() string {
	if s.Samples == 0 {
		return "host contention not sampled"
	}
	if !s.Attributed {
		return fmt.Sprintf("host used %.1f of %d cores (mean over %d samples); "+
			"fleet share unattributed — no fleet root process was found, which is not the "+
			"same as the fleet being idle",
			s.MeanUsedCores(), s.Cores, s.Samples)
	}
	verdict := "host had spare capacity"
	if s.Contended() {
		verdict = fmt.Sprintf("HOST SATURATED for %d of %d samples", s.SaturatedSamples, s.Samples)
	}
	return fmt.Sprintf("%s — fleet held %.1f of %d cores (peak %.1f across %d procs), "+
		"non-fleet %.1f, so %.0f%% of the load was ours. "+
		"Saturation is CPU consumed, so it detects a full host and cannot say how far past full; "+
		"it is also blind to a step slowed by I/O rather than CPU. "+
		"Peak 1-min load average %.1f, reported as context and not used in this verdict.",
		verdict, s.MeanFleetCores, s.Cores, s.PeakFleetCores, s.PeakFleetProcs,
		s.MeanExternalCores, s.FleetShare()*100, s.PeakLoadAvg1)
}

// Tracker accumulates samples over the life of one step. Safe for concurrent
// use: the gate's heartbeat goroutine adds to it while a reader renders it.
type Tracker struct {
	mu  sync.Mutex
	sum Summary
	// running totals, kept separately so Summary stays a value type
	fleetTotal    float64
	externalTotal float64
}

// Add folds one sample in.
func (t *Tracker) Add(s Sample) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sum.Samples++
	t.sum.Cores = s.Cores
	t.fleetTotal += s.FleetCores
	t.externalTotal += s.ExternalCores
	if s.FleetCores > t.sum.PeakFleetCores {
		t.sum.PeakFleetCores = s.FleetCores
	}
	if s.FleetProcs > t.sum.PeakFleetProcs {
		t.sum.PeakFleetProcs = s.FleetProcs
	}
	if s.LoadAvg1 > t.sum.PeakLoadAvg1 {
		t.sum.PeakLoadAvg1 = s.LoadAvg1
	}
	if s.Cores > 0 && s.UsedCores()/float64(s.Cores) >= SaturatedAt {
		t.sum.SaturatedSamples++
	}
	// Once anything has been attributed, the run is attributed: a root that
	// exits partway through must not erase what it was measured doing.
	if s.Attributed {
		t.sum.Attributed = true
	}
}

// Summary returns the accumulated summary.
func (t *Tracker) Summary() Summary {
	if t == nil {
		return Summary{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.sum
	if out.Samples > 0 {
		out.MeanFleetCores = t.fleetTotal / float64(out.Samples)
		out.MeanExternalCores = t.externalTotal / float64(out.Samples)
	}
	return out
}

// FleetHeavy reports whether the fleet is already holding enough of this host
// that adding more fleet work degrades what is running.
//
// Deliberately a property of a single sample rather than of a Summary: a
// dispatch decision is about the host as it is now, not about how it has been.
func (s Sample) FleetHeavy() bool {
	return s.Attributed && s.Cores > 0 && s.FleetSaturation() >= FleetHeavyAt
}

// DispatchAdvice renders the sentence a dispatcher should act on, and its
// reasoning, so a refusal is arguable rather than opaque.
func (s Sample) DispatchAdvice() string {
	if !s.Attributed {
		return fmt.Sprintf("PROCEED (unattributed): no fleet root process was found, so the fleet's "+
			"share of this host could not be measured. Host is using %.1f of %d cores. "+
			"This is missing information, not evidence of an idle fleet.",
			s.UsedCores(), s.Cores)
	}
	if s.FleetHeavy() {
		return fmt.Sprintf("HOLD: the fleet is already holding %.1f of %d cores (%.0f%%) across %d "+
			"processes, at or above the %.0f%% mark where more fleet work slows the work in flight "+
			"and itself. Non-fleet load is %.1f cores and is not counted against you. "+
			"Re-check rather than queueing, and re-check THIS NUMBER rather than the agent count: "+
			"the refusal clears when the fleet's core share falls below %.0f%%, which is not the same "+
			"event as an agent exiting. Measured 2026-08-13 (mg-eb47), the fleet went from 6 agents "+
			"holding 6.1 of 10 cores to 5 agents holding 7.0 — fewer agents, MORE cores, with the "+
			"process count falling 32 to 21 in the same step, because two refinery gates that each "+
			"parallelise across packages outweighed the agents that left. `pogo host load` reads this "+
			"number without costing a spawn attempt.",
			s.FleetCores, s.Cores, s.FleetSaturation()*100, s.FleetProcs, FleetHeavyAt*100,
			s.ExternalCores, FleetHeavyAt*100)
	}
	return fmt.Sprintf("PROCEED: the fleet is holding %.1f of %d cores (%.0f%%), below the %.0f%% mark. "+
		"Non-fleet load is %.1f cores; %.1f cores are free.",
		s.FleetCores, s.Cores, s.FleetSaturation()*100, FleetHeavyAt*100,
		s.ExternalCores, s.FreeCores())
}
