// Package progresswatch answers the one question none of this fleet's
// instruments answers: IS THE FLEET GETTING ANYTHING DONE?
//
// # The state that had no instrument (mg-516e)
//
// During the network outage of 2026-08-14 (mg-c058) the fleet reached this, at
// 05:17Z, on three readings mayor took by hand:
//
//	pogo agent list        ->  all 7 polecats PTY-active within 4 minutes
//	worktree file mtimes   ->  NONE had written a file in 15 minutes
//	pogo host load         ->  fleet holding 0.10 of 10 cores
//	                       and no merge had landed in ~30 minutes
//
// No alarm fired, and none could have. Every signal this fleet has answers "is
// it dead?" (exit, registry absence, wedge) or "is it erroring?"
// (failing_turns). A worker blocked on a slow or unreachable API is neither: it
// is ALIVE, NOT FAILING, and producing nothing. The detector states were
// effectively {failing, fine} and this state is a third one with no cell.
//
// Mayor found it because a routine liveness check came back CONFUSING rather
// than red, and chose to chase the confusion. A coordinator who was not already
// suspicious would have seen eight healthy agents.
//
// # Why the conjunction, and why no member of it alone
//
// Each of the four readings is ordinary on its own — an agent thinking, a
// read-only task, a quiet minute, a long gate. The conjunction is not, and it
// has one ordinary explanation: everyone is waiting on the same remote. So this
// package measures all four and reports the AND, and it reports every one of
// the four numbers whether or not the AND holds. That second half is the point
// as much as the first: the reading mayor needed was one place showing all four
// disagreeing, and no such place existed.
//
// # What it is NOT, so it is not confused with its neighbours
//
//   - internal/synthwatch pages on turns that ERRORED. A blocked worker errors
//     nothing; it waits. Fixing mg-c058 completely leaves this state invisible.
//   - internal/turnwatch is the pogod-resident floor under FLEET DOWN, at 3h
//     staleness with a 30m hold-down and a 45m grace. That is deliberately
//     coarse — it separates "the fleet is doing anything" from "the fleet is
//     down". A 30-minute stall of seven workers sits entirely inside its blind
//     spot, which is the gap this fills rather than a mistuning to argue about.
//   - internal/absentwatch and internal/deafwatch judge one agent's presence and
//     reachability. This judges the FLEET's output, and its population is the
//     live WORKERS — the agents whose job is to produce merges.
//
// # Three things the incident taught that are wired into the rules
//
//  1. A GATING worker reads negative on both PTY activity and worktree writes
//     BY CONSTRUCTION: its `go test` writes to /tmp and the build cache and its
//     output may be captured rather than streamed. Mayor nearly declared p27c0
//     stalled on exactly that pair; the process subtree showed `go test` with
//     live children, 2m42s in. So CPU here is measured over each worker's
//     PROCESS SUBTREE (internal/hostload with the worker pids as roots), never
//     over the worker process itself — a parent under-reports a fan-out
//     workload, measured at 0.0% while children burned 2–11% (mg-eb47).
//  2. The same zero-writes shape is BENIGN on a young worker still reading its
//     ticket; p0f24 showed it at 10 minutes and was healthy. What made 05:18Z a
//     fleet signal was SEVEN AT ONCE. So a worker younger than MinWorkerAge is
//     not judged at all, and fewer than MinWorkers blocked is not a finding.
//  3. The reported value must say WHAT IT MEASURED, not a state token
//     (mg-c058's lesson, learned when "AGENTS ARE FAILING EVERY TURN" paged a
//     sleeping human over 2 errors in a trailing 30m). "no completions in 31m,
//     7 workers PTY-active, worker subtree at 0.10 of 10 cores" is actionable;
//     "STALLED" invites the same present-tense over-reading. Reading.String is
//     the whole measurement and there is deliberately no bare state token
//     exported for a caller to print instead.
//
// # Blindness is a third answer, never a quiet green
//
// A conjunction one of whose members could not be measured is not FALSE; it is
// UNKNOWN, and the two must not collapse. An unresolvable CPU sample reports
// zeros that mean "this host cannot tell", not "the fleet is idle"
// (hostload.Sample.Unresolvable), and an unreadable worktree is not an
// unwritten one. Any such gap suppresses the finding — asserting a stall on an
// unmeasured member is how a detector cries wolf — and is recorded in
// Reading.Blind, which the runner emits as its own event. Rounding an unknown
// toward the quieter answer without saying so is how this lineage's founding
// bug is spelled.
//
// REPORT-ONLY. This package mails and emits. It has no seam through which it
// could restart, nudge or stop anything: a fleet waiting on a remote is not
// fixed by killing the agents that are waiting, and mg-18d0 costed that
// alternative at ~66 restarts against a fault no restart addressed.
package progresswatch

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Defaults for the conjunction's four thresholds plus the two population
// guards. Every one of them is set against the 05:18Z measurements in mg-516e,
// which is the only incident this detector has, and each is stated with the
// number it must have caught.
const (
	// DefaultPTYActiveWithin is how recently a worker must have written to its
	// PTY to count as ALIVE. At 05:18Z all seven were active within 4 minutes,
	// so this must exceed that; 10 minutes also clears the fleet's */10 mail
	// cadence, below which a merely quiet worker would fail the aliveness half
	// and be dropped from the population rather than counted as blocked.
	DefaultPTYActiveWithin = 10 * time.Minute
	// DefaultQuietWritesFor is how long a worker must have written NO file in
	// its worktree to count as producing nothing. Fifteen minutes is what was
	// observed; 10 is used because it is still long enough to sit through a
	// think-and-then-write cycle and short enough that the whole conjunction
	// resolves inside the 30m completion window rather than after it.
	DefaultQuietWritesFor = 10 * time.Minute
	// DefaultIdleCores is the ceiling, IN CORES, under which the worker subtrees
	// count as computing nothing. The observed reading was 0.10 of 10 cores. Half
	// a core is deliberately well above that and well below anything a real
	// build reaches — a single `go test` on this host holds cores, not
	// hundredths of one — so the gating-worker false positive of note (1) is
	// excluded by measurement rather than by a special case.
	DefaultIdleCores = 0.5
	// DefaultNoProgressFor is how long the fleet must have landed nothing.
	// ~30 minutes was the observed cost of the incident, and it is the figure
	// the ticket names.
	DefaultNoProgressFor = 30 * time.Minute
	// DefaultMinWorkers is how many workers must be blocked at once before the
	// conjunction is a fleet signal rather than one agent's Tuesday. Three is
	// chosen against note (2): one blocked worker is routine and was measured
	// benign, seven was the incident. Three is the smallest number that cannot
	// be one agent's own behaviour.
	DefaultMinWorkers = 3
	// DefaultMinWorkerAge is how old a worker must be before its silence is
	// evidence of anything. A worker still reading its ticket has written no
	// file and that is correct; p0f24 was healthy in exactly that shape at 10
	// minutes. It must therefore be at least DefaultQuietWritesFor, or a
	// newborn would satisfy the quiet-writes test the instant it was counted.
	DefaultMinWorkerAge = 15 * time.Minute
)

// Thresholds is the conjunction's tuning. The zero value is usable: every
// field falls back to its Default above, so a caller that wants the shipped
// judgement passes Thresholds{}.
type Thresholds struct {
	// PTYActiveWithin bounds how stale a worker's last PTY write may be and
	// still count as alive.
	PTYActiveWithin time.Duration
	// QuietWritesFor is how long a worker must have written no file.
	QuietWritesFor time.Duration
	// IdleCores is the cores ceiling under which the worker subtrees count as
	// computing nothing.
	IdleCores float64
	// NoProgressFor is how long the fleet must have landed nothing.
	NoProgressFor time.Duration
	// MinWorkers is how many blocked workers make it a fleet signal.
	MinWorkers int
	// MinWorkerAge excludes a worker too young to be judged.
	MinWorkerAge time.Duration
}

func (t Thresholds) resolved() Thresholds {
	out := t
	if out.PTYActiveWithin <= 0 {
		out.PTYActiveWithin = DefaultPTYActiveWithin
	}
	if out.QuietWritesFor <= 0 {
		out.QuietWritesFor = DefaultQuietWritesFor
	}
	if out.IdleCores <= 0 {
		out.IdleCores = DefaultIdleCores
	}
	if out.NoProgressFor <= 0 {
		out.NoProgressFor = DefaultNoProgressFor
	}
	if out.MinWorkers <= 0 {
		out.MinWorkers = DefaultMinWorkers
	}
	if out.MinWorkerAge <= 0 {
		out.MinWorkerAge = DefaultMinWorkerAge
	}
	return out
}

// Worker is one live worker as the detector sees it: identity, age, and the two
// per-agent halves of the conjunction. It mirrors what pogod's registry can
// answer without importing it, so this package stays a pure function of a plain
// struct and its tests build fixtures by hand — the separation
// internal/absentwatch, internal/deafwatch and internal/ackwatch all draw, and
// for the same reason (three tickets exist for tests that read the developer's
// live ~/.pogo).
type Worker struct {
	// Name is the registry name; WorkItemID is the item it claimed, empty when
	// it was spawned without one.
	Name       string `json:"name"`
	WorkItemID string `json:"work_item_id,omitempty"`
	// Age is how long since the worker was spawned. Under MinWorkerAge the
	// worker is not judged; see note (2) in the package comment.
	Age time.Duration `json:"age"`

	// PTYIdle is how long since the worker last wrote to its PTY, valid only
	// when HasOutput. A worker that has NEVER written has an UNMEASURABLE idle
	// time, not a short one — it may be seconds into spawn or wedged before its
	// first turn (mg-ce61's unsubmitted paste) — so the two are separate fields
	// rather than a zero sentinel, exactly as agent.PolecatActivity splits them.
	PTYIdle   time.Duration `json:"pty_idle"`
	HasOutput bool          `json:"has_output"`

	// WriteIdle is how long since the newest file mtime anywhere in the
	// worker's worktree, valid only when WritesKnown. HasWrites distinguishes a
	// tree that has never been written from one that has.
	WriteIdle   time.Duration `json:"write_idle"`
	HasWrites   bool          `json:"has_writes"`
	WritesKnown bool          `json:"writes_known"`
	// WritesError is why the worktree could not be read, when WritesKnown is
	// false. An unreadable tree is not an unwritten one and must never be
	// counted as quiet.
	WritesError string `json:"writes_error,omitempty"`
}

// alive reports whether this worker has produced PTY output recently enough to
// be called alive. A worker with no output at all is NOT alive by this test:
// its idleness is unmeasurable, and the incident's signature is workers that
// are demonstrably awake and producing nothing.
func (w Worker) alive(within time.Duration) bool {
	return w.HasOutput && w.PTYIdle <= within
}

// quiet reports whether this worker has written no file for long enough to
// count as producing nothing, and whether that could be determined at all.
//
// A tree that has NEVER been written counts as quiet once the worker is old
// enough — an unwritten worktree at 20 minutes is the same fact as one last
// written 20 minutes ago — which is why the age guard and this test are tied
// together rather than independent.
//
// In production HasWrites is nearly always true: the walk pogod uses stats the
// worktree ROOT as well as its contents, so an untouched tree reports its own
// creation rather than nothing at all. The !HasWrites arm is kept because a
// SOURCE is free to report the two separately and the arm's answer must not
// depend on which choice it made — the two paths agree by construction, both
// resolving to "nothing has come out of this worker for as long as it has
// existed".
func (w Worker) quiet(for_ time.Duration) (quiet, known bool) {
	if !w.WritesKnown {
		return false, false
	}
	if !w.HasWrites {
		return w.Age >= for_, true
	}
	return w.WriteIdle >= for_, true
}

// Snapshot is one reading of the fleet: who is working, what the host says they
// are computing, and when the fleet last landed anything.
//
// Every "known" flag on it exists because its absence and a zero value are
// different facts. A source that cannot answer must say so; see the package
// comment on blindness.
type Snapshot struct {
	// Now is the sample time.
	Now time.Time `json:"now"`

	// Workers is every LIVE worker, including ones too young to judge. The
	// young ones are carried rather than filtered by the source so the reading
	// can state the whole population it looked at.
	Workers []Worker `json:"workers"`

	// HostCores is the host's logical core count — the denominator, carried so
	// a cores figure is never printed without one.
	HostCores int `json:"host_cores"`
	// WorkerCores is CPU consumed over the sample window by the worker
	// PROCESS SUBTREES, in cores. Subtrees, not processes: see note (1).
	WorkerCores float64 `json:"worker_cores"`
	// CoresKnown is false when the sample was unresolvable or nothing could be
	// attributed. WorkerCores is then quantisation noise, not a measurement.
	CoresKnown bool `json:"cores_known"`
	// CoresError is why, when CoresKnown is false.
	CoresError string `json:"cores_error,omitempty"`

	// LastProgress is the most recent moment the fleet produced something:
	// a merge landing, a branch reaching the merge queue, or a work item being
	// marked done. Zero when the observable window contains none.
	LastProgress time.Time `json:"last_progress,omitempty"`
	// LastProgressWhat names it, e.g. "merge mr-0f2a landed" — an unnamed
	// timestamp cannot be chased.
	LastProgressWhat string `json:"last_progress_what,omitempty"`
	// ProgressSince is the start of the window the source could observe. It is
	// what makes a ZERO LastProgress readable: "nothing since pogod started
	// 40m ago" is a measurement, "nothing, ever" is not.
	ProgressSince time.Time `json:"progress_since,omitempty"`
	// ProgressKnown is false when neither the merge history nor the work items
	// could be read. "Nothing landed" and "I could not look" must not collapse.
	ProgressKnown bool `json:"progress_known"`
	// ProgressError is why, when ProgressKnown is false.
	ProgressError string `json:"progress_error,omitempty"`

	// InFlight is the merge request holding the refinery's serial slot, empty
	// when none is; InFlightSince is when it took the slot. A merge in flight
	// is the fleet producing something, and it runs in pogod's subtree rather
	// than any worker's, so it would otherwise be invisible to every member of
	// the conjunction. See Evaluate for the bound on how long it may excuse.
	InFlight      string    `json:"in_flight,omitempty"`
	InFlightSince time.Time `json:"in_flight_since,omitempty"`
}

// Reading is what the detector concluded AND every number it concluded it from.
// The numbers are populated on every reading, findings and clean ones alike:
// the instrument mg-516e asks for is one place a coordinator can see all four
// measurements disagree, and a struct that only fills itself in when it fires
// is not that.
type Reading struct {
	// Now is the sample time; Thresholds is the tuning the verdict used, so a
	// reading can be judged without consulting this source file.
	Now        time.Time  `json:"now"`
	Thresholds Thresholds `json:"thresholds"`

	// Stalled is the conjunction. It is never true while anything in Blind is
	// set — which is exactly why it must not be read on its own: `stalled:false`
	// is what a healthy fleet produces AND what a run that measured nothing
	// produces. Switch on Verdict, which states which of the two it was
	// (mg-e75b).
	Stalled bool `json:"stalled"`

	// LiveWorkers is the whole live population; Judged excludes the ones under
	// MinWorkerAge; Blocked is the subset that is alive AND writing nothing.
	LiveWorkers int `json:"live_workers"`
	Judged      int `json:"judged_workers"`
	Blocked     int `json:"blocked_workers"`
	// BlockedNames identifies them, sorted. The names are the argument a
	// coordinator cannot guess from a count.
	BlockedNames []string `json:"blocked_names,omitempty"`
	// MaxPTYIdle is the STALEST last-PTY-write among the blocked workers — the
	// conservative end of "all of them are awake".
	MaxPTYIdle time.Duration `json:"max_pty_idle"`
	// MinWriteIdle is the FRESHEST last-file-write among the blocked workers,
	// i.e. the least quiet of them. Reporting the freshest rather than an
	// average is deliberate: it is the number that could falsify the finding.
	MinWriteIdle time.Duration `json:"min_write_idle"`

	// WorkerCores and HostCores are the subtree measurement and its
	// denominator; CoresKnown says whether they are a measurement at all.
	WorkerCores float64 `json:"worker_cores"`
	HostCores   int     `json:"host_cores"`
	CoresKnown  bool    `json:"cores_known"`

	// SinceProgress is how long the fleet has landed nothing, measured from
	// LastProgress or, when there is none in the window, from ProgressSince.
	SinceProgress    time.Duration `json:"since_progress"`
	LastProgress     time.Time     `json:"last_progress,omitempty"`
	LastProgressWhat string        `json:"last_progress_what,omitempty"`
	ProgressKnown    bool          `json:"progress_known"`

	// InFlight and InFlightFor describe a merge holding the refinery slot.
	InFlight    string        `json:"in_flight,omitempty"`
	InFlightFor time.Duration `json:"in_flight_for,omitempty"`

	// Held lists, in words, the conjuncts that did NOT hold. It is empty when
	// Stalled. This is the half that makes a clean reading useful rather than a
	// bare "fine" — mayor's three readings were individually unremarkable and
	// the diagnosis was in how they disagreed.
	Held []string `json:"held,omitempty"`
	// Blind lists measurements that could not be taken at all. Any entry
	// forces Stalled false, and is a finding of its own kind: a detector that
	// cannot see is not a fleet that is fine.
	Blind []string `json:"blind,omitempty"`
}

// Verdict is the state a reading resolves to — the tri-state clean / stalled /
// blind, plus a sentinel for a Reading that was never evaluated at all. It is
// the field a machine consumer should switch on, and it exists because
// `stalled` alone cannot be switched on: `stalled:false` is emitted for a CLEAN reading and for a BLIND
// one alike, and the only thing separating them in the JSON used to be the
// PRESENCE of `blind` — an omitempty array, so the distinguishing evidence was
// absent in precisely the case that looked healthy (mg-e75b).
//
// That is the same shape as the render defect mg-516e fixed one layer up, where
// the blind paragraph opened with the clean paragraph's own headline. This
// detector was built because every signal read green while the fleet did
// nothing; shipping a green nobody can tell from a blind reading reproduces the
// outage inside the instrument.
//
// No one of the four values is a substring of another, asserted by a test,
// because mg-516e's failure was a grep matching where an equality test would not
// have — and a consumer piping --json through grep is the likeliest reader of
// this field.
type Verdict string

const (
	// VerdictClean is a reading that was TAKEN and found nothing. It is an
	// assertion, never an inference from an absence.
	VerdictClean Verdict = "clean"
	// VerdictStalled is the conjunction holding: alive, silent, idle, landing
	// nothing.
	VerdictStalled Verdict = "stalled"
	// VerdictBlind is a run that could not measure a member of the conjunction.
	// It is not a clean fleet — it is a fleet nobody measured.
	VerdictBlind Verdict = "blind"
	// VerdictUnknown is a Reading that was never evaluated at all: the zero
	// value, which callers get back alongside an error. It is spelled out
	// because the alternative is that a default-constructed Reading answers
	// "clean", and a zero value that reads as healthy is the defect this whole
	// field exists to remove.
	VerdictUnknown Verdict = "unknown"
)

// Verdict derives the state from the reading rather than storing it.
//
// Deriving is deliberate. A stored copy of a state that is also encoded in
// Stalled and Blind is a second source of truth that can drift from the first,
// and a verdict field that disagreed with the booleans would be a worse false
// green than the one it replaced. Derivation also makes the field correct for a
// Reading decoded from an OLDER pogod that never emitted it: the value is
// recomputed from fields that daemon did send.
func (r Reading) Verdict() Verdict {
	switch {
	case r.Now.IsZero():
		return VerdictUnknown
	case len(r.Blind) > 0:
		return VerdictBlind
	case r.Stalled:
		return VerdictStalled
	default:
		return VerdictClean
	}
}

// MarshalJSON emits the derived verdict alongside the measurements, WITHOUT
// omitempty: a consumer must be able to check one field and get an answer in
// every case, including the healthy one. Anything omitempty is evidence that
// vanishes exactly when the reading looks fine.
func (r Reading) MarshalJSON() ([]byte, error) {
	// The alias sheds Reading's methods, so this does not recurse.
	type alias Reading
	return json.Marshal(struct {
		Verdict Verdict `json:"verdict"`
		alias
	}{Verdict: r.Verdict(), alias: alias(r)})
}

// Evaluate applies the thresholds to a snapshot. It is a pure function: the
// same snapshot yields the same reading, which is what lets the runner's
// hysteresis and the CLI share one judgement instead of two that drift.
func Evaluate(s Snapshot, t Thresholds) Reading {
	th := t.resolved()
	r := Reading{
		Now:              s.Now,
		Thresholds:       th,
		LiveWorkers:      len(s.Workers),
		WorkerCores:      s.WorkerCores,
		HostCores:        s.HostCores,
		CoresKnown:       s.CoresKnown,
		LastProgress:     s.LastProgress,
		LastProgressWhat: s.LastProgressWhat,
		ProgressKnown:    s.ProgressKnown,
		InFlight:         s.InFlight,
	}

	// The worker halves.
	var blocked []Worker
	var unknownWrites []string
	for _, w := range s.Workers {
		if w.Age < th.MinWorkerAge {
			continue
		}
		r.Judged++
		if !w.alive(th.PTYActiveWithin) {
			continue
		}
		quiet, known := w.quiet(th.QuietWritesFor)
		if !known {
			unknownWrites = append(unknownWrites, w.Name)
			continue
		}
		if quiet {
			blocked = append(blocked, w)
		}
	}
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].Name < blocked[j].Name })
	r.Blocked = len(blocked)
	for i, w := range blocked {
		r.BlockedNames = append(r.BlockedNames, w.Name)
		if w.PTYIdle > r.MaxPTYIdle {
			r.MaxPTYIdle = w.PTYIdle
		}
		// The FRESHEST write among the blocked, which for a worker that has
		// never written is its whole age — that is how long it has been true
		// that nothing came out of it.
		idle := w.WriteIdle
		if !w.HasWrites {
			idle = w.Age
		}
		if i == 0 || idle < r.MinWriteIdle {
			r.MinWriteIdle = idle
		}
	}

	// The fleet halves.
	r.SinceProgress, _ = sinceProgress(s)
	if !s.InFlightSince.IsZero() {
		r.InFlightFor = s.Now.Sub(s.InFlightSince)
	}

	// Blindness first: an unmeasured member makes the conjunction UNKNOWN, and
	// unknown is never a finding.
	if len(unknownWrites) > 0 {
		r.Blind = append(r.Blind, fmt.Sprintf("worktree unreadable for %d worker(s): %s",
			len(unknownWrites), strings.Join(unknownWrites, ", ")))
	}
	if !s.CoresKnown {
		r.Blind = append(r.Blind, "worker CPU unmeasurable"+because(s.CoresError))
	}
	if !s.ProgressKnown {
		r.Blind = append(r.Blind, "fleet completions unreadable"+because(s.ProgressError))
	}
	if s.ProgressKnown && s.LastProgress.IsZero() && s.ProgressSince.IsZero() {
		r.Blind = append(r.Blind,
			"no observable completion window — nothing to measure 'nothing landed' against")
	}

	// The conjuncts, each recorded when it fails so a clean reading still says
	// which number rescued it.
	if r.Blocked < th.MinWorkers {
		r.Held = append(r.Held, fmt.Sprintf(
			"%d worker(s) alive-and-writing-nothing, under the %d it takes to be a fleet signal",
			r.Blocked, th.MinWorkers))
	}
	if s.CoresKnown && s.WorkerCores >= th.IdleCores {
		r.Held = append(r.Held, fmt.Sprintf(
			"worker subtrees are computing: %.2f cores, at or above the %.2f floor",
			s.WorkerCores, th.IdleCores))
	}
	if s.ProgressKnown && r.SinceProgress < th.NoProgressFor {
		r.Held = append(r.Held, fmt.Sprintf(
			"the fleet landed something %s ago, inside the %s window",
			round(r.SinceProgress), round(th.NoProgressFor)))
	}
	// A merge holding the serial slot IS the fleet producing, and it runs in
	// pogod's subtree where no worker measurement can see it. It excuses the
	// fleet only while it is younger than the completion window: a gate that
	// has held the slot longer than the fleet has been silent is not evidence
	// of progress, it is part of what is being reported.
	if r.InFlight != "" && r.InFlightFor < th.NoProgressFor {
		r.Held = append(r.Held, fmt.Sprintf(
			"merge %s has held the refinery slot for %s — the fleet is producing",
			r.InFlight, round(r.InFlightFor)))
	}

	r.Stalled = len(r.Blind) == 0 && len(r.Held) == 0
	return r
}

// sinceProgress returns how long the fleet has landed nothing and whether that
// was measured against a real completion (true) or against the start of the
// observable window (false).
func sinceProgress(s Snapshot) (time.Duration, bool) {
	if !s.LastProgress.IsZero() {
		d := s.Now.Sub(s.LastProgress)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	if !s.ProgressSince.IsZero() {
		d := s.Now.Sub(s.ProgressSince)
		if d < 0 {
			d = 0
		}
		return d, false
	}
	return 0, false
}

func because(msg string) string {
	if msg == "" {
		return ""
	}
	return ": " + msg
}

// round renders a duration the way a reader says it out loud, without losing
// the magnitude a threshold comparison turned on. Go's own String would print
// the incident's window as "31m0s"; the trailing zero units read as precision
// this measurement does not have, and a number that reads as spurious precision
// is a number a skimmer discounts.
func round(d time.Duration) string {
	if d >= time.Minute {
		d = d.Round(time.Minute)
	} else {
		d = d.Round(time.Second)
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// String is the reading, and it states WHAT IT MEASURED. There is deliberately
// no bare state token for a caller to print instead: mg-c058's page said
// "AGENTS ARE FAILING EVERY TURN" over 2 errors in a trailing 30m, and the
// lesson taken from it was that a token invites a present-tense over-reading
// the measurement cannot.
func (r Reading) String() string {
	var b strings.Builder
	// Switching on the same derived tri-state the JSON carries, so this lead
	// phrase and the `verdict` field cannot say different things about one
	// reading (mg-e75b).
	switch r.Verdict() {
	case VerdictStalled:
		b.WriteString("FLEET IS ALIVE AND LANDING NOTHING — ")
	case VerdictBlind, VerdictUnknown:
		b.WriteString("NOT MEASURED — ")
	default:
		b.WriteString("no finding — ")
	}
	b.WriteString(r.Measurements())
	if len(r.Held) > 0 {
		b.WriteString("\n  not reported because:")
		for _, h := range r.Held {
			b.WriteString("\n    - " + h)
		}
	}
	if len(r.Blind) > 0 {
		b.WriteString("\n  could not measure:")
		for _, x := range r.Blind {
			b.WriteString("\n    - " + x)
		}
	}
	return b.String()
}

// Measurements is the four readings on one line, in the order mayor took them.
// It is exported because it is the part worth quoting into mail, an event, or a
// ticket, and quoting it should not require re-deriving it from the fields.
func (r Reading) Measurements() string {
	var parts []string

	parts = append(parts, fmt.Sprintf("%d of %d judged worker(s) alive and writing nothing",
		r.Blocked, r.Judged))
	if r.Blocked > 0 {
		parts = append(parts, fmt.Sprintf("stalest PTY write %s ago, freshest file write %s ago",
			round(r.MaxPTYIdle), round(r.MinWriteIdle)))
	}
	if r.CoresKnown {
		parts = append(parts, fmt.Sprintf("worker subtrees at %.2f of %d cores",
			r.WorkerCores, r.HostCores))
	} else {
		parts = append(parts, "worker CPU not measured")
	}
	switch {
	case !r.ProgressKnown:
		parts = append(parts, "fleet completions not read")
	case r.LastProgressWhat != "":
		parts = append(parts, fmt.Sprintf("nothing landed in %s (last: %s)",
			round(r.SinceProgress), r.LastProgressWhat))
	default:
		parts = append(parts, fmt.Sprintf("nothing landed in the %s observed",
			round(r.SinceProgress)))
	}
	if r.InFlight != "" {
		parts = append(parts, fmt.Sprintf("merge %s in flight for %s",
			r.InFlight, round(r.InFlightFor)))
	}
	if r.LiveWorkers != r.Judged {
		parts = append(parts, fmt.Sprintf("%d live worker(s), %d too young to judge",
			r.LiveWorkers, r.LiveWorkers-r.Judged))
	}
	return strings.Join(parts, ", ")
}

// Subject is the mail subject for a finding. It carries the numbers rather than
// a verdict word for the same reason String does — a subject line is the part
// that gets skimmed and forwarded, so the hedge and the measurement have to
// reach it.
func (r Reading) Subject() string {
	return fmt.Sprintf("fleet is ALIVE and LANDING NOTHING — %d workers, nothing in %s, %.2f of %d cores",
		r.Blocked, round(r.SinceProgress), r.WorkerCores, r.HostCores)
}
