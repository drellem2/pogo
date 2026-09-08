package agent

// The QUEUED-NUDGE record: pogod's own memory of the one delivery it could
// neither confirm nor deny.
//
// deliverConfirmed (nudge.go) escalates on a missing submission receipt —
// message, bare return, message again — and that escalation covers the agent
// that was IDLE when the bytes arrived. It deliberately does NOT cover the
// agent that was mid-turn: Claude Code emits no UserPromptSubmit for a prompt
// typed into the middle of a turn, so absence of a receipt is a blind spot
// rather than evidence, and a bare return there would re-submit a message that
// very probably landed. deliverConfirmed therefore stops after one step and
// returns ErrNudgeQueued.
//
// That is the correct decision AT DELIVERY TIME and it leaves an obligation
// nobody was carrying. The prompt is sitting in the composer of an agent that
// is still working. When the turn ends, one of two things happens:
//
//   - the harness drains the composer and the receipt count moves — the
//     overwhelmingly common case, and the only one anything used to observe; or
//   - it does not, and the agent parks at a composer holding an unsubmitted
//     instruction, having produced output, with a fresh transcript, idle in a
//     way that is indistinguishable from thinking. Nothing recovers it.
//
// The second case is mg-5246's mid-session wedge, and this record is what makes
// it *detectable at all*: it names the moment a submit became OWED and the
// receipt count that owing is measured against. Without it a quiescent composer
// is ambiguous — a polecat that has finished its work and is holding for the
// coordinator sits at exactly the same reading, forever, by design.
//
// Only the LAST unconfirmed delivery is kept. A second queued nudge to the same
// agent supersedes the first: both are in the same composer, and one receipt
// drains whatever is loaded, so tracking them separately would count one
// recovery as several failures.

import "time"

// QueuedNudge is pogod's record of a delivery it wrote to the PTY and could not
// prove was submitted, because the harness was mid-turn.
type QueuedNudge struct {
	// At is when the unconfirmable delivery happened.
	At time.Time
	// Submits is the receipt count at that moment. The obligation is discharged
	// when the agent's count exceeds it — a POSITIVE observation of the harness
	// submitting something, not an inference from silence.
	Submits int
}

// noteQueuedNudge records an unconfirmable mid-turn delivery. Called from
// deliverConfirmed's ErrNudgeQueued branch and nowhere else.
func (a *Agent) noteQueuedNudge(submits int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queuedNudge = &QueuedNudge{At: time.Now(), Submits: submits}
}

// clearQueuedNudge drops the record. Called when a confirmed delivery proves
// the composer is draining normally, so a later quiescence is not judged
// against an obligation that was already met.
func (a *Agent) clearQueuedNudge() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queuedNudge = nil
}

// QueuedNudge returns the outstanding unconfirmable delivery, or nil. The
// returned value is a copy: the caller cannot mutate the agent's record.
func (a *Agent) QueuedNudge() *QueuedNudge {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.queuedNudge == nil {
		return nil
	}
	q := *a.queuedNudge
	return &q
}

// HasReceiptSignal reports whether this agent can prove delivery at all — that
// is, whether pogod installed a submission-receipt hook it could resolve.
//
// Exported because a detector built on the receipt count MUST be able to tell
// "no submit was recorded" from "submits are not recorded here". Conflating
// them would judge an agent blind and act on the judgement, which is the exact
// failure shape internal/blindwatch exists to make visible.
func (a *Agent) HasReceiptSignal() bool { return a.hasReceiptSignal() }
