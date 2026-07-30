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
}

func (e *gateTimeoutError) Error() string {
	var observed string
	if e.EverSpoke {
		observed = fmt.Sprintf("it produced %s of output and had been silent for %s",
			plural(e.OutputLines, "line"), roundDur(e.SilentFor))
	} else {
		observed = "it produced no output at all"
	}
	return fmt.Sprintf("gate %q exceeded its %s timeout after %s and was killed — %s. %s"+
		"If this gate is legitimately slower than %s, raise [gates] timeout in .pogo/refinery.toml "+
		"(or set it to \"0\" to remove the bound); if it is genuinely stuck, this is the intended outcome.",
		e.Gate, roundDur(e.Timeout), roundDur(e.Elapsed), observed, e.contentionClause(), roundDur(e.Timeout))
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
	// Report freshness on any bytes at all, and count only completed lines: a
	// gate that writes a progress spinner without newlines is still visibly
	// alive, which is the property being measured.
	g.watch.sawOutput(bytes.Count(p[:n], []byte{'\n'}))
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
		}
	}
	if err != nil && ctx.Err() != nil && errors.Is(context.Cause(ctx), errCancelRequested) {
		return output, fmt.Errorf("gate %q killed by cancellation after %s: %w",
			command, roundDur(time.Since(start)), errCancelRequested)
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
