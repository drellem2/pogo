package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// mergedPolecatStopTimeout bounds how long reapMergedPolecat waits for a
// SIGTERMed polecat to exit before force-killing it — same budget as the
// pogo agent stop HTTP handler.
const mergedPolecatStopTimeout = 5 * time.Second

// deferDoneBackstopTimeout bounds how long a --defer-done polecat may stay
// alive after its branch merges before the backstop reaps + escalates it (gh
// drellem2/pogo #81). It is deliberately generous — a healthy post-merge flow
// (open PR, verify, mail PR URL, mg done) finishes in well under a minute — so
// the backstop only fires on a genuinely stuck polecat, never on a slow-but-
// live one. A deferred polecat that never ends its lifecycle would otherwise
// hold its slot forever and re-submit its branch, regressing the gh #34/#35
// slot-protection guarantees the auto-stop path exists to enforce.
const deferDoneBackstopTimeout = 15 * time.Minute

// polecatReaper is the slice of agent.Registry that reapMergedPolecat needs.
type polecatReaper interface {
	GetByWorkItemOrName(id string) *agent.Agent
	Stop(name string, timeout time.Duration) error
	// ReleaseClaimAfterExit returns the work item of a polecat that ended its
	// own process to available/, reporting whether a held claim was actually
	// released. Registry.Stop covers the polecats pogod tears down; this covers
	// the deferred polecat that dies on its own between merge and `mg done`
	// (mg-c8d5).
	ReleaseClaimAfterExit(a *agent.Agent) (bool, error)
}

// reapMergedPolecat implements the event-driven polecat stop (gh #35): the
// moment the refinery reports a successful merge, pogod records the work item
// as done on the polecat's behalf and stops the polecat, instead of leaving
// both to the mayor's next coordination cycle — 90s+ of lag during which a
// lingering polecat holds a slot and can re-submit its branch (gh #34).
//
// The registry's OnExit hook owns the rest of the cleanup once the process
// exits (worktree removal, mail-check schedule reaping), and the mayor's reap
// loop stays as the backstop for polecats this path misses — e.g. a merge
// that lands while pogod is down. Blocks up to mergedPolecatStopTimeout, so
// the refinery loop invokes it in a goroutine.
//
// Only polecats are stopped: crew agents (or a human) can author MRs too, but
// their lifecycle is not tied to a single work item.
func reapMergedPolecat(reg polecatReaper, mr *refinery.MergeRequest, complete func(id, resultJSON string) error, backstop *deferredBackstop) {
	if mr.Author == "" {
		return
	}
	// Resolve by work-item id OR registry name: a polecat registers under its
	// bare id (a.Name, e.g. "d087") but authors its MR with the full work-item
	// id (mr.Author == a.WorkItemID, e.g. "mg-d087"), so a plain Get(mr.Author)
	// misses and the polecat lingers post-merge (gh #48).
	a := reg.GetByWorkItemOrName(mr.Author)
	if a == nil || a.Type != agent.TypePolecat {
		return
	}

	// The polecat owns its own post-merge lifecycle in two cases:
	//
	//   - --defer-done (gh #81): the submitter asked for it explicitly.
	//   - PR flow (mg-7746): the merge landed on an *integration* branch, not
	//     the repo's default branch. That is an intermediate step — the
	//     deliverable is a PR to the default branch that the polecat has not
	//     opened yet — so this merge is categorically not completion. Derived
	//     by the refinery from the target ref rather than requested, because
	//     three separate items (mg-74ee, mg-6579, this one) merged, were marked
	//     done, and left no PR precisely because nobody passed the flag.
	//
	// Either way the polecat still has work to do after the merge lands — open
	// the PR, run verify checks, mail the PR URL — and calls `mg done` itself
	// when that flow finishes. Skip the auto-done + auto-stop below, which
	// would kill it mid-flow, and arm a bounded backstop so a deferred polecat
	// that never ends its lifecycle is still reaped + escalated. The backstop
	// is disarmed by the OnExit hook when the polecat's process ends (it
	// completed cleanly and freed its slot).
	if mr.DeferDone || mr.PRFlow {
		reason := "submitted --defer-done (gh #81)"
		if mr.PRFlow {
			reason = fmt.Sprintf("merged into integration branch %q, not the repo default — PR still pending (mg-7746)", mr.TargetRef)
		}
		log.Printf("refinery: merged polecat %s %s — skipping auto-done/auto-stop; polecat owns its lifecycle", a.Name, reason)
		if backstop != nil {
			backstop.arm(a.Name, mr)
		}
		return
	}

	// Record completion before stopping: the polecat's own protocol runs
	// mg done when it observes the merged status, but it will be gone
	// before its next poll. If the polecat won the race after all, the
	// call fails with an already-done error — harmless. Keyed on mr.Author,
	// which is the mg work-item id.
	//
	// `target` is recorded because the mayor's classification logic reads it to
	// tell an integration-branch merge from a completed one; omitting it made a
	// documented signal actively misleading (mg-7746).
	//
	// There is deliberately no `pr_flow` key, and there never can be one: this
	// writer is only reached on the completion path, because a PR-flow merge
	// returns above without calling `mg done` at all. A sidecar written HERE is
	// by construction a default-branch completion. The PR-flow half of the
	// classification lives on the merge request instead (`pogo refinery show
	// <id> --json`), which is where the refinery records it. mg-c8d5 corrects
	// the docs that claimed otherwise.
	result, _ := json.Marshal(map[string]any{
		"branch":       mr.Branch,
		"mr":           mr.ID,
		"completed_by": "refinery",
		"target":       mr.TargetRef,
	})
	if err := complete(mr.Author, string(result)); err != nil {
		log.Printf("refinery: mg done %s on merged polecat's behalf failed (may already be done): %v", mr.Author, err)
	}

	// Stop keys on the registry name, which is the bare id — not mr.Author.
	if err := reg.Stop(a.Name, mergedPolecatStopTimeout); err != nil {
		log.Printf("refinery: failed to stop merged polecat %s: %v", a.Name, err)
		return
	}
	log.Printf("refinery: stopped merged polecat %s (event-driven, gh #35)", a.Name)
}

// backstopTimer is the subset of *time.Timer the deferred backstop needs. It
// is an interface so tests can substitute a hand-fired fake and drive both
// directions of the acceptance control deterministically, without waiting real
// wall-clock time.
type backstopTimer interface {
	Stop() bool
}

// deferredBackstop is the bounded safety net for --defer-done polecats (gh
// #81). When a deferred polecat's branch merges, reapMergedPolecat arms a
// timer here instead of stopping it. Two outcomes:
//
//   - The polecat finishes its post-merge flow, calls `mg done`, and its
//     process ends. pogod's OnExit hook calls cancel(), which disarms the
//     timer — the clean, common path.
//   - The polecat never ends its lifecycle. The timer fires after the bounded
//     window: fire() reaps the still-running process and escalates to the
//     mayor, so a deferred polecat can never silently LINGER holding its slot
//     (the gh #34/#35 regression this backstop exists to prevent).
//
// A third outcome was unhandled until mg-c8d5: the polecat DIES between the
// merge and `mg done`. cancel() disarmed the timer on any process exit, so that
// death drew no escalation and — because a self-exit never goes through
// Registry.Stop — left the work item stranded in claimed/ under a dead pid, the
// mg-fb13 leak through the one door mg-fb13 did not close. cancel() now settles
// the exit instead of merely disarming it.
//
// All state is guarded by mu; arm/cancel/fire are safe to call concurrently
// (arm runs on the refinery loop's reap goroutine, cancel on the OnExit hook,
// fire on the timer goroutine).
type deferredBackstop struct {
	mu      sync.Mutex
	timeout time.Duration
	timers  map[string]backstopTimer // keyed by registry name (bare id)
	// merged snapshots the MergeRequest each armed polecat merged, keyed like
	// timers. fire() carries its own copy in the timer closure; cancel() needs
	// one too, because the death path escalates with the same context and runs
	// off a hook that only knows the agent.
	merged   map[string]*refinery.MergeRequest
	reg      polecatReaper
	escalate func(mr *refinery.MergeRequest)

	// escalateDeath reports a deferred polecat that DIED after its merge landed
	// and before its work item left claimed/ — a merged branch whose post-merge
	// flow (PR creation, verify, mail) never finished and whose item pogod has
	// just returned to available/. Distinct from escalate, which reports the
	// opposite failure: a polecat that stayed alive too long. Nil disables the
	// mail; the release still happens (mg-c8d5).
	escalateDeath func(mr *refinery.MergeRequest, a *agent.Agent, released bool, releaseErr error)

	// workItemDone reports whether the deferred polecat's work item reached a
	// terminal state. A polecat that opened its PR, mailed it, and called
	// `mg done` is told by its own protocol to stay alive until the mayor
	// stops it — so a live process at the deadline does NOT imply an unfinished
	// flow. Consulted before escalating so that common, healthy case is reaped
	// quietly rather than reported as a failure (mg-7746). Nil (or an error
	// from the probe) means "unknown", and the conservative escalate-anyway
	// path applies.
	workItemDone func(id string) (bool, error)

	// afterFunc schedules f to run after d and returns a stoppable handle.
	// Defaults to time.AfterFunc; tests inject a fake for deterministic firing.
	afterFunc func(d time.Duration, f func()) backstopTimer
}

// newDeferredBackstop builds a backstop that reaps via reg and escalates via
// the given mailer. escalate may be nil (reap without a mail — the process is
// still freed). The caller sets workItemDone if it wants completed-but-lingering
// polecats distinguished from stalled ones; leaving it nil keeps the
// conservative escalate-on-every-fire behaviour.
func newDeferredBackstop(timeout time.Duration, reg polecatReaper, escalate func(mr *refinery.MergeRequest)) *deferredBackstop {
	return &deferredBackstop{
		timeout:   timeout,
		timers:    make(map[string]backstopTimer),
		merged:    make(map[string]*refinery.MergeRequest),
		reg:       reg,
		escalate:  escalate,
		afterFunc: func(d time.Duration, f func()) backstopTimer { return time.AfterFunc(d, f) },
	}
}

// arm starts the bounded backstop for a merged --defer-done polecat. name is
// the registry name (bare id); mr carries the escalation context. Re-arming an
// already-armed polecat is a no-op — the first deadline stands.
func (b *deferredBackstop) arm(name string, mr *refinery.MergeRequest) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.timers[name]; ok {
		return
	}
	// Snapshot the MR: the caller's *mr may be mutated/pruned after arm returns,
	// but the escalation, minutes later, must report the state at merge time.
	mrCopy := *mr
	b.merged[name] = &mrCopy
	b.timers[name] = b.afterFunc(b.timeout, func() { b.fire(name, &mrCopy) })
	log.Printf("refinery: armed defer-done backstop for polecat %s — reaps + escalates if it does not complete within %s (gh #81)", name, b.timeout)
}

// cancel disarms the backstop for a polecat whose process has ended, and then
// settles what that exit meant. Called from pogod's OnExit hook. Safe to call
// for an agent that was never armed (the common case — most polecats are not
// deferred), in which case it does nothing at all.
//
// Disarming alone was the whole of this function until mg-c8d5, and it treated
// every exit as a clean completion. It is not: an armed polecat has merged and
// been left running precisely because it still owes work, so its process ending
// is only good news if the work item actually left claimed/. See settleExit.
func (b *deferredBackstop) cancel(a *agent.Agent) {
	if b == nil || a == nil {
		return
	}
	name := a.Name
	b.mu.Lock()
	t, armed := b.timers[name]
	mr := b.merged[name]
	if armed {
		t.Stop()
		delete(b.timers, name)
		delete(b.merged, name)
	}
	b.mu.Unlock()
	if !armed {
		return
	}
	log.Printf("refinery: disarmed defer-done backstop for polecat %s — its process ended (gh #81)", name)
	b.settleExit(a, mr)
}

// settleExit decides what a deferred polecat's OWN exit meant, and is the
// deferred-death half of the claim-leak fix (mg-c8d5).
//
// The question is not "did the process end" — cancel already knows that — but
// "did it end having handed its work item on". A polecat in this lane merged,
// was deliberately not auto-done'd, and owes a PR plus its own `mg done`. Two
// endings look identical to OnExit:
//
//   - It finished: `mg done` moved the item out of claimed/, the mayor stopped
//     it (or it exited), and there is nothing to clean up.
//   - It died mid-flow: crash, OOM, harness failure, an operator kill. The
//     branch is merged, the PR is not open, and the item sits in claimed/ under
//     a pid that no longer exists — never dispatched again, and invisible to
//     stall-watch, which only scans available/.
//
// The claim file is what tells them apart, so that is what is asked. The
// release is scoped to this polecat's own work item and is a no-op when the
// claim is not held, which also makes the ordinary mayor-initiated stop quiet:
// that path goes through Registry.Stop, which has already released.
//
// Only a claim that was actually stranded (or one we failed to release)
// escalates. Escalating on every deferred exit would page the mayor for every
// clean PR-flow completion and for every agent lost to a fleet drain.
func (b *deferredBackstop) settleExit(a *agent.Agent, mr *refinery.MergeRequest) {
	released, err := b.reg.ReleaseClaimAfterExit(a)
	if !released && err == nil {
		log.Printf("refinery: deferred polecat %s exited with no claim held on %s — its post-merge flow completed (gh #81)", a.Name, a.WorkItemID)
		return
	}
	if err != nil {
		log.Printf("refinery: deferred polecat %s DIED holding the claim on %s and the release FAILED: %v — the item is stranded in claimed/ under a dead pid; release it by hand with `mg unclaim %s` (mg-c8d5)", a.Name, a.WorkItemID, err, a.WorkItemID)
	} else {
		log.Printf("refinery: deferred polecat %s DIED between its merge and `mg done` — released the claim on %s so the item returns to available/ (mg-c8d5)", a.Name, a.WorkItemID)
	}
	if b.escalateDeath != nil {
		b.escalateDeath(mr, a, released, err)
	}
}

// fire runs when a deferred polecat outlives the backstop window without
// ending its lifecycle. It reaps the lingering process and escalates to the
// mayor. If the polecat has already left the registry (it exited in the race
// between the timer firing and cancel acquiring the lock), there is nothing to
// reap and no escalation is sent.
func (b *deferredBackstop) fire(name string, mr *refinery.MergeRequest) {
	b.mu.Lock()
	// If cancel already removed our entry, a concurrent cancel won — stand down.
	if _, ok := b.timers[name]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.timers, name)
	delete(b.merged, name)
	b.mu.Unlock()

	a := b.reg.GetByWorkItemOrName(name)
	if a == nil {
		// Raced with a clean exit — the slot is already free, no escalation.
		log.Printf("refinery: defer-done backstop for polecat %s fired but the polecat was already gone — no action (gh #81)", name)
		return
	}

	// Did the polecat actually finish? A PR-flow polecat that opened its PR and
	// called `mg done` is instructed to stay alive until the mayor stops it, so
	// "still running" is not evidence of a stalled flow. Reap it either way —
	// slot protection is the point — but only escalate when the work is
	// genuinely incomplete (mg-7746).
	completed := false
	if b.workItemDone != nil && mr.Author != "" {
		done, err := b.workItemDone(mr.Author)
		if err != nil {
			log.Printf("refinery: defer-done backstop could not read the state of work item %s (%v) — escalating conservatively", mr.Author, err)
		} else {
			completed = done
		}
	}

	if completed {
		log.Printf("refinery: defer-done backstop fired for polecat %s — work item %s is already done, so its post-merge flow completed; reaping the lingering process to free its slot, no escalation (mg-7746)", name, mr.Author)
	} else {
		log.Printf("refinery: defer-done backstop FIRED for polecat %s — merged but did not complete within %s; reaping + escalating (gh #34/#35 slot protection, gh #81)", name, b.timeout)
	}
	if err := b.reg.Stop(a.Name, mergedPolecatStopTimeout); err != nil {
		log.Printf("refinery: defer-done backstop failed to stop lingering polecat %s: %v", a.Name, err)
	}
	if !completed && b.escalate != nil {
		b.escalate(mr)
	}
}
