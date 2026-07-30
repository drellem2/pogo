package main

import (
	"log"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// The done-item reaper: completion, not merge, is what frees a slot (mg-56d1).
//
// THE DEFECT. Every automatic polecat teardown in this daemon hangs off ONE
// event: the refinery reporting a successful merge (reapMergedPolecat, gh #35).
// That is the right hook for a build polecat, whose deliverable IS a branch. It
// reaches nothing else. A triage polecat, an audit-only polecat, an
// investigation polecat — none of them merge anything. They finish by calling
// `mg done` themselves, and at that moment nothing in the fleet is watching:
//
//	merge-producing polecat  -> refinery merges -> pogod stops it, marks done  (automatic)
//	non-merge polecat        -> calls `mg done` -> nothing whatsoever          (manual)
//
// Measured 2026-07-30: polecat d764 finished its triage, delivered its packet,
// filed its successor item, went idle, and held one of five concurrency slots
// for 7m16s with two high-priority items queued and undispatchable. Nothing
// would have ended it but a coordinator noticing. Worse, the loss is invisible
// in the shape operators actually look at: `pogo agent list` says
// status=running, and only `pogo agent diagnose` says idle. It reads exactly
// like healthy saturation. Same family as mg-18d0 — the list reports LIVENESS,
// never PRODUCTIVITY.
//
// WHICH SIGNAL THIS KEYS ON, AND WHY IT IS NOT THE MERGE HOOK. The condition is
// the WORK ITEM reaching a terminal state (done/archived). Merge is one path to
// that state; `mg done` is the other; `done` is the general fact both produce.
// Extending the merge hook would have been the smaller change and would have
// fixed nothing here, because the polecats that leak are precisely the ones the
// refinery never hears about. So the merge hook stays exactly as it is — it has
// obligations this reaper does not (writing the completion, honouring
// --defer-done / PR-flow / post-merge-work, arming the deferred backstop) — and
// this is a second, independent detector keyed on the general condition.
//
// WHY IT POLLS RATHER THAN HOOKS THE TRANSITION. `mg done` runs in the
// POLECAT's own process, as a separate `mg` binary against the macguffin store.
// pogod is not in that call path and macguffin offers it no callback: OnMerged
// exists only because the refinery lives INSIDE pogod. So `done` can be
// observed but not delivered, and observing it is a per-polecat `mg show
// --json` on the heartbeat tick — the same shape as every other detector here.
// Calling this "event-driven" would be a claim the wiring cannot support.
//
// WHY `done` ALONE IS NOT SUFFICIENT. The polecat protocol tells a polecat to
// call `mg done` and then STAY ALIVE until the coordinator stops it, and work
// legitimately follows that call: mailing a verdict packet, filing a successor
// item, answering a coordinator follow-up. LivePolecatSet's doc names
// done-but-alive as "every polecat's NORMAL end state". Stopping on the `done`
// write alone would kill a polecat mid-sentence — the one outcome strictly
// worse than the leak, because a lost verdict is unrecoverable and a held slot
// is merely expensive.
//
// So the condition is a CONJUNCTION: the item is terminal AND the polecat has
// been quiet on its PTY for doneReapIdleGrace.
//
// GRACE PERIOD, NOT AN EXPLICIT "I AM FINISHED" SIGNAL. The alternative was to
// have polecats declare completion. It was rejected: a signal the polecat must
// remember to send fails for exactly the polecats that most need stopping — the
// ones that ran off the end of their protocol — and this tree has already
// measured that failure mode (mg-ddf7's ack deficit: an instructed step that a
// third of agents simply never perform). A grace period asks the polecat for
// nothing.
//
// The grace is measured from the LAST PTY WRITE, not from the `done`
// transition, and that choice does the post-`done` work case for free. A
// timer from the transition cannot tell a polecat still typing from one that
// stopped; PTY quiescence can. It also self-extends in the one case that
// worried us: an incoming coordinator mail is delivered as a PTY nudge, the
// answer is more PTY output, so a polecat handling a follow-up keeps resetting
// its own clock and is only reaped once it goes quiet again.
//
// WHAT IT DELIBERATELY CANNOT DO. It never marks an item done (the item is
// already done — that is the trigger), never mails, never restarts. The
// restart-suppression rules (mg-18d0/mg-8cdb) are therefore not in tension with
// it: stopping a finished polecat without replacing it is the correct end of a
// lifecycle, not remediation. And a polecat whose item is still `claimed` is
// untouchable no matter how long it idles — item state is the gate, idleness is
// only the qualifier that says "not mid-sentence". That is the acceptance
// control for this ticket: the healthy 42-minute idle polecat mid-work must
// survive, and it does so structurally rather than by tuning.

// doneReapIdleGrace is how long a polecat whose work item has reached a
// terminal state must be quiet on its PTY before it is stopped.
//
// Two minutes is chosen against the two failure costs, which are asymmetric.
// Too short kills a polecat mid-mail — unrecoverable. Too long re-creates the
// leak — merely expensive, and bounded. The post-`done` tail work a polecat
// actually does (one `mg mail send`, one `mg new`) is seconds; two minutes of
// unbroken silence is many multiples of it. The measured leak sat 7m16s, so
// this cuts it to under 25% while leaving a wide margin over any real tail. It
// is well inside deferDoneBackstopTimeout's 15 minutes, which bounds a
// different and looser question (did a deferred polecat's post-merge flow
// happen at all).
const doneReapIdleGrace = 2 * time.Minute

// doneReapRegistry is the slice of agent.Registry the done-item reaper needs.
// Narrow on purpose, like polecatReaper: it can read who is alive and it can
// stop one of them. It has no seam through which it could mark an item, mail,
// nudge, or spawn.
type doneReapRegistry interface {
	PolecatActivityAt(now time.Time) []agent.PolecatActivity
	Stop(name string, timeout time.Duration) error
}

// doneReaper stops polecats whose work item has concluded and which have gone
// quiet. See the file comment for the reasoning behind the condition; this type
// is only the mechanics.
type doneReaper struct {
	reg doneReapRegistry
	// itemDone reports whether a work item reached a terminal state
	// (client.MGWorkItemDone in production). An ERROR means "cannot tell", and
	// the polecat is left alone: a store we could not read is not evidence of
	// completion. That direction matches resolvePostMergeWork, and for the same
	// reason — the expensive mistake here is asserting a completion we have no
	// standing to assert.
	itemDone func(id string) (bool, error)
	// grace is the required PTY quiet window. Zero falls back to
	// doneReapIdleGrace; a negative value would stop a done polecat the instant
	// it is seen, which only a test should ask for.
	grace time.Duration

	// mu serialises Check against itself. The heartbeat fires every ~30s and
	// dispatches this in a goroutine, while a Check shells out to `mg show`
	// once per live polecat and can block on a slow store — so two Checks can
	// overlap. Without the guard the second would re-decide against the same
	// polecat the first is already stopping, and issue a duplicate Stop.
	mu       sync.Mutex
	checking bool
}

// newDoneReaper builds a reaper over reg. done is the terminal-state probe;
// grace of zero means doneReapIdleGrace.
func newDoneReaper(reg doneReapRegistry, done func(id string) (bool, error), grace time.Duration) *doneReaper {
	if grace == 0 {
		grace = doneReapIdleGrace
	}
	return &doneReaper{reg: reg, itemDone: done, grace: grace}
}

// Check samples the live polecats and stops every one whose work item has
// concluded and which has been quiet for at least the grace window. Safe to
// call from the heartbeat tick in a goroutine; overlapping calls return
// immediately.
//
// Returns the names it stopped, in the order it stopped them, so a test can
// assert BOTH arms of the acceptance control from one call: who was reaped, and
// by omission who was not.
func (d *doneReaper) Check(now time.Time) []string {
	if d == nil || d.reg == nil || d.itemDone == nil {
		return nil
	}
	d.mu.Lock()
	if d.checking {
		d.mu.Unlock()
		return nil
	}
	d.checking = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.checking = false
		d.mu.Unlock()
	}()

	var stopped []string
	for _, p := range d.reg.PolecatActivityAt(now) {
		if !d.eligible(p) {
			continue
		}
		done, err := d.itemDone(p.WorkItemID)
		if err != nil {
			// Cannot read the item: say so and leave the polecat running. This
			// is the only branch that logs a non-action, because it is the only
			// one where the reaper wanted to decide and could not.
			log.Printf("donereap: could not read work item %s for polecat %s (%v) — leaving it running (mg-56d1)", p.WorkItemID, p.Name, err)
			continue
		}
		if !done {
			continue
		}
		// The healthy, expected end of a non-merge polecat's life. Logged at
		// info, never mailed: there is no fault here to escalate, and an
		// escalation per completed triage would be pure noise.
		log.Printf("donereap: stopping polecat %s — work item %s is done and it has been idle %s (>= %s); freeing its slot (mg-56d1)",
			p.Name, p.WorkItemID, p.IdleFor.Truncate(time.Second), d.grace)
		if err := d.reg.Stop(p.Name, mergedPolecatStopTimeout); err != nil {
			// Losing the race with a clean exit lands here ("agent not found"),
			// as does a genuine stop failure. Either way the next tick re-decides
			// from a fresh snapshot, so there is nothing to retry inline.
			log.Printf("donereap: failed to stop polecat %s: %v", p.Name, err)
			continue
		}
		stopped = append(stopped, p.Name)
	}
	return stopped
}

// eligible applies the two cheap, local gates before the reaper spends an `mg
// show` on a polecat. Split out so the tests can pin each refusal
// independently of the store probe.
func (d *doneReaper) eligible(p agent.PolecatActivity) bool {
	// No work item: nothing to ask, so nothing to conclude. A polecat spawned
	// without one is not on this lifecycle at all.
	if p.WorkItemID == "" {
		return false
	}
	// Never wrote to its PTY: its idleness is UNMEASURABLE, not zero (see
	// PolecatActivity.HasOutput). A polecat seconds into spawn and one wedged
	// before its first turn look identical here, and neither is finished.
	if !p.HasOutput {
		return false
	}
	return p.IdleFor >= d.grace
}
