package refinery

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// errCancelRequested marks a merge that stopped because someone cancelled it,
// as opposed to one that failed on its own merits. The distinction matters
// downstream: a cancelled MR must not count against its author's failure
// streak and must not fire the failed callback, which is what reopens a work
// item. mg-8595 records a case where a redundant operator action reopened a
// work item whose branch had already landed; a cancel that reported itself as
// a gate failure would do the same thing on purpose.
var errCancelRequested = errors.New("cancelled by operator")

// CancelOutcome describes what a Cancel call actually did. A caller that
// cannot tell "removed before it ran" from "asked a running merge to stop" is
// back to guessing, so the two are named separately.
type CancelOutcome string

const (
	// CancelRemovedFromQueue means the MR never started and is now cancelled.
	// Terminal and immediate.
	CancelRemovedFromQueue CancelOutcome = "removed_from_queue"
	// CancelRequestedInFlight means the MR was already processing. The
	// running gate has been killed and the pipeline will stop at the next
	// step boundary — but a merge that had already pushed still resolves as
	// merged. Poll the MR for the real outcome; do not assume this one.
	CancelRequestedInFlight CancelOutcome = "requested_in_flight"
)

// gateContext returns the context the quality gate for mr should run under. It
// is that merge's own lane context when one is installed, and Background
// otherwise (tests that call processMerge directly).
//
// It takes the merge request rather than reading one shared field because a
// cancel must reach exactly one gate. With per-repo lanes a single shared
// context would make Cancel a broadcast — killing every repo's gate to stop
// one — and that failure would be silent, since each victim would report
// itself as cancelled by an operator.
func (r *Refinery) gateContext(mr *MergeRequest) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mr != nil {
		if ln := r.laneHoldingLocked(mr.ID); ln != nil && ln.ctx != nil {
			return ln.ctx
		}
	}
	return context.Background()
}

// cancelWasRequested reports whether a cancel has been asked for on mr.
// processMerge consults it at every boundary where it would otherwise start
// more work.
func (r *Refinery) cancelWasRequested(mr *MergeRequest) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mr == nil {
		return false
	}
	ln := r.laneHoldingLocked(mr.ID)
	return ln != nil && ln.cancelRequested
}

// requestInFlightCancelLocked kills the lane's running gate and records the
// request. Must be called with mu held.
func (r *Refinery) requestInFlightCancelLocked(ln *lane) {
	ln.cancelRequested = true
	if ln.cancel != nil {
		// context.WithCancelCause would carry the reason, but the gate only
		// needs to die; the cause is attached where the error is built.
		ln.cancel()
	}
	mr := ln.mr
	log.Printf("refinery: cancel requested for PROCESSING MR %s branch=%s author=%s repo-lane=%s — "+
		"running gate killed, pipeline stops at the next step boundary "+
		"(an already-pushed merge still resolves as merged). Other lanes are unaffected.",
		mr.ID, mr.Branch, mr.Author, ln.key)
}

// InFlightCancelNote is the message a caller should show after a
// CancelRequestedInFlight outcome. It is a single sentence about what has and
// has not been decided, because the honest answer to "is it cancelled?" at
// that moment is "not yet, poll it".
const InFlightCancelNote = "merge request was already processing: its running quality gate was killed and the " +
	"pipeline will stop at the next step boundary. This is not yet a final status — if the merge had " +
	"already pushed to the target it still resolves as merged. Poll 'pogo refinery show <id>' for the outcome."

// cancelledMergeError wraps errCancelRequested with the step it stopped at.
func cancelledMergeError(step string) error {
	return fmt.Errorf("merge stopped at step=%s: %w", step, errCancelRequested)
}

// isCancelled reports whether an error is the operator-cancel sentinel.
func isCancelled(err error) bool {
	return errors.Is(err, errCancelRequested)
}
