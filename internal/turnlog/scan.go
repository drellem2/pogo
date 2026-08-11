package turnlog

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// DefaultMaxAge is the age at which a last-completed-turn stops counting as
// live.
//
// Three hours, and the number is measured rather than chosen for roundness. A
// scheduler fire is only ackable at a turn boundary and long turns are normal:
// on 2026-08-09 ackwatch recorded a two-hour stretch with all six crew agents
// up and demonstrably working, 84 fires delivered and ZERO completed. Any
// ongoing-liveness threshold under ~3h false-positives on that population,
// which is also why ackwatch's own blackout window is 3h (measurement from
// mg-3cbb).
//
// This is therefore the floor for ONGOING liveness and cannot be tightened into
// a wedge detector. The much shorter threshold that IS safe — 45 minutes —
// exists only for the spawn-scoped question ("has this agent completed a turn
// since it started"), where the healthy and outage populations separate
// completely: 67 healthy spawns reached their first completion within 33.7 min,
// 20 spawns inside the three outages took at least 150.8 min, and nothing at
// all falls between. That question belongs to internal/firstturn; the two
// thresholds are why there are two readers rather than one.
//
// The threshold is an argument here, not a constant this package defends.
const DefaultMaxAge = 3 * time.Hour

// ErrNoPopulation is returned when the set of agents that are PRESENT could not
// be determined.
//
// This is a hard failure and never a clean report, and the reason is the whole
// design of this scan. The population comes from the presence layer (pogod's
// registry: who is running) and the evidence comes from the agent layer (who
// finished a turn). If the population is unavailable, the only remaining way to
// produce a list would be to enumerate the turnlog directory — which would make
// the report structurally blind to exactly the agents it exists to find. An
// agent with no turnlog file has no entry in that directory, and "mayor writes
// nothing" is the finding, not the absence of one.
var ErrNoPopulation = errors.New("turnlog: could not determine which agents are present")

// Verdict is one agent's reading.
type Verdict string

const (
	// VerdictLive — present, and completed a turn within MaxAge.
	VerdictLive Verdict = "live"
	// VerdictStale — present, has completed turns, but none recently.
	VerdictStale Verdict = "stale"
	// VerdictSilent — present and has NEVER written a completed turn. The
	// state mayor, pa and architect were in for the whole of mg-a270's
	// history, and the one this instrument exists to make visible.
	VerdictSilent Verdict = "silent"
	// VerdictUnreadable — a turnlog exists and could not be read or parsed.
	// Counted separately and never as a pass: an instrument that cannot read
	// its own artifact has measured nothing about that agent.
	VerdictUnreadable Verdict = "unreadable"
)

// Finding reports whether this verdict is a red reading.
func (v Verdict) Finding() bool { return v != VerdictLive }

// State is one agent's row.
type State struct {
	Agent   string    `json:"agent"`
	Type    string    `json:"type,omitempty"`
	Verdict Verdict   `json:"verdict"`
	Last    time.Time `json:"last,omitempty"`
	AgeSecs float64   `json:"age_secs,omitempty"`
	Note    string    `json:"note,omitempty"`
	Path    string    `json:"path"`
	Started time.Time `json:"started,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// Age is the time since this agent's last completed turn. Zero when there is
// no last turn — read Verdict, not Age, to decide whether the agent is live.
func (s State) Age() time.Duration { return time.Duration(s.AgeSecs * float64(time.Second)) }

// Present is one agent the presence layer says is running.
type Present struct {
	Name string
	Type string
	// StartedAt is when this agent's process started, when the presence layer
	// knows. Carried through to State so a reader can tell "has not completed a
	// turn yet" from "has stopped completing them" — an agent thirty seconds
	// old with no turnlog line is not a finding, and a reader without this
	// field cannot avoid calling it one.
	StartedAt time.Time
}

// Options configures Scan.
type Options struct {
	// Population returns the agents that are PRESENT. Required. An error is
	// propagated as ErrNoPopulation and never swallowed — see that variable.
	Population func() ([]Present, error)
	// MaxAge is the staleness threshold. Zero uses DefaultMaxAge.
	MaxAge time.Duration
	// Now overrides the clock, for tests and the probe.
	Now time.Time
	// Root overrides the turnlog directory. Empty uses Dir(). Set by the
	// probe so the positive control runs this same function over a
	// throwaway tree instead of reimplementing it.
	Root string
}

// Report is the joined reading.
type Report struct {
	Dir      string    `json:"dir"`
	Now      time.Time `json:"now"`
	MaxAge   string    `json:"max_age"`
	Agents   []State   `json:"agents"`
	Live     int       `json:"live"`
	Stale    int       `json:"stale"`
	Silent   int       `json:"silent"`
	Bad      int       `json:"unreadable"`
	Findings int       `json:"findings"`
}

// Scan joins the present population against the turn-completion artifacts.
//
// The join direction is the point: iterate the POPULATION and look up each
// agent's evidence, never iterate the evidence. A scan built the other way
// reports on the agents that are already instrumented and is silent about the
// ones that are not, which is the failure mode this package was written to
// end.
func Scan(opts Options) (Report, error) {
	if opts.Population == nil {
		return Report{}, ErrNoPopulation
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	maxAge := opts.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	root := opts.Root
	if root == "" {
		root = Dir()
	}
	pop, err := opts.Population()
	if err != nil {
		return Report{}, fmt.Errorf("%w: %v", ErrNoPopulation, err)
	}

	rep := Report{Dir: root, Now: now.UTC(), MaxAge: maxAge.String()}
	for _, p := range pop {
		st := State{Agent: p.Name, Type: p.Type, Path: PathIn(root, p.Name), Started: p.StartedAt}
		last, err := LastIn(root, p.Name)
		switch {
		case err == nil:
			st.Last = last.At
			st.AgeSecs = now.Sub(last.At).Seconds()
			st.Note = last.Note
			if now.Sub(last.At) <= maxAge {
				st.Verdict = VerdictLive
			} else {
				st.Verdict = VerdictStale
				st.Detail = "last completed turn is older than " + maxAge.String()
			}
		case errors.Is(err, os.ErrNotExist):
			st.Verdict = VerdictSilent
			st.Detail = "no turn-completion artifact exists for this agent"
		default:
			st.Verdict = VerdictUnreadable
			st.Detail = err.Error()
		}
		rep.Agents = append(rep.Agents, st)
	}
	sort.Slice(rep.Agents, func(i, j int) bool { return rep.Agents[i].Agent < rep.Agents[j].Agent })

	for _, s := range rep.Agents {
		switch s.Verdict {
		case VerdictLive:
			rep.Live++
		case VerdictStale:
			rep.Stale++
		case VerdictSilent:
			rep.Silent++
		case VerdictUnreadable:
			rep.Bad++
		}
		if s.Verdict.Finding() {
			rep.Findings++
		}
	}
	return rep, nil
}
