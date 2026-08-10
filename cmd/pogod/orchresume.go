package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/server"
)

// The firing half of the restart obligation (mg-5af1). The arming half — and
// the whole argument for why the obligation cannot belong to the procedure that
// stopped the fleet — is in internal/server/orchestrationresume.go.
//
// This is a detector on the heartbeat tick, in the shape every other detector
// here has, with one difference that has to be said out loud: it ACTS. Of the
// twelve subsystems riding hb.OnTick, exactly two do anything but report, and
// the other one (doneReap) only stops a process whose work is provably over.
// This one starts a fleet.
//
// WHY ACTING IS RIGHT HERE AND REPORTING IS NOT. A report goes to the
// coordinator's mailbox. The coordinator is a crew agent. Crew agents are
// exactly what is not running when this condition is true — on 2026-08-08 the
// scheduler delivered 198 consecutive fires to a pm-pogo that was not there, and
// two daily sweeps were "delivered" to nobody. Every reporting channel in this
// daemon is downstream of the fleet being up, so a report-only version of this
// detector would be a mechanism whose only output is destroyed by the condition
// it detects. It mails anyway — after acting, so the mail lands in a fleet that
// can read it.
//
// WHAT BOUNDS IT. Three things, and they are what make the positive-control arm
// of the acceptance pass rather than the negative one being sufficient:
//
//  1. It cannot act before the deadline, which is armed at the stop and sized so
//     that an ordinary stop/restart cycle finishes inside it. A normal deploy
//     never meets the precondition, so it cannot be fought.
//  2. It cannot act when the mode is full. There is no path from "the fleet is
//     up" to a restart here — StartOrchestration is called only from the
//     overdue branch.
//  3. It cannot act twice on one stop. The restore disarms the obligation
//     (transitionToFull calls disarmResume), so the second tick after a
//     successful resume finds nothing armed.
//
// WHAT IT CANNOT COVER, STATED. If pogod is killed while index-only, this
// mechanism dies with it. That is not a gap in the covered case — a restarted
// pogod boots into full mode by construction, so the successor is already in the
// state a surviving obligation would have restored. It IS a gap in the case
// where pogod is killed and never restarted, and that is a dead-daemon problem
// belonging to the tier-1 reaper, not to a watcher that needs pogod alive to
// run at all.

// orchResumeServer is the slice of *server.Server this needs, as an interface
// so the acceptance controls can drive a stop/restart sequence — including one
// that fails — without a live daemon on :10000.
type orchResumeServer interface {
	ResumeObligation() (server.ResumeObligation, bool)
	StartOrchestrationWithCause(server.Cause) (server.StartReport, error)
}

// orchResumer restores a fleet whose stopper never came back.
type orchResumer struct {
	// get resolves the server late. pogod builds its heartbeat OnTick closure
	// before server.New runs, so a resumer holding a *Server captured at
	// construction would hold nil forever.
	get   func() orchResumeServer
	raise func(pogodCondition, time.Time)
	clear func(string, time.Time)
	// to is the mailbox the alarm goes to — the coordinator, per the routing
	// rule in conditions.go.
	to    string
	retry time.Duration

	mu          sync.Mutex
	lastAttempt time.Time
	// attemptedFor is the Since of the obligation the last attempt acted on, so
	// a NEW stop is never throttled by the previous stop's failed retry.
	attemptedFor time.Time
}

// orchResumeConditionID is the suppression key. One key for the whole condition
// rather than one per stop: two overlapping breaches are the same fault with
// the same remedy, and Clear() on the healthy path means a later breach still
// reads as a fresh transition and mails.
const orchResumeConditionID = "orchestration_left_stopped"

func newOrchResumer(get func() orchResumeServer, ann *conditionAnnunciator, to string, cfg config.OrchestrationResumeConfig) *orchResumer {
	retry := cfg.Retry
	if retry <= 0 {
		retry = config.DefaultOrchestrationResumeRetry
	}
	return &orchResumer{
		get:   get,
		raise: func(c pogodCondition, now time.Time) { ann.Raise(c, now); ann.flush() },
		clear: func(id string, now time.Time) { ann.Clear(id, now); ann.flush() },
		to:    to,
		retry: retry,
	}
}

// Check is the heartbeat tick. It returns true when it performed a restore, for
// the tests; nothing in production reads the value.
func (r *orchResumer) Check(now time.Time) bool {
	if r == nil || r.get == nil {
		return false
	}
	srv := r.get()
	if srv == nil {
		return false
	}

	ob, armed := srv.ResumeObligation()
	if !armed {
		// The fleet is up. Clear so that a later breach mails as a fresh
		// transition instead of inheriting a resolved incident's quiet window —
		// the half that is easy to forget because nothing fails when you do.
		r.mu.Lock()
		r.attemptedFor = time.Time{}
		r.mu.Unlock()
		r.clear(orchResumeConditionID, now)
		return false
	}
	if !ob.Overdue(now) {
		// Stopped, but inside the window its stopper declared or was given.
		// THIS IS THE POSITIVE CONTROL: an ordinary stop/restart cycle lives
		// entirely in this branch and the resumer never touches it.
		return false
	}

	r.mu.Lock()
	if ob.Since.Equal(r.attemptedFor) && now.Sub(r.lastAttempt) < r.retry {
		r.mu.Unlock()
		return false
	}
	r.lastAttempt = now
	r.attemptedFor = ob.Since
	r.mu.Unlock()

	down := now.Sub(ob.Since).Round(time.Second)
	log.Printf("pogod: ⚠ orchestration has been stopped for %s and its deadline passed at %s — "+
		"restoring the fleet, because whoever stopped it has not (mg-5af1)",
		down, ob.Due.UTC().Format(time.RFC3339))

	report, err := srv.StartOrchestrationWithCause(server.ResumeCause(ob, now))

	// Raised AFTER the attempt so the notice can say what came back, and raised
	// on BOTH outcomes: a fleet that was left down long enough for this to fire
	// is news even when the recovery worked. The Clear below is what keeps the
	// success case from re-notifying.
	r.raise(conditionOrchestrationLeftStopped(r.to, ob, down, report, err), now)
	if err != nil {
		log.Printf("pogod: ⚠ resume FAILED — the fleet is still down: %v", err)
		return false
	}
	// Belt and braces: transitionToFull already disarmed the obligation, so the
	// next tick clears on its own. Clearing here too means the resolved event is
	// on the spine in the same tick as the raise, which is what makes "raised
	// and recovered" distinguishable from "raised and still broken".
	r.clear(orchResumeConditionID, now)
	return true
}

// conditionOrchestrationLeftStopped is the alarm.
//
// It is NOT a row of the 2026-07-30 enumeration — those are boot-time and
// decision-point facts inside pogod, and this is a sampled state of the fleet.
// It uses the same annunciator because the annunciator is what owns suppression,
// the mail seam and the event contract, and having a second copy of those would
// be fourteen-notifiers reasoning at n=15. Row carries the originating work item
// instead of an enumeration row; see the Row field's doc.
func conditionOrchestrationLeftStopped(to string, ob server.ResumeObligation, down time.Duration, report server.StartReport, err error) pogodCondition {
	who := ob.Cause.Client
	if who == "" {
		who = ob.Cause.Agent
	}
	if who == "" {
		who = ob.Cause.Detail
	}
	if who == "" {
		who = "an UNATTRIBUTED caller (no X-Pogo-Client, no X-Pogo-Agent)"
	}
	if ob.Cause.ClientPid != "" {
		who += " (pid " + ob.Cause.ClientPid + ")"
	}

	outcome := "pogod restored full mode itself."
	subject := fmt.Sprintf("fleet was left stopped for %s — pogod restarted it", down)
	if err != nil {
		outcome = "pogod tried to restore full mode and FAILED: " + err.Error()
		subject = fmt.Sprintf("fleet has been stopped for %s and pogod could NOT restart it", down)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Orchestration was stopped at %s by %s.\n",
		ob.Since.UTC().Format(time.RFC3339), who)
	fmt.Fprintf(&b, "It was due back by %s. It was not.\n\n", ob.Due.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "%s\n\n", outcome)
	if err == nil {
		fmt.Fprintf(&b, "What came back: refinery_restarted=%v started=%v already_running=%v parked=%v failed=%d%s\n\n",
			report.RefineryRestarted, report.AgentsStarted, report.AgentsAlreadyRunning,
			report.AgentsParked, len(report.AgentsFailed),
			func() string {
				if report.AgentStartSkipped != "" {
					return " (no sweep ran: " + report.AgentStartSkipped + ")"
				}
				return ""
			}())
	}

	b.WriteString("WHAT IT COSTS WHILE UNFIXED\n")
	b.WriteString("  Every second orchestration is stopped, /agents, /refinery and /scheduler\n")
	b.WriteString("  return 503: no polecat is dispatched, no merge runs, no schedule fires, and\n")
	b.WriteString("  no crew agent is alive to notice. On 2026-08-08 that state lasted 33 hours\n")
	b.WriteString("  while the scheduler logged 198 successful deliveries to an absent pm-pogo.\n")
	b.WriteString("  The fleet is back now; what is NOT fixed is whatever stopped it and died.\n\n")

	b.WriteString("WHAT TO DO\n")
	b.WriteString("  1. Find out what stopped the fleet and why it never restarted it. The stop\n")
	b.WriteString("     is attributed above and on the spine:\n")
	b.WriteString("       pogo events list --type=server_mode_changed\n")
	b.WriteString("  2. Confirm the fleet really is back — full mode is not the same fact as a\n")
	b.WriteString("     populated crew:\n")
	b.WriteString("       pogo server status\n")
	b.WriteString("       curl -s http://127.0.0.1:10000/server/mode\n")
	if err != nil {
		b.WriteString("  3. THE RESTORE FAILED. Start it by hand, now:\n")
		b.WriteString("       pogo server start\n")
		b.WriteString("\n")
		b.WriteString("  NOTE ON THIS PARTICULAR NOTICE: the fleet is still down as it is written,\n")
		b.WriteString("  so there may be NO IN-FLEET READER for it — the coordinator is one of the\n")
		b.WriteString("  agents orchestration is stopped for. It is in the maildir and will be read\n")
		b.WriteString("  whenever the crew is next up, which may be after a human notices. Closing\n")
		b.WriteString("  that residue needs an out-of-process alarm and this is not one.\n")
	} else {
		b.WriteString("  3. If the stop was DELIBERATE and meant to last, declare it next time so\n")
		b.WriteString("     this does not fight you:  pogo server stop --hold 4h\n")
	}
	b.WriteString("\n")

	b.WriteString("WHY THIS IS MAIL\n")
	b.WriteString("  Because a log line was the previous answer and it did not work. This is\n")
	b.WriteString("  the ownership half of mg-5af1: mg-6d2f (974edc1) made a DEPLOY that leaves\n")
	b.WriteString("  the fleet stopped exit loudly, but the 2026-08-08 run never reached an exit\n")
	b.WriteString("  at all — it hung for 31h39m — so a loud exit would not have fired. The\n")
	b.WriteString("  restart obligation needed a holder that outlives the procedure, and pogod\n")
	b.WriteString("  is it. This notice is that holder reporting that it had to act.\n")

	return pogodCondition{
		ID:  orchResumeConditionID,
		Row: "mg-5af1",
		To:  to,
		Detail: fmt.Sprintf("orchestration stopped %s by %s, overdue at %s, restore_err=%v",
			ob.Since.UTC().Format(time.RFC3339), who, ob.Due.UTC().Format(time.RFC3339), err),
		// Fingerprinted on the STOP, not on the elapsed time: a failed restore
		// re-attempted every minute must not read as a materially different
		// condition and mail every minute.
		Fingerprint: "stop:" + ob.Since.UTC().Format(time.RFC3339),
		Subject:     subject,
		Body:        b.String(),
	}
}
