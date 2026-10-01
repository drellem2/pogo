package main

import (
	"log"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// requestedStopReapGrace is how long a REQUESTED stop of an agent pogod will
// not respawn holds that agent's mail-check rows before they are reaped.
//
// Long enough for any stop+start cycle — an external supervisor restarted its
// seats within 94-128ms in drellem2/pogo#217, and a person typing `pogo agent
// stop` then `pogo agent start` takes seconds. Short against the cost of
// holding: a mail-check fires every 10 minutes, so a row held 30s for an agent
// that does NOT come back is at worst one fire at nobody, before the reap.
const requestedStopReapGrace = 30 * time.Second

// mailCheckReapHolds is the drellem2/pogo#217 branch (b) fix: a requested stop
// of an agent pogod will not respawn no longer reaps its mail-check rows on the
// spot.
//
// WHAT SELECTS THE BRANCH. #217's reporter measured two mutually exclusive
// outcomes of the same supervised stop+start, each repeating per seat: (a) the
// mail-check survives and a false restart_failed is raised, or (b) no
// condition, but schedule_removed mail-check-<agent> reason=agent_gone ~2ms
// after the stop, so the restarted agent comes up deaf. The selector is the
// seat's own restart_on_crash. pogod's OnExit hook has exactly two arms:
//
//   - restart_on_crash = true takes the RESPAWN arm. It schedules the 2s-deferred
//     respawn, never touches the schedule — and, before #159's fix, raised A6
//     when the supervisor's start won that 2s race. Branch (a).
//   - restart_on_crash = false takes the CLEANUP arm. It never respawns, so it
//     can never raise A6 — but it reaped the mail-check eagerly, inside the exit
//     hook, milliseconds before the supervisor's start. Branch (b).
//
// Seat-scoped, exclusive, never both and never neither: that is one boolean in
// the seat's prompt frontmatter choosing between two arms of one if.
//
// THE FIX. On a stop someone asked for (stop_cause=request), the cleanup arm
// holds the rows for requestedStopReapGrace instead, and reaps them at the end
// only if no agent by that name is running by then. The hold also answers the
// GC sweep (registryLiveness reports a held agent UNKNOWN), because the sweep
// runs on every heartbeat tick and would otherwise reap the row inside the very
// window the hold exists for. A stop pogod's own machinery issued — a reaper, a
// fleet drain — is not followed by a start, so it keeps the eager reap.
type mailCheckReapHolds struct {
	mu    sync.Mutex
	until map[string]time.Time // schedule-agent alias -> end of hold
}

func newMailCheckReapHolds() *mailCheckReapHolds {
	return &mailCheckReapHolds{until: map[string]time.Time{}}
}

// hold starts (or extends) a hold on every non-empty alias, ending at until.
func (h *mailCheckReapHolds) hold(until time.Time, aliases ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range aliases {
		if a != "" && until.After(h.until[a]) {
			h.until[a] = until
		}
	}
}

// held reports whether alias is inside a hold at now. Expired holds are
// dropped on the way past. Safe on a nil receiver (no holds).
func (h *mailCheckReapHolds) held(alias string, now time.Time) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	u, ok := h.until[alias]
	if !ok {
		return false
	}
	if now.Before(u) {
		return true
	}
	delete(h.until, alias)
	return false
}

// mailCheckReaper is the slice of *scheduler.Scheduler the deferred reap uses.
type mailCheckReaper interface {
	RemoveMailChecksForAgent(now time.Time, aliases ...string) int
}

// reapMailChecksAfterExit is the cleanup arm's mail-check reap. requested and
// cause are the exited agent's a.StopRequested(). On a stop someone asked for
// it holds the rows for grace and returns; finish, run after grace, makes the
// decision. Otherwise it reaps immediately, as it always has.
//
// It returns finish rather than sleeping itself so that pogod can run it on the
// agent's own GoSafe goroutine and a test can run it without a 30s sleep.
// finish is nil when nothing was held.
func reapMailChecksAfterExit(sched mailCheckReaper, holds *mailCheckReapHolds, name, eventAgent string,
	requested bool, cause string, grace time.Duration, now time.Time,
	aliveNow func() bool) (finish func(now time.Time)) {
	if requested && cause == agent.StopCauseRequest && holds != nil {
		holds.hold(now.Add(grace), name, eventAgent)
		log.Printf("agent %s: stopped on request; holding its mail-check schedule(s) for %s in case it is "+
			"started again (drellem2/pogo#217)", name, grace)
		return func(now time.Time) {
			// A later stop of the same agent extended the hold: its own finish
			// owns the decision, so this one stands down.
			if holds.held(name, now) || holds.held(eventAgent, now) {
				return
			}
			if aliveNow != nil && aliveNow() {
				log.Printf("agent %s: running again after a requested stop; kept its mail-check schedule(s)", name)
				return
			}
			if n := sched.RemoveMailChecksForAgent(now, name, eventAgent); n > 0 {
				log.Printf("agent %s: not started again within %s of a requested stop; reaped %d stale "+
					"mail-check schedule(s)", name, grace, n)
			}
		}
	}
	// Eagerly reap this agent's mail-check loop so it stops firing the moment
	// the agent is gone, rather than on the next Tick sweep (gh
	// drellem2/macguffin #15). Match on both the bare name and the cat-/crew-
	// event identity a schedule may be addressed by.
	if n := sched.RemoveMailChecksForAgent(now, name, eventAgent); n > 0 {
		log.Printf("agent %s: reaped %d stale mail-check schedule(s)", name, n)
	}
	return nil
}
