package refinery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/drellem2/pogo/internal/hostload"
)

// defaultGateTimeout bounds how long a single quality gate may run before it
// is killed and the MR fails.
//
// The number is a judgement, and the ticket that asked for it (mg-8595) is
// right that any such number is a guess about how slow is too slow. What
// makes the guess acceptable here is the heartbeat that ships with it: a
// killed gate is killed with a record of what it was observed doing, so a
// wrong bound is diagnosable rather than mysterious. Sixty minutes is roughly
// twice the longest gate run observed on this fleet (~30 minutes, the run
// that prompted the ticket).
//
// Set [gates] timeout in .pogo/refinery.toml to change it, or "0" to disable
// the bound entirely and let a gate run until the daemon stops.
const defaultGateTimeout = 60 * time.Minute

// gateWaitDelay is the backstop bound on how long Wait blocks after a killed
// gate's direct child exits, waiting on output pipes some descendant may still
// hold open. The process-group kill below should make it unnecessary; it stays
// because trading a hung gate for a hung runner would take the heartbeat down
// with it, and the heartbeat is the thing being fixed.
const gateWaitDelay = 10 * time.Second

// gateTimeoutError reports a gate killed for exceeding its timeout. It
// carries what was observed while the gate ran, so the failure an operator
// reads is evidence rather than a bare deadline.
type gateTimeoutError struct {
	Gate        string
	Timeout     time.Duration
	Elapsed     time.Duration
	OutputLines int
	SilentFor   time.Duration
	EverSpoke   bool
	// Contention is what the host was doing while the gate ran. A timeout
	// reached on a saturated host and a timeout reached on an idle one are the
	// same event with opposite meanings, and until this was recorded nothing
	// downstream could tell them apart — mg-1b8c's measured instance was a
	// gate on track to exceed a 45-minute timeout doing ~17 minutes of work.
	Contention hostload.Summary
	// Signals is the per-layer evidence as measured at the moment of the kill,
	// one row per instrument (mg-e565). It is carried into the TERMINAL record
	// rather than only into the live heartbeat log, because the live view is
	// gone by the time anyone reads the failure — and the failure is what a
	// polecat and a triaging coordinator actually see.
	//
	// The rows are rendered as rows on purpose. Collapsing them into one
	// sentence is what made a deadlock and a slow gate render identically, and
	// no single row settles it in either direction.
	Signals []Signal
}

func (e *gateTimeoutError) Error() string {
	var observed string
	if e.EverSpoke {
		observed = fmt.Sprintf("it produced %s of output and had been silent for %s",
			plural(e.OutputLines, "line"), roundDur(e.SilentFor))
	} else {
		observed = "it produced no output at all"
	}
	// The denial leads. A gate timeout used to arrive worded exactly like a red
	// test, and the part of a failure that travels is its first line — a caveat
	// further down does not reach the reader who forwards the headline.
	return fmt.Sprintf("gate %q was KILLED at its %s timeout after %s — THIS IS NOT A VERDICT ON THE BRANCH: "+
		"the gate never finished, so it neither passed nor failed this change.\n"+
		"When it was killed, %s. %s%s"+
		"If this gate is legitimately slower than %s, raise [gates] timeout in .pogo/refinery.toml "+
		"(or set it to \"0\" to remove the bound); if it is genuinely stuck, this is the intended outcome.",
		e.Gate, roundDur(e.Timeout), roundDur(e.Elapsed), observed,
		e.contentionClause(), e.signalBlock(), roundDur(e.Timeout))
}

// signalBlock renders the evidence, attributed to the layer each reading was
// taken at, and says out loud that the rows may disagree.
//
// It ends with the two caveats that must not be designed away, both measured
// on this host: a healthy gate was observed silent for 22 minutes before it
// printed again, and an idle subtree is also what a process blocked on I/O or
// on a lock looks like — which is exactly what the incident behind this ticket
// turned out to be. Anyone reading these rows should be able to reach "I cannot
// tell yet", because on this evidence that is often the true answer.
func (e *gateTimeoutError) signalBlock() string {
	if len(e.Signals) == 0 {
		// No measurement is not the same as an idle one. Say nothing rather
		// than render an empty evidence block that reads as evidence.
		return ""
	}
	b := &strings.Builder{}
	b.WriteString("\nWhat was measured when it was killed — these are separate instruments at " +
		"different layers and they can disagree; no single one settles it:\n")
	for _, s := range e.Signals {
		fmt.Fprintf(b, "  %-6s %-15s %-12s %s\n", s.Layer, s.Name, s.State, s.Detail)
	}
	// No figure that could be mistaken for a host measurement appears in this
	// prose: a run whose host was never sampled must not acquire either an
	// excuse or an accusation, and a number in a disclaimer reads like one that
	// was measured (guarded by TestUnsampledTimeoutIsUnchanged).
	b.WriteString("  (RUNNER is the pogod-side supervisor. It beats identically for a gate computing " +
		"flat out and one deadlocked on a mutex, so its freshness is not evidence about the gate.)\n" +
		"  (GATE stdout going quiet does not prove a hang: a healthy gate on this host was measured " +
		"silent for 22 minutes and then printed. An IDLE subtree does not prove one either — a process " +
		"blocked on I/O or holding a lock consumes no CPU and looks exactly like this.)\n")
	return b.String()
}

// contentionClause states what the host was doing, and says out loud what a
// timeout under contention does and does not establish.
//
// The kill still happens and the merge still fails: a control that stops
// failing when the host is busy is a control that can be silenced by making
// the host busy. What changes is that the failure no longer reads as a verdict
// on the branch when the evidence does not support one.
func (e *gateTimeoutError) contentionClause() string {
	if e.Contention.Samples == 0 {
		return ""
	}
	if !e.Contention.Contended() {
		return fmt.Sprintf("The host was not saturated while this ran (%s), so the elapsed time "+
			"is about this gate. ", e.Contention.Report())
	}
	return fmt.Sprintf("TIMED OUT ON A CONTENDED HOST — %s. This gate was competing for CPU for most "+
		"of its run, so the timeout is evidence about the host as much as about the branch: it does "+
		"NOT establish that this change is slow or broken, and it does not establish that it is fine "+
		"either. The kill stands, because a timeout that could be silenced by loading the host would "+
		"not be a bound at all. Re-run on a quieter host before reading this as a defect in the change. ",
		e.Contention.Report())
}

// gateOutputWriter accumulates a gate's combined output while reporting every
// write to the progress watch.
//
// It replaces cmd.CombinedOutput's internal buffer, and is assigned to both
// Stdout and Stderr: os/exec reuses one pipe when those two interfaces are
// equal, so ordering and interleaving match what CombinedOutput produced
// before. The accumulated string is still returned in full and stored on the
// MR — the watch adds a live count, it does not replace the output.
type gateOutputWriter struct {
	buf   bytes.Buffer
	watch *gateWatch
}

func (g *gateOutputWriter) Write(p []byte) (int, error) {
	n, err := g.buf.Write(p)
	// The watch is handed the bytes, not a count derived from them: it keeps a
	// bounded window of the gate's actual text as well as the counters, and the
	// text is the half that is unreachable anywhere else until the gate ends
	// (mg-9adc).
	g.watch.sawOutput(p[:n])
	return n, err
}

// runGate runs one quality gate command in the worktree.
//
// The gate is watched for as long as it runs (see gateWatch) and bounded by
// timeout when one is set. ctx cancellation kills the gate — that is what
// makes cancelling an in-flight merge request possible.
func runGate(ctx context.Context, wtDir, command string, timeout time.Duration, w *gateWatch) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Distinguish "we hit the timeout" from "someone cancelled us": both
	// arrive as a cancelled context and they need opposite reports.
	var deadlineCause context.Context
	if timeout > 0 {
		var cancel context.CancelFunc
		deadlineCause, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = deadlineCause
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = wtDir
	cmd.Env = append(os.Environ(), "POGO_REFINERY=1")
	cmd.WaitDelay = gateWaitDelay

	// Run the gate in its own process group and kill the group, not just the
	// shell. `sh -c "./build.sh"` forks rather than execs for anything
	// compound, so killing the shell alone leaves the real work — a test
	// binary, a compiler — running and still holding the output pipe open.
	// Wait then blocks on that pipe until WaitDelay expires, so a timeout that
	// should have taken effect at once instead stalls, and the killed work
	// keeps consuming the worktree it was told to stop using.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Setpgid makes the child's PGID equal its PID, so the negated PID
		// addresses the whole group. Fall back to the single process if the
		// group is already gone.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}

	out := &gateOutputWriter{watch: w}
	cmd.Stdout = out
	cmd.Stderr = out

	// Start/Wait rather than Run so the gate's pid can be published the moment
	// it exists. The pid roots the process-subtree CPU measurement, which is
	// the half of the liveness signal that output staleness cannot supply
	// (mg-0c51) — without it a silent-but-computing gate is indistinguishable
	// from a hung one.
	var err error
	if err = cmd.Start(); err == nil {
		w.setPID(cmd.Process.Pid)
		err = cmd.Wait()
	}
	output := out.buf.String()

	if err != nil && deadlineCause != nil && errors.Is(deadlineCause.Err(), context.DeadlineExceeded) {
		silentFor, everSpoke := w.lastOutputAge(time.Now())
		return output, &gateTimeoutError{
			Gate:        command,
			Timeout:     timeout,
			Elapsed:     time.Since(start),
			OutputLines: w.outputLines(),
			SilentFor:   silentFor,
			EverSpoke:   everSpoke,
			Contention:  w.contention(),
			Signals:     w.signals(time.Now()),
		}
	}
	if err != nil && ctx.Err() != nil && errors.Is(context.Cause(ctx), errCancelRequested) {
		return output, fmt.Errorf("gate %q killed by cancellation after %s: %w",
			command, roundDur(time.Since(start)), errCancelRequested)
	}
	// A gate the refinery did not kill, that died of a signal anyway (mg-0502).
	//
	// It comes LAST of the three, after both of the refinery's own kill paths
	// have had their say, and it is additionally guarded on ctx.Err() == nil —
	// so whatever reaches here was signalled by something outside this process.
	// That ordering is what lets the report say the deadline and the
	// cancellation are ruled out: they are ruled out by construction, not by a
	// guess about the signal number.
	//
	// Without this, os/exec's error is the bare string `signal: terminated`,
	// which arrives at the classifier looking exactly like any other gate
	// failure and comes out DEFECT with "the gate ran on this tree and returned
	// a verdict" — a sentence about an event that did not happen.
	if err != nil && ctx.Err() == nil {
		if sig, killed := signalThatKilled(err); killed {
			silentFor, everSpoke := w.lastOutputAge(time.Now())
			return output, &gateSignalError{
				Gate:        command,
				Signal:      sig,
				Elapsed:     time.Since(start),
				Timeout:     timeout,
				OutputLines: w.outputLines(),
				SilentFor:   silentFor,
				EverSpoke:   everSpoke,
				Contention:  w.contention(),
				Err:         err,
			}
		}
	}
	return output, err
}

// gateTimeout returns the timeout in force for a repo's gates.
func (cfg refineryConfig) gateTimeout() time.Duration {
	if cfg.GateTimeoutSet {
		return cfg.GateTimeout
	}
	return defaultGateTimeout
}

// parseGateTimeout reads a [gates] timeout value. Accepts a Go duration
// string ("45m", "90s") or a bare number of minutes, and treats "0"/"off"/
// "none" as "no bound". Returns ok=false for anything it cannot read, so an
// unreadable value falls back to the default rather than silently disabling
// the bound.
func parseGateTimeout(raw string) (time.Duration, bool) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"'`))
	if s == "" {
		return 0, false
	}
	switch strings.ToLower(s) {
	case "0", "off", "none", "never", "disabled":
		return 0, true
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return 0, false
		}
		return d, true
	}
	// Bare number: minutes, matching how the value is talked about.
	if mins, err := time.ParseDuration(s + "m"); err == nil && mins >= 0 {
		return mins, true
	}
	return 0, false
}
