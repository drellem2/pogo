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

// Evidence is one agent's fire traffic SINCE THAT AGENT'S OWN SPAWN. Both
// fields are anchored to the same instant Detect compares them against, which
// is not a detail: they are consumed as a join against Agent.StartedAt, and a
// window wider than the agent's life makes both of them answer a question
// nobody asked (mg-21ad).
type Evidence struct {
	// FirstCompletion is the earliest scheduler_fire_completed addressed to this
	// agent at or after its spawn. Zero when there was none.
	FirstCompletion time.Time
	// Delivered counts scheduler_fire_delivered events addressed to it since its
	// spawn. It is the gate against blaming an agent nothing was asked of, and
	// it is also printed verbatim in the notice ("delivered N fires since"), so
	// an inflated one is a false number in an operator's hands.
	Delivered int
}

// ReadEvidence measures per-agent fire traffic in logPath, counting each
// agent's traffic FROM ITS OWN SPAWN — never from a population-wide floor.
//
// # The anchor is per agent, and that is the whole of mg-21ad
//
// This function used to take a single `since` for the entire population, and
// pogod passed it EarliestStart: the oldest spawn among the running crew. That
// is the correct lower bound for the READ and the wrong one for the JOIN, and
// Evidence is consumed as a join — Attach folds FirstCompletion straight into
// an Agent whose Completed() asks whether it is at or after THAT AGENT's spawn.
//
// So a crew member respawned on its own, into a fleet whose other members had
// been up for hours, got the fleet's window rather than its own. Its
// FirstCompletion came back as the earliest completion since the OLDEST agent's
// spawn — a timestamp from before it existed — and Completed() read that as
// "never completed". Measured on this box on 2026-08-19: mayor respawned alone
// at 15:20:07Z into a crew last spawned at 06:54:22Z, completed a fire at
// 15:32:21Z, and first-turn mailed "mayor has completed nothing since it
// spawned 51m0s ago" at 16:11:16Z. Its FirstCompletion was 07:02:48Z, 8h17m
// before the spawn it was being compared against. The same mis-anchoring
// inflated the notice's own denominator: it reported "56 fires delivered since"
// where 20 had been, 56 being the count since 06:54:22Z exactly.
//
// The cost is why this is a defect and not a rounding error. This arm is the
// only instrument that can report a coordinator which never came up, because
// every other fleet-wide check on this box routes through that coordinator. A
// false positive here is not noise in a redundant channel; it is noise in the
// sole channel.
//
// The window is therefore computed HERE, from the population, rather than
// accepted from the caller — one function decides both the read bound and the
// join anchor, so the two cannot drift apart again. Agents whose spawn is
// unknown or older than lookback are given no anchor at all and accumulate no
// evidence: they are unjudgeable either way (Detect files them under
// BeyondLookback), and counting traffic for them would only re-create a number
// nobody can interpret.
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
func ReadEvidence(logPath string, agents []Agent, now time.Time, lookback time.Duration) (map[string]Evidence, string) {
	// A log that is not there has to be named as blindness HERE, because the
	// events layer deliberately treats a nonexistent path as "no events yet"
	// rather than as an error (see events.ScanFile). Left to Detect, an absent
	// log would arrive as "nobody completed anything", which is this arm's
	// finding — the detector would alarm on its own blindness.
	if _, err := os.Stat(logPath); err != nil {
		return nil, "scheduler event log unreadable: " + err.Error()
	}
	anchors := spawnAnchors(agents, now, lookback)
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
		to, _ := ev.Details["to"].(string)
		if to == "" {
			return
		}
		anchor, ok := anchors[to]
		if !ok {
			return
		}
		at, perr := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if perr != nil {
			return
		}
		// Strictly at or after THIS agent's spawn. An event from before it
		// existed is another incarnation's and says nothing about this one.
		if at.Before(anchor) {
			return
		}
		if !now.IsZero() && at.After(now) {
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

// spawnAnchors maps each judgeable agent to the instant its evidence starts:
// its own StartedAt. An agent with no start time, or one older than the
// lookback, gets no entry — it cannot be judged from this sample, and giving it
// the population's floor is exactly the substitution that made mg-21ad.
//
// When a name appears twice the LATEST spawn wins: the incarnation being judged
// is the running one.
func spawnAnchors(agents []Agent, now time.Time, lookback time.Duration) map[string]time.Time {
	var floor time.Time
	if !now.IsZero() && lookback > 0 {
		floor = now.Add(-lookback)
	}
	out := make(map[string]time.Time, len(agents))
	for _, a := range agents {
		if a.Name == "" || a.StartedAt.IsZero() {
			continue
		}
		if !floor.IsZero() && a.StartedAt.Before(floor) {
			continue
		}
		if prev, ok := out[a.Name]; ok && !a.StartedAt.After(prev) {
			continue
		}
		out[a.Name] = a.StartedAt
	}
	return out
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
