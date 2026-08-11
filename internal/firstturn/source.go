package firstturn

import (
	"os"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
)

// This file is the ONLY place firstturn touches live pogo state. Detect takes a
// Snapshot and nothing else, so every test in this package builds fixtures by
// hand — mg-6092, mg-e8e7 and mg-5336 are three separate tickets for tests that
// read the developer's live ~/.pogo, and this package does not add a fourth.

// Evidence is one agent's fire traffic since a reference time.
type Evidence struct {
	// FirstCompletion is the earliest scheduler_fire_completed addressed to this
	// agent at or after the reference. Zero when there was none.
	FirstCompletion time.Time
	// Delivered counts scheduler_fire_delivered events addressed to it.
	Delivered int
}

// ReadEvidence measures per-agent fire traffic in logPath over [since, now).
//
// # Why the events log and not the scheduler's own counters
//
// scheduler.Entry carries FiresDelivered / FiresCompleted / LastCompletion in
// hand, and reading those would be free. They are the wrong source here for a
// reason this arm cannot work around: registering a schedule with an `--id`
// that already exists REPLACES the entry and zeroes them, every crew agent
// re-registers its mail-check on startup by procedure, and mg-42ac made a
// redeploy nightly. So on precisely the boot this arm exists to judge, the
// counters read 0/0 for reasons that have nothing to do with the agent. The
// events log is where a delivery and a completion both survive a restart.
//
// It never returns an error. A window it could not read comes back with Err
// set, which Detect renders as StateBlind — a failed measurement that looked
// like a measurement of zero would be a false alarm on every agent at once, and
// one that looked like a clean scan would be the silence this package exists to
// end.
func ReadEvidence(logPath string, since, now time.Time) (map[string]Evidence, string) {
	// A log that is not there has to be named as blindness HERE, because the
	// events layer deliberately treats a nonexistent path as "no events yet"
	// rather than as an error (see events.ScanFile). Left to Detect, an absent
	// log would arrive as "nobody completed anything", which is this arm's
	// finding — the detector would alarm on its own blindness.
	if _, err := os.Stat(logPath); err != nil {
		return nil, "scheduler event log unreadable: " + err.Error()
	}
	out := map[string]Evidence{}
	// ScanFile rather than ReadFiltered: this is an aggregate over a
	// potentially multi-day window of a log that runs to tens of MB, and
	// materialising every matching event to count them is the pattern
	// events.ReadFiltered's own doc comment tells callers not to use.
	err := events.ScanFile(logPath, func(ev events.Event) {
		switch ev.EventType {
		case "scheduler_fire_delivered", "scheduler_fire_completed":
		default:
			return
		}
		at, perr := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if perr != nil {
			return
		}
		if at.Before(since) {
			return
		}
		if !now.IsZero() && at.After(now) {
			return
		}
		to, _ := ev.Details["to"].(string)
		if to == "" {
			return
		}
		e := out[to]
		if ev.EventType == "scheduler_fire_delivered" {
			e.Delivered++
		} else if e.FirstCompletion.IsZero() || at.Before(e.FirstCompletion) {
			e.FirstCompletion = at
		}
		out[to] = e
	})
	if err != nil {
		return nil, "scheduler event log unreadable: " + err.Error()
	}
	return out, ""
}

// CrewAgents extracts the running CREW population from a registry, with each
// agent's spawn time.
//
// Polecats are excluded deliberately and not as an oversight: a polecat's first
// ack legitimately trails its first real work, which is a task of unbounded
// length, so the measured spawn-to-first-ack separation this arm's grace rests
// on is a property of the crew population and does not transfer. See the
// package header.
//
// A nil registry yields ok=false rather than an empty population: a detector
// that cannot look has not found a healthy fleet.
func CrewAgents(reg *agent.Registry) (running []Agent, scanned int, ok bool) {
	if reg == nil {
		return nil, 0, false
	}
	for _, a := range reg.List() {
		if a == nil {
			continue
		}
		scanned++
		if a.Status != agent.StatusRunning || a.Type != agent.TypeCrew {
			continue
		}
		running = append(running, Agent{
			Name:      a.Name,
			Identity:  a.EventAgent(),
			StartedAt: a.StartTime,
		})
	}
	return running, scanned, true
}

// EarliestStart returns the oldest spawn time in the population, clamped to
// now-lookback. It is the lower bound of the evidence read: reaching further
// back buys nothing, and reaching less far would make "no completion since
// spawn" unprovable for the oldest agent in the set.
func EarliestStart(agents []Agent, now time.Time, lookback time.Duration) time.Time {
	floor := now.Add(-lookback)
	earliest := now
	for _, a := range agents {
		if a.StartedAt.IsZero() || a.StartedAt.Before(floor) {
			continue
		}
		if a.StartedAt.Before(earliest) {
			earliest = a.StartedAt
		}
	}
	if earliest.Before(floor) {
		return floor
	}
	return earliest
}

// Attach folds measured evidence into a population, returning the snapshot
// Detect judges.
func Attach(agents []Agent, ev map[string]Evidence, now time.Time, scanned int, lookback time.Duration, readErr string) Snapshot {
	snap := Snapshot{Now: now, Scanned: scanned, Lookback: lookback, Err: readErr}
	for _, a := range agents {
		e := ev[a.Name]
		a.FirstCompletion = e.FirstCompletion
		a.Delivered = e.Delivered
		snap.Agents = append(snap.Agents, a)
	}
	return snap
}
