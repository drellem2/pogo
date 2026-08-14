package server

import (
	"fmt"
	"sync"
	"time"
)

// The RESUME OBLIGATION: whoever stops the fleet is not allowed to be the only
// thing that can start it again (mg-5af1).
//
// THE DEFECT THIS EXISTS FOR. Every procedure that stops orchestration is a
// two-step sequence — stop, then restart — performed by one process. `pogo
// service install` quiesces the crew and restores it seven steps later
// (installorchestration.go); an operator's `pogo server stop` is undone by their
// later `pogo server start`; a redeploy stops a fleet it intends to bring back.
// In every one of them the restart is contingent on the stopper surviving, and
// nothing else in the tree is responsible for the fleet being up.
//
// On 2026-08-08 that contingency was collected. The crew was stopped cleanly at
// 00:44:20Z — five agents, exit_code=0, reason=requested, 0.42s apart. The
// nightly deploy then hung at 02:00:05Z inside an unbounded git call and did not
// return for 31h39m. The fleet stayed dark for 33 hours. Every supervisor
// behaved correctly: a requested stop is not a crash, so restart_on_crash did
// not fire, and nothing is entitled to undo a deliberate shutdown it cannot
// distinguish from an intended one.
//
// THE MECHANISM IS MEASURED, THE CALLER IS NOT (mg-a95f). This paragraph used to
// say "one StopAll-shaped command", which was an inference from the shape.
// ~/Library/Logs/pogo/pogod.log.1 settles the first half:
//
//	2026/08/08 01:44:19 server: transitioning to index-only mode
//	2026/08/08 01:44:20 agent architect: exited (err=<nil>)      (+4 more, to 01:44:21)
//	2026/08/08 01:44:21 refinery: stopped
//	2026/08/08 01:44:21 server: now in index-only mode
//	2026/08/08 01:44:22 agent architect: restart failed: registry shut down  (+4 more)
//
// (local +01:00; 01:44:19 local is 00:44:19Z.) That is transitionToIndexOnly,
// so it was one command, and the shutdown latch is why every restart_on_crash
// respawn was refused three seconds later. It also settles what the fire it
// PRECEDED cannot have been: predeploy-stop-noncritical-mayor was delivered at
// 00:45:08Z, 48s after the fleet was already down.
//
// WHO POSTED IT REMAINS UNIDENTIFIED, and the reason is worth keeping because
// it is the trap, not the answer. The obvious query — `pogo events list
// --type=server_mode_changed` around that minute — returns nothing, and that
// nothing is a NULL INSTRUMENT rather than a negative result. modeaudit.go
// landed at 2026-08-07T18:49:27Z (bce9b08); the pogod that logged the line above
// had been running since 17:37:28Z at the latest (its crew's duration_seconds at
// exit), so it predated its own emitter by an hour. The first
// server_mode_boot in events.log is 2026-08-09T09:41:19Z and the first
// server_mode_changed is 2026-08-09T22:12:22Z — both more than 33 hours after
// the stop. Merged is not running; see the same distinction in modeaudit.go.
//
// What was ruled out, so it is not re-derived a fourth time: the nightly deploy
// (its log starts at 02:00:05Z, 1h16m later), a polecat (the mayor measured
// `polecats: 0` at 00:44:16Z), an interactive `pogo server stop` (~/.zsh_history
// has no such entry and has not been written since Jul 7), and `pogo service
// install` / `install-deploy` — the leading candidate for the ANALOGOUS 08-07
// stop, and the reason installorchestration.go quiesces at all — since neither
// com.pogo.daemon.plist (Apr 28) nor com.pogo.deploy.plist (Aug 7 14:03) was
// rewritten at that time. Ruling out is not identifying, and this has happened
// once in the observable log.
//
// mg-6d2f (974edc1) closed the ALERTING half of this: a deploy run that leaves
// the fleet stopped now exits 11/12 and says the fleet is down. That is real,
// and it is not ownership. The 08-08 run never reached an exit at all, so a loud
// exit would not have fired. Exiting loudly and holding the obligation are
// different jobs.
//
// WHY IT LIVES HERE, IN pogod, AND NOT IN THE PROCEDURE. The constraint is that
// the holder must outlive the stopper, which rules out everything the stopper
// owns: a shell `trap` dies with the shell, a background child dies with its
// process group, and a deferred restore inside the procedure is exactly the
// thing that did not run. It also rules out the crew — a watcher held by the
// fleet cannot fire when the fleet is what is down, which is the same constraint
// mg-a14c carries.
//
// pogod satisfies both. It is a separate process from every stopper, it stays up
// across the whole index-only window (that is what index-only MEANS: the daemon
// is serving, orchestration is not), and its heartbeat loop is not gated on the
// run mode. So the deadline is armed at the transition site — the one place
// every stop must pass through, the same argument Cause makes above — and fired
// by cmd/pogod's resumer riding the heartbeat.
//
// WHAT ARMS AND WHAT DISARMS. Arming happens inside transitionToIndexOnly,
// under the transition lock, before the stop work: an obligation recorded after
// StopAll would be missing for the up-to-5s window in which the fleet is already
// dark, and missing entirely if the process died mid-drain. Disarming happens in
// transitionToFull, and it happens for a return to full mode by ANY route —
// including the resumer's own. Nothing latches.
//
// A REMEDY IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT, so here is this one
// checked against its own finding — "a two-step obligation whose owner does not
// outlive step 1", and "a watcher inside the thing it watches":
//
//   - The ARM is not a step the stopper can skip. There is exactly one site that
//     sets ModeIndexOnly and this is it; SetModeWithCause and the HTTP handler
//     both route through StopOrchestrationWithCause. A stopper cannot stop the
//     fleet without arming.
//   - The FIRE has its own step 2 — the alarm — and it runs after the restore in
//     the same goroutine. A pogod that dies between the two leaves the fleet UP
//     and nobody told. That is a real residue and it is the harmless direction:
//     the obligation is discharged, so the outcome is a missing notice, not a
//     dark fleet.
//   - The TICK it rides has no external watcher. pogod's heartbeat supervises
//     every com.pogo.* job except pogod itself — a child cannot reap its parent
//     and launchd will not (mg-50e0) — so a wedged heartbeat silently disables
//     this along with every other detector on that tick. That is an INHERITED
//     single point of failure, not one this adds, and closing it needs the
//     out-of-process instrument mg-a14c is about.
//
// THE ONE HOLE, STATED RATHER THAN PAPERED OVER. The obligation is in memory and
// dies with the process. That is correct here and not luck: a pogod that is
// killed while index-only cannot come back index-only, because server.New
// hard-codes ModeFull and no config key selects otherwise (the same fact
// verify_orchestration's assertion rests on). So the successor process is
// already in the state the obligation would have restored. The case that is NOT
// covered is a pogod that is killed and never restarted at all — a dead daemon,
// which is the tier-1 reaper's problem and explicitly not this one's.

// DefaultResumeGrace is how long orchestration may stay stopped before pogod
// concludes that whoever stopped it is not coming back.
//
// Sized against the two windows it has to separate. A legitimate stop/restart
// cycle is short: `pogo service install`'s quiesce-to-handoff is seconds to a
// couple of minutes, and an operator's stop/start round trip is however long
// they take to type the second command. The failure it must catch ran 33 hours.
// Fifteen minutes is comfortably above the first and irrelevantly below the
// second, and it is long enough that a human who stopped the fleet to look at
// something has time to look at it before being interrupted.
//
// It is not a tuning knob for "how long may the fleet be down" — anything
// wanting longer should DECLARE it (see the hold below) rather than widen the
// default for everyone.
const DefaultResumeGrace = 15 * time.Minute

// ResumeObligation is the record of one stop that owes a restart.
type ResumeObligation struct {
	// Since is when orchestration was stopped.
	Since time.Time
	// Due is when the fleet must be back. ZERO means no deadline: either the
	// stopper declared a hold longer than any deadline, or the mechanism is
	// configured off. A zero Due is a deliberate statement that nobody is
	// holding this obligation, not an absent one.
	Due time.Time
	// Hold is what the stopper asked for, zero when it asked for nothing and
	// took the default grace. Recorded so a resume decision can say whether it
	// is overriding a declaration or filling a silence.
	Hold time.Duration
	// Cause is the attribution captured at the stop — who stopped the fleet.
	// This is the whole reason the alarm can name a party rather than reporting
	// an unexplained outage.
	Cause Cause
}

// Overdue reports whether the fleet should already be back by now.
func (o ResumeObligation) Overdue(now time.Time) bool {
	return !o.Due.IsZero() && !now.Before(o.Due)
}

// resumeState is the Server's half of the obligation. Split into its own type
// so the zero value is unambiguously "nothing is stopped".
type resumeState struct {
	mu    sync.Mutex
	grace time.Duration
	ob    *ResumeObligation
	now   func() time.Time
}

func (r *resumeState) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// SetResumeGrace sets the default deadline applied to a stop that declares no
// hold of its own. A grace of zero or less DISARMS the mechanism: stops are
// still recorded (so `/server/mode` can report how long the fleet has been
// down) but no deadline is set and nothing will restore it.
func (s *Server) SetResumeGrace(d time.Duration) {
	s.resume.mu.Lock()
	defer s.resume.mu.Unlock()
	s.resume.grace = d
}

// ResumeGrace reports the configured default deadline.
func (s *Server) ResumeGrace() time.Duration {
	s.resume.mu.Lock()
	defer s.resume.mu.Unlock()
	return s.resume.grace
}

// SetResumeClock overrides the wall clock used to stamp obligations.
//
// A TEST SEAM, in the same spirit as SetEmitter: production never calls it and
// nil leaves time.Now in place. It is exported because the acceptance arms for
// this mechanism live in cmd/pogod — the firing half is there — and they have to
// drive a 33-hour outage without waiting 33 hours. The alternative was to have
// those tests derive their clock from whatever the server happened to stamp,
// which works but makes the incident being reproduced unreadable in the test.
func (s *Server) SetResumeClock(fn func() time.Time) {
	s.resume.mu.Lock()
	defer s.resume.mu.Unlock()
	s.resume.now = fn
}

// ResumeObligation returns the outstanding obligation, and false when
// orchestration is not stopped.
func (s *Server) ResumeObligation() (ResumeObligation, bool) {
	s.resume.mu.Lock()
	defer s.resume.mu.Unlock()
	if s.resume.ob == nil {
		return ResumeObligation{}, false
	}
	return *s.resume.ob, true
}

// armResume records the obligation created by a stop. hold is what the stopper
// declared; zero or less means "no declaration", which takes the default grace.
func (s *Server) armResume(cause Cause, hold time.Duration) ResumeObligation {
	s.resume.mu.Lock()
	defer s.resume.mu.Unlock()

	now := s.resume.clock()
	ob := ResumeObligation{Since: now, Hold: hold, Cause: cause}
	window := hold
	if window <= 0 {
		window = s.resume.grace
	}
	if window > 0 {
		ob.Due = now.Add(window)
	}
	s.resume.ob = &ob
	return ob
}

// disarmResume clears the obligation. Called on every return to full mode, by
// whatever route, so the resumer's own restore closes the obligation it acted
// on rather than leaving it to fire twice.
func (s *Server) disarmResume() {
	s.resume.mu.Lock()
	defer s.resume.mu.Unlock()
	s.resume.ob = nil
}

// StopReport describes what a stop did, and — the part that is new — when the
// fleet is due back and who is holding that.
//
// It exists for the same reason StartReport does. "mode": "index-only" is a
// true and useless answer to "what just happened to the fleet": it is equally
// true of a two-second quiesce and of the start of a 33-hour outage. The
// difference between them is the deadline, so the deadline is in the response
// where the caller that just stopped the fleet cannot avoid seeing it.
type StopReport struct {
	Mode string `json:"mode"`
	// AlreadyStopped means orchestration was already index-only, so this call
	// changed nothing — including the existing obligation, which keeps running
	// from the ORIGINAL stop. A second stopper does not get to reset the clock.
	AlreadyStopped bool `json:"already_stopped,omitempty"`
	// StoppedSince is when the fleet went down, RFC3339.
	StoppedSince string `json:"stopped_since,omitempty"`
	// ResumeDue is when pogod will restore full mode by itself, RFC3339. Empty
	// means NOBODY is holding the obligation — the stopper is the only thing
	// that can bring the fleet back.
	ResumeDue string `json:"resume_due,omitempty"`
	// ResumeHold echoes a declared hold, so a caller can see its declaration
	// was understood rather than silently discarded.
	ResumeHold string `json:"resume_hold,omitempty"`
}

// resumeReport renders an obligation for the wire.
func resumeReport(mode string, ob ResumeObligation, already bool) StopReport {
	rep := StopReport{
		Mode:           mode,
		AlreadyStopped: already,
		StoppedSince:   ob.Since.UTC().Format(time.RFC3339),
	}
	if !ob.Due.IsZero() {
		rep.ResumeDue = ob.Due.UTC().Format(time.RFC3339)
	}
	if ob.Hold > 0 {
		rep.ResumeHold = ob.Hold.String()
	}
	return rep
}

// resumeCause is the attribution the resumer stamps on the restore it performs.
// Its Detail names the party that owed the restart, so the audit trail reads as
// "pogod finished somebody else's sequence" rather than as an unattributed
// transition of unknown origin.
func ResumeCause(ob ResumeObligation, now time.Time) Cause {
	who := ob.Cause.Client
	if who == "" {
		who = ob.Cause.Agent
	}
	if who == "" {
		who = ob.Cause.Detail
	}
	if who == "" {
		who = "an unattributed caller"
	}
	return Cause{
		Trigger: "resume-deadline",
		Detail: fmt.Sprintf(
			"orchestration stopped %s ago by %s and not restored by its deadline (mg-5af1)",
			now.Sub(ob.Since).Round(time.Second), who),
		ClientPid: ob.Cause.ClientPid,
	}
}
