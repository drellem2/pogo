// Package orphanwatch reports compute processes that outlived the polecat that
// started them.
//
// # The defect
//
// mg-4518. A polecat starts background work — `nohup ... & ` from a tool-call
// shell that then exits — the work reparents to launchd, the polecat finishes
// its ticket, merges its branch, and is reaped. The compute keeps running.
// Measured instances: 38% CPU out of an onethird audit instrument's directory;
// 94% for 44 minutes after its owner's branch had already merged, writing into a
// scratchpad with no reader left; three simultaneous survivors at 40%, 40% and
// 39% aged from 44 minutes to 2h21m.
//
// It is not merely untidy. The box has 10 cores and the fleet has driven it to
// load 137. At that contention `TestGateWatchMeasuresARealSubtreesCPU` — a test
// that measures a real subtree's CPU, and so measures the contention — FAILED in
// the refinery gate, costing two unrelated branches a merge attempt each and
// sending a reader hunting a second code defect that did not exist. An orphan
// does not only waste CPU; it manufactures deterministic-looking failures in
// branches that have nothing to do with it.
//
// # It is a REPORTER. It does not kill.
//
// Nothing in this package signals anything. The rule below is a strong
// heuristic with named blind spots (see OwnerFromCwd), and a killer built on a
// heuristic destroys live work on its false positives — which here means a
// polecat's parallel search dying mid-computation. Killing stays a human
// decision until the rule has been right for a while. If that changes it is a
// different command, and it must carry its own argument.
//
// # What it keys on, and what it must never key on
//
//	cwd -> owning polecat -> registry liveness.   Orphan iff the owner is dead.
//
// NOT ppid. `ppid=1` is the signature of ANY polecat starting background work,
// not of a leak; the reasoning and the measurement are in OwnerFromCwd. A sweep
// keyed on it would have killed four live workers on 2026-08-07.
//
// # And not CPU alone either
//
// CPU is the second half of the predicate, and it is there to separate this
// defect from a different one that looks similar from a distance. A
// pogo-deploy.sh started 2026-08-08T02:00:05Z was still alive 31h39m later,
// blocked forever in an unbounded `git fetch` — correctly parented, reported by
// nothing, and consuming ~0% CPU. That is a stuck process, not detached compute,
// and it routes elsewhere. So the rate floor is a discriminator between two
// defect classes, not a severity filter.
//
// The rate is measured by differencing cumulative CPU time across a window, not
// read from `ps %cpu`. `%cpu` is a lifetime average and understated a live
// instance of this defect by about 3x; two `ps` reads of the same population
// disagreed by a factor of three within minutes. See internal/proctable.
package orphanwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/proctable"
)

const (
	// DefaultWindow is the sampling window for the CPU rate. Long enough that
	// the rate is a measurement rather than a rounding artifact on every
	// supported host, short enough that a detector run is not itself a wait.
	DefaultWindow = 2 * time.Second

	// DefaultFloor is the rate, in cores, at or above which a process counts as
	// doing compute. The measured instances of this defect sat between 0.38 and
	// 0.94 cores; the blocked-fetch class this must not collect sits at ~0.00.
	// 0.20 is comfortably between them and well clear of proctable's
	// quantisation.
	DefaultFloor = 0.20
)

// ErrNoLiveness is returned when the agent registry could not be reached.
//
// It is an ERROR and never a report, because the registry answer is the only
// thing standing between "orphan" and "somebody's running work". With it
// missing, every attributable process on the box has a dead-looking owner and a
// detector that shrugged and carried on would name all of them. Failing closed
// here is the whole safety margin.
var ErrNoLiveness = errors.New("orphanwatch: agent registry unreachable; cannot decide owner liveness")

// Orphan is one compute process whose owning polecat is gone.
type Orphan struct {
	PID int `json:"pid"`
	// PPID is REPORTED but not used in the verdict. It is here so a reader can
	// see for themselves that it carries no information — healthy fan-out shows
	// ppid=1 too — rather than having to take that on trust.
	PPID int `json:"ppid"`
	PGID int `json:"pgid"`
	// Owner is the polecat id read out of Cwd.
	Owner string `json:"owner"`
	Cwd   string `json:"cwd"`
	// Cores is CPU-seconds per wall second over the sampling window.
	Cores float64 `json:"cores"`
	// CPU is the cumulative CPU time the process has consumed since it started,
	// which is the closest thing available to "how much has this cost so far".
	CPU time.Duration `json:"cpu"`
}

// Disposition is the bucket one busy process landed in. The Report's counts are
// this value's histogram; the value itself is what lets a caller ask about a
// process it CONSTRUCTED rather than about the population as a whole.
//
// That distinction is why this exists (mg-db12). The constructive probe used to
// read `Reported == false` and conclude the detector had failed, when the report
// beside it said `cwd_unreadable=1` — the orphan had been seen and binned as an
// instrument limit. Aggregate counts cannot say WHICH process that was, so a
// transient lsof refusal under host load was indistinguishable from a detector
// that does not fire, and it failed a merge gate on an unrelated branch.
type Disposition string

const (
	// DispositionOrphan is a verdict: attributed to an owner the registry says
	// is gone.
	DispositionOrphan Disposition = "orphan"
	// DispositionLiveOwner is a verdict: attributed to an owner still running,
	// and therefore spared.
	DispositionLiveOwner Disposition = "live_owner"
	// DispositionUnattributable is a BLIND SPOT, not a verdict: the cwd was read
	// and carries no polecat marker.
	DispositionUnattributable Disposition = "unattributable"
	// DispositionCwdUnreadable is an INSTRUMENT LIMIT, not a verdict: the
	// working directory could not be read at all.
	DispositionCwdUnreadable Disposition = "cwd_unreadable"
)

// Report is one scan. Every busy process examined lands in exactly one bucket,
// so a reader can tell "nothing to report" from "nothing looked at".
type Report struct {
	// Source is the process-table reader and its CPU-time precision.
	Source string `json:"source"`
	// Window and Floor are the thresholds this run applied, reported because a
	// finding count is meaningless without them.
	Window time.Duration `json:"window"`
	Floor  float64       `json:"floor_cores"`
	// PolecatsRoot is the tree whose child directories name polecats.
	PolecatsRoot string `json:"polecats_root"`

	// Sampled is every process visible in the table at the second sample.
	Sampled int `json:"sampled"`
	// Busy is how many met the rate floor and so were candidates at all.
	Busy int `json:"busy"`
	// CwdUnreadable is how many busy processes would not yield a working
	// directory — another user's process, or one that exited during the scan.
	// An INSTRUMENT LIMIT, counted rather than judged.
	CwdUnreadable int `json:"cwd_unreadable"`
	// Unattributable is how many busy processes had a readable cwd that carries
	// no polecat marker. A BLIND SPOT, counted rather than judged: a worker that
	// chdir'd elsewhere looks exactly like this, and so does every unrelated
	// program on the machine.
	Unattributable int `json:"unattributable"`
	// LiveOwner is how many were attributed to a polecat that is still running,
	// and therefore spared. This is the positive control and the number that
	// matters most on a healthy box — it is the count of processes a
	// ppid-keyed sweep would have killed.
	LiveOwner int `json:"live_owner"`
	// Orphans are the findings, costliest first.
	Orphans []Orphan `json:"orphans,omitempty"`

	// Dispositions is the bucket each BUSY pid landed in, keyed by pid. It holds
	// exactly the same information as the four counters above, per process
	// rather than in aggregate — the counters are its histogram, and
	// TestDispositionsAreTheCountersPerProcess pins that they cannot drift.
	//
	// A pid ABSENT from this map was never a candidate: it was below the rate
	// floor, born inside the window, or is this process. That is a fifth state
	// and deliberately not a fifth bucket — "not busy" is a fact about the
	// process, whereas every bucket here is a fact about what the attribution
	// step could do with it.
	Dispositions map[int]Disposition `json:"dispositions,omitempty"`
}

// DispositionOf reports which bucket pid landed in. The false return means the
// pid was never a CANDIDATE at all — see Dispositions — which must not be
// flattened into any of the buckets.
func (r Report) DispositionOf(pid int) (Disposition, bool) {
	d, ok := r.Dispositions[pid]
	return d, ok
}

// TotalCores is the compute the reported orphans are consuming right now.
func (r Report) TotalCores() float64 {
	var sum float64
	for _, o := range r.Orphans {
		sum += o.Cores
	}
	return sum
}

// Options configures a scan. The two function fields are injection points so
// the constructive probe (see Probe) can stand a known population in front of
// the same code the live scan runs.
type Options struct {
	// PolecatsRoot is the directory whose immediate children are polecat
	// worktrees named after the agents that own them. Empty means DefaultRoot.
	PolecatsRoot string
	// Window is the CPU sampling window; zero means DefaultWindow.
	Window time.Duration
	// Floor is the rate in cores at or above which a process is a candidate;
	// zero means DefaultFloor.
	Floor float64
	// LiveOwners returns the set of polecat names the registry currently
	// considers running. An error from it aborts the scan with ErrNoLiveness —
	// see that variable for why this may not degrade.
	LiveOwners func() (map[string]bool, error)
	// Table reads the process table; nil means proctable.Read.
	Table func() ([]proctable.Row, error)
	// Cwds resolves working directories; nil means ReadCwds.
	Cwds func(pids []int) map[int]string
	// Source describes the precision of the CPU column; zero means
	// proctable.Current().
	Source proctable.Source
	// Sleep waits out the sampling window; nil means time.Sleep.
	Sleep func(time.Duration)
	// Now reads the clock; nil means time.Now.
	//
	// It is injectable because the rate is CPU-time-over-WALL-time, and a test
	// that stubs Sleep without also stubbing the clock divides a synthetic CPU
	// delta by a few microseconds of real elapsed time — producing rates in the
	// millions that pass any "is it over the floor" assertion while meaning
	// nothing. Stubbing both makes the arithmetic itself checkable.
	Now func() time.Time
}

// Scan samples the process table twice, keeps the processes doing real compute,
// attributes them by working directory, and reports the ones whose owning
// polecat is no longer running.
//
// It never signals a process.
func Scan(opts Options) (Report, error) {
	root := opts.PolecatsRoot
	if root == "" {
		root = DefaultRoot()
	}
	window := opts.Window
	if window <= 0 {
		window = DefaultWindow
	}
	floor := opts.Floor
	if floor <= 0 {
		floor = DefaultFloor
	}
	source := opts.Source
	if source.Name == "" {
		source = proctable.Current()
	}
	read := opts.Table
	if read == nil {
		read = proctable.Read
	}
	cwds := opts.Cwds
	if cwds == nil {
		cwds = ReadCwds
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	rep := Report{
		Source:       source.String(),
		Window:       window,
		Floor:        floor,
		PolecatsRoot: root,
	}
	if root == "" {
		return rep, errors.New("orphanwatch: no polecats root; nothing can be attributed")
	}
	if reason := source.CannotResolve(window); reason != "" {
		// A window this host cannot resolve produces zeroes, and a zero rate
		// reported as a measurement is how a CPU signal goes silently blind
		// (mg-79e3). Refuse instead.
		return rep, fmt.Errorf("orphanwatch: %s", reason)
	}
	if opts.LiveOwners == nil {
		return rep, ErrNoLiveness
	}
	roots := rootSpellings(root)

	first, err := read()
	if err != nil {
		return rep, fmt.Errorf("orphanwatch: first process-table sample: %w", err)
	}
	before := make(map[int]time.Duration, len(first))
	for _, row := range first {
		before[row.PID] = row.CPU
	}
	start := now()
	sleep(window)
	// The ACTUAL elapsed time, not the requested window: on a host loaded
	// enough to produce this defect a 2s sleep routinely returns late, and
	// dividing by the window we asked for rather than the one we got inflates
	// every rate in the report by exactly the overshoot.
	elapsed := now().Sub(start)
	if elapsed <= 0 {
		elapsed = window
	}

	second, err := read()
	if err != nil {
		return rep, fmt.Errorf("orphanwatch: second process-table sample: %w", err)
	}
	rep.Sampled = len(second)

	// Ask the registry AFTER the samples, so the liveness answer is as late as
	// possible relative to the population it judges. A polecat that died during
	// the window is better read as dead-and-its-work-survived than as alive.
	live, err := opts.LiveOwners()
	if err != nil {
		return rep, fmt.Errorf("%w: %v", ErrNoLiveness, err)
	}

	self := os.Getpid()
	type candidate struct {
		row   proctable.Row
		cores float64
	}
	var busy []candidate
	for _, row := range second {
		if row.PID == self {
			continue
		}
		prior, existed := before[row.PID]
		if !existed {
			// Born inside the window. It cannot yet have the sustained cost
			// this looks for, and it has no earlier reading to difference.
			continue
		}
		delta := row.CPU - prior
		if delta <= 0 {
			continue
		}
		cores := delta.Seconds() / elapsed.Seconds()
		if cores < floor {
			continue
		}
		busy = append(busy, candidate{row: row, cores: cores})
	}
	rep.Busy = len(busy)
	if len(busy) == 0 {
		return rep, nil
	}

	pids := make([]int, 0, len(busy))
	for _, c := range busy {
		pids = append(pids, c.row.PID)
	}
	dirs := cwds(pids)
	rep.Dispositions = make(map[int]Disposition, len(busy))

	for _, c := range busy {
		cwd, ok := dirs[c.row.PID]
		if !ok || cwd == "" {
			rep.CwdUnreadable++
			rep.Dispositions[c.row.PID] = DispositionCwdUnreadable
			continue
		}
		owner, ok := OwnerFromAnyRoot(roots, cwd)
		if !ok {
			rep.Unattributable++
			rep.Dispositions[c.row.PID] = DispositionUnattributable
			continue
		}
		if live[owner] {
			rep.LiveOwner++
			rep.Dispositions[c.row.PID] = DispositionLiveOwner
			continue
		}
		rep.Dispositions[c.row.PID] = DispositionOrphan
		rep.Orphans = append(rep.Orphans, Orphan{
			PID:   c.row.PID,
			PPID:  c.row.PPID,
			PGID:  c.row.PGID,
			Owner: owner,
			Cwd:   cwd,
			Cores: c.cores,
			CPU:   c.row.CPU,
		})
	}

	sort.Slice(rep.Orphans, func(i, j int) bool {
		if rep.Orphans[i].Cores != rep.Orphans[j].Cores {
			return rep.Orphans[i].Cores > rep.Orphans[j].Cores
		}
		return rep.Orphans[i].PID < rep.Orphans[j].PID
	})
	return rep, nil
}

// rootSpellings returns the configured polecats root and, when it differs, its
// symlink-resolved form. See OwnerFromAnyRoot for why both are needed.
func rootSpellings(root string) []string {
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		roots = append(roots, resolved)
	}
	return roots
}

// DefaultRoot is the directory polecat worktrees live under, delegated to
// gitgc.DefaultPolecatsDir so this detector and the worktree reaper cannot
// disagree about where polecats are.
//
// Resolving it independently would be a live hazard on this fleet rather than a
// tidiness point: an old shell integration exports POGO_HOME=$HOME, which the
// canonical resolver normalizes to $HOME/.pogo and a naive join would turn into
// $HOME/polecats — a directory that does not exist, attributing nothing and
// reporting a box full of orphans as clean.
func DefaultRoot() string {
	dir, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		return ""
	}
	return dir
}
