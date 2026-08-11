// Package firstturn is the detection floor for the one thing every fleet
// outage in this system's history has had in common: a spawn that succeeded
// and an agent that then never did anything.
//
// # What it asserts, in one line
//
// A SPAWN IS NOT A SUCCESS. `autostart: started pm-pogo (pid=41773)` plus a
// registered mail-check schedule is evidence that pogod did its job, and no
// evidence at all that the agent is alive in the only sense that matters. This
// package watches for the agent's FIRST COMPLETED TURN after each spawn and
// alarms when it never arrives.
//
// # Why this rung exists when the fleet-blackout rung already does
//
// It is a genuine question, and the answer is a measured gap rather than a
// preference — mg-e2a4 built internal/ackwatch's blackout arm for the adjacent
// case and it WORKS. Across the 22h outage of 2026-08-10/11 it fired 33
// consecutive times, correctly, naming all five agents ("90 fires delivered in
// the last 3h0m0s, NONE completed"). It is pogod-resident, it escalates
// structurally rather than on a timer, and it is not a crew member. Nothing
// here replaces it and nothing here weakens it.
//
// What it cannot do is speak about a FRESH agent, and that is not a bug in it:
// its judgement is an absolute completion RATIO over a trailing window, so
// Snapshot.RunningSince holds an agent ineligible until it has been up for the
// WHOLE window. That gate is load-bearing — without it the arm mails a person
// at 4am every night, because "fires delivered, nothing completed" is also what
// an EMPTY fleet looks like. The cost is arithmetic: after a bounce, the
// earliest the blackout arm can say anything at all is one full window later.
//
// Measured on the outage that motivated this package. pogod restarted at
// 2026-08-11T02:01:33Z and spawned five crew agents that completed nothing for
// 17 hours. The blackout arm's first post-bounce firing was 05:03:36Z — 3h02m,
// exactly its window. This arm's grace is 45 minutes, so it speaks at 02:46Z:
// 2h17m earlier, on the same event log, with no new instrumentation anywhere.
//
// The two arms therefore partition the failure rather than overlapping on it:
//
//	ackwatch blackout  the fleet WAS alive and went dark   (ratio, 3h window)
//	firstturn          the fleet was never alive at all    (first ack, 45m grace)
//
// # Why the grace is 45 minutes and not a guess
//
// Swept from this box's events log: every crew `agent_spawned` since completion
// tracking existed (2026-07-23), paired with the first `scheduler_fire_completed`
// addressed to that agent at or after the spawn. 87 spawns, and the distribution
// is bimodal with nothing whatever in the middle:
//
//	healthy population   67 spawns    max  33.7 min   (p50 12.6 min)
//	outage population    20 spawns    min 150.8 min   (max 1139 min = 19h)
//
// The 20 in the upper mode are not a slow tail. They are the three outages
// themselves: the 2026-08-10 spend_limit episode (150-181 min), the
// 2026-08-11 nightly-deploy spawn of five inert agents (1044-1064 min), and the
// 2026-08-08 hung-deploy outage (1139 min). Between 33.7 and 150.8 minutes
// there is not one observation.
//
// 45 minutes sits in that empty band: 1.33x the healthy maximum, 3.35x below the
// smallest real outage. Zero false positives against all 67 healthy spawns, and
// it fires on all 20 outage spawns. See DefaultGrace, and rerun the sweep before
// moving it — a threshold justified by a measurement whose data has moved is a
// threshold justified by nothing.
//
// # Why only crew, and why the arm says so
//
// Only crew agents are judged. A polecat's first ack legitimately trails its
// first real work, which is a task of unbounded length, so the bimodal
// separation above is a property of the CREW population and does not transfer.
// Crew is also what the floor is a floor for: "no crew member has completed a
// turn" is the sentence three outages needed somebody to say. Polecats have
// their own machinery (internal/stallwatch, internal/wedgewatch).
//
// # The gate against blaming the silent for the scheduler's silence
//
// An agent nothing was ever asked of cannot be faulted for answering nothing.
// A finding therefore requires MinDeliveries fires actually delivered to that
// agent since it spawned — evidence that the loop reached it and it declined to
// come back. Without this the arm would fire on an agent whose mail-check
// failed to register, which is internal/deafwatch's finding and a different
// remedy.
//
// # Absence is never health here
//
// Every path that cannot look reports itself as BLIND, never as calm: a nil
// registry, an unreadable events log, a source error. This is the founding bug
// of the whole watcher lineage one level up — a detector that reads green
// because it cannot see is worse than no detector, because it is trusted.
// Detect also reports the population it DECLINED to judge (too fresh, beyond
// the lookback, never addressed), so a quiet sample is legible as a decision
// rather than as an absence.
package firstturn

import (
	"sort"
	"time"
)

// DefaultGrace is how long after a spawn an agent may go without completing a
// single turn before it is a finding.
//
// 45 minutes, from the bimodal sweep in the package header: 1.33x the slowest
// healthy crew spawn ever observed on this box (33.7 min) and 3.35x below the
// fastest real outage (150.8 min). The empty band between those two numbers is
// what makes this constant defensible; if a future sweep fills it in, this
// number is wrong and the comment above says how to find out.
const DefaultGrace = 45 * time.Minute

// DefaultInterval is the gap between samples. Well under the grace, so a
// finding is announced within roughly one interval of becoming true rather than
// waiting out a second full grace period.
const DefaultInterval = 10 * time.Minute

// DefaultLookback bounds how far back the evidence read reaches, and therefore
// which agents can be judged at all: an agent that spawned before the window
// cannot be shown to have completed nothing SINCE ITS SPAWN, only to have
// completed nothing recently — and "was alive, went dark" is the blackout arm's
// finding, not this one.
//
// 48 hours, chosen to exceed the longest outage on record (33h, mg-56ac) so a
// fresh-spawn outage stays inside this arm's competence for its whole life
// rather than aging out of it midway and reading as cleared.
const DefaultLookback = 48 * time.Hour

// DefaultMinDeliveries is how many fires must have reached an agent since it
// spawned before its silence is evidence. Two, not one: one delivery and no ack
// is a single missed cycle, which every long turn produces; two means the loop
// reached it twice across at least one full cadence and it came back neither
// time.
const DefaultMinDeliveries = 2

// DefaultMinFleetAgents is how many judged agents must ALL be findings before
// the report is a fleet claim rather than N per-agent ones. Two: "every agent
// is dark" said of a population of one is a statement about one agent, and this
// arm's fleet branch changes the routing (see Report.Fleet), so the claim has
// to be true before it changes who gets woken.
const DefaultMinFleetAgents = 2

// State is the tri-state verdict for a sample. Its zero value is StateBlind —
// the "no claim" answer — so a caller that reads an unpopulated Report cannot
// accidentally read health out of it.
type State int

const (
	// StateBlind means the sample could not be taken or judged: no registry, an
	// unreadable events log, a source error. NOT a health claim in either
	// direction.
	StateBlind State = iota
	// StateCalm means the sample was taken and every judged agent has completed
	// at least one turn since it spawned. It is emitted rather than left silent,
	// because a correct quiet and a dead detector are otherwise the same
	// observation.
	StateCalm
	// StateDark means at least one judged crew agent has completed nothing since
	// it spawned, past the grace, having been delivered fires the whole time.
	StateDark
)

// String renders the state for events and logs.
func (s State) String() string {
	switch s {
	case StateCalm:
		return "calm"
	case StateDark:
		return "dark"
	default:
		return "blind"
	}
}

// Agent is one running crew agent's evidence. It is a plain struct with no
// registry or scheduler dependency so every test in this package builds
// fixtures by hand — mg-6092, mg-e8e7 and mg-5336 are three separate tickets
// for tests that read the developer's live ~/.pogo, and this package does not
// add a fourth.
type Agent struct {
	// Name is the bare agent name (`pogo agent diagnose <name>`).
	Name string
	// Identity is the event-log identity ("crew-mayor"), for the event roster.
	Identity string
	// StartedAt is when this incarnation of the agent was spawned. It is the
	// clock this arm measures from, and a zero value makes the agent
	// unjudgeable rather than infinitely old.
	StartedAt time.Time
	// FirstCompletion is the first fire completion observed at or after
	// StartedAt. Zero means none was observed, which is the whole finding.
	FirstCompletion time.Time
	// Delivered is how many fires were delivered to this agent at or after
	// StartedAt. The gate that keeps this arm from blaming an agent nothing was
	// ever asked of.
	Delivered int
}

// Completed reports whether this incarnation has ever finished a turn.
func (a Agent) Completed() bool {
	return !a.FirstCompletion.IsZero() && !a.FirstCompletion.Before(a.StartedAt)
}

// Snapshot is one reading: the running crew population plus the evidence for
// each, as measured by the caller.
type Snapshot struct {
	// Now is the sample time.
	Now time.Time
	// Agents is the running CREW population. The caller does the filtering, so
	// this package holds no opinion about what a polecat is.
	Agents []Agent
	// Scanned is how many agents the caller looked at before filtering, purely
	// so a report can state its own denominator (mg-7a20: "3 of 3 match" reads
	// as a pass when the denominator is unstated).
	Scanned int
	// Err, when non-empty, says why this sample could not be measured. A
	// measurement that failed must never arrive looking like a measurement of
	// zero — that is the same absence-as-evidence error one level up.
	Err string
	// Lookback is the span the caller's evidence read covered. An agent that
	// spawned before Now-Lookback is not judged, because "no completion inside
	// the window" and "no completion since spawn" are different claims and only
	// the second one is this arm's.
	Lookback time.Duration
}

// Params tunes the detector. The zero value is not usable; call DefaultParams.
type Params struct {
	// Grace is how long after a spawn an agent may complete nothing.
	Grace time.Duration
	// MinDeliveries is how many fires must have reached the agent since spawn.
	MinDeliveries int
	// MinFleetAgents is how many all-dark judged agents make a fleet claim.
	MinFleetAgents int
}

// DefaultParams returns the measured defaults. See the package header for where
// Grace comes from.
func DefaultParams() Params {
	return Params{
		Grace:          DefaultGrace,
		MinDeliveries:  DefaultMinDeliveries,
		MinFleetAgents: DefaultMinFleetAgents,
	}
}

// Finding is one agent that has completed nothing since it spawned.
type Finding struct {
	Agent
	// DarkFor is how long the agent has been running without completing
	// anything. It is the number that belongs in a subject line, because it is
	// the one that GROWS — 33 notices carrying an identical 3-hour window is
	// what the blackout arm sent through this outage, and the 33rd was
	// indistinguishable from the 1st.
	DarkFor time.Duration
}

// Report is one sample's verdict.
type Report struct {
	// State is the tri-state verdict. Read this before anything else.
	State State
	// BlindReason explains a StateBlind verdict.
	BlindReason string
	// Findings are the agents past grace with no completion since spawn, oldest
	// dark first.
	Findings []Finding
	// Fleet is true when EVERY judged agent is a finding and there are at least
	// MinFleetAgents of them. It changes the routing, not the severity of any
	// one finding: a fleet that has never come up cannot be asked to fix
	// itself, so this escalates on its first sample rather than on an age gate.
	Fleet bool
	// DarkFor is the shortest DarkFor across the findings when Fleet is true —
	// the most recently spawned dark agent, and therefore the conservative
	// answer to "how long has the whole fleet been like this".
	DarkFor time.Duration
	// Judged names the agents this sample actually judged.
	Judged []string
	// TooFresh, BeyondLookback and NeverAddressed name the populations the
	// sample declined to judge, and why. A report that states its own
	// denominator cannot be misread as coverage it did not have.
	TooFresh       []string
	BeyondLookback []string
	NeverAddressed []string
	// Scanned is the caller's pre-filter population size.
	Scanned int
}

// Detect judges one snapshot. It is pure: no clock, no filesystem, no registry.
func Detect(snap Snapshot, p Params) Report {
	if p.Grace <= 0 {
		p.Grace = DefaultGrace
	}
	if p.MinDeliveries <= 0 {
		p.MinDeliveries = DefaultMinDeliveries
	}
	if p.MinFleetAgents <= 0 {
		p.MinFleetAgents = DefaultMinFleetAgents
	}

	rep := Report{Scanned: snap.Scanned}
	if snap.Err != "" {
		rep.BlindReason = snap.Err
		return rep
	}
	now := snap.Now
	if now.IsZero() {
		rep.BlindReason = "sample has no timestamp"
		return rep
	}
	lookback := snap.Lookback
	if lookback <= 0 {
		lookback = DefaultLookback
	}

	for _, a := range snap.Agents {
		if a.Name == "" {
			continue
		}
		switch {
		case a.StartedAt.IsZero():
			// An agent with no start time cannot be measured from its spawn. It
			// is named as unjudged rather than assumed old, because assuming
			// would make an unknown into a finding.
			rep.BeyondLookback = append(rep.BeyondLookback, a.Name)
			continue
		case a.StartedAt.Before(now.Add(-lookback)):
			rep.BeyondLookback = append(rep.BeyondLookback, a.Name)
			continue
		case now.Sub(a.StartedAt) < p.Grace:
			rep.TooFresh = append(rep.TooFresh, a.Name)
			continue
		}
		if a.Completed() {
			rep.Judged = append(rep.Judged, a.Name)
			continue
		}
		if a.Delivered < p.MinDeliveries {
			// Nothing was asked of it. That is deafwatch's finding (no mail
			// loop) or the scheduler's, and blaming this agent for it would
			// point the remedy at the wrong component.
			rep.NeverAddressed = append(rep.NeverAddressed, a.Name)
			continue
		}
		rep.Judged = append(rep.Judged, a.Name)
		rep.Findings = append(rep.Findings, Finding{Agent: a, DarkFor: now.Sub(a.StartedAt)})
	}

	sort.Strings(rep.Judged)
	sort.Strings(rep.TooFresh)
	sort.Strings(rep.BeyondLookback)
	sort.Strings(rep.NeverAddressed)
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		if rep.Findings[i].DarkFor != rep.Findings[j].DarkFor {
			return rep.Findings[i].DarkFor > rep.Findings[j].DarkFor
		}
		return rep.Findings[i].Name < rep.Findings[j].Name
	})

	if len(rep.Findings) == 0 {
		rep.State = StateCalm
		return rep
	}
	rep.State = StateDark
	rep.Fleet = len(rep.Findings) == len(rep.Judged) && len(rep.Findings) >= p.MinFleetAgents
	// The SHORTEST dark span, not the longest: the fleet has been in this state
	// only for as long as its most recently spawned member has, and an alarm
	// that rounds that up is an alarm that overstates its own evidence.
	rep.DarkFor = rep.Findings[len(rep.Findings)-1].DarkFor
	return rep
}

// Names returns the finding agent names, sorted, for events and mail.
func Names(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// Identities returns the finding event-log identities, sorted, for the episode
// roster the notifier coalesces on (mg-55b2 contract).
func Identities(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		id := f.Identity
		if id == "" {
			id = f.Name
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
