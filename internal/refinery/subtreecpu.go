package refinery

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// This file measures the OTHER half of gate liveness: whether the gate's
// process subtree is doing work.
//
// # Why output staleness alone is not enough
//
// mg-8595 added `last_output`, and mg-0c51 was filed after it was read as the
// answer to "is this gate alive". It is not. A real, healthy gate was observed
// at:
//
//	elapsed=9m0s   gate_output_lines=114  last_output=7m31s ago
//	elapsed=9m30s  gate_output_lines=114  last_output=8m1s ago
//	elapsed=10m0s  gate_output_lines=114  last_output=8m31s ago
//
// — output frozen for 85% of the run, `last_output` climbing monotonically. On
// those samples alone the honest reading is "this gate has stopped". The
// process tree said otherwise: a descendant was burning ~3.9 cores for the
// whole silent window. It was a long parallel computation between two print
// statements.
//
// So a rule of the form "warn when last_output exceeds N" would fire on every
// healthy run of that gate, and an alert that fires on healthy work trains its
// reader to ignore it — which is how the genuinely hung case gets missed. The
// distinguishing evidence is whether the subtree is consuming CPU, and that is
// not in the gate's output at all.
//
// # Why a RATE and not `ps -o %cpu`
//
// `%cpu` means different things on different systems: a decayed recent
// estimate on darwin, an average over the whole process lifetime on Linux. The
// second is useless here — a subtree that burned a core for nine minutes and
// then wedged still reports a high lifetime average. So this samples CUMULATIVE
// CPU time twice and divides the difference by the wall time between the
// samples. That is a measurement of work actually performed in the window, and
// it means the same thing everywhere.
//
// # What this can and cannot discriminate
//
//   - Busy subtree => the gate is computing. This is a POSITIVE proof of work
//     and it is why a silent gate can be declared healthy.
//   - Idle subtree => grounds for suspicion and a closer look, NEVER for
//     automatic intervention. A process blocked on I/O, on a lock, or sleeping
//     between phases consumes no CPU and is perfectly healthy. Killing a
//     healthy gate is destructive; waiting on a hung one costs time. The
//     asymmetry is deliberate.
//   - No sample => say so. An unmeasurable subtree must not be reported as an
//     idle one.

// subtreeIdleCores is the rate below which a subtree is called idle. A window
// of one heartbeat interval at 0.02 cores is ~0.6 CPU-seconds — comfortably
// above the noise of `ps` rounding and a stray signal handler, and far below
// anything that could be called work.
const subtreeIdleCores = 0.02

// procRow is one line of the process table.
type procRow struct {
	PID  int
	PPID int
	PGID int
	CPU  time.Duration // cumulative CPU time consumed by this process
}

// psTimeout bounds the process-table read. The gate this measures runs on a
// host that may be under heavy contention — that is precisely when a refinery
// gets looked at — and an unbounded exec would let the sampler hang. A hung
// sampler must degrade to "no measurement", which the record reports honestly,
// rather than stall anything.
const psTimeout = 10 * time.Second

// psSnapshot reads the process table. Replaced in tests.
var psSnapshot = func() ([]procRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	// -A is the POSIX spelling of "every process" and is accepted by both the
	// BSD ps on darwin and procps on Linux; the trailing "=" on each field
	// suppresses the header so the output is pure data.
	out, err := exec.CommandContext(ctx, "ps", "-Ao", "pid=,ppid=,pgid=,time=").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ps did not return within %s", psTimeout)
		}
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(string(out)), nil
}

// parsePS turns `ps -Ao pid=,ppid=,pgid=,time=` output into rows, skipping
// anything it cannot read rather than failing the whole sample: one malformed
// line must not cost the measurement.
func parsePS(out string) []procRow {
	var rows []procRow
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		pgid, err3 := strconv.Atoi(f[2])
		cpu, ok := parseCPUTime(f[3])
		if err1 != nil || err2 != nil || err3 != nil || !ok {
			continue
		}
		rows = append(rows, procRow{PID: pid, PPID: ppid, PGID: pgid, CPU: cpu})
	}
	return rows
}

// parseCPUTime reads the ps TIME column: [[DD-]HH:]MM:SS[.ff].
func parseCPUTime(s string) (time.Duration, bool) {
	days := 0
	if i := strings.Index(s, "-"); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days = d
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var hours, mins int
	secs := parts[len(parts)-1]
	if len(parts) == 3 {
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, false
		}
		hours = h
	}
	m, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		return 0, false
	}
	mins = m
	sec, err := strconv.ParseFloat(secs, 64)
	if err != nil {
		return 0, false
	}
	total := float64(days)*86400 + float64(hours)*3600 + float64(mins)*60 + sec
	if total < 0 {
		return 0, false
	}
	return time.Duration(total * float64(time.Second)), true
}

// subtreeSample is one reading of the CPU consumed by a gate's process
// subtree: cumulative CPU time keyed by pid, and when it was taken.
type subtreeSample struct {
	At  time.Time
	CPU map[int]time.Duration
}

// sampleSubtree reads the process table and returns the subtree rooted at
// rootPID.
//
// Membership is the UNION of two relations, because either one alone loses
// processes that matter:
//
//   - Same process group. runGate starts the gate with Setpgid, so every
//     descendant inherits the group. This is what keeps an orphaned
//     grandchild — whose ppid became 1 when its parent exited — inside the
//     subtree.
//   - Descendant by ppid. This covers a gate that called setsid or otherwise
//     left the group, and is the fallback when Setpgid did not take.
func sampleSubtree(rootPID int, now time.Time) (*subtreeSample, error) {
	if rootPID <= 0 {
		return nil, fmt.Errorf("no gate pid")
	}
	rows, err := psSnapshot()
	if err != nil {
		return nil, err
	}
	byPID := make(map[int]procRow, len(rows))
	children := make(map[int][]int, len(rows))
	for _, row := range rows {
		byPID[row.PID] = row
		children[row.PPID] = append(children[row.PPID], row.PID)
	}
	root, ok := byPID[rootPID]
	if !ok {
		// The root is gone. That is not a measurement failure — it is the
		// answer "the gate process no longer exists", reported as an empty
		// subtree so a caller can tell it apart from an unreadable table.
		return &subtreeSample{At: now, CPU: map[int]time.Duration{}}, nil
	}

	members := map[int]time.Duration{rootPID: root.CPU}
	// Only trust the process group when the root LEADS it. Setpgid makes the
	// gate its own group leader, so pgid == rootPID is the signal that the
	// group is the gate's. If Setpgid did not take, the root inherited pogod's
	// group — and sweeping that in would attribute the whole daemon, every
	// agent it spawned, and their CPU to this one gate.
	if root.PGID == rootPID {
		for _, row := range rows {
			if row.PGID == root.PGID {
				members[row.PID] = row.CPU
			}
		}
	}
	// Walk descendants breadth-first. Every member found so far seeds the
	// walk, not just the root: a group member that then called setsid has
	// children outside the group, and starting only from the root would miss
	// them because their parent is already marked seen. The visited set is the
	// members map itself, so a ppid cycle (which the kernel will not produce,
	// but a malformed table could) cannot loop forever.
	queue := make([]int, 0, len(members))
	for pid := range members {
		queue = append(queue, pid)
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if _, seen := members[child]; seen {
				continue
			}
			members[child] = byPID[child].CPU
			queue = append(queue, child)
		}
	}
	return &subtreeSample{At: now, CPU: members}, nil
}

// subtreeActivity is what two samples say about the work a subtree did
// between them.
type subtreeActivity struct {
	// Cores is CPU-seconds consumed per wall second — "how many cores' worth
	// of work happened", so 3.9 reads directly as "burning about four cores".
	Cores float64
	// Procs is how many processes were in the subtree at the later sample.
	Procs int
	// Churn is how many processes appeared or vanished between the samples.
	// It is counted because a gate that forks short-lived workers can perform
	// a great deal of work while every individual process is born and reaped
	// inside one window, leaving Cores near zero. Churn catches that case.
	Churn int
	// Window is the wall time the two samples span.
	Window time.Duration
}

// Busy reports whether the subtree did measurable work.
func (a subtreeActivity) Busy() bool {
	return a.Cores >= subtreeIdleCores || a.Churn > 0
}

// compareSubtree derives the activity between two samples. ok is false when
// the pair cannot support a rate — no window, or no earlier sample.
func compareSubtree(prev, cur *subtreeSample) (subtreeActivity, bool) {
	if prev == nil || cur == nil {
		return subtreeActivity{}, false
	}
	window := cur.At.Sub(prev.At)
	if window <= 0 {
		return subtreeActivity{}, false
	}
	var consumed time.Duration
	churn := 0
	for pid, cpu := range cur.CPU {
		before, existed := prev.CPU[pid]
		if !existed {
			churn++
			// A process born inside the window did all of its CPU inside the
			// window, so all of it counts.
			consumed += cpu
			continue
		}
		if d := cpu - before; d > 0 {
			consumed += d
		}
	}
	for pid := range prev.CPU {
		if _, still := cur.CPU[pid]; !still {
			churn++
		}
	}
	return subtreeActivity{
		Cores:  consumed.Seconds() / window.Seconds(),
		Procs:  len(cur.CPU),
		Churn:  churn,
		Window: window,
	}, true
}
