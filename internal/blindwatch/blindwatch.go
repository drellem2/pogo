// Package blindwatch is the CONSUMER for a detector that declines to answer
// (mg-d616).
//
// # The channel that had no reader
//
// internal/wedgewatch is careful about its own blindness. An agent whose work
// counter it cannot parse is not folded into healthy; it becomes stateBlind and
// a `wedge_watch_error` event whose message ends:
//
//	"The agent could NOT be judged, which is not the same as healthy."
//
// That is the instrument behaving correctly. What it did not have was a reader.
// pm-riemann measured, in `~/.pogo/events.log`:
//
//	2609  wedge_watch_error  identity=crew-pm-riemann
//	first 2026-08-16T22:10:37   — 18 days before the ticket was filed
//
// Eighteen days of an instrument saying out loud, 2609 times, that it could not
// answer, into a channel with no consumer. Pair that with the crew heartbeat
// check (internal/heartwatch), whose only executor was a step in the
// coordinator's own loop, and the state on 2026-09-01 was: the instrument that
// CAN judge had stopped, and the instrument still running was reporting that it
// COULD NOT judge. Nothing was watching either.
//
// This package is the second half of that repair. It watches the detector, not
// the fleet.
//
// # Why the source is the detector's own state and not the event log
//
// "Give wedge_watch_error a consumer" reads like a job for a log reader, and a
// log reader is the wrong instrument. wedgewatch emits NOTHING on a clean pass.
// So in the event log:
//
//	no wedge_watch_error, detector judging everyone healthy   -> zero rows
//	no wedge_watch_error, detector stopped sampling entirely  -> zero rows
//
// Two different world-states, one reading, and the second one is silently the
// worse. Reading the log alone would rebuild the exact defect this ticket is
// about, one level out — a check whose failure mode is indistinguishable from
// success.
//
// So the source is wedgewatch.Watcher.Judgement(), which carries the blind set
// AND the last completed sample time AND the population size. This package
// treats a detector that has not sampled inside StaleAfter as a finding in its
// own right, worded apart from blindness because they are different faults:
//
//	BLIND    the detector ran and could not judge N agents
//	STOPPED  the detector has not produced a verdict at all since T
//	EMPTY    the detector sampled a population of zero
//
// # Routing
//
// Same rule as internal/turnwatch and internal/heartwatch, and for the same
// reason: a finding naming the coordinator goes to the escalation box, never to
// the coordinator. It also escalates when the DETECTOR is the subject — a
// stopped or empty wedge-watcher is a statement about pogod's own instrument
// set, and the coordinator is not the agent that can repair it.
//
// # What this is not
//
// REPORT-ONLY, and it does not route wedgewatch's FINDINGS. Escalating a
// fleet-level wedge outside the wedged party is mg-fc8d item (3), an
// alerting-policy decision reserved to Daniel and deliberately not built. That
// reservation is about confirmed wedges. It is not about the detector's own
// blindness, which is a statement about an instrument rather than about an
// agent, and which had gone unreported for 18 days at the time this shipped.
package blindwatch

import (
	"time"
)

// Kind is what a finding is about.
type Kind string

const (
	// KindBlind — the detector ran and explicitly could not judge some agents.
	KindBlind Kind = "blind"
	// KindStopped — the detector has produced no verdict for longer than
	// StaleAfter. Never folded into "nothing blind".
	KindStopped Kind = "stopped"
	// KindEmpty — the detector's last sample had a population of zero. Zero
	// examined yields zero blind, which is not a fleet it can see.
	KindEmpty Kind = "empty"
)

// Target is one agent the detector could not judge.
type Target struct {
	Name  string    `json:"name"`
	Why   string    `json:"why"`
	Since time.Time `json:"since"`
}

// Age is how long this target has been unjudgeable, as of now.
func (t Target) Age(now time.Time) time.Duration {
	if t.Since.IsZero() {
		return 0
	}
	return now.Sub(t.Since)
}

// Snapshot is one reading of the detector's own state.
type Snapshot struct {
	// Detector names the instrument being watched, for the notice.
	Detector string
	// Blind is the set the detector said it could not judge.
	Blind []Target
	// SampledAt is when the detector last COMPLETED a sample, whatever it
	// found. Zero means it has never completed one.
	SampledAt time.Time
	// Examined is the population size of that sample.
	Examined int
	// Since is when the detector was armed. A detector that has never sampled
	// is only a finding once it has had time to.
	Since time.Time
}

// Finding is one reportable condition about the detector.
type Finding struct {
	Kind    Kind     `json:"kind"`
	Detail  string   `json:"detail"`
	Targets []Target `json:"targets,omitempty"`
}
