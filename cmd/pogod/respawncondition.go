package main

import (
	"errors"
	"log"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// conditionRaiser is the slice of *conditionAnnunciator that the deferred
// respawn's outcome path uses. Narrow on purpose: the outcome of one respawn is
// one row, and a test should not have to build a whole annunciator (with its
// store, its mailer and its wake retry loop) to assert which way that row went.
type conditionRaiser interface {
	Raise(c pogodCondition, now time.Time)
	Clear(id string, now time.Time)
	flush()
}

// respawnOutcome is what one 2s-deferred respawn reports to the A6 row.
type respawnOutcome struct {
	// Agent is the name the respawn was for.
	Agent string
	// Err is RespawnFromGeneration's result.
	Err error
	// StopRequested and StopCause are the exiting agent's a.StopRequested():
	// whether the exit that scheduled this respawn was a Stop someone asked for,
	// and which path asked. They change the WORDING of a raise, never whether
	// one happens (drellem2/pogo#159).
	StopRequested bool
	StopCause     string
	// AliveNow re-checks, at the moment of raising, whether an agent by this name
	// is running. nil means "cannot tell", which raises as before.
	AliveNow func() bool
}

// registryAgentAlive is respawnOutcome.AliveNow against the live registry: is
// the agent currently registered under name running? It asks by NAME, not of
// the *Agent that exited — that one is dead by construction, and whatever is
// alive now is a different process that won the respawn race.
func registryAgentAlive(reg *agent.Registry, name string) func() bool {
	return func() bool {
		cur := reg.Get(name)
		return cur != nil && cur.Alive()
	}
}

// noteRespawnOutcome records the result of the 2s-deferred respawn scheduled by
// pogod's OnExit hook on the A6 `restart_failed` row.
//
// The split IS the fix for mg-0208 and drellem2/pogo#159. The site used to be
// two-way — nil clears the row, anything else raises it — which is wrong
// because `RespawnFromGeneration` returns an error for quite different reasons:
//
//   - It TRIED and FAILED. The agent is configured to come back, pogod
//     attempted it, and it did not. Nothing will try again (the respawn is
//     one-shot), so that agent is simply gone and A6 exists to say so.
//
//   - It DECLINED, because the fleet the respawn belonged to no longer exists:
//     the shutdown latch is up, or the generation moved. Both are the guards
//     doing their job during a deliberate stop, and the agent is not "gone" in
//     any sense that wants a mail — it was stopped on purpose, one second
//     earlier, by the operator now being alarmed about it.
//
//   - It found the agent ALREADY RUNNING (agent.ErrRespawnAgentAlive).
//     Something else started it inside the backoff — `pogo agent start`, an
//     external supervisor's stop+start cycle, a park then a wake. This is the
//     state the respawn was trying to produce, so it is handled like the nil
//     case: the row is CLEARED. Raising here mailed "<name> CRASHED and its
//     restart FAILED — that agent is gone" about an agent that was demonstrably
//     alive, with "is still running" in the notice's own DETAIL line
//     (drellem2/pogo#159, #217). It also defeated the row's own rate limit: the
//     operator's start cleared the row (noteAgentStarted) and this re-raised it
//     2s later as reason=new, so every supervised cycle mailed `human` afresh
//     (drellem2/pogo#218).
//
// Collapsing the second case into the first is what put five false
// `restart_failed:<name>` conditions in front of the coordinator at the
// 2026-08-09 22:12 fleet stop — one per auto_start crew member, and the first
// five things that channel ever emitted. It also destroyed the row's
// discriminator: a genuine respawn failure during normal operation rendered
// identically to shutdown noise, so the only way to read A6 was to ignore it.
//
// The declined case deliberately leaves the row ALONE rather than clearing it.
// A `restart_failed` raised by a real failure earlier is still true; a fleet
// stop afterwards is not evidence against it. The already-running case clears
// it on the opposite evidence: an agent that is running cannot still be gone.
//
// LIVENESS IS RE-CHECKED BEFORE ANY RAISE. The sentinel above names one way
// for the respawn to lose to a live agent; AliveNow covers the class — any
// error at all, from any path, about an agent that is running by the time we
// would say it is gone. A6's sentence is "that agent is gone", and a raise
// must not contradict the process table. The check reads state at raise time,
// so an agent that comes up and dies again before it still raises, which is
// correct: then it really is gone.
//
// Note what this does NOT do: it does not decide whether to respawn. That
// decision, and the suppression that keeps a stopped fleet stopped, live in
// agent.Registry.respawn and in ShouldRespawnAgent, and this function runs
// strictly downstream of both — so no bug here can leave a genuinely wedged
// agent unrestarted (the mg-6092 trap).
func noteRespawnOutcome(conds conditionRaiser, coordinator string, o respawnOutcome, now time.Time) {
	rerr := o.Err
	switch {
	case rerr == nil:
		conds.Clear(rowA6RestartPrefix+o.Agent, now)
		conds.flush()

	case agent.IsExpectedRespawnRefusal(rerr):
		// Log it — the attempt is real and worth seeing in pogod.log — but do
		// not alarm anyone. This is teardown working.
		log.Printf("agent %s: deferred respawn declined (%v); the fleet it was scheduled in is gone, "+
			"so this is a deliberate stop and NOT a restart failure — no condition raised",
			o.Agent, rerr)

	case errors.Is(rerr, agent.ErrRespawnAgentAlive):
		log.Printf("agent %s: deferred respawn found it already running (%v) — something else started it "+
			"inside the backoff; that is the outcome the respawn wanted, so restart_failed:%s is CLEARED, not raised",
			o.Agent, rerr, o.Agent)
		conds.Clear(rowA6RestartPrefix+o.Agent, now)
		conds.flush()

	case o.AliveNow != nil && o.AliveNow():
		log.Printf("agent %s: deferred respawn failed (%v), but %s is running now — not raising "+
			"restart_failed about a live agent; clearing it", o.Agent, rerr, o.Agent)
		conds.Clear(rowA6RestartPrefix+o.Agent, now)
		conds.flush()

	default:
		log.Printf("agent %s: restart failed: %v", o.Agent, rerr)
		// A6 (mg-342d). The respawn is ONE-SHOT: nothing tries again, so a crew
		// agent whose restart failed is simply gone, and until this row existed
		// the only trace was the log line above. There is no stall to detect and
		// no missed ack to count for an agent that is not running at all.
		c := conditionRestartFailed(coordinator, o.Agent, rerr.Error())
		if o.StopRequested {
			c = conditionRestartFailedAfterStop(coordinator, o.Agent, o.StopCause, rerr.Error())
		}
		conds.Raise(c, now)
		conds.flush()
	}
}

// noteAgentStarted retires the rows that assert agentName is NOT running —
// A6 `restart_failed:` and A5 `autostart_failed:` — because it now is. It is
// wired to the registry's OnStart hook, which fires on every successful start.
//
// Until mg-f474 each row was cleared only by the path that raised it: A6 by a
// successful DEFERRED respawn (noteRespawnOutcome, above) and A5 by the next
// boot's auto-start sweep. An agent brought back any other way — `pogo agent
// start`, `pogo agent wake`, a boot auto-start after an A6 — never reached
// either, so the row outlived the fault indefinitely. Three A6 rows asserted
// "that agent is gone" about agents that had been running for 150h, 29 days
// after they were raised. The cost is not only a wrong display: a live row is
// suppression state (conditionAnnunciator.Raise). A GENUINE later A6 for that
// agent is judged against the stale row rather than as "new" — suppressed
// outright if its detail matches, or within the hour since the stale row last
// mailed — and when it is mailed it inherits the stale row's first_seen, so it
// reports a month-old fault instead of the one that just happened.
//
// Clearing on start does not hide a real failure. A6 and A5 each describe an
// agent that is absent; once the agent is running that is false, and if it
// crashes again and the respawn fails again, the row is raised afresh — and,
// because the stale one is gone, notified afresh.
func noteAgentStarted(conds conditionRaiser, agentName string, now time.Time) {
	conds.Clear(rowA6RestartPrefix+agentName, now)
	conds.Clear(rowA5AutoStartPrefix+agentName, now)
	conds.flush()
}
