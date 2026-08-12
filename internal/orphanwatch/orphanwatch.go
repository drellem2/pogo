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
//
// # The floor is per OWNER, not per process (mg-c675)
//
// A polecat generating synthetic load orphaned 52 busy-loops, which held 8.7 of
// this host's 10 cores for 41 minutes, failed an unrelated branch's merge gate,
// and corrupted the load thresholds two open investigations had recorded. Fed
// that exact population, this detector reported a clean host — not one finding
// suppressed, but zero processes even examined.
//
// The arithmetic is the whole point. 8.7 cores shared by 52 contending
// processes is 0.167 cores each, under a floor of 0.20 that was calibrated on
// mg-4518's population of one to three orphans at 0.38 to 0.94 cores apiece. And
// it is not a matter of the constant being a little too high: processes
// contending for a fixed number of cores get capacity/N each, so a per-process
// floor goes BLINDER AS THE LEAK GETS WORSE, and the leak large enough to
// saturate the host is precisely the one it cannot report. A threshold with that
// sign on its error is not a threshold that can be retuned.
//
// So the reported unit is the OWNER. A dead polecat is reported when the
// processes it left behind TOGETHER meet Floor, which subdividing cannot get
// under. Floor keeps its name, its flag and its default, because "how much
// compute is a dead owner holding" is what it always meant to ask; only the
// population it is summed over changed. mg-4518's single 0.94-core orphan still
// trips it, unchanged, as its own test asserts.
//
// A second, much lower CandidateFloor now decides what gets ATTRIBUTED at all.
// It still separates this defect from the stuck-process class at ~0.00 cores
// described above, and it still bounds the cwd batch, but it no longer decides
// what gets REPORTED — which is the job it was quietly doing and was never the
// right instrument for.
//
// The residual blind spot, stated rather than papered over: a population escapes
// when its total clears Floor while every member sits under CandidateFloor,
// which needs more than Floor/CandidateFloor = 10 members ALL below 0.02 cores.
// Spinning processes only get that small when there are more than ~500 of them
// on a 10-core box. Duty-cycled work — awake 2% of the time — is that small at
// any count, and is deliberately not collected: at ~0.00 cores it is
// indistinguishable from the stuck-process class the floor exists to exclude.
// That is a trade this package is making knowingly, not a case it has missed.
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

	// DefaultFloor is the rate, in cores, at or above which a DEAD OWNER'S
	// SURVIVING PROCESSES — summed, see the package doc — count as compute worth
	// reporting. The measured instances of this defect sat between 0.38 and 0.94
	// cores for a single orphan and at 8.7 cores for a swarm of 52; the
	// blocked-fetch class this must not collect sits at ~0.00. 0.20 is
	// comfortably between them and well clear of proctable's quantisation.
	DefaultFloor = 0.20

	// DefaultCandidateFloor is the rate, in cores, at or above which a single
	// process is worth attributing to an owner at all. It is not a reporting
	// threshold and must not be read as one — see the package doc for what
	// happened when it was.
	//
	// It has two jobs, and both are satisfied far below DefaultFloor. It keeps
	// out the stuck-process class at ~0.00 cores, which is a different defect
	// and routes elsewhere. And it bounds the cwd batch, which on darwin is one
	// lsof invocation whose cost grows with the number of pids in it.
	//
	// 0.02 is four times proctable's coarsest supported quantum over the default
	// window (10ms of CPU resolved over 2s is 0.005 cores), so a process at this
	// rate is measured rather than rounded into existence.
	DefaultCandidateFloor = 0.02
)

// ErrNoLiveness is returned when the agent registry could not be reached.
//
// It is an ERROR and never a report, because the registry answer is the only
// thing standing between "orphan" and "somebody's running work". With it
// missing, every attributable process on the box has a dead-looking owner and a
// detector that shrugged and carried on would name all of them. Failing closed
// here is the whole safety margin.
var ErrNoLiveness = errors.New("orphanwatch: agent registry unreachable; cannot decide owner liveness")

// ErrCandidateFloorAboveFloor is returned when the per-process candidate floor
// is set at or above the per-owner reporting floor.
//
// It is REFUSED rather than clamped, and the first attempt at this clamped. A
// candidate floor at the reporting floor means no process is attributed unless
// it individually clears the reporting floor — which is the per-process rule of
// mg-c675 exactly, restored from the command line, and a clamp to Floor produces
// it rather than preventing it. There is no safe value to silently substitute:
// any choice either ignores what the operator asked for or reinstates the
// defect, so the run says it cannot be conducted instead of conducting a
// different one.
var ErrCandidateFloorAboveFloor = errors.New(
	"orphanwatch: candidate floor is at or above the reporting floor, which reinstates the " +
		"per-process rule (mg-c675): no process would be attributed unless it alone cleared the " +
		"floor, so a dead owner's swarm would go unreported however large it got")

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

// OwnerLoad is one dead polecat's surviving compute, summed. It is the unit the
// reporting decision is made on — see the package doc.
type OwnerLoad struct {
	Owner string `json:"owner"`
	// Procs is how many of that owner's processes are still running.
	Procs int `json:"procs"`
	// Cores is their summed rate over the sampling window: what this owner is
	// costing the host right now.
	Cores float64 `json:"cores"`
	// CPU is their summed cumulative CPU time: what it has cost so far.
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
	// DispositionBelowOwnerFloor is a verdict: attributed to an owner the
	// registry says is gone, but that owner's surviving processes TOGETHER do
	// not meet Floor. Spared as trivial rather than missed.
	//
	// It is a separate bucket from "not a candidate" on purpose. A process here
	// was attributed to a dead polecat and the detector decided about it; a
	// process absent from Dispositions was never looked at. Flattening the two
	// would put the only evidence that the owner floor is set too high into the
	// same silence as an idle box.
	DispositionBelowOwnerFloor Disposition = "below_owner_floor"
)

// Report is one scan. Every busy process examined lands in exactly one bucket,
// so a reader can tell "nothing to report" from "nothing looked at".
type Report struct {
	// Source is the process-table reader and its CPU-time precision.
	Source string `json:"source"`
	// Window, Floor and CandidateFloor are the thresholds this run applied,
	// reported because a finding count is meaningless without them. Floor is
	// summed over an OWNER's processes; CandidateFloor is the per-process rate
	// below which a process is not attributed at all.
	Window         time.Duration `json:"window"`
	Floor          float64       `json:"floor_cores"`
	CandidateFloor float64       `json:"candidate_floor_cores"`
	// PolecatsRoot is the tree whose child directories name polecats.
	PolecatsRoot string `json:"polecats_root"`

	// Sampled is every process visible in the table at the second sample.
	Sampled int `json:"sampled"`
	// Busy is how many met the CANDIDATE floor and so were attributed at all.
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
	// BelowOwnerFloor is how many were attributed to a DEAD owner whose
	// surviving processes together fall under Floor. A verdict, not a blind
	// spot: these were decided about and spared as trivial. A large count here
	// beside no findings is the shape of a floor set too high.
	BelowOwnerFloor int `json:"below_owner_floor"`
	// Orphans are the findings, costliest first.
	Orphans []Orphan `json:"orphans,omitempty"`
	// Owners is the same findings aggregated to the unit the verdict is
	// actually reached on, costliest first. One dead polecat holding 8.7 cores
	// across 52 processes is ONE thing that happened, and reading it off 52
	// nearly identical process lines is how its scale was missed for 41 minutes.
	Owners []OwnerLoad `json:"owners,omitempty"`

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
	// Floor is the rate in cores at or above which a dead owner's processes,
	// SUMMED, are reported; zero means DefaultFloor.
	Floor float64
	// CandidateFloor is the per-process rate in cores at or above which a
	// process is attributed to an owner at all; zero means
	// DefaultCandidateFloor. A value at or above Floor is REFUSED — see
	// ErrCandidateFloorAboveFloor.
	CandidateFloor float64
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
// attributes them by working directory, sums what each dead owner is holding,
// and reports the processes of every dead owner whose total meets Floor.
//
// The sum is the step that matters: see the package doc for the population that
// a per-process floor could not see, and for why it got harder to see the worse
// it got.
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
	candidateFloor := opts.CandidateFloor
	if candidateFloor <= 0 {
		candidateFloor = DefaultCandidateFloor
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
		Source:         source.String(),
		Window:         window,
		Floor:          floor,
		CandidateFloor: candidateFloor,
		PolecatsRoot:   root,
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
	if candidateFloor >= floor {
		return rep, fmt.Errorf("%w: candidate floor %.3f, reporting floor %.3f",
			ErrCandidateFloorAboveFloor, candidateFloor, floor)
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
		if cores < candidateFloor {
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

	// First pass: attribute. No process is convicted here, because the verdict
	// is reached on the owner's total and the total is not known until every
	// process has been attributed.
	var dead []Orphan
	loads := map[string]*OwnerLoad{}
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
		dead = append(dead, Orphan{
			PID:   c.row.PID,
			PPID:  c.row.PPID,
			PGID:  c.row.PGID,
			Owner: owner,
			Cwd:   cwd,
			Cores: c.cores,
			CPU:   c.row.CPU,
		})
		load := loads[owner]
		if load == nil {
			load = &OwnerLoad{Owner: owner}
			loads[owner] = load
		}
		load.Procs++
		load.Cores += c.cores
		load.CPU += c.row.CPU
	}

	// Second pass: convict by owner. Subdividing the same compute across more
	// processes changes no term of this comparison, which is the property the
	// per-process floor did not have.
	for _, o := range dead {
		if loads[o.Owner].Cores < floor {
			rep.BelowOwnerFloor++
			rep.Dispositions[o.PID] = DispositionBelowOwnerFloor
			continue
		}
		rep.Dispositions[o.PID] = DispositionOrphan
		rep.Orphans = append(rep.Orphans, o)
	}
	for _, load := range loads {
		if load.Cores >= floor {
			rep.Owners = append(rep.Owners, *load)
		}
	}

	sort.Slice(rep.Orphans, func(i, j int) bool {
		if rep.Orphans[i].Cores != rep.Orphans[j].Cores {
			return rep.Orphans[i].Cores > rep.Orphans[j].Cores
		}
		return rep.Orphans[i].PID < rep.Orphans[j].PID
	})
	sort.Slice(rep.Owners, func(i, j int) bool {
		if rep.Owners[i].Cores != rep.Owners[j].Cores {
			return rep.Owners[i].Cores > rep.Owners[j].Cores
		}
		return rep.Owners[i].Owner < rep.Owners[j].Owner
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
