// Package heartwatch is the POGOD-RESIDENT reader of the crew heartbeat
// (mg-d616).
//
// # The circularity this exists to break
//
// Every crew agent refreshes a heartbeat line in its sweep.log on every
// mail-check — a ten-minute cadence — and the file's mtime is the fleet's
// tightest liveness signal. Until this package, reading it was a STEP IN THE
// COORDINATOR'S OWN COORDINATION LOOP: mayor.md §3a says to list
// `~/.pogo/agents/pm/*/sweep.log`, nudge at 90 minutes and restart at 120. It
// had no other executor. So it did not degrade when the coordinator stopped:
// it stopped.
//
// That is not a threshold to tune. It is a detector routed through the one
// agent whose failure it would have to report, and the bill was measured:
//
//	pm-onethird sweep.log   mtime stale 14d   (~168x past T_restart=120m)
//	pm-riemann  sweep.log   mtime stale 14d   (~168x past T_restart=120m)
//	neither was nudged or restarted by anything
//
// Both PMs asked, independently and in the same hour, whether they had been
// classified as expected-quiet. They had not. THE READER WAS DOWN. That
// hypothesis is recorded here as refuted because it is the natural one and it
// sends the fix at the classifier.
//
// # What this package is, stated at its true strength
//
// It is a SECOND EXECUTOR for a check that had one, living where
// internal/turnwatch lives and for the same reason (see that package's doc):
// pogod is the only participant on this machine that is not a crew agent and
// does not route through the coordinator. What it adds is an executor and a
// routing rule. It does not add a signal — the sweep.log mtime was always
// there, and mayor's prompt still describes reading it.
//
// It is NOT a replacement for turnwatch and must not be collapsed into one.
// The two read different artifacts with different meanings:
//
//	turnwatch    ~/.pogo/agents/turnlog/<name>.log — one line per COMPLETED
//	             TURN. Nothing but a finished turn writes it. Coarse (3h) and
//	             unfakeable.
//	heartwatch   <agent dir>/sweep.log mtime — refreshed by a mail-check. Ten
//	             times tighter, and a present-but-idle agent keeps touching it.
//
// A heartbeat is the weaker evidence and the faster clock. Keeping both is the
// point: an agent that stops writing turnlog lines while still refreshing a
// heartbeat stays visible, and the converse. Merging them would collapse two
// independent witnesses onto one evidence source and one blind state.
//
// # REPORT-ONLY, deliberately, even though the prompt it mirrors restarts
//
// mayor.md §3a nudges at T_stall and restarts at T_restart. This does neither,
// and the asymmetry is not timidity. A stale heartbeat has two causes that look
// identical from outside and take OPPOSITE responses — a wedged session
// (restart is right) and an agent failing every turn in ~10ms on an expired
// credential or a spend cap (restart destroys the transcript that diagnoses it,
// and the replacement inherits the credential). On 2026-07-22 that distinction
// cost 23h30m, and the 120-minute rule applied without it would have produced
// ~66 restarts that recovered nothing (mg-18d0, mg-8cdb).
//
// pogod already distinguishes those elsewhere. This detector's job is to make
// the condition VISIBLE AT ALL when the agent that would have looked is dark.
//
// # An empty population is not a clean fleet
//
// Scan iterates the POPULATION (pogod's registry) and looks up the evidence. It
// never lists the sweep.log tree to decide who to report on, and there is a
// measured reason: this machine carries `~/.pogo/agents/pm/lineara/sweep.log`
// and two siblings whose agents have not existed for months. A tree-first scan
// reports those forever, and a detector that is permanently red is a detector
// nobody reads.
//
// The converse trap is the one this lineage was founded on, so Report says it
// out loud: Examined == 0 produces zero findings, which is exactly the shape of
// green that hid a 22-hour outage. Callers must print the count, not just the
// findings.
//
// # WHAT THIS DOES NOT COVER, stated here so the next reader does not see "the
// # circularity is closed" and assume it is closed everywhere
//
// The recursion has to stop somewhere and it stops here: NOTHING WATCHES THIS
// DETECTOR. If the runner in watcher.go stops ticking, it emits nothing, and
// nothing is what a fleet of fresh heartbeats also produces. That is the same
// property the check it replaces had — one level out, and against a different
// host.
//
// What that buys is worth naming precisely rather than overstating:
//
//	closed    FLEET DOWN, POGOD UP. The coordinator stops, or every crew agent
//	          does, and pogod is still running. That is mg-d616's own case and
//	          all three recorded outages.
//	NOT       POGOD WEDGED rather than exited. A resident reader wedges with its
//	closed    host, and launchd restarts on exit only. Identical to the gap
//	          internal/turnwatch names in its own doc; it is real, and widening
//	          scope for it here would be a second detector with the same host.
//
// The honest difference between this and the prompt step it doubles is that the
// two now fail INDEPENDENTLY. A coordinator that stops no longer takes the check
// with it, and a pogod that wedges leaves mayor.md §3a still written down and
// still executable by hand. Neither alone is a floor.
package heartwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// HeartbeatFile is the per-agent artifact whose mtime is the signal.
const HeartbeatFile = "sweep.log"

// Thresholds. The two staleness numbers are mayor.md §3a's T_stall and
// T_restart, carried over unchanged so the prompt and the daemon cannot
// disagree about when an agent is late. They are conservative on purpose:
// mg-60ca's real wedge was caught by hand at ~14 minutes, and 90 minutes buys
// immunity from network blips, long tool calls and clock skew.
const (
	// DefaultStallAfter is mayor.md's T_stall.
	DefaultStallAfter = 90 * time.Minute
	// DefaultRestartAfter is mayor.md's T_restart. Nothing here restarts
	// anything; the threshold is kept because it is the line at which the
	// coordinator's own rules call the reading actionable, and a notice that
	// cannot say which side of it an agent is on is harder to act on.
	DefaultRestartAfter = 120 * time.Minute
	// DefaultGrace is how long after an agent starts it may go without a
	// heartbeat before it is judged. The mail-check that writes one is
	// registered at spawn and fires on a ten-minute cron, so three cadences is
	// ample and still an order of magnitude inside T_stall.
	DefaultGrace = 30 * time.Minute
	// DefaultWakeGrace is how long after a system_wake findings are suppressed.
	// It is mayor.md §3a's `--since=20m` suppression, moved into code: after a
	// host sleep the schedules are still replaying and a stale heartbeat is
	// EXPECTED, not a fault.
	DefaultWakeGrace = 20 * time.Minute
)

// ErrNoPopulation is returned when the presence layer cannot be read. It is an
// error rather than an empty report on purpose: without a population this run
// measured nothing, and reporting nothing as a clean fleet is the founding bug
// of every detector in this tree.
var ErrNoPopulation = errors.New("heartwatch: no population: the agent registry could not be read, so this scan measured nothing")

// Verdict is one agent's reading.
type Verdict string

const (
	// VerdictFresh — a heartbeat inside T_stall.
	VerdictFresh Verdict = "fresh"
	// VerdictStale — T_stall < age <= T_restart. mayor.md nudges here.
	VerdictStale Verdict = "stale"
	// VerdictRestartDue — age > T_restart. mayor.md restarts here, after its
	// pre-restart check. Nothing in this package acts.
	VerdictRestartDue Verdict = "restart_due"
	// VerdictMissing — the agent is present and publishes no sweep.log at all.
	//
	// Never folded into fresh. "No file" and "a file nobody was watching" are
	// the two readings this whole ticket is about, and an agent whose prompt
	// tier keeps no heartbeat is a true reading of a different fact — which is
	// why it is its own verdict rather than an omission from the census.
	VerdictMissing Verdict = "missing"
	// VerdictUnreadable — a sweep.log exists and could not be stat'd. Counted
	// separately and never as a pass.
	VerdictUnreadable Verdict = "unreadable"
)

// Finding reports whether this verdict is a red reading.
func (v Verdict) Finding() bool { return v != VerdictFresh }

// Actionable reports whether this verdict is past T_restart.
func (v Verdict) Actionable() bool { return v == VerdictRestartDue }

// Present is one agent the presence layer says is running.
type Present struct {
	Name string
	Type string
	// StartedAt is when the process started, when the presence layer knows it.
	// Carried into State so a reader can tell "has not written one yet" from
	// "has stopped writing them" — an agent ninety seconds old with no
	// heartbeat is not a finding, and a reader without this field cannot avoid
	// calling it one.
	StartedAt time.Time
}

// State is one agent's row.
type State struct {
	Agent   string    `json:"agent"`
	Type    string    `json:"type,omitempty"`
	Verdict Verdict   `json:"verdict"`
	Last    time.Time `json:"last,omitempty"`
	AgeSecs float64   `json:"age_secs,omitempty"`
	Path    string    `json:"path,omitempty"`
	Started time.Time `json:"started,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	// Searched is every path this scan looked at for this agent. Present on a
	// `missing` row so the reader can check the claim rather than take it: an
	// error message must say what was established, not what would have been
	// true had the code looked somewhere else (mg-20eb).
	Searched []string `json:"searched,omitempty"`
}

// Age is the time since this agent's last heartbeat. Zero when there is none —
// read Verdict, not Age.
func (s State) Age() time.Duration { return time.Duration(s.AgeSecs * float64(time.Second)) }

// ScanOptions configures Scan.
type ScanOptions struct {
	// Population returns the agents that are PRESENT. Required.
	Population func() ([]Present, error)
	// StallAfter and RestartAfter are T_stall and T_restart. Zero uses the
	// defaults.
	StallAfter   time.Duration
	RestartAfter time.Duration
	// Grace is the post-start window in which an agent with no heartbeat is
	// reported as `fresh` rather than `missing`. Zero uses DefaultGrace;
	// negative disables it.
	Grace time.Duration
	// Now overrides the clock, for tests and the probe.
	Now time.Time
	// Root overrides the agent tree. Empty uses Dir(). Set by tests and by the
	// probe so the positive control runs THIS function over a throwaway tree
	// rather than reimplementing it.
	Root string
	// WokeAt is the most recent system_wake, supplied by the caller because no
	// artifact under Root records one. Zero means no suppression.
	WokeAt time.Time
	// WakeGrace is how long after WokeAt findings are suppressed. Zero uses
	// DefaultWakeGrace; negative disables suppression.
	WakeGrace time.Duration
}

// Report is the joined reading.
type Report struct {
	Root         string    `json:"root"`
	Now          time.Time `json:"now"`
	StallAfter   string    `json:"stall_after"`
	RestartAfter string    `json:"restart_after"`
	// Examined is the size of the population. PRINT IT: zero examined produces
	// zero findings, and that is the shape of green that hides an outage.
	Examined   int     `json:"examined"`
	Agents     []State `json:"agents"`
	Fresh      int     `json:"fresh"`
	Stale      int     `json:"stale"`
	RestartDue int     `json:"restart_due"`
	Missing    int     `json:"missing"`
	Bad        int     `json:"unreadable"`
	Findings   int     `json:"findings"`
	InGrace    int     `json:"in_grace"`
	// WakeSuppressed is true when a system_wake landed inside WakeGrace. The
	// VERDICTS ARE STILL COMPUTED AND STILL REPORTED — suppression gates
	// ACTION, never the reading. A detector that goes blank after a host sleep
	// is a detector that goes blank at exactly the moment the fleet is most
	// likely to be stuck.
	WakeSuppressed bool      `json:"wake_suppressed"`
	WokeAt         time.Time `json:"woke_at,omitempty"`
}

// Dir returns the agent tree root: $POGO_HOME/agents.
//
// It mirrors agent.PromptDir()'s construction rather than calling it so this
// package stays importable from anywhere in the tree without dragging the
// agent runtime along.
func Dir() string { return filepath.Join(config.PogoHome(), "agents") }

// PathsIn returns the candidate heartbeat paths for one agent under root, in
// search order.
//
// There are two shapes because the tree grew two. A PM's heartbeat lives beside
// its project memory at `agents/pm/<name>/sweep.log` (pm-template.md), while
// every other crew agent — the coordinator included — keeps one in its own home
// dir at `agents/<name>/sweep.log` (mayor.md). Probing both and taking the
// NEWEST that exists is the liveness-correct rule: a fresh heartbeat anywhere
// under an agent's name means that agent is alive, and preferring one path
// would make the answer depend on which tier a reader guessed.
func PathsIn(root, name string) []string {
	return []string{
		filepath.Join(root, "pm", name, HeartbeatFile),
		filepath.Join(root, name, HeartbeatFile),
	}
}

// Paths returns the candidate heartbeat paths under the live Dir().
func Paths(name string) []string { return PathsIn(Dir(), name) }

// Scan joins the present population against the heartbeat artifacts.
//
// The join direction is the design: iterate the POPULATION and look up each
// agent's evidence, never iterate the evidence. See the package doc for the
// three dead sweep.log files on this machine that a tree-first scan would
// report forever.
func Scan(opts ScanOptions) (Report, error) {
	if opts.Population == nil {
		return Report{}, ErrNoPopulation
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	stall := opts.StallAfter
	if stall <= 0 {
		stall = DefaultStallAfter
	}
	restart := opts.RestartAfter
	if restart <= 0 {
		restart = DefaultRestartAfter
	}
	if restart < stall {
		// A restart threshold under the stall threshold would make
		// VerdictStale unreachable and quietly turn the tighter reading off.
		// Clamp rather than accept it: a misconfiguration must not remove a
		// verdict from the answer space.
		restart = stall
	}
	grace := opts.Grace
	if grace == 0 {
		grace = DefaultGrace
	}
	if grace < 0 {
		grace = 0
	}
	wakeGrace := opts.WakeGrace
	if wakeGrace == 0 {
		wakeGrace = DefaultWakeGrace
	}
	if wakeGrace < 0 {
		wakeGrace = 0
	}
	root := opts.Root
	if root == "" {
		root = Dir()
	}

	pop, err := opts.Population()
	if err != nil {
		return Report{}, fmt.Errorf("%w: %v", ErrNoPopulation, err)
	}

	rep := Report{
		Root:         root,
		Now:          now,
		StallAfter:   stall.String(),
		RestartAfter: restart.String(),
		Examined:     len(pop),
		WokeAt:       opts.WokeAt,
	}
	if wakeGrace > 0 && !opts.WokeAt.IsZero() && now.Sub(opts.WokeAt) < wakeGrace {
		rep.WakeSuppressed = true
	}

	for _, p := range pop {
		st := inspect(root, p, now, stall, restart, grace)
		switch st.Verdict {
		case VerdictFresh:
			rep.Fresh++
		case VerdictStale:
			rep.Stale++
		case VerdictRestartDue:
			rep.RestartDue++
		case VerdictMissing:
			rep.Missing++
		case VerdictUnreadable:
			rep.Bad++
		}
		if st.Verdict.Finding() {
			rep.Findings++
		}
		if st.Detail == graceDetail {
			rep.InGrace++
		}
		rep.Agents = append(rep.Agents, st)
	}
	sort.Slice(rep.Agents, func(i, j int) bool { return rep.Agents[i].Agent < rep.Agents[j].Agent })
	return rep, nil
}

const graceDetail = "inside the post-start grace window — no heartbeat is owed yet"

// inspect reads one agent's evidence.
func inspect(root string, p Present, now time.Time, stall, restart, grace time.Duration) State {
	st := State{Agent: p.Name, Type: p.Type, Started: p.StartedAt}
	candidates := PathsIn(root, p.Name)

	var (
		best     time.Time
		bestPath string
		badPath  string
		badErr   error
	)
	for _, path := range candidates {
		fi, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				// A file that is there and cannot be read is NOT an absence.
				// Recorded separately so "we looked and found nothing" stays
				// distinguishable from "we could not look".
				badPath, badErr = path, err
			}
			continue
		}
		if fi.IsDir() {
			badPath, badErr = path, errors.New("is a directory, not a heartbeat file")
			continue
		}
		if mt := fi.ModTime(); mt.After(best) {
			best, bestPath = mt, path
		}
	}

	if best.IsZero() {
		st.Searched = candidates
		if badErr != nil {
			st.Verdict = VerdictUnreadable
			st.Path = badPath
			st.Detail = badErr.Error()
			return st
		}
		if grace > 0 && !p.StartedAt.IsZero() && now.Sub(p.StartedAt) < grace {
			// Not a finding: the mail-check that writes the first heartbeat is
			// registered at spawn and fires on a ten-minute cron. Judging here
			// would make every spawn a finding and teach the reader to ignore
			// this detector.
			st.Verdict = VerdictFresh
			st.Detail = graceDetail
			return st
		}
		st.Verdict = VerdictMissing
		st.Detail = "present, and publishes no " + HeartbeatFile + " under any known path"
		return st
	}

	st.Path = bestPath
	st.Last = best.UTC()
	age := now.Sub(best)
	if age < 0 {
		// A heartbeat in the future is a clock fault, not freshness. Report it
		// rather than let a skewed mtime buy an agent unlimited silence.
		st.Verdict = VerdictUnreadable
		st.Detail = fmt.Sprintf("heartbeat mtime is %s in the FUTURE — clock skew or a forged mtime; "+
			"this agent's liveness was not established", (-age).Round(time.Second))
		return st
	}
	st.AgeSecs = age.Seconds()
	switch {
	case age > restart:
		st.Verdict = VerdictRestartDue
	case age > stall:
		st.Verdict = VerdictStale
	default:
		st.Verdict = VerdictFresh
	}
	return st
}
