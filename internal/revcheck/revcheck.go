// Package revcheck answers one question, in three values: is the pogod that is
// running RIGHT NOW built from the revision it is supposed to be running?
//
// WHY IT EXISTS (mg-ed4a). Four code paths restart or verify pogod, and until
// this package only one of them asked that question:
//
//	scripts/pogo-self-deploy  verify_running()       polls /version against $MAIN
//	internal/service          verifyLaunchdRunning() launchctl list + /health
//	internal/service          restartLaunchd()       nothing
//	scripts/launchd/pogo-recovery.sh                 the kickstart's exit code
//
// /health answers "is something listening". It does not answer "is the RIGHT
// thing listening", and the difference is not hypothetical: on 2026-08-07 this
// box was running a pogod built from a 2026-07-30 commit, 92 commits behind
// main, alive and healthy and passing every check except the deploy script's.
// It had been in that state for eight days. A restart that re-launches the same
// stale binary is the NORMAL outcome of a kickstart — launchd re-execs whatever
// is on disk — so a path that reports success on "the process came back" is
// reporting on the wrong property.
//
// HOW THIS DIFFERS FROM internal/selfdrift, AND WHY IT IS NOT A SECOND COPY OF
// IT. selfdrift is the standing three-way REPORT behind `pogo service status`:
// running vs installed vs main, answered once, for a human deciding what to do.
// This package is the RESTART-TIME predicate: one comparison, against one
// expectation the caller supplies, POLLED until it stops being able to change.
// The poll is the whole difference — for a few seconds after a kickstart the
// old process is still answering with the old revision and then nothing is
// answering at all, so a single sample of a restarting daemon is not an answer.
//
// Every observation still comes from selfdrift: its sentinels, its BinaryRev,
// its RunningRev. Re-deriving those here would recreate, one layer down, the
// exact defect this ticket is about — a repair that landed in one place while
// the other copies kept the old shape.
//
// THE THREE VALUES ARE THE POINT. This is the mg-e605 shape: an absent or
// unreadable answer renders as UNKNOWN and never as agreement. The failure this
// package exists to remove is a check that goes green because it could not
// measure anything, so "I could not read the running revision" and "the running
// revision matches" must not arrive at a caller as the same value. Compare
// therefore has no boolean form — callers read Verdict, and Result.OK() is true
// for Agrees alone.
//
// WHAT "EXPECTED" MEANS DEPENDS ON THE CALLER, AND THAT IS DELIBERATE. The
// deploy script expects $MAIN, because a deploy's job is to put main's HEAD on
// the box. The service paths expect the revision stamped into the pogod binary
// that launchd is configured to exec, because a restart's job is to put the
// on-disk binary into the running process — and that expectation needs no repo,
// no network and no config, so it is armed everywhere the service package runs.
// Both are "the revision it is supposed to be running"; this package takes the
// expectation as an argument rather than deciding it.
//
// AND THEREFORE: AN `AGREES` AGAINST THE ON-DISK BINARY DOES NOT MEAN CURRENT.
// It means the restart worked — the process is running the binary launchd
// execs. If that binary is itself eight days old, this check says AGREES and is
// right to. Measured on this box on 2026-08-07, and it is the reading a reader
// is most likely to over-claim from:
//
//	running  d31297f493cd (2026-07-30)   AGREES  ← the restart did its job
//	expected d31297f493cd                        ← ...and the disk is stale too
//	main     22e0541f7fd2                DIFFERS ← with --expect main's HEAD
//
// Getting that wrong would be this ticket's own defect one level up: an
// indicator going green on a property adjacent to the one the reader cares
// about. So the two questions have two instruments and neither pretends to be
// the other — "is the DISK current?" is internal/selfdrift (`pogo service
// status`) and internal/driftwatch's standing alarm; "did the RESTART take?" is
// here. Pass --expect to ask this one the first question deliberately.
//
// SCOPE. Report-only. Nothing here restarts, installs or reconciles anything —
// a check that repairs what it measures cannot be called from a repair path
// without risking a loop (the unbounded-reaper shape, mg-345b).
package revcheck

import (
	"fmt"
	"time"

	"github.com/drellem2/pogo/internal/selfdrift"
)

// The absence sentinels, re-exported from selfdrift so a caller of this package
// need not import both to name what it got back. They are the same constants,
// not copies — comparing a revcheck sentinel to a selfdrift one is comparing a
// value to itself, which is what keeps the two packages from drifting apart.
const (
	// RevUnreachable: nothing answered GET /version.
	RevUnreachable = selfdrift.RevUnreachable
	// RevUnstamped: the daemon or binary answered but carries no vcs.revision.
	RevUnstamped = selfdrift.RevUnstamped
	// RevMissing: the binary whose revision we wanted is not on disk.
	RevMissing = selfdrift.RevMissing
)

// Verdict is the three-valued answer. There is no fourth value and no boolean
// projection other than Result.OK, which is true for Agrees only.
type Verdict string

const (
	// Agrees means both revisions were READ and they are equal. It is the only
	// verdict that may be reported as a successful restart.
	Agrees Verdict = "AGREES"
	// Differs means both revisions were read and they are not equal: the daemon
	// is running something other than what it was supposed to be running.
	Differs Verdict = "DIFFERS"
	// Unknown means at least one side could not be read. It is NOT agreement and
	// NOT disagreement — it is the statement that this check did not measure the
	// property, which is the thing the callers used to report as health.
	Unknown Verdict = "UNKNOWN"
)

// IsSentinel reports whether rev is an absence rather than a revision. The
// empty string counts: a caller that forgot to set Expected must land in
// UNKNOWN, not in a comparison against "".
func IsSentinel(rev string) bool {
	switch rev {
	case "", RevUnreachable, RevUnstamped, RevMissing:
		return true
	}
	return false
}

// Short abbreviates a revision for a log line, leaving sentinels intact —
// truncating "<unreachable>" would produce something that reads like a hash.
// selfdrift.Short already has that property; this is the same function, so a
// revision renders identically in a restart line and in a drift report.
func Short(rev string) string {
	if rev == "" {
		return "<unset>"
	}
	return selfdrift.Short(rev)
}

// Result is one answer, carrying everything a log line or a mail needs so the
// reader never has to re-derive what was compared.
type Result struct {
	// Verdict is the three-valued answer.
	Verdict Verdict
	// Running is the revision the live daemon reported, or a sentinel.
	Running string
	// Expected is the revision it was supposed to be, or a sentinel.
	Expected string
	// Reason explains an UNKNOWN. Empty for Agrees and Differs, whose meaning is
	// carried entirely by the two revisions.
	Reason string
	// Waited is how long Wait polled before settling on this verdict. Zero for a
	// bare Compare.
	Waited time.Duration
}

// OK is true for Agrees alone. Unknown is deliberately not OK: the whole defect
// this package addresses is a path that treated "could not measure" as pass.
func (r Result) OK() bool { return r.Verdict == Agrees }

// String is the one line every caller logs. It always names the verdict FIRST,
// so a reader scanning a log cannot mistake an UNKNOWN for a pass by skimming
// past a hash, and it always shows both sides.
func (r Result) String() string {
	s := fmt.Sprintf("revision check %s: running=%s expected=%s",
		r.Verdict, Short(r.Running), Short(r.Expected))
	if r.Reason != "" {
		s += " — " + r.Reason
	}
	if r.Waited > 0 {
		s += fmt.Sprintf(" (after %s)", r.Waited.Round(time.Second))
	}
	return s
}

// Compare is the pure predicate: no clock, no network, no filesystem. Every
// path in this package funnels through it so there is exactly one place where
// "the same revision" is defined.
func Compare(running, expected string) Result {
	r := Result{Running: running, Expected: expected, Verdict: Unknown}
	switch {
	case IsSentinel(expected):
		r.Reason = expectedReason(expected)
	case IsSentinel(running):
		r.Reason = runningReason(running)
	case running == expected:
		r.Verdict = Agrees
	default:
		r.Verdict = Differs
	}
	return r
}

func expectedReason(expected string) string {
	switch expected {
	case RevMissing:
		return "the binary the daemon should be running is not on disk, so there is nothing to expect"
	case RevUnstamped:
		return "the binary the daemon should be running carries no vcs.revision, so it cannot be compared"
	case RevUnreachable:
		return "the expected revision came from a source that did not answer"
	default:
		return "no expected revision was supplied"
	}
}

func runningReason(running string) string {
	switch running {
	case RevUnreachable:
		return "pogod is not answering GET /version, so what it is running is unmeasured"
	case RevUnstamped:
		return "pogod answered /version but reports no vcs.revision — it cannot say what it is"
	default:
		return "the running revision could not be established"
	}
}

// RunningRevision asks the LIVE daemon what it is, and returns a sentinel
// rather than an error so the answer can flow straight into Compare.
//
// It must come from the process, not from the on-disk file: `go install`
// rewrites that file underneath a live daemon, so the file's revision diverges
// from the running one the instant a rebuild happens. That divergence IS the
// state this check exists to catch, so reading the file for both sides would
// compare a value to itself.
func RunningRevision(baseURL string) string {
	rev, _ := selfdrift.RunningRev(baseURL)
	return rev
}

// BinaryRevision reads the vcs stamp out of an on-disk binary — the revision a
// restart is SUPPOSED to put into the running process. It reads the file's
// build metadata directly (no Go toolchain required), which matters because the
// callers here are launchd-spawned with a minimal PATH.
func BinaryRevision(path string) string { return selfdrift.BinaryRev(path) }

// Options configures Wait.
type Options struct {
	// BaseURL is the daemon's origin, e.g. http://127.0.0.1:10000.
	BaseURL string
	// Expected is the revision the daemon should be running. A sentinel or an
	// empty string yields UNKNOWN immediately — see Wait.
	Expected string
	// Timeout bounds the whole poll. Defaults to DefaultTimeout.
	Timeout time.Duration
	// Interval is the gap between probes. Defaults to DefaultInterval.
	Interval time.Duration

	// running is the probe seam. Production leaves it nil and gets
	// RunningRevision; tests substitute a scripted sequence so a restart that
	// converges late — and one that never converges — are both replayable
	// without a daemon or a real clock.
	running func() string
	// sleep is the delay seam, nil in production.
	sleep func(time.Duration)
	// now is the clock seam, nil in production.
	now func() time.Time
}

// DefaultTimeout matches scripts/pogo-self-deploy's verify_running: 60s is long
// enough for a kickstarted pogod to bind and answer on a loaded box, and short
// enough that a caller blocking on it is not mistaken for a hang.
const DefaultTimeout = 60 * time.Second

// DefaultInterval is the probe gap, also matching verify_running.
const DefaultInterval = 3 * time.Second

// Wait polls the live daemon until it agrees with Expected, or until the
// timeout, and returns the LAST result rather than a boolean.
//
// It polls through DIFFERS as well as through UNREACHABLE, because both are
// expected transient states during a restart: for a few seconds after a
// kickstart the old process may still be answering with the old revision, and
// for a few seconds after that nothing is answering at all. The verdict is only
// meaningful once it has stopped being able to change, which is what the
// deadline stands in for.
//
// It does NOT poll when Expected is unknown. Waiting 60s to re-derive an answer
// that cannot change is a pure delay in a restart path, and the reason for the
// UNKNOWN is on the expected side where no amount of probing helps.
func Wait(o Options) Result {
	running := o.running
	if running == nil {
		running = func() string { return RunningRevision(o.BaseURL) }
	}
	sleep := o.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	now := o.now
	if now == nil {
		now = time.Now
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	interval := o.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	if IsSentinel(o.Expected) {
		return Compare(RevUnreachable, o.Expected)
	}

	start := now()
	deadline := start.Add(timeout)
	for {
		res := Compare(running(), o.Expected)
		res.Waited = now().Sub(start)
		if res.OK() {
			return res
		}
		if !now().Add(interval).Before(deadline) {
			return res
		}
		sleep(interval)
	}
}
