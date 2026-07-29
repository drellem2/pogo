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

// gateContext returns the context a quality gate should run under. It is the
// per-MR cancellable context when one is installed, and Background otherwise
// (tests that call processMerge directly).
func (r *Refinery) gateContext() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gateCtx != nil {
		return r.gateCtx
	}
	return context.Background()
}

// beginProcessing installs a fresh cancellable context for the MR about to be
// processed. Must be called with mu held.
func (r *Refinery) beginProcessingLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	r.gateCtx, r.gateCancel = ctx, cancel
	r.cancelRequested = false
}

// endProcessing tears down the per-MR cancel context and reports whether a
// cancellation had been requested. Must be called with mu held.
func (r *Refinery) endProcessingLocked() bool {
	requested := r.cancelRequested
	if r.gateCancel != nil {
		r.gateCancel()
	}
	r.gateCtx, r.gateCancel = nil, nil
	r.cancelRequested = false
	return requested
}

// cancelWasRequested reports whether a cancel has been asked for on the
// in-flight MR. processMerge consults it at every boundary where it would
// otherwise start more work.
func (r *Refinery) cancelWasRequested() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelRequested
}

// requestInFlightCancelLocked kills the running gate and records the request.
// Must be called with mu held.
func (r *Refinery) requestInFlightCancelLocked(mr *MergeRequest) {
	r.cancelRequested = true
	if r.gateCancel != nil {
		// context.WithCancelCause would carry the reason, but the gate only
		// needs to die; the cause is attached where the error is built.
		r.gateCancel()
	}
	log.Printf("refinery: cancel requested for PROCESSING MR %s branch=%s author=%s — "+
		"running gate killed, pipeline stops at the next step boundary "+
		"(an already-pushed merge still resolves as merged)", mr.ID, mr.Branch, mr.Author)
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
