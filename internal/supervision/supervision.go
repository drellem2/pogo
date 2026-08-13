// Package supervision answers one question, in three values: is the pogod that
// owns this POGO_HOME the process launchd is supervising?
//
// WHY IT EXISTS (mg-fa79). Between 2026-08-05 12:45:49 and 2026-08-07 18:37:18
// this box ran two pogods' worth of intent and one pogod's worth of process.
// A pogod started outside launchd at fleet-start held the lockfile and served
// :10000; the loaded com.pogo.daemon job could not acquire that lock, exited 1,
// and KeepAlive respawned it roughly every ten seconds for forty-six hours.
// The rotated daemon log holds 19,274 "Cannot acquire pogod lock … held by pid
// 4368" lines, all inside that window, none after it.
//
// Nothing noticed. That is the defect this package removes, and the reason it
// is a separate question from every check pogo already ships:
//
//	launchctl list        says a job is registered, and reported "no PID, last
//	                      exit 1" for a daemon that was up and serving.
//	GET /health           says something is listening.
//	GET /version          says WHAT is listening — and in a displacement the
//	                      orphan usually execs the same on-disk binary, so it
//	                      answers with the same revision the job would have.
//	pogo service verify-revision
//	                      compares revisions, and therefore says AGREES for the
//	                      displaced state whenever both sides were built from
//	                      the same commit. It is not wrong; it is answering a
//	                      different question.
//
// Every one of those reads a PROPERTY of whatever answers. None reads IDENTITY.
// The displaced state is invisible to all of them because the wrong process is
// healthy, current, and listening — it is simply not the one launchd restarts
// when it wedges. KeepAlive was restarting a process that exited in
// milliseconds; a hung orphan would have been restarted by nobody.
//
// SO THIS PACKAGE COMPARES PIDS, NOT PROPERTIES. Two readings, one comparison:
//
//	the pid launchd attributes to the job   `launchctl print gui/$UID/<label>`
//	the pid that holds the pogod lockfile   config.LockfilePath()
//
// The lockfile is the right second reading rather than, say, the listener on
// :10000, because the lockfile is the component that NOTICED in 2026-08: it is
// the definition of "owns this POGO_HOME", and its refusal is what produced the
// 19,274-line record the episode was reconstructed from. A check built on the
// thing that already works needs no new mechanism to be trusted.
//
// PPID 1 IS NOT EVIDENCE, AND THE REPORT SAYS SO. Three separate readings of
// this ticket used `ps -o ppid` and took PPID 1 as showing launchd started the
// process. It does not. A process orphaned by its spawner reparents to launchd
// too, and the displacing pogod in this very episode was spawned by a CLI that
// setsid()s the daemon (internal/client.newServerCmd) — so it had PPID 1 from
// its first instant, and looked exactly like a launchd-started daemon. Only
// launchd's own attribution distinguishes them, which is why that is the
// reading this package takes. PPID travels in the report as context and never
// as a verdict input.
//
// COUNTERS ARE NOT EVIDENCE EITHER. `runs` and `last exit code` from launchctl
// are lifetime totals that keep climbing across a repair — this box read
// runs=24991 on a healthy daemon while the ticket that recorded "129 failures"
// was still open. LastExitReason is carried in the report for the same reason
// PPID is (it described a REAL and otherwise unrecorded event on 2026-08-13,
// see below), and like PPID it never changes the verdict.
//
// THE THREE VALUES ARE THE POINT (the mg-e605 shape, same as revcheck). An
// unreadable answer renders as Unknown and never as Supervised. A check that
// goes green because it measured nothing would reproduce the original defect
// one layer up: in the 2026-08 episode every instrument that could not see the
// problem reported health, and that is what let forty-six hours pass.
//
// ONE SAMPLE, AND WHERE THAT BOUND BITES. Check judges a single reading; it
// does not poll the way revcheck does. `launchctl kickstart -k` SIGKILLs the
// old process before spawning, and the kernel releases its lock at death, so
// the ordinary restart cannot produce a window where launchd names a new pid
// while an old one still holds the lock. A GRACEFUL stop can: for as long as a
// SIGTERMed pogod takes to unlock, a fresh launchd pid and the departing
// holder are two different live pids and this reads Unsupervised. That is the
// one known false positive, it is seconds wide, and the remedy is to read it
// again — which is why the deploy's caller takes its reading only after the
// revision and orchestration verifies have already established that the new
// daemon is up.
//
// WHAT UNSUPERVISED DOES NOT MEAN. It does not mean the daemon is broken. In
// the whole 2026-08 episode pogod was up, current and serving the entire time.
// It means the supervision anyone believes com.pogo.daemon provides is not
// being provided, and that a restart issued through launchd — which is how
// scripts/pogo-self-deploy restarts pogod — acts on a process nobody is using.
package supervision

import (
	"fmt"
	"strings"
)

// Verdict is the three-valued answer. There is deliberately no boolean form:
// see the package doc on why "could not tell" must not arrive at a caller as
// the same value as "yes".
type Verdict string

const (
	// Supervised — launchd's job and the lockfile name the same pid.
	Supervised Verdict = "SUPERVISED"
	// Unsupervised — a loaded job and a lock holder that are not the same
	// process. Both halves are required: a lock holder with no job loaded is
	// the fleet-owns-it configuration, not a split brain.
	Unsupervised Verdict = "UNSUPERVISED"
	// Unknown — at least one side could not be read, or nothing is running.
	// NOT a pass.
	Unknown Verdict = "UNKNOWN"
)

// Observation is the raw reading, separated from the judgement so the
// judgement is unit-testable without launchctl or a lockfile.
//
// Each pid field carries its own ok flag rather than relying on a zero value,
// because "no such pid" and "could not read" owe different verdicts and pid 0
// cannot distinguish them.
type Observation struct {
	// Label is the launchd job label the reading is about, for the report.
	Label string `json:"label"`

	// JobLoaded is whether a job with that label is loaded at all. False
	// means `launchctl print` found nothing — which on a host that never
	// installed the service is the normal, healthy state.
	JobLoaded bool `json:"job_loaded"`
	// JobPID is the pid launchd currently attributes to the job.
	// JobPIDOK is false when the job is loaded but has no live process —
	// the exact 2026-08-05 reading, and the one that pairs with a live
	// LockPID to prove displacement.
	JobPID   int  `json:"job_pid"`
	JobPIDOK bool `json:"job_pid_ok"`

	// LockPID is the pid holding config.LockfilePath(). LockPIDOK is false
	// when the lockfile is absent, stale, or unreadable.
	//
	// BOUND: liveness, not identity. The lockfile reader validates that the
	// recorded pid is running; it does not confirm that the running pid is
	// still the pogod that wrote it, so a recycled pid would read as a live
	// holder. That bound is inherited deliberately — this is the same reader
	// pogod itself uses to name the holder in "Cannot acquire pogod lock …
	// held by pid N", so the check and the component that noticed in 2026-08
	// share one definition of ownership rather than two that can disagree.
	LockPID   int  `json:"lock_pid"`
	LockPIDOK bool `json:"lock_pid_ok"`

	// LockPPID is the parent pid of LockPID, carried for the report only.
	// See the package doc: PPID 1 does not establish launchd parentage and
	// this field never reaches the verdict.
	LockPPID   int  `json:"lock_ppid"`
	LockPPIDOK bool `json:"lock_ppid_ok"`

	// LastExitReason is launchd's `last exit reason` line for the job, if
	// any. Report-only, like LockPPID — it describes a PREVIOUS instance and
	// says nothing about the one running now.
	LastExitReason string `json:"last_exit_reason,omitempty"`

	// ReadErr describes why a reading is missing, when the reason is known
	// and worth printing (launchctl unavailable, lockfile permission denied).
	// Empty when every reading that could be taken was taken.
	ReadErr string `json:"read_err,omitempty"`
}

// Result is the judgement plus enough of the reading to act on it.
type Result struct {
	Verdict Verdict     `json:"verdict"`
	Reason  string      `json:"reason"`
	Obs     Observation `json:"observation"`
}

// OK reports whether the check passed. True for Supervised alone — Unknown is
// not a pass.
func (r Result) OK() bool { return r.Verdict == Supervised }

// Check judges an Observation. Pure: no launchctl, no filesystem, no clock.
func Check(obs Observation) Result {
	res := Result{Obs: obs}

	switch {
	// Nothing loaded and nothing holding the lock: there is no daemon to
	// supervise. Not a pass, because the check established nothing about
	// supervision — it established that there is nothing running.
	case !obs.JobLoaded && !obs.LockPIDOK:
		res.Verdict = Unknown
		res.Reason = fmt.Sprintf("no %s job is loaded and nothing holds the pogod lockfile — there is no daemon here to supervise", obs.Label)

	// A lock holder with no job loaded is the fleet-owns-it half of mg-fa79's
	// either/or: one owner, no supervision claimed, nothing looping. It is a
	// legitimate configuration, but this check cannot call it supervised —
	// saying so would be the green-because-unmeasured failure.
	case !obs.JobLoaded && obs.LockPIDOK:
		res.Verdict = Unknown
		res.Reason = fmt.Sprintf("pid %d owns this POGO_HOME but no %s job is loaded — nothing this check can see supervises it. That is a valid single-owner configuration; it is not a supervised one", obs.LockPID, obs.Label)

	// Loaded job, no lock holder. Either the daemon is mid-start, or it is
	// down and launchd has not yet produced one. Unreadable, not displaced.
	case obs.JobLoaded && !obs.LockPIDOK:
		res.Verdict = Unknown
		res.Reason = fmt.Sprintf("%s is loaded but nothing holds the pogod lockfile — the daemon is starting, stopped, or the lockfile could not be read", obs.Label)

	// THE 2026-08-05 STATE. A loaded job with no live process, and a live
	// process owning the POGO_HOME. launchd is restarting something; it is not
	// this. Named explicitly rather than folded into the pid-mismatch case
	// because it is the shape that actually occurred and the shape that
	// `launchctl list` renders as "no PID, last exit 1" on a healthy box.
	case obs.JobLoaded && !obs.JobPIDOK:
		res.Verdict = Unsupervised
		res.Reason = fmt.Sprintf("%s is loaded but has NO live process, while pid %d owns this POGO_HOME and is serving. launchd is supervising nothing: whatever it restarts is not the running daemon, and a wedged pid %d would never be restarted", obs.Label, obs.LockPID, obs.LockPID)

	// Both live and disagreeing: two pogods, one of which launchd will
	// restart and one of which owns the POGO_HOME.
	case obs.JobPID != obs.LockPID:
		res.Verdict = Unsupervised
		res.Reason = fmt.Sprintf("%s is running pid %d, but pid %d holds the pogod lockfile — launchd supervises a process that does not own this POGO_HOME", obs.Label, obs.JobPID, obs.LockPID)

	default:
		res.Verdict = Supervised
		res.Reason = fmt.Sprintf("%s and the pogod lockfile name the same process (pid %d)", obs.Label, obs.JobPID)
	}
	return res
}

// String renders the one-line verdict a human reads first.
func (r Result) String() string {
	return fmt.Sprintf("%s: %s", r.Verdict, r.Reason)
}

// Text renders the verdict plus the readings it was made from, including the
// two report-only fields. They are printed BELOW the verdict and labelled with
// what they do not prove, because both have already been misread on this box:
// PPID 1 as launchd parentage (three times), and a lifetime exit counter as
// evidence of a current restart loop.
func (r Result) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", r.String())
	if r.Obs.ReadErr != "" {
		fmt.Fprintf(&b, "  reading incomplete: %s\n", r.Obs.ReadErr)
	}
	if r.Obs.JobPIDOK {
		fmt.Fprintf(&b, "  launchd job pid : %d\n", r.Obs.JobPID)
	} else if r.Obs.JobLoaded {
		fmt.Fprintf(&b, "  launchd job pid : none (job loaded, no live process)\n")
	} else {
		fmt.Fprintf(&b, "  launchd job pid : n/a (%s is not loaded)\n", r.Obs.Label)
	}
	if r.Obs.LockPIDOK {
		fmt.Fprintf(&b, "  lockfile holder : %d\n", r.Obs.LockPID)
	} else {
		fmt.Fprintf(&b, "  lockfile holder : none\n")
	}
	if r.Obs.LockPPIDOK {
		fmt.Fprintf(&b, "  holder's ppid   : %d — NOT EVIDENCE of who started it. A daemon orphaned by its spawner reparents to launchd (ppid 1) and is indistinguishable this way from one launchd started; the 2026-08 displacer was setsid() from a CLI and had ppid 1 throughout.\n", r.Obs.LockPPID)
	}
	if r.Obs.LastExitReason != "" {
		fmt.Fprintf(&b, "  last exit reason: %s — describes a PREVIOUS instance, not the one running now. It stays set across a repair and must not be read as a current fault.\n", r.Obs.LastExitReason)
	}
	return b.String()
}
