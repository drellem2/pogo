package refinery

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/drellem2/pogo/internal/hostload"
)

// A quality gate STOPPED BY A SIGNAL did not return a verdict, and until
// mg-0502 the refinery said that it did.
//
// # What happened
//
// On 2026-08-13 the gate on branch polecat-pc26d died and the refinery recorded
// (mr-d9uqe8atjv1j0e4isn1g):
//
//	Error: quality gate: ./build.sh failed: signal: terminated
//	DEFECT — establishes a fact about the branch. A fix is warranted.
//	No further retry: the build gate ran on this tree and returned a verdict —
//	                  re-running establishes the same fact
//
// `signal: terminated` is os/exec's rendering of a child that was killed by
// SIGTERM. It never reached an exit status, so it returned NOTHING — and the
// no-retry rationale asserts the exact opposite of what happened. The rule it
// quotes is correct for a gate that ran to completion and reported red, where
// re-running re-establishes the same fact; on a kill there is no fact to
// re-establish, which is precisely why re-running is informative here rather
// than pointless.
//
// # Why this is not the timeout class arriving by another route
//
// The refinery's own deadline path kills with SIGKILL, on the process group
// (gaterun.go's cmd.Cancel), and reports a gateTimeoutError that never reaches
// this classification at all. An operator cancellation goes through the same
// context and is reported as a cancellation. So SIGTERM cannot have come from
// either, and the incident's numbers agree: the gate died at 18.84s wall
// against a 20m per-package budget and a 60m gate timeout, burning 0.38 cores,
// with the contention sampler reporting zero saturated samples. Nothing that
// the refinery knows how to do to a gate happened to that gate.
//
// # Why INDETERMINATE and not INFRASTRUCTURE
//
// Infrastructure's triage note clears the branch outright — "establishes
// nothing about the branch, resubmit". A gate CAN kill its own shell: the gate
// runs in its own process group (Setpgid, for the kill path above), so a script
// or test that runs `kill 0` or an unanchored `pkill -f` signals the group it is
// itself a member of. That is a real defect wearing this signature, and clearing
// the branch on a signal would be the same error mg-e565 fixed, in the opposite
// direction. The honest reading is the one ClassIndeterminate already carries:
// the run was cut short and the question is still open.
//
// # Why one re-run rather than an automatic retry
//
// Over the seven days before this ticket the refinery recorded 72 error rows
// and exactly ONE carried a signal kill. There is no measured recurring source
// to retry against, and an automatic retry against a gate that signals ITSELF
// spends the merge slot re-deriving the same kill. The remedy is one deliberate
// re-run, which the report asks for by name.

// gateSignalError reports a gate whose process was killed by a signal that did
// not come from the refinery. It carries the signal, what the gate had been
// observed doing, and — the half a bare "your gate was killed" cannot give an
// author — which of the candidate sources the evidence rules OUT.
type gateSignalError struct {
	Gate   string
	Signal syscall.Signal
	// Elapsed is how long the gate ran before the signal landed, and Timeout is
	// the bound it was running under (0 when unbounded). The pair is what makes
	// "nowhere near any deadline" readable rather than asserted.
	Elapsed time.Duration
	Timeout time.Duration
	// OutputLines / SilentFor / EverSpoke are the gate's own stdout as measured
	// at the kill, on the same terms gateTimeoutError reports them.
	OutputLines int
	SilentFor   time.Duration
	EverSpoke   bool
	// Contention is what the host was doing while the gate ran. Samples == 0
	// means it was never sampled, which is NOT a measurement of an idle host.
	Contention hostload.Summary
	// Err is os/exec's own error, kept verbatim so errors.As still reaches the
	// *exec.ExitError and nothing downstream has to trust this struct's copy.
	Err error
}

func (e *gateSignalError) Unwrap() error { return e.Err }

func (e *gateSignalError) Error() string {
	b := &strings.Builder{}
	// The denial leads, for the reason gateTimeoutError's and
	// hostResourceError's do: the part of a failure that travels is its first
	// line, and this whole ticket is about a first line that said the opposite.
	fmt.Fprintf(b, "gate %q WAS KILLED BY %s after %s — THIS IS NOT A VERDICT ON THE BRANCH: "+
		"the gate never reached an exit status, so it neither passed nor failed this change. "+
		"A signal is not an answer.\n",
		e.Gate, signalName(e.Signal), roundDur(e.Elapsed))
	fmt.Fprintf(b, "When it was killed, %s. %s", e.observedClause(), e.contentionClause())
	b.WriteString(e.sourceBlock())
	b.WriteString("Re-run this branch ONCE. Unlike a red gate, a kill is not reproduced by re-running: " +
		"there is no fact here to re-establish, so a second run is informative rather than wasted. " +
		"It was NOT retried automatically — a gate that signals ITSELF (see above) would be re-killed " +
		"on every attempt, spending the merge slot to re-derive the same kill.")
	if e.Err != nil {
		fmt.Fprintf(b, "\nos/exec's own error, kept verbatim: %v", e.Err)
	}
	return b.String()
}

// observedClause reports the gate's stdout as measured, and never manufactures
// a silence out of a gate that was never heard from.
func (e *gateSignalError) observedClause() string {
	if !e.EverSpoke {
		return "it had produced no output at all"
	}
	return fmt.Sprintf("it had produced %s of output and had been silent for %s",
		plural(e.OutputLines, "line"), roundDur(e.SilentFor))
}

// contentionClause states what the host was doing, and says nothing at all when
// nothing was sampled — an unsampled run must not acquire either an excuse or
// an accusation, which is the rule gateTimeoutError applies to the same reading.
func (e *gateSignalError) contentionClause() string {
	if e.Contention.Samples == 0 {
		return ""
	}
	if !e.Contention.Contended() {
		return fmt.Sprintf("The host was not saturated while this ran (%s). ", e.Contention.Report())
	}
	return fmt.Sprintf("The host WAS saturated while this ran (%s), so read the source list below "+
		"against a loaded box. ", e.Contention.Report())
}

// sourceBlock is the half of this report an author can act on. "Your gate was
// killed" leaves them nowhere; naming which candidate sources the evidence
// RULES OUT narrows where to look, and the ones it cannot rule out are said to
// be open rather than quietly omitted.
//
// Every line here is a fact about this run or about code in this repo, not a
// guess: the refinery's kill signal is gaterun.go's, the deadline comparison is
// this run's own clock, and the self-signal case is a consequence of the
// Setpgid the kill path needs.
func (e *gateSignalError) sourceBlock() string {
	b := &strings.Builder{}
	b.WriteString("\nWhere the signal did NOT come from, and what is still open:\n")

	// The refinery's two kill paths. Both are eliminated by construction — they
	// are reported as other errors and never arrive here — so this states the
	// mechanism rather than merely asserting the conclusion.
	b.WriteString("  RULED OUT  the refinery's gate deadline and `pogo refinery cancel`. Both kill with " +
		"SIGKILL on the process group and are reported as a timeout or a cancellation, not as this error.\n")
	switch {
	case e.Timeout <= 0:
		b.WriteString("  no bound   no gate timeout was in force for this run ([gates] timeout = 0), " +
			"so no deadline of the refinery's could have fired.\n")
	case e.Elapsed < e.Timeout/2:
		// Scoped to the refinery's OWN bound, deliberately. A timeout enforced
		// INSIDE the gate keeps its own clock and can be far shorter, so "it is
		// nowhere near the bound" rules out one deadline and not every deadline —
		// widening it to "any deadline of this gate's" would be an assertion this
		// number does not support.
		fmt.Fprintf(b, "  RULED OUT  the refinery's own bound: it died at %s against a %s timeout, "+
			"nowhere near it. A timeout enforced INSIDE the gate keeps its own clock — read the output.\n",
			roundDur(e.Elapsed), roundDur(e.Timeout))
	default:
		fmt.Fprintf(b, "  OPEN       it died at %s against a %s bound — close enough that a timeout "+
			"enforced INSIDE the gate is worth reading the output for.\n", roundDur(e.Elapsed), roundDur(e.Timeout))
	}

	// The OOM reading, and the one line here that had to be walked back while
	// writing it. "The OOM killer sends SIGKILL, so a SIGTERM rules it out" is
	// true of the KERNEL killer on both platforms — macOS jetsam and Linux's
	// oom-killer — and FALSE of userspace daemons: earlyoom sends SIGTERM by
	// default. Stating the strong version would have been this ticket's own
	// defect committed inside its remedy, so the caveat is carried off-darwin
	// rather than dropped for a tidier line.
	switch {
	case e.Signal == syscall.SIGKILL:
		b.WriteString("  OPEN       an out-of-memory kill. Every OOM killer sends SIGKILL, which is what this " +
			"was; check the system log for a jetsam/oom-killer entry naming this pid.\n")
	case runtime.GOOS == "darwin":
		fmt.Fprintf(b, "  RULED OUT  an out-of-memory kill: macOS jetsam sends SIGKILL and this was %s.\n",
			signalName(e.Signal))
	default:
		fmt.Fprintf(b, "  RULED OUT  the KERNEL out-of-memory killer, which sends SIGKILL, not %s. NOT ruled "+
			"out: a userspace OOM daemon — earlyoom and similar send SIGTERM by default — if one runs here.\n",
			signalName(e.Signal))
	}

	// The case that keeps this INDETERMINATE rather than INFRASTRUCTURE.
	b.WriteString("  OPEN       the gate signalling ITSELF. It runs in its own process group, so a `kill 0` " +
		"or an unanchored `pkill -f` anywhere in the gate hits the gate's own shell. Grep the gate's " +
		"scripts for those before assuming the signal came from off-box.\n")
	b.WriteString("  OPEN       anything else on this host that signals by pattern or by group — another " +
		"agent's cleanup, a shutdown, a stray kill.\n")
	return b.String()
}

// signalName renders a signal the way an operator reading a log wants it —
// SIGTERM, not "terminated" and not "15". os/exec prints the English word, and
// "signal: terminated" is the string this whole ticket is about failing to
// recognise as a kill.
func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGXCPU:
		return "SIGXCPU"
	}
	return fmt.Sprintf("signal %d (%v)", int(sig), sig)
}

// signalThatKilled reports the signal a process died of, reading the wait
// status rather than the error's English.
//
// Reading the status is the point. `signal: terminated` is os/exec's rendering
// of exactly this condition, and matching that STRING against gate output would
// be the speculative text-matching failureclass.go refuses — a test that prints
// the words would then have its author's defect taken away from them. The wait
// status is the kernel's own answer about the process the refinery started, and
// nothing a gate prints can forge it.
//
// A process that exits 128+N of its own accord is NOT signalled and is not
// matched here: it chose its status, so it returned a verdict.
func signalThatKilled(err error) (syscall.Signal, bool) {
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ProcessState == nil {
		return 0, false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, false
	}
	return ws.Signal(), true
}
