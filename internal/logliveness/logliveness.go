// Package logliveness answers one question, in three values: is the file the
// service definition names as pogod's log a live record of the pogod that is
// actually running?
//
// WHY IT EXISTS (mg-a19a). Measured on this box 2026-09-03 20:53Z:
//
//	running daemon      pogod pid=6610, up 57h45m, started Sep 1 12:07:50 local
//	its parent          pid 3996 — Emacs. Not launchd.
//	its fd 2            /dev/ttys007
//	its fds on pogod.log  none
//	pogod.log mtime     2026-09-02 01:05:57
//	lines pogod 6610 ever wrote to pogod.log   ZERO
//
// The daemon holding the lock, serving :10000, delivering scheduled fires and
// running the refinery gate had written nothing to the path its own service
// definition names, for its entire 57-hour life. Every diagnostic on the box
// that greps pogod.log was reading a file some OTHER process wrote.
//
// It was not empty and it was not obviously wrong. It was 8.9 MB of correctly
// formatted, richly detailed daemon output — which is what makes this class
// expensive. On 2026-09-03 an architect grepped it for a fleet-stop window and
// found 51 `cause=modal_wedge signatures=[rating_dialog]` lines plus 1,875
// `ANIMATING BUT NOT WORKING` lines, and built a specific, mechanically
// plausible root cause on them. All of those lines predate the window by
// days. The real cause was an entitlement refusal (mg-6616). A frozen log does
// not look empty; it silently answers questions about periods it does not
// cover, and renders "the record is absent" identically to "the record is
// negative".
//
// WHY MTIME IS NOT THE INSTRUMENT, and this is the whole design. The obvious
// check — "is pogod.log fresh?" — reads GREEN in the worst case. From
// 2026-09-01 12:08:21 to 2026-09-02 01:05:57 launchd respawned a pogod every
// ~10 seconds; each one correctly refused the lock held by 6610 and exited 1,
// and each wrote two lines to pogod.log on its way out. `launchctl print`
// reports runs = 4639 and the file holds exactly 4,639 "Cannot acquire pogod
// lock … held by pid 6610" lines. So for thirteen hours the file the live
// daemon never touched had an mtime under ten seconds old, continuously. A
// freshness check would have passed every time it was run, on the strength of
// writes from processes that lived milliseconds.
//
// An instrument that returns the same answer under two different world-states
// is not evidence about either. So this package compares IDENTITY, not
// recency: the file descriptor the running daemon's stderr actually points at,
// against the path the service definition names. mtime, size and the daemon's
// start time are carried in the report as CONTEXT and never reach the verdict
// — printed under a line saying what they do not prove, because on this box
// they have already been misread.
//
// RELATION TO internal/supervision. supervision asks whether launchd is
// supervising the running daemon; this asks whether the named log records it.
// The 2026-09 state answers both badly and they are the same underlying
// displacement — but they are different questions with different readers and
// different remedies, and neither substitutes for the other. Nobody about to
// run `grep refinery: "$log"` has a reason to first ask a question about
// launchd supervision, which is precisely why 43 hours of wrong answers went
// uncorrected while `pogo service supervision` sat one command away, already
// shipped, already correct, and unrun.
//
// WHAT DETACHED DOES NOT MEAN. It does not mean the daemon is broken. Through
// the whole 2026-09 episode pogod was up, current and serving. It means the
// file is not a record of it, and that an empty grep against that file is not
// a negative result.
package logliveness

import (
	"fmt"
	"strings"
	"time"
)

// Verdict is the three-valued answer. There is deliberately no boolean form:
// "could not tell" must not arrive at a caller as the same value as "yes",
// because a check that goes green because it measured nothing reproduces the
// defect it exists to catch.
type Verdict string

const (
	// Live — the running daemon's stderr IS the named file. An empty grep
	// against it is a real negative result.
	Live Verdict = "LIVE"

	// Detached — the running daemon writes its output somewhere else (a tty,
	// /dev/null, a different file), so every byte in the named file was
	// written by some other process. Whatever is in it may be perfectly
	// formed and perfectly irrelevant.
	Detached Verdict = "DETACHED"

	// Unknown — a reading was missing: no daemon to ask about, or its stderr
	// could not be resolved. NOT a pass.
	Unknown Verdict = "UNKNOWN"
)

// Observation is the raw reading, separated from the judgement so the
// judgement is unit-testable without a live daemon, lsof, or a filesystem.
//
// Each reading carries its own ok flag rather than relying on a zero value:
// "not readable" and "read as absent" owe different verdicts, and an empty
// string cannot distinguish them.
type Observation struct {
	// LogPath is the path being judged — the one a diagnostician is about to
	// grep. Callers derive it from the loaded job where they can and fall
	// back to the compiled-in default; either way the verdict is about THIS
	// path, so it is recorded rather than re-derived here.
	LogPath string `json:"log_path"`

	// JobLogPath is the path the loaded launchd job names, when it could be
	// read. Context only: it is here so a disagreement with LogPath (plist
	// drift) is visible rather than silently deciding the comparison.
	JobLogPath string `json:"job_log_path,omitempty"`

	// DaemonPID is the pid of the pogod that owns this POGO_HOME.
	// DaemonPIDOK is false when nothing holds the lockfile.
	DaemonPID   int  `json:"daemon_pid"`
	DaemonPIDOK bool `json:"daemon_pid_ok"`

	// StderrPath is where the daemon's fd 2 actually points, resolved. For
	// the 2026-09 state this reads "/dev/ttys007". StderrOK is false when the
	// descriptor could not be inspected at all — no lsof, permission denied,
	// unparseable output — which is Unknown and never a pass.
	StderrPath string `json:"stderr_path"`
	StderrOK   bool   `json:"stderr_ok"`

	// LogExists, LogMtime and LogSize describe the named file.
	//
	// CONTEXT ONLY, all three. See the package doc: the mtime of this file was
	// under ten seconds old, continuously, for the thirteen hours during which
	// the running daemon wrote nothing to it. They are reported because they
	// tell a reader how much material is in the file they are about to trust;
	// they are not inputs to Verdict and must never become inputs.
	LogExists bool      `json:"log_exists"`
	LogMtime  time.Time `json:"log_mtime,omitempty"`
	LogSize   int64     `json:"log_size"`

	// DaemonStart is when the running daemon started, when it could be read.
	// Context only, and the most legible line in the report: an mtime that
	// PREDATES the daemon's start proves the daemon has written nothing to
	// the file for its entire life, which is a stronger statement than
	// staleness and the one that actually held here.
	DaemonStart   time.Time `json:"daemon_start,omitempty"`
	DaemonStartOK bool      `json:"daemon_start_ok"`

	// Now is the clock the report ages things against, passed in so Text is
	// deterministic under test.
	Now time.Time `json:"now"`

	// ReadErr describes why a reading is missing, when the reason is known
	// and worth printing. Empty when every reading that could be taken was.
	ReadErr string `json:"read_err,omitempty"`
}

// Result is the judgement plus enough of the reading to act on it.
type Result struct {
	Verdict Verdict     `json:"verdict"`
	Reason  string      `json:"reason"`
	Obs     Observation `json:"observation"`
}

// OK reports whether the named log is a live record. True for Live alone —
// Unknown is not a pass.
func (r Result) OK() bool { return r.Verdict == Live }

// Check judges an Observation. Pure: no lsof, no filesystem, no clock.
//
// Note what is absent from every branch below: LogMtime, LogSize and
// DaemonStart. That absence is the package's central claim and is pinned by
// test — a future edit that lets a fresh mtime contribute to Live would
// reintroduce exactly the instrument that read green for thirteen hours.
func Check(obs Observation) Result {
	res := Result{Obs: obs}

	switch {
	case !obs.DaemonPIDOK:
		res.Verdict = Unknown
		res.Reason = "nothing holds the pogod lockfile, so there is no running daemon to compare the log against. This says nothing about whether the file is current — it says there is no daemon whose record it could be"

	case !obs.StderrOK:
		res.Verdict = Unknown
		res.Reason = fmt.Sprintf("pid %d owns this POGO_HOME but its stderr could not be inspected, so the comparison was never made. NOT a pass: the 2026-09 state is invisible to every check that reads a property of the file instead of the descriptor", obs.DaemonPID)

	case !obs.LogExists:
		res.Verdict = Detached
		res.Reason = fmt.Sprintf("%s does not exist, while pid %d writes to %s. A grep against a missing file prints nothing and exits quietly — indistinguishable from a real negative", obs.LogPath, obs.DaemonPID, obs.StderrPath)

	case obs.StderrPath != obs.LogPath:
		res.Verdict = Detached
		res.Reason = fmt.Sprintf("pid %d writes its output to %s, NOT to %s. Every byte in that file was written by some other process; an empty grep against it is not a negative result", obs.DaemonPID, obs.StderrPath, obs.LogPath)

	default:
		res.Verdict = Live
		res.Reason = fmt.Sprintf("pid %d writes its output to %s — the file and the running daemon are the same record", obs.DaemonPID, obs.LogPath)
	}
	return res
}

// MtimePredatesDaemon reports whether the named log's newest write happened
// before the running daemon started — i.e. the daemon has written nothing to
// it, ever, not merely nothing lately. Both readings must be present.
func (r Result) MtimePredatesDaemon() bool {
	o := r.Obs
	return o.LogExists && o.DaemonStartOK && !o.LogMtime.IsZero() && o.LogMtime.Before(o.DaemonStart)
}

// String renders the one-line verdict a human reads first.
func (r Result) String() string { return fmt.Sprintf("%s: %s", r.Verdict, r.Reason) }

// Text renders the verdict plus the readings it was made from. The three
// context readings are printed BELOW the verdict and labelled with what they
// do not prove — on this box a fresh mtime on this exact file was the reading
// that would have been trusted, and it was produced entirely by processes that
// lived milliseconds.
func (r Result) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", r.String())
	if r.Obs.ReadErr != "" {
		fmt.Fprintf(&b, "  reading incomplete: %s\n", r.Obs.ReadErr)
	}
	fmt.Fprintf(&b, "  log path        : %s\n", r.Obs.LogPath)
	if r.Obs.JobLogPath != "" && r.Obs.JobLogPath != r.Obs.LogPath {
		fmt.Fprintf(&b, "  job names       : %s — DRIFT: the loaded job redirects somewhere other than the path judged above. Grep the job's path, not this one.\n", r.Obs.JobLogPath)
	}
	if r.Obs.DaemonPIDOK {
		fmt.Fprintf(&b, "  daemon pid      : %d\n", r.Obs.DaemonPID)
	} else {
		fmt.Fprintf(&b, "  daemon pid      : none (nothing holds the lockfile)\n")
	}
	if r.Obs.StderrOK {
		fmt.Fprintf(&b, "  daemon's fd 2   : %s\n", r.Obs.StderrPath)
	} else {
		fmt.Fprintf(&b, "  daemon's fd 2   : unreadable\n")
	}

	if !r.Obs.LogExists {
		fmt.Fprintf(&b, "  log file        : ABSENT\n")
		return b.String()
	}
	age := "unknown age"
	if !r.Obs.Now.IsZero() && !r.Obs.LogMtime.IsZero() {
		age = r.Obs.Now.Sub(r.Obs.LogMtime).Round(time.Minute).String() + " old"
	}
	fmt.Fprintf(&b, "  log mtime       : %s (%s), %d bytes — CONTEXT, NOT A VERDICT INPUT. A fresh mtime on this file does not mean the running daemon wrote it: on 2026-09-01/02 a launchd respawn loop kept this exact file under ten seconds old for thirteen hours while the live daemon wrote to a tty (4,639 runs, 4,639 lock refusals).\n",
		r.Obs.LogMtime.Format(time.RFC3339), age, r.Obs.LogSize)
	if r.MtimePredatesDaemon() {
		fmt.Fprintf(&b, "  ALSO            : that mtime PREDATES the running daemon's start (%s) by %s. Pid %d has written nothing to this file for its entire life — not stale, absent.\n",
			r.Obs.DaemonStart.Format(time.RFC3339),
			r.Obs.DaemonStart.Sub(r.Obs.LogMtime).Round(time.Minute), r.Obs.DaemonPID)
	}
	return b.String()
}
