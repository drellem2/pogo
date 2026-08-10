package main

import (
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

// noteRespawnOutcome records the result of the 2s-deferred respawn scheduled by
// pogod's OnExit hook on the A6 `restart_failed` row.
//
// The three-way split IS the fix for mg-0208. The site used to be two-way —
// nil clears the row, anything else raises it — which is wrong because
// `RespawnFromGeneration` returns an error for two quite different reasons:
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
// Collapsing the second case into the first is what put five false
// `restart_failed:<name>` conditions in front of the coordinator at the
// 2026-08-09 22:12 fleet stop — one per auto_start crew member, and the first
// five things that channel ever emitted. It also destroyed the row's
// discriminator: a genuine respawn failure during normal operation rendered
// identically to shutdown noise, so the only way to read A6 was to ignore it.
//
// The declined case deliberately leaves the row ALONE rather than clearing it.
// A `restart_failed` raised by a real failure earlier is still true; a fleet
// stop afterwards is not evidence against it.
//
// Note what this does NOT do: it does not decide whether to respawn. That
// decision, and the suppression that keeps a stopped fleet stopped, live in
// agent.Registry.respawn and in ShouldRespawnAgent, and this function runs
// strictly downstream of both — so no bug here can leave a genuinely wedged
// agent unrestarted (the mg-6092 trap).
func noteRespawnOutcome(conds conditionRaiser, coordinator, agentName string, rerr error, now time.Time) {
	switch {
	case rerr == nil:
		conds.Clear(rowA6RestartPrefix+agentName, now)
		conds.flush()

	case agent.IsExpectedRespawnRefusal(rerr):
		// Log it — the attempt is real and worth seeing in pogod.log — but do
		// not alarm anyone. This is teardown working.
		log.Printf("agent %s: deferred respawn declined (%v); the fleet it was scheduled in is gone, "+
			"so this is a deliberate stop and NOT a restart failure — no condition raised",
			agentName, rerr)

	default:
		log.Printf("agent %s: restart failed: %v", agentName, rerr)
		// A6 (mg-342d). The respawn is ONE-SHOT: nothing tries again, so a crew
		// agent whose restart failed is simply gone, and until this row existed
		// the only trace was the log line above. There is no stall to detect and
		// no missed ack to count for an agent that is not running at all.
		conds.Raise(conditionRestartFailed(coordinator, agentName, rerr.Error()), now)
		conds.flush()
	}
}
