package refinery

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"
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
	// OutputLines counts newline-terminated lines the gate has produced;
	// LastOutput is when it last produced any bytes at all. Zero and zero
	// mean the gate has said nothing since it started.
	OutputLines int       `json:"output_lines"`
	LastOutput  time.Time `json:"last_output,omitempty"`
	// TimeoutAt is when the gate will be killed for exceeding its timeout,
	// zero when no timeout is configured. Present so an operator deciding
	// whether to wait knows how long waiting can possibly last.
	TimeoutAt time.Time `json:"timeout_at,omitempty"`
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
		return fmt.Sprintf("DEAD: no heartbeat for %s, which is more than %d intervals of %s — "+
			"the process running this gate is gone, not slow. Waiting will not help.",
			roundDur(p.HeartbeatAge(now)), heartbeatStaleAfter, p.HeartbeatInterval)
	}
	// The runner is alive. Whether the gate under it is making progress is a
	// separate question, answered by the gate's own output.
	if p.OutputLines == 0 && p.LastOutput.IsZero() {
		return fmt.Sprintf("ALIVE, gate silent: runner heartbeat is %s old, but the gate has produced "+
			"no output in %s. The runner is not dead; whether the gate is working or stuck cannot be "+
			"told from here — a silent gate looks the same either way. %s",
			roundDur(p.HeartbeatAge(now)), elapsed, p.timeoutNote(now))
	}
	return fmt.Sprintf("ALIVE and working: runner heartbeat is %s old, gate has produced %s, "+
		"last %s ago, running %s. Slow, not hung — waiting is correct.",
		roundDur(p.HeartbeatAge(now)), plural(p.OutputLines, "line"), roundDur(now.Sub(p.LastOutput)), elapsed)
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

	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
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
	return w
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
		return &StepProgress{
			Step:              "quality-gates",
			StartTime:         now,
			Heartbeat:         now,
			HeartbeatInterval: w.interval.String(),
			OutputLines:       lines,
			LastOutput:        last,
		}
	}

	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	p := w.mr.Progress
	if p == nil {
		return nil
	}
	p.OutputLines = lines
	p.LastOutput = last
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
	return fmt.Sprintf("MR %s step=%s gate=%s alive elapsed=%s heartbeat=%d/%s gate_output_lines=%d last_output=%s",
		id, p.Step, gate, roundDur(p.Elapsed(now)), p.Beats, p.HeartbeatInterval, p.OutputLines, outputAge)
}

// finish stops the beat goroutine and seals the record with a final count.
func (w *gateWatch) finish() {
	if w == nil {
		return
	}
	close(w.stop)
	<-w.done
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
