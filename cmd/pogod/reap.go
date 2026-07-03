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
	Get(name string) *agent.Agent
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
	a := reg.Get(mr.Author)
	if a == nil || a.Type != agent.TypePolecat {
		return
	}

	// Record completion before stopping: the polecat's own protocol runs
	// mg done when it observes the merged status, but it will be gone
	// before its next poll. If the polecat won the race after all, the
	// call fails with an already-done error — harmless.
	result, _ := json.Marshal(map[string]string{
		"branch":       mr.Branch,
		"mr":           mr.ID,
		"completed_by": "refinery",
	})
	if err := complete(mr.Author, string(result)); err != nil {
		log.Printf("refinery: mg done %s on merged polecat's behalf failed (may already be done): %v", mr.Author, err)
	}

	if err := reg.Stop(mr.Author, mergedPolecatStopTimeout); err != nil {
		log.Printf("refinery: failed to stop merged polecat %s: %v", mr.Author, err)
		return
	}
	log.Printf("refinery: stopped merged polecat %s (event-driven, gh #35)", mr.Author)
}
