// Package hostload measures how much of a host's CPU the pogo fleet is
// actually consuming, so a decision about contention can be made on the
// resource rather than on a count of agents.
//
// # Why not the load average
//
// The obvious instrument is `uptime`'s first column, and it is the wrong one.
// Measured on this fleet's host on 2026-07-30 (mg-1b8c): a load average of 214
// coincided with roughly 7.5 of 10 cores actually in use. On Darwin the load
// average counts runnable *and* uninterruptible-sleep tasks, so a host doing
// heavy I/O reports a number that has almost nothing to do with how much CPU
// is left. A dispatch guard keyed on that number refuses to start work while
// cores sit idle.
//
// It is also not all ours. In the same measurement a VPN extension held ~0.9
// cores and the system indexer another ~0.3. A fleet-side guard keyed on total
// load hands an unrelated process a veto over dispatch.
//
// So the load average is carried here as *context* — it is what let a
// coordinator notice something was wrong in the first place — and it is never
// the number anything decides on. The number to decide on is FleetCores.
//
// # Why not a count of agents
//
// The failure that prompted this package was first described as "two heavy
// polecats on one host". The live instance was ONE polecat that had
// self-parallelised into three Python processes at ~5.7 cores of a 10-core
// box. Any guard that counts agents would have seen a single agent and called
// the host idle. Attribution here is therefore by process subtree, not by
// agent: every descendant of the fleet's root process counts, however many
// there are.
//
// # How the measurement is taken
//
// Two snapshots of cumulative per-process CPU time, separated by a window,
// differenced and divided by the window. That yields cores-in-use over that
// window. It is deliberately not `ps`'s own %CPU column, which is a lifetime
// average on Linux and a decaying one on Darwin — neither answers "what is
// happening right now", which is the question a gate or a dispatch decision is
// asking.
//
// # Where it can be measured
//
// A differenced rate is only as good as the precision of the column it
// differenced, and that precision is a property of the HOST. internal/proctable
// picks the best available source and states its resolution: 10ms on darwin and
// on any Linux with a readable /proc, whole seconds where only procps `ps` is
// available. Below a source's minimum window the difference is not a small
// number, it is zero — for a saturated host exactly as much as an idle one.
// Sample.Unresolvable carries that case, and it is the difference between
// "the fleet is using nothing" and "this host cannot tell me". Reporting the
// first when the second is true is how a dispatch guard goes blind (mg-79e3).
package hostload

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/drellem2/pogo/internal/proctable"
)

// DefaultWindow is how long a sample observes the host for. Long enough that
// scheduler noise averages out, short enough that a caller deciding whether to
// dispatch is not left waiting.
const DefaultWindow = time.Second

// Sample is one observation of what a host's CPUs are doing, split by whether
// the fleet is responsible for it.
//
// The split is the point. "The host is busy" cannot tell a coordinator whether
// to hold a dispatch, because the fleet can only decide about its own work.
type Sample struct {
	// Time is when the sample finished; Window is how long it observed.
	Time   time.Time     `json:"time"`
	Window time.Duration `json:"window"`

	// Cores is the host's logical core count — the denominator for everything
	// below.
	Cores int `json:"cores"`

	// FleetCores is CPU consumed over the window by the fleet's root process
	// and every descendant of it, expressed in cores. This is the number to
	// decide on.
	FleetCores float64 `json:"fleet_cores"`
	// ExternalCores is CPU consumed by everything else on the host. Reported
	// because it bounds what the fleet can hope to use, and never gated on:
	// the fleet does not get a veto from someone else's VPN client, and must
	// not give one either.
	ExternalCores float64 `json:"external_cores"`
	// FleetProcs is how many distinct processes contributed to FleetCores.
	// One agent can be many processes; that is the case this package exists
	// for, so the count is reported next to the cores rather than instead.
	FleetProcs int `json:"fleet_procs"`

	// LoadAvg1 is the host's 1-minute load average. CONTEXT ONLY — see the
	// package comment. Nothing in this package or its callers decides on it.
	LoadAvg1 float64 `json:"load_avg_1"`

	// Attributed reports whether a fleet root was found. When false,
	// FleetCores is 0 because nothing could be attributed, which is not the
	// same fact as "the fleet is idle" and must not be read as one.
	Attributed bool `json:"attributed"`

	// Source names the process-table reader and the precision of its CPU
	// column. Carried on every sample so a reading states the environment it
	// was taken in — this class of failure is invisible until it does.
	Source string `json:"source,omitempty"`
	// Unresolvable is set when Window was too short for Source's resolution to
	// separate work from none. The core figures are then quantisation noise
	// rather than a measurement, and must not be read as "nothing is running".
	Unresolvable string `json:"unresolvable,omitempty"`
}

// Resolved reports whether the sample's numbers are a measurement at all.
// A caller deciding on cores must check this: an unresolvable sample reports
// zeros that mean "cannot tell", not "idle".
func (s Sample) Resolved() bool { return s.Unresolvable == "" }

// UsedCores is total CPU in use over the window, fleet and otherwise.
func (s Sample) UsedCores() float64 { return s.FleetCores + s.ExternalCores }

// FreeCores is how much CPU was not in use over the window. Can go slightly
// negative under measurement jitter; clamped at zero because a negative
// headroom is not a meaningful thing to report.
func (s Sample) FreeCores() float64 {
	free := float64(s.Cores) - s.UsedCores()
	if free < 0 {
		return 0
	}
	return free
}

// FleetSaturation is the fraction of the host's cores the fleet is holding,
// 0..1+ (it can exceed 1 when the fleet oversubscribes).
func (s Sample) FleetSaturation() float64 {
	if s.Cores <= 0 {
		return 0
	}
	return s.FleetCores / float64(s.Cores)
}

// String renders the sample as one line, with the load average last and
// labelled, so it cannot be mistaken for the decision number.
func (s Sample) String() string {
	if !s.Resolved() {
		return fmt.Sprintf("host load unmeasurable: %s", s.Unresolvable)
	}
	if !s.Attributed {
		return fmt.Sprintf("fleet unattributed (no fleet root found), host using %.1f of %d cores "+
			"(loadavg %.2f, context only)", s.UsedCores(), s.Cores, s.LoadAvg1)
	}
	return fmt.Sprintf("fleet %.1f of %d cores across %d procs, external %.1f, free %.1f "+
		"(loadavg %.2f, context only)",
		s.FleetCores, s.Cores, s.FleetProcs, s.ExternalCores, s.FreeCores(), s.LoadAvg1)
}

// Sampler takes host samples. *Reader is the production implementation;
// callers hold the interface so a package that needs to reason about a KNOWN
// host in its tests can supply one, rather than asserting against whatever the
// machine running the suite happens to be doing.
type Sampler interface {
	Read(ctx context.Context) (Sample, error)
}

// FixedSampler returns the same sample every time. It is exported because the
// packages that consume host load — the refinery's gate watch, the dispatch
// gate — have to be testable against a saturated host and an idle one, and
// neither is a state a test can create on the machine it runs on.
type FixedSampler struct {
	Sample Sample
	Err    error
}

// Read implements Sampler.
func (f FixedSampler) Read(ctx context.Context) (Sample, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return Sample{}, ctx.Err()
		default:
		}
	}
	if f.Err != nil {
		return Sample{}, f.Err
	}
	return f.Sample, nil
}

// procRow is one process as `ps` reports it.
type procRow struct {
	pid  int
	ppid int
	cpu  time.Duration // cumulative CPU time consumed since the process started
}

// psSnapshot is the seam tests replace. Real callers get readPS.
type psSnapshot func() (map[int]procRow, time.Time, error)

// Reader takes samples. The zero value is usable and reads the real host.
type Reader struct {
	// Roots are the pids whose subtrees count as fleet. Normally one entry,
	// pogod's pid.
	Roots []int
	// Window overrides DefaultWindow.
	Window time.Duration
	// Source overrides the detected process-table source. Zero value means
	// proctable.Current(). Exported so a test can assert what this reader does
	// on a host whose CPU column is too coarse for the window it was given —
	// a state no test can create on the machine it happens to run on.
	Source proctable.Source

	// snapshot and loadavg are test seams.
	snapshot psSnapshot
	loadavg  func() float64
	cores    int
}

func (r *Reader) source() proctable.Source {
	if r.Source.Resolution > 0 {
		return r.Source
	}
	return proctable.Current()
}

func (r *Reader) window() time.Duration {
	if r.Window > 0 {
		return r.Window
	}
	return DefaultWindow
}

func (r *Reader) numCores() int {
	if r.cores > 0 {
		return r.cores
	}
	return runtime.NumCPU()
}

func (r *Reader) snap() psSnapshot {
	if r.snapshot != nil {
		return r.snapshot
	}
	return readPS
}

func (r *Reader) load() float64 {
	if r.loadavg != nil {
		return r.loadavg()
	}
	return readLoadAvg1()
}

// Read takes one sample. It blocks for the reader's window.
//
// ctx cancellation aborts the wait and returns ctx.Err(): a caller that has
// been told to stop should not be held for a second by an observability call.
func (r *Reader) Read(ctx context.Context) (Sample, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	first, t0, err := r.snap()()
	if err != nil {
		return Sample{}, fmt.Errorf("hostload: first snapshot: %w", err)
	}
	select {
	case <-ctx.Done():
		return Sample{}, ctx.Err()
	case <-time.After(r.window()):
	}
	second, t1, err := r.snap()()
	if err != nil {
		return Sample{}, fmt.Errorf("hostload: second snapshot: %w", err)
	}

	elapsed := t1.Sub(t0)
	if elapsed <= 0 {
		return Sample{}, fmt.Errorf("hostload: non-positive sample window %s", elapsed)
	}

	src := r.source()
	if reason := src.CannotResolve(elapsed); reason != "" {
		// Not a small measurement — no measurement. Every per-process
		// difference quantises to zero over a window this short, so the cores
		// figures would read as an idle host whatever the host is doing.
		// Returning the reason instead is the whole point of the field.
		return Sample{
			Time:         t1,
			Window:       elapsed,
			Cores:        r.numCores(),
			LoadAvg1:     r.load(),
			Source:       src.Name,
			Unresolvable: reason,
		}, nil
	}

	fleetPIDs, attributed := fleetSubtree(second, r.Roots)

	var fleetCPU, externalCPU time.Duration
	fleetProcs := 0
	for pid, now := range second {
		var delta time.Duration
		if before, ok := first[pid]; ok {
			delta = now.cpu - before.cpu
		} else {
			// A process that appeared during the window. Its whole CPU time
			// was spent inside the window (it did not exist before), but cap
			// at the window times cores so a bad `ps` row cannot skew a
			// sample without bound.
			delta = now.cpu
			if max := elapsed * time.Duration(r.numCores()); delta > max {
				delta = max
			}
		}
		if delta <= 0 {
			continue
		}
		if fleetPIDs[pid] {
			fleetCPU += delta
			fleetProcs++
		} else {
			externalCPU += delta
		}
	}

	return Sample{
		Time:          t1,
		Window:        elapsed,
		Cores:         r.numCores(),
		FleetCores:    float64(fleetCPU) / float64(elapsed),
		ExternalCores: float64(externalCPU) / float64(elapsed),
		FleetProcs:    fleetProcs,
		LoadAvg1:      r.load(),
		Attributed:    attributed,
		Source:        src.Name,
	}, nil
}

// fleetSubtree returns the set of pids that are a root or descend from one.
//
// Descent is what makes this count the resource rather than the agents: a
// polecat that forks three compute processes contributes all three, and an
// agent doing nothing contributes nothing.
func fleetSubtree(rows map[int]procRow, roots []int) (map[int]bool, bool) {
	set := map[int]bool{}
	live := 0
	for _, root := range roots {
		if root <= 0 {
			continue
		}
		if _, ok := rows[root]; ok {
			live++
		}
		set[root] = true
	}
	if len(set) == 0 {
		return set, false
	}

	// Walk each process up to a root. Bounded by the table size so a cycle in
	// a malformed ps table cannot spin.
	for pid := range rows {
		cur, hops := pid, 0
		for hops <= len(rows) {
			if set[cur] {
				set[pid] = true
				break
			}
			row, ok := rows[cur]
			if !ok || row.ppid <= 0 || row.ppid == cur {
				break
			}
			cur = row.ppid
			hops++
		}
	}
	return set, live > 0
}

// readPS snapshots every process's cumulative CPU time. The reading itself —
// and, critically, its precision — belongs to internal/proctable: the same
// question is asked by the refinery's gate watch, and two readers that
// disagree about how finely CPU time can be read is how one of them ends up
// reporting a zero it cannot justify.
func readPS() (map[int]procRow, time.Time, error) {
	table, err := proctable.Read()
	at := time.Now()
	if err != nil {
		return nil, at, err
	}
	rows := make(map[int]procRow, len(table))
	for _, r := range table {
		rows[r.PID] = procRow{pid: r.PID, ppid: r.PPID, cpu: r.CPU}
	}
	return rows, at, nil
}
