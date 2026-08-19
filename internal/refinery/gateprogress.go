package refinery

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drellem2/pogo/internal/hostload"
	"github.com/drellem2/pogo/internal/proctable"
)

// gateHeartbeatInterval is the bounded interval on which a running quality
// gate emits an observable progress record: one log line, and one refresh of
// the MR's persisted Progress field.
//
// THIRTY SECONDS is the stated interval mg-8595's acceptance criterion asks
// for. While a gate runs, something observable changes at least this often —
// so "running for 30 minutes" and "last seen 30 minutes ago" stop looking
// identical from outside.
const gateHeartbeatInterval = 30 * time.Second

// heartbeatStaleAfter is how many missed intervals it takes before a
// heartbeat is read as "the runner is gone" rather than "the host is loaded".
// Three intervals (90s at the default) tolerates a scheduling hiccup or a
// slow state write without tolerating a dead process.
const heartbeatStaleAfter = 3

// StepProgress is the liveness record for a long-running pipeline step —
// today only quality-gates, the one step that can run for tens of minutes.
//
// # What each field can and cannot discriminate
//
// This distinction is the entire point of the record, so it is written down
// rather than left to the reader. mg-8595 was filed after an operator
// collected two observations — a silent log and a frozen worktree mtime —
// concluded a gate had hung, and was wrong. Both observations look identical
// whether the gate is slow or dead, so having two of them added confidence
// and no information.
//
//   - Heartbeat is written by the goroutine running the gate. A dead runner
//     CANNOT write it. So a fresh Heartbeat proves the runner is alive, and a
//     stale one proves it is not. This is the signal that was missing.
//   - OutputLines / LastOutput are written when the gate's own subprocess
//     produces bytes. A hung subprocess CANNOT produce them, while the
//     runner's heartbeat keeps beating. So the pair separates "the runner
//     died" from "the runner is fine and the gate it is waiting on is stuck".
//   - HeartbeatInterval is carried in the record so a reader can judge
//     staleness without consulting this source file. A timestamp with no
//     stated cadence is not enough to call anything stale.
//   - CPUCores measures whether the gate's process subtree is actually
//     computing (see subtreecpu.go). It is the field that rescues a healthy
//     gate whose OutputLines have been frozen for minutes — a real one was
//     observed silent for 8m31s while a descendant burned 3.9 cores. Output
//     staleness ALONE cannot tell that gate from a hung one, so treating
//     LastOutput as the liveness answer is a mistake this record must not
//     let a reader make (mg-0c51).
//
// Nothing here is derived from the workspace's *state* (file mtimes, lock
// files, worktree contents). Any such signal reproduces the original defect
// with extra steps: a long test suite reads files rather than writing them,
// so workspace state looks the same for a healthy slow gate and a dead one.
type StepProgress struct {
	// Step names the pipeline step being tracked, e.g. "quality-gates".
	Step string `json:"step"`
	// Gate is the command currently running, with its position in the
	// configured list (GateIndex of GateCount, both 1-based).
	Gate      string `json:"gate,omitempty"`
	GateIndex int    `json:"gate_index,omitempty"`
	GateCount int    `json:"gate_count,omitempty"`
	// StartTime is when this gate started. Elapsed time is derived from it.
	StartTime time.Time `json:"start_time"`
	// EndTime is set when the gate finishes. A non-zero EndTime means the
	// record is history and makes no claim about anything being alive.
	EndTime time.Time `json:"end_time,omitempty"`
	// Heartbeat is the last time the runner proved it was alive; Beats counts
	// how many times it has. Written only by the runner goroutine.
	Heartbeat time.Time `json:"heartbeat"`
	Beats     int       `json:"beats"`
	// HeartbeatInterval is the cadence Heartbeat is written on, as a duration
	// string. Present so staleness is judgeable from the record alone.
	HeartbeatInterval string `json:"heartbeat_interval"`
	// Contention summarises what the host was doing while this gate ran. It
	// is what separates "this change is slow" from "this host was full", a
	// distinction elapsed time alone cannot make: mg-1b8c measured identical
	// work taking 11.5s on a host with capacity and 90.3s on a saturated one.
	// Nil when sampling is unavailable or has not produced a sample yet.
	Contention *hostload.Summary `json:"contention,omitempty"`
	// OutputLines counts newline-terminated lines the gate has produced;
	// LastOutput is when it last produced any bytes at all. Zero and zero
	// mean the gate has said nothing since it started.
	OutputLines int       `json:"output_lines"`
	LastOutput  time.Time `json:"last_output,omitempty"`
	// TimeoutAt is when the gate will be killed for exceeding its timeout,
	// zero when no timeout is configured. Present so an operator deciding
	// whether to wait knows how long waiting can possibly last.
	TimeoutAt time.Time `json:"timeout_at,omitempty"`

	// GatePID is the pid of the shell the gate runs under, zero before it
	// starts. The subtree measured below is rooted here.
	GatePID int `json:"gate_pid,omitempty"`
	// CPUSampledAt is when the subtree measurement below was taken. ZERO means
	// there is no measurement — which is a different statement from an idle
	// subtree and must be reported as such. CPUUnavailable says why.
	CPUSampledAt time.Time `json:"cpu_sampled_at,omitempty"`
	// CPUCores is CPU-seconds consumed by the gate's process subtree per wall
	// second over CPUWindow: 3.9 means the subtree was burning about four
	// cores. It is a RATE measured between two samples, not `ps -o %cpu`,
	// which on Linux is a lifetime average and would keep reporting a subtree
	// as busy long after it wedged.
	CPUCores float64 `json:"cpu_cores,omitempty"`
	// CPUProcs is how many processes were in the subtree when it was sampled;
	// CPUChurn is how many appeared or vanished since the previous sample.
	// Churn is carried because a gate forking short-lived workers can do heavy
	// work with CPUCores near zero.
	CPUProcs int `json:"cpu_procs,omitempty"`
	CPUChurn int `json:"cpu_churn,omitempty"`
	// CPUWindow is the wall time the rate spans, as a duration string.
	CPUWindow string `json:"cpu_window,omitempty"`
	// CPUUnavailable states why no measurement exists, so "could not measure"
	// is never rendered as "measured, and idle".
	CPUUnavailable string `json:"cpu_unavailable,omitempty"`
	// CPUEverFound is the POSITIVE CONTROL for the measurement above: true
	// once the sampler has located GatePID in the process table at least once
	// since this gate started. It is what licenses the sampler to report an
	// empty subtree as "the gate process is gone" — without it, an empty
	// subtree is equally the answer of a check that cannot see this process
	// at all (wrong pid, a table the daemon may not read, a pid namespace it
	// is not in), and reporting that as "GONE" invents a fact.
	//
	// mg-48d8: a process check was proposed, shipped nowhere, and reported a
	// healthy gate as absent — because nothing ever established that it could
	// find a gate that existed. A "not found" is only evidence when the
	// finder has been shown to find.
	CPUEverFound bool `json:"cpu_ever_found,omitempty"`
	// CPUSource names the process-table source the numbers came from and the
	// precision of its CPU column. Recorded because whether this measurement
	// works at all is a property of the host: the same gate reads 1.00 cores
	// where the column is in hundredths and 0.00 where it is in whole seconds,
	// and a record that does not name its instrument cannot be told apart from
	// one taken with a broken one (mg-79e3).
	CPUSource string `json:"cpu_source,omitempty"`
}

// SubtreeState is what the CPU measurement says about the gate's processes.
type SubtreeState int

const (
	// SubtreeUnknown means no usable measurement — the sampler failed, or not
	// enough samples have been taken yet to form a rate. It is NOT "idle".
	SubtreeUnknown SubtreeState = iota
	// SubtreeBusy means the subtree performed measurable work. This is a
	// positive proof of computation and is what lets a SILENT gate be called
	// healthy.
	SubtreeBusy
	// SubtreeIdle means the subtree performed no measurable work. It is
	// grounds for a closer look, never for automatic intervention: a process
	// blocked on I/O or a lock is idle and healthy.
	SubtreeIdle
	// SubtreeGone means the gate's process no longer exists — and, crucially,
	// that the sampler HAD found it before it vanished. Only that pairing
	// makes an empty subtree evidence of an exit.
	SubtreeGone
	// SubtreeNeverFound means the sampler has looked and found nothing, and
	// has never found anything since this gate started. That is a statement
	// about the INSTRUMENT, not about the gate: a check with no positive
	// control cannot report an absence. It is kept apart from SubtreeGone
	// because the two lead to opposite actions — "the runner is supervising
	// nothing, look now" versus "this reading means nothing, use another
	// signal".
	SubtreeNeverFound
)

// Subtree classifies the recorded CPU measurement.
func (p *StepProgress) Subtree() SubtreeState {
	if p == nil || p.CPUSampledAt.IsZero() {
		return SubtreeUnknown
	}
	if p.CPUProcs == 0 {
		if !p.CPUEverFound {
			return SubtreeNeverFound
		}
		return SubtreeGone
	}
	if p.CPUCores >= subtreeIdleCores || p.CPUChurn > 0 {
		return SubtreeBusy
	}
	return SubtreeIdle
}

// CPUSummary renders the subtree measurement as one short phrase, including
// the case where there is none.
func (p *StepProgress) CPUSummary() string {
	if p == nil {
		return "no record"
	}
	switch p.Subtree() {
	case SubtreeUnknown:
		reason := p.CPUUnavailable
		if reason == "" {
			reason = "not sampled yet"
		}
		return "UNKNOWN (" + reason + ")"
	case SubtreeGone:
		return "process gone (it was found earlier, so this absence is a real exit)"
	case SubtreeNeverFound:
		return fmt.Sprintf("NEVER LOCATED (pid %d has not once appeared in the process table since this "+
			"gate started, so this check has no positive control here and its absence proves nothing)", p.GatePID)
	case SubtreeBusy:
		s := fmt.Sprintf("%.1f cores busy, %s over %s", p.CPUCores, plural(p.CPUProcs, "proc"), p.CPUWindow)
		if p.CPUChurn > 0 {
			s += fmt.Sprintf(", %s churned", plural(p.CPUChurn, "proc"))
		}
		return s
	default:
		return fmt.Sprintf("idle (%.2f cores, %s, over %s)", p.CPUCores, plural(p.CPUProcs, "proc"), p.CPUWindow)
	}
}

// OutputAge returns how long the gate has been silent and whether it has ever
// spoken.
func (p *StepProgress) OutputAge(now time.Time) (time.Duration, bool) {
	if p == nil || p.LastOutput.IsZero() {
		return 0, false
	}
	return now.Sub(p.LastOutput), true
}

// minOutputFreshWindow floors the freshness window below. Silence shorter than
// this carries no information about anything — every gate is silent between two
// consecutive lines — so treating it as evidence would be noise. It matters
// because the window is otherwise derived from the heartbeat cadence, which
// tests compress to milliseconds.
const minOutputFreshWindow = 5 * time.Second

// OutputFresh reports whether the gate produced bytes recently enough that its
// own output proves it is working, without consulting the process table.
//
// The window is the one used for heartbeat staleness, floored at
// minOutputFreshWindow. There is deliberately no ALERT attached to falling
// outside it: a real gate was observed silent for 8m31s of a 10m run while
// burning four cores, so "silent for longer than N" fires on healthy work and
// an alert that fires on healthy work trains its reader to ignore it. Falling
// outside this window only means the answer has to come from somewhere else.
func (p *StepProgress) OutputFresh(now time.Time) bool {
	age, spoke := p.OutputAge(now)
	if !spoke {
		return false
	}
	window := p.staleWindow()
	if window < minOutputFreshWindow {
		window = minOutputFreshWindow
	}
	return age <= window
}

// Elapsed returns how long the gate has been running, or how long it ran if
// it has finished.
func (p *StepProgress) Elapsed(now time.Time) time.Duration {
	if p == nil || p.StartTime.IsZero() {
		return 0
	}
	end := now
	if !p.EndTime.IsZero() {
		end = p.EndTime
	}
	return end.Sub(p.StartTime)
}

// HeartbeatAge returns how long it has been since the runner last proved it
// was alive.
func (p *StepProgress) HeartbeatAge(now time.Time) time.Duration {
	if p == nil || p.Heartbeat.IsZero() {
		return 0
	}
	return now.Sub(p.Heartbeat)
}

// staleWindow returns the age past which a heartbeat is read as a dead
// runner. Falls back to the package default when the record carries no
// interval (an older state file).
func (p *StepProgress) staleWindow() time.Duration {
	interval := gateHeartbeatInterval
	if p != nil && p.HeartbeatInterval != "" {
		if d, err := time.ParseDuration(p.HeartbeatInterval); err == nil && d > 0 {
			interval = d
		}
	}
	return heartbeatStaleAfter * interval
}

// RunnerAlive reports whether the heartbeat is fresh enough to prove the
// process running this gate still exists. False for a finished record: it
// makes no liveness claim either way.
func (p *StepProgress) RunnerAlive(now time.Time) bool {
	if p == nil || !p.EndTime.IsZero() || p.Heartbeat.IsZero() {
		return false
	}
	return p.HeartbeatAge(now) <= p.staleWindow()
}

// Diagnosis renders the one thing an operator actually needs from this
// record: whether waiting is the right move. It is deliberately explicit
// about the case it cannot resolve, because the failure this ticket
// documents was an operator filling such a gap with a guess.
func (p *StepProgress) Diagnosis(now time.Time) string {
	if p == nil {
		return "no progress record"
	}
	if !p.EndTime.IsZero() {
		return fmt.Sprintf("finished after %s — this record is history and claims nothing about liveness",
			roundDur(p.Elapsed(now)))
	}
	if p.Heartbeat.IsZero() {
		return fmt.Sprintf("no heartbeat recorded yet (interval %s) — too early to read", p.HeartbeatInterval)
	}
	elapsed := roundDur(p.Elapsed(now))
	if !p.RunnerAlive(now) {
		return fmt.Sprintf("RUNNER DEAD: no heartbeat for %s, which is more than %d intervals of %s — "+
			"the process running this gate is gone, not slow. Waiting will not help.",
			roundDur(p.HeartbeatAge(now)), heartbeatStaleAfter, p.HeartbeatInterval)
	}
	// The runner is alive. Whether the gate under it is making progress is a
	// separate question, and it takes TWO signals to answer: the gate's own
	// output, and — when that has gone quiet — whether its processes are
	// computing. Either one alone is confidently wrong in a case that happens
	// routinely (see subtreecpu.go).
	if p.OutputFresh(now) {
		// The GATE's own evidence leads, and the runner's heartbeat is named
		// as the runner's and parenthesised. mg-48d8: this sentence used to
		// open with the heartbeat, which made a supervisor's liveness read as
		// the supervised process's progress — it printed "ALIVE and working"
		// over a gate whose last output was 26 minutes old, and would have
		// printed the same over a deadlock.
		//
		// The contention note matters here as much as in the silent cases, and
		// for a different reason: this is the shape of the run that prompted
		// mg-1b8c. A gate talking steadily and healthily, on track to blow a
		// timeout, purely because it is sharing the host. Nothing about
		// "ALIVE and working" would tell a reader that.
		return fmt.Sprintf("GATE ALIVE and working: the gate itself has produced %s, last %s ago, "+
			"running %s — that is the gate's own output, which only a running gate can produce. "+
			"(RUNNER: supervisor heartbeat %s old.) Slow, not hung — waiting is correct.%s",
			plural(p.OutputLines, "line"), roundDur(now.Sub(p.LastOutput)), elapsed,
			roundDur(p.HeartbeatAge(now)), p.contentionNote())
	}

	// Silent. Describe the silence precisely, then let the process table
	// decide what it means.
	var silence string
	if age, spoke := p.OutputAge(now); spoke {
		silence = fmt.Sprintf("the gate has produced %s but has been silent for %s of its %s",
			plural(p.OutputLines, "line"), roundDur(age), elapsed)
	} else {
		silence = fmt.Sprintf("the gate has produced no output at all in %s", elapsed)
	}
	// The heartbeat is labelled with the LAYER it belongs to every time it is
	// quoted, because in every branch below it is the one fact that is NOT
	// about the gate. A reader who takes it for the gate's own liveness has
	// been told the opposite of what was measured (mg-48d8).
	beat := fmt.Sprintf("the RUNNER (the pogod-side supervisor, not the gate) heartbeat is %s old, "+
		"which proves the supervisor is alive and says nothing about the gate under it",
		roundDur(p.HeartbeatAge(now)))

	switch p.Subtree() {
	case SubtreeBusy:
		return fmt.Sprintf("GATE ALIVE and computing: %s, and %s — but its process subtree is %s. "+
			"Silence here is a gate that logs at boundaries, not a stall. Waiting is correct.%s",
			beat, silence, p.CPUSummary(), p.contentionNote())
	case SubtreeNeverFound:
		// Deliberately NOT "gone". The sampler failing to find the gate is a
		// fact about the sampler until it has found one, and this branch is
		// the whole reason SubtreeNeverFound exists as a state.
		return fmt.Sprintf("GATE UNPROVEN: %s; %s; and the subtree check has NEVER located the gate "+
			"process (pid %d) in the process table — not once since this gate started, so it has no "+
			"positive control here. That is a broken instrument, not an absent gate: a check that has "+
			"never found what it looks for cannot report an absence. Read the gate's log or ps the pid "+
			"by hand; do not conclude the runner is supervising nothing from this line. %s",
			beat, silence, p.GatePID, p.timeoutNote(now))
	case SubtreeIdle:
		return fmt.Sprintf("SUSPECT: %s, %s, AND its process subtree is %s. "+
			"Silent and idle together is the shape of a stall — but it is not proof: a process "+
			"blocked on I/O, on a lock, or sleeping between phases is idle and healthy. "+
			"Look closer (ps the subtree, check the gate's log) before intervening; killing a "+
			"healthy gate is destructive, waiting on a hung one only costs time.%s %s",
			beat, silence, p.CPUSummary(), p.contentionNote(), p.timeoutNote(now))
	case SubtreeGone:
		return fmt.Sprintf("SUSPECT: %s, %s, and the gate process (pid %d) is GONE from the process "+
			"table — the sampler found it earlier in this run and no longer does, which is what makes "+
			"this absence a real exit rather than a blind check. The runner is still beating, so this "+
			"should resolve on its own within a beat or two; if it does not, the runner is waiting on "+
			"something that will not return. %s",
			beat, silence, p.GatePID, p.timeoutNote(now))
	default:
		return fmt.Sprintf("GATE UNDETERMINED: %s, %s, and its process subtree could not be "+
			"measured (%s) — so whether the gate is working or stuck cannot be told from here. "+
			"Output staleness ALONE does not settle it: a healthy gate was observed silent for "+
			"8m31s while burning four cores, so a long silence is not evidence of a stall.%s %s",
			beat, silence, p.CPUSummary(), p.contentionNote(), p.timeoutNote(now))
	}
}

// contentionNote appends what the HOST was doing, and only when the host was
// full for most of the run.
//
// # How this differs from the subtree measurement next to it
//
// CPUCores (mg-0c51) asks "is this gate computing?", rooted at the gate's own
// pid. This asks "was there any CPU to compute WITH?", rooted at the fleet.
// They answer different questions and the pair is worth more than either:
//
//   - Against SubtreeBusy, contention explains a long elapsed time without
//     implicating the change.
//   - Against SubtreeIdle it matters most, and is the reason this clause is
//     attached there. A saturated host starves a runnable process, and a
//     starved process consumes almost no CPU — so it measures as IDLE while
//     being perfectly healthy. Reading "silent and idle" as a stall is exactly
//     the wrong call on a full host, and without this line nothing in the
//     verdict would say so.
//
// Not attached to SubtreeGone: a missing process is not something a busy host
// explains.
//
// The silence in the ordinary case is deliberate. A note on every verdict
// trains a reader to skip it, and the number is only load-bearing when it
// changes the reading. When it is absent, elapsed time means what it always
// meant.
func (p *StepProgress) contentionNote() string {
	if p == nil || p.Contention == nil || !p.Contention.Contended() {
		return ""
	}
	return " HOST CONTENTION: " + p.Contention.Report() +
		" Elapsed time here is partly the host, so it is weaker evidence about this change than usual," +
		" and a low subtree CPU reading may be this gate being starved rather than stalled."
}

// timeoutNote states how long the unresolvable case can last, so the answer
// to "how long do I wait?" is a bound rather than a guess.
func (p *StepProgress) timeoutNote(now time.Time) string {
	if p.TimeoutAt.IsZero() {
		return "No gate timeout is configured, so this will not resolve on its own " +
			"(set [gates] timeout in .pogo/refinery.toml)."
	}
	if remaining := p.TimeoutAt.Sub(now); remaining > 0 {
		return fmt.Sprintf("The gate timeout kills it in %s if it never finishes.", roundDur(remaining))
	}
	return "The gate timeout has passed; the kill is in flight."
}

// roundDur trims noise so durations read as durations. Precision follows
// magnitude: whole seconds for the minutes-long values this code actually
// reports on, milliseconds below a second so a short duration does not print
// as a misleading "0s".
func roundDur(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(time.Second)
}

// plural renders "1 line" / "2 lines" for counts embedded in operator-facing
// messages.
func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// gateWatch is the running half of the progress signal. It owns a goroutine
// that beats on a fixed interval for as long as one gate runs, and counts the
// bytes the gate emits.
//
// The beat is what a dead runner cannot fake, so the goroutine is
// deliberately trivial: a ticker, a timestamp, a log line, a state write. It
// must not depend on the gate subprocess making progress, or it would go
// quiet in exactly the case it exists to report on.
type gateWatch struct {
	r  *Refinery
	mr *MergeRequest

	// lines and lastOutput are written from the exec output writer and read
	// from the beat goroutine, so they are atomic rather than mu-guarded:
	// the output path is hot (every write from the gate) and must not
	// contend on the refinery's main lock.
	lines      atomic.Int64
	lastOutput atomic.Int64 // UnixNano; 0 means the gate has emitted nothing

	// pid is the gate shell's process id, published by runGate the moment it
	// starts. Atomic because the beat goroutine reads it while the exec path
	// writes it.
	pid atomic.Int64

	// prevSample and cpu belong to the SAMPLER goroutine and are read by
	// snapshot, so they are mutex-guarded. prevSample is the previous
	// process-table reading (a rate needs two); cpu is the latest derived
	// measurement, folded into the persisted record by snapshot.
	cpuMu      sync.Mutex
	prevSample *subtreeSample
	cpu        cpuReading

	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	// cpuDone closes when the sampler goroutine has stopped. The sampler runs
	// SEPARATELY from the beat because reading the process table shells out to
	// `ps`, and a `ps` that is slow — which is most likely exactly when a host
	// is loaded enough for someone to be inspecting the refinery — must never
	// delay the heartbeat. The heartbeat is the proof that the runner is
	// alive; a late one is read as a dead runner, so it must not depend on
	// anything outside this process.
	cpuDone chan struct{}

	// load samples the HOST while the gate runs; tracker accumulates them.
	// Distinct from the subtree sampler above and asking a different question:
	// that one measures whether this gate is computing, this one measures
	// whether there was CPU available to compute with. On its own goroutine
	// for the same reason cpuDone's is, and the reason is worth restating —
	// the beat is the one thing a dead runner cannot fake, so it must not be
	// made to wait on an exec of `ps`. A wedged or slow sampler costs the
	// contention record and nothing else.
	load     hostload.Sampler
	tracker  *hostload.Tracker
	loadStop chan struct{}
	loadDone chan struct{}
}

// cpuReading is the beat goroutine's latest verdict on the gate's subtree.
// Unavailable is set when there is no measurement, and is deliberately a
// REASON rather than a bool: "could not measure" and "measured, idle" lead to
// opposite actions and must never collapse into one another.
type cpuReading struct {
	At          time.Time
	Activity    subtreeActivity
	Unavailable string
}

// gateHeartbeat returns the heartbeat interval in force, honouring a test
// override.
func (r *Refinery) gateHeartbeat() time.Duration {
	if r != nil && r.heartbeatInterval > 0 {
		return r.heartbeatInterval
	}
	return gateHeartbeatInterval
}

// startGateWatch installs a fresh progress record on the MR and starts
// beating. The returned watch must be finished with finish().
//
// mr may be nil (unit tests that exercise a gate without a merge request);
// the watch then only counts output and logs, which keeps runGate's shape
// identical in both paths.
func startGateWatch(r *Refinery, mr *MergeRequest, step, gate string, index, count int, deadline time.Time) *gateWatch {
	interval := r.gateHeartbeat()
	w := &gateWatch{
		r:        r,
		mr:       mr,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		cpuDone:  make(chan struct{}),
		load:     r.hostLoadSampler(),
		tracker:  &hostload.Tracker{},
		loadStop: make(chan struct{}),
		loadDone: make(chan struct{}),
	}
	now := time.Now()
	if r != nil && mr != nil {
		r.mu.Lock()
		mr.Progress = &StepProgress{
			Step:              step,
			Gate:              gate,
			GateIndex:         index,
			GateCount:         count,
			StartTime:         now,
			Heartbeat:         now,
			Beats:             0,
			HeartbeatInterval: interval.String(),
			TimeoutAt:         deadline,
		}
		r.saveStateLocked()
		r.mu.Unlock()
	}
	go w.run()
	go w.runCPU()
	go w.runLoad()
	return w
}

// hostLoadSampler returns the sampler in force, or nil when sampling is off.
func (r *Refinery) hostLoadSampler() hostload.Sampler {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadSampler
}

// setPID publishes the gate shell's process id. Until it is set there is no
// subtree to measure, and the record says so rather than reporting idle.
func (w *gateWatch) setPID(pid int) {
	if w == nil {
		return
	}
	w.pid.Store(int64(pid))
}

// runCPU samples the gate's process subtree until stopped. It is its own
// goroutine so that a slow `ps` costs a CPU number and never a heartbeat.
func (w *gateWatch) runCPU() {
	defer close(w.cpuDone)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case now := <-t.C:
			w.sampleCPU(now)
		}
	}
}

// sampleCPU takes one process-table reading and folds it against the previous
// one, publishing the result for snapshot to pick up.
func (w *gateWatch) sampleCPU(now time.Time) {
	pid := int(w.pid.Load())
	if pid <= 0 {
		w.publishCPU(nil, cpuReading{Unavailable: "gate process not started yet"})
		return
	}
	w.cpuMu.Lock()
	prev := w.prevSample
	w.cpuMu.Unlock()

	// The `ps` exec happens OUTSIDE the lock: it is the slow part, and holding
	// a lock across it would put the delay back on whatever reads the record.
	cur, err := sampleSubtree(pid, now)
	if err != nil {
		w.publishCPU(nil, cpuReading{Unavailable: "reading the process table failed: " + err.Error()})
		return
	}
	activity, ok := compareSubtree(prev, cur)
	if !ok {
		// One sample cannot make a rate. Saying "first sample of two" is the
		// honest report; saying "idle" would be a fabrication.
		w.publishCPU(cur, cpuReading{Unavailable: fmt.Sprintf("first sample of two (a rate needs two, %s apart)", w.interval)})
		return
	}
	// A window shorter than the host's CPU-time resolution can support does
	// not yield a small number — it yields zero, for a spinning subtree and a
	// sleeping one alike. Publishing that zero would make the signal report
	// "idle" on every gate on such a host, which is the one answer it must
	// never invent. Keep cur as the baseline so a later, wider window can
	// still produce a rate.
	if reason := procSource.CannotResolve(activity.Window); reason != "" {
		w.publishCPU(cur, cpuReading{Unavailable: reason})
		return
	}
	w.publishCPU(cur, cpuReading{At: now, Activity: activity})
}

// publishCPU stores the sampler's latest reading and the sample it was derived
// from. A nil sample clears the baseline, so the next reading reports "first
// sample of two" rather than diffing across a gap of unknown width.
func (w *gateWatch) publishCPU(sample *subtreeSample, reading cpuReading) {
	w.cpuMu.Lock()
	defer w.cpuMu.Unlock()
	w.prevSample = sample
	w.cpu = reading
}

// sawOutput records that the gate produced bytes. Called on every write from
// the gate, so it does no locking and no I/O.
func (w *gateWatch) sawOutput(newlines int) {
	if w == nil {
		return
	}
	if newlines > 0 {
		w.lines.Add(int64(newlines))
	}
	w.lastOutput.Store(time.Now().UnixNano())
}

// run beats until stopped.
func (w *gateWatch) run() {
	defer close(w.done)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.beat()
		}
	}
}

// runLoad samples the host for as long as the gate runs and folds each sample
// into the MR's contention record.
//
// It takes the first sample immediately rather than after one interval: a gate
// that dies early still leaves an answer to "what was the host doing", and
// with a 30-second beat the alternative is a whole interval of nothing.
func (w *gateWatch) runLoad() {
	defer close(w.loadDone)
	if w.load == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-w.loadStop
		cancel()
	}()

	// Sample no faster than the heartbeat, and never faster than the window a
	// sample needs to observe. The heartbeat is deliberately short in tests
	// (milliseconds); without the floor those runs would exec `ps` in a tight
	// loop, which is both slow and a way of measuring the observer.
	every := w.interval
	if every < hostload.DefaultWindow {
		every = hostload.DefaultWindow
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		w.sampleLoad(ctx)
		select {
		case <-w.loadStop:
			return
		case <-t.C:
		}
	}
}

// sampleLoad takes one host sample and records it. Failures are dropped: a
// missing contention record makes a gate no worse off than it was before this
// existed, and failing a merge over an observability call would be a strictly
// worse trade.
func (w *gateWatch) sampleLoad(ctx context.Context) {
	s, err := w.load.Read(ctx)
	if err != nil {
		return
	}
	w.tracker.Add(s)
	if w.r == nil || w.mr == nil {
		return
	}
	sum := w.tracker.Summary()
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if w.mr.Progress == nil {
		return
	}
	w.mr.Progress.Contention = &sum
	w.r.saveStateLocked()
}

// contention returns what the host has been doing for this gate's lifetime.
func (w *gateWatch) contention() hostload.Summary {
	if w == nil {
		return hostload.Summary{}
	}
	return w.tracker.Summary()
}

// beat records one proof of life: refresh the persisted record and log a
// line. Both are emitted from the runner, so both stop the instant it dies.
func (w *gateWatch) beat() {
	now := time.Now()
	snap := w.snapshot(now, false)
	if snap == nil {
		return
	}
	log.Printf("refinery: %s", w.line(snap, now))
}

// snapshot folds the atomic counters into the MR's progress record under the
// refinery lock and returns a copy for logging. Returns a synthetic record
// when there is no MR to write to, so logging works in both paths.
func (w *gateWatch) snapshot(now time.Time, final bool) *StepProgress {
	lines := int(w.lines.Load())
	var last time.Time
	if nanos := w.lastOutput.Load(); nanos != 0 {
		last = time.Unix(0, nanos)
	}

	if w.r == nil || w.mr == nil {
		p := &StepProgress{
			Step:              "quality-gates",
			StartTime:         now,
			Heartbeat:         now,
			HeartbeatInterval: w.interval.String(),
			OutputLines:       lines,
			LastOutput:        last,
		}
		w.applyCPU(p)
		return p
	}

	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	p := w.mr.Progress
	if p == nil {
		return nil
	}
	p.OutputLines = lines
	p.LastOutput = last
	w.applyCPU(p)
	if final {
		p.EndTime = now
	} else {
		p.Heartbeat = now
		p.Beats++
	}
	w.r.saveStateLocked()
	cp := *p
	return &cp
}

// applyCPU copies the beat goroutine's latest subtree measurement onto the
// record. Called with the refinery lock held (or on a detached record).
func (w *gateWatch) applyCPU(p *StepProgress) {
	p.GatePID = int(w.pid.Load())
	p.CPUSource = procSource.Name
	w.cpuMu.Lock()
	c := w.cpu
	w.cpuMu.Unlock()
	if c.At.IsZero() {
		// No measurement. Clear the numbers along with the timestamp so a
		// stale rate can never be read as current, and carry the reason.
		p.CPUSampledAt = time.Time{}
		p.CPUCores, p.CPUProcs, p.CPUChurn, p.CPUWindow = 0, 0, 0, ""
		p.CPUUnavailable = c.Unavailable
		return
	}
	p.CPUSampledAt = c.At
	p.CPUCores = c.Activity.Cores
	p.CPUProcs = c.Activity.Procs
	p.CPUChurn = c.Activity.Churn
	p.CPUWindow = roundDur(c.Activity.Window).String()
	p.CPUUnavailable = ""
}

// line renders the heartbeat log line. It reports the runner's liveness and
// the gate's output separately — collapsing them into one "still running"
// would rebuild the ambiguity this replaces.
func (w *gateWatch) line(p *StepProgress, now time.Time) string {
	id := "-"
	if w.mr != nil {
		id = w.mr.ID
	}
	gate := p.Gate
	if p.GateCount > 1 {
		gate = fmt.Sprintf("%s (%d/%d)", gate, p.GateIndex, p.GateCount)
	}
	outputAge := "never"
	if !p.LastOutput.IsZero() {
		outputAge = roundDur(now.Sub(p.LastOutput)).String() + " ago"
	}
	return fmt.Sprintf("MR %s step=%s gate=%s alive elapsed=%s heartbeat=%d/%s gate_output_lines=%d last_output=%s gate_cpu=%s",
		id, p.Step, gate, roundDur(p.Elapsed(now)), p.Beats, p.HeartbeatInterval, p.OutputLines, outputAge, p.CPUSummary())
}

// finish stops the beat goroutine and seals the record with a final count.
func (w *gateWatch) finish() {
	if w == nil {
		return
	}
	close(w.stop)
	<-w.done
	// Both samplers are joined — but with a bound, because either may be inside
	// a `ps` that is not returning. Trading a hung gate for a hung runner would
	// take the heartbeat down with it, and the heartbeat is the thing this
	// whole record exists to provide.
	select {
	case <-w.cpuDone:
	case <-time.After(proctable.ReadTimeout + time.Second):
	}
	close(w.loadStop)
	select {
	case <-w.loadDone:
	case <-time.After(proctable.ReadTimeout + time.Second):
	}
	w.snapshot(time.Now(), true)
}

// outputLines reports how many lines the gate has produced. Used to attach
// evidence to a timeout kill, so the kill is reported with what was observed
// rather than as a bare deadline.
func (w *gateWatch) outputLines() int {
	if w == nil {
		return 0
	}
	return int(w.lines.Load())
}

// lastOutputAge reports how long the gate has been silent, and whether it has
// ever spoken.
func (w *gateWatch) lastOutputAge(now time.Time) (time.Duration, bool) {
	if w == nil {
		return 0, false
	}
	nanos := w.lastOutput.Load()
	if nanos == 0 {
		return 0, false
	}
	return now.Sub(time.Unix(0, nanos)), true
}
