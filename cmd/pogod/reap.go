package main

import (
	"encoding/json"
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

	// --defer-done (gh #81): the polecat owns its own post-merge lifecycle. It
	// still has work to do after the merge lands — open the PR, run verify
	// checks, mail the PR URL — and calls `mg done` itself when that flow
	// finishes. Skip the auto-done + auto-stop below, which would kill it
	// mid-flow (the exact bug #81 fixes), and arm a bounded backstop so a
	// deferred polecat that never ends its lifecycle is still reaped +
	// escalated. The backstop is disarmed by the OnExit hook when the polecat's
	// process ends (it completed cleanly and freed its slot).
	if mr.DeferDone {
		log.Printf("refinery: merged polecat %s submitted --defer-done — skipping auto-done/auto-stop; polecat owns its lifecycle (gh #81)", a.Name)
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
	result, _ := json.Marshal(map[string]string{
		"branch":       mr.Branch,
		"mr":           mr.ID,
		"completed_by": "refinery",
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
// All state is guarded by mu; arm/cancel/fire are safe to call concurrently
// (arm runs on the refinery loop's reap goroutine, cancel on the OnExit hook,
// fire on the timer goroutine).
type deferredBackstop struct {
	mu       sync.Mutex
	timeout  time.Duration
	timers   map[string]backstopTimer // keyed by registry name (bare id)
	reg      polecatReaper
	escalate func(mr *refinery.MergeRequest)

	// afterFunc schedules f to run after d and returns a stoppable handle.
	// Defaults to time.AfterFunc; tests inject a fake for deterministic firing.
	afterFunc func(d time.Duration, f func()) backstopTimer
}

// newDeferredBackstop builds a backstop that reaps via reg and escalates via
// the given mailer. escalate may be nil (reap without a mail — the process is
// still freed).
func newDeferredBackstop(timeout time.Duration, reg polecatReaper, escalate func(mr *refinery.MergeRequest)) *deferredBackstop {
	return &deferredBackstop{
		timeout:   timeout,
		timers:    make(map[string]backstopTimer),
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
	b.timers[name] = b.afterFunc(b.timeout, func() { b.fire(name, &mrCopy) })
	log.Printf("refinery: armed defer-done backstop for polecat %s — reaps + escalates if it does not complete within %s (gh #81)", name, b.timeout)
}

// cancel disarms the backstop for name. Called from pogod's OnExit hook when a
// polecat's process ends: a gone process has freed its slot, so there is
// nothing left to reap. Safe to call for a name that was never armed (the
// common case — most polecats are not --defer-done).
func (b *deferredBackstop) cancel(name string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.timers[name]; ok {
		t.Stop()
		delete(b.timers, name)
		log.Printf("refinery: disarmed defer-done backstop for polecat %s (completed cleanly, gh #81)", name)
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
	b.mu.Unlock()

	a := b.reg.GetByWorkItemOrName(name)
	if a == nil {
		// Raced with a clean exit — the slot is already free, no escalation.
		log.Printf("refinery: defer-done backstop for polecat %s fired but the polecat was already gone — no action (gh #81)", name)
		return
	}
	log.Printf("refinery: defer-done backstop FIRED for polecat %s — merged but did not complete within %s; reaping + escalating (gh #34/#35 slot protection, gh #81)", name, b.timeout)
	if err := b.reg.Stop(a.Name, mergedPolecatStopTimeout); err != nil {
		log.Printf("refinery: defer-done backstop failed to stop lingering polecat %s: %v", a.Name, err)
	}
	if b.escalate != nil {
		b.escalate(mr)
	}
}

// worktreeUnlinker is the slice of agent.Registry that
// unlinkSubmittedPolecatWorktree needs.
type worktreeUnlinker interface {
	GetByWorkItemOrName(id string) *agent.Agent
}

// unlinkSubmittedPolecatWorktree runs on the refinery's OnSubmit hook: when a
// polecat submits an MR, its worktree is unlinked so the branch is no longer
// marked "checked out" in the source repo, which would otherwise trigger
// "already checked out" errors in the refinery's clone. The polecat's directory
// is left intact so it can keep polling for merge results.
//
// Like reapMergedPolecat, it resolves the polecat by work-item id OR registry
// name: mr.Author carries the full work-item id (== a.WorkItemID) while the
// polecat registers under its bare id (a.Name), so a plain Get(mr.Author)
// misses — the same gh #48 defect as the reap path. unlink is injected so the
// hook is testable without touching git.
func unlinkSubmittedPolecatWorktree(reg worktreeUnlinker, mr *refinery.MergeRequest, unlink func(sourceRepo, worktreeDir string) error) {
	if mr.Author == "" {
		return
	}
	a := reg.GetByWorkItemOrName(mr.Author)
	if a == nil || a.WorktreeDir == "" || a.SourceRepo == "" {
		return
	}
	if err := unlink(a.SourceRepo, a.WorktreeDir); err != nil {
		log.Printf("refinery: failed to unlink polecat worktree for %s: %v", mr.Author, err)
	} else {
		log.Printf("refinery: unlinked polecat worktree for %s at %s", mr.Author, a.WorktreeDir)
	}
}
