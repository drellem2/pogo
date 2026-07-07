package main

import (
	"encoding/json"
	"log"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// mergedPolecatStopTimeout bounds how long reapMergedPolecat waits for a
// SIGTERMed polecat to exit before force-killing it — same budget as the
// pogo agent stop HTTP handler.
const mergedPolecatStopTimeout = 5 * time.Second

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
func reapMergedPolecat(reg polecatReaper, mr *refinery.MergeRequest, complete func(id, resultJSON string) error) {
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
