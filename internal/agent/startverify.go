package agent

import (
	"context"
	"log"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Start-verification / auto-renudge defaults (mg-feb3).
//
// After the initial nudge is delivered, a healthy polecat's FIRST protocol
// action is `mg claim <work-item>`, which moves the item out of the available/
// queue. Under a concurrent spawn wave a CPU-starved node/Ink process can miss
// that kickoff: WaitForReady's idle gate misreads starvation-silence as
// input-loop-ready (nudge.go: IsIdle is just "time since last PTY write"), so
// the nudge is delivered before Claude Code is listening, piles in the kernel
// input buffer, and Ink absorbs it as one paste block whose CR never
// re-tokenizes as a submit (mg-ce61). The agent is alive+running but never
// claims its item.
//
// The mayor's manual workaround was a post-spawn "unstarted polecat" check that
// nudged "1" if the item was still available ~30-60s later. This watcher
// productizes that into a pogod daemon guarantee: watch for the HARD
// started-signal (the work item leaving available/) and, if it is still absent,
// re-deliver a bare submit terminator (CR) to flush the paste-buffered kickoff.
// It is failure-mode-agnostic — it recovers the wedge whether the cause is the
// false-idle gate, a stale prompt-ready sentinel (mg-ce4c), or the paste
// pileup — and gates on the claim signal (NOT output quiescence), because the
// whole bug is that an idle heuristic misreads starvation-silence, so a
// quiescence-based re-check would reproduce the same false-idle failure.
const (
	// DefaultStartVerifyDelay is how long to wait after a nudge before checking
	// whether the agent has started. Longer than the settle windows so a
	// slow-but-healthy start is not renudged needlessly.
	DefaultStartVerifyDelay = 25 * time.Second

	// DefaultStartVerifyMaxAttempts bounds the auto-renudge retries. Each attempt
	// waits DefaultStartVerifyDelay, checks the started-signal, and (only if the
	// item is still unclaimed) delivers one bare CR. Bounded so a genuinely dead
	// agent — or one whose work item was cancelled out from under it — does not
	// draw an unbounded stream of stray keystrokes.
	DefaultStartVerifyMaxAttempts = 3
)

// StartVerifier reports whether a freshly spawned polecat has begun its work.
// started is true once the agent's mg work item has left the available/ queue
// (been claimed) — the polecat's first protocol action and the HARD
// started-signal the auto-renudge watcher gates on. A non-nil error means the
// mg state could not be read; the watcher treats that as inconclusive and does
// NOT renudge (renudging blind risks injecting a stray char into a working
// agent). pogod backs this with a macguffin-backed query; unit tests substitute
// a closure. See internal/workitem and the mg-feb3 ticket.
type StartVerifier func(workItemID string) (started bool, err error)

// SetStartVerifier installs the start-verification query used by the post-spawn
// auto-renudge watcher. Call once at startup before any polecat is spawned. A
// nil verifier disables auto-renudge — the initial nudge then stands on its own,
// exactly as before mg-feb3.
func (r *Registry) SetStartVerifier(v StartVerifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startVerifier = v
}

func (r *Registry) getStartVerifier() StartVerifier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.startVerifier
}

func (r *Registry) startVerifyDelayOrDefault() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.startVerifyDelay > 0 {
		return r.startVerifyDelay
	}
	return DefaultStartVerifyDelay
}

func (r *Registry) startVerifyMaxAttemptsOrDefault() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.startVerifyMaxAttempts > 0 {
		return r.startVerifyMaxAttempts
	}
	return DefaultStartVerifyMaxAttempts
}

// verifyStartAndRenudge watches a freshly nudged agent for the HARD
// started-signal and, if it is still absent after the start-verify window,
// re-delivers a bare submit terminator to flush a paste-buffered kickoff. It
// runs on the initial-nudge goroutine after the kickoff nudge is delivered.
//
// It is a no-op — returning without touching the PTY — when no start verifier is
// wired (bare registry / unit tests) or the agent carries no work item id
// (crew agents, bare spawns): there is no hard signal to gate on, so the initial
// nudge stands on its own. The renudge is a bare CR (a.Nudge("") writes only the
// provider's SubmitTerminator), the payload least likely to corrupt a working
// agent's input, and it is delivered ONLY while the item is still provably
// unclaimed — never on a quiescence heuristic (see the package doc).
func (r *Registry) verifyStartAndRenudge(a *Agent) {
	verifier := r.getStartVerifier()
	if verifier == nil || a.WorkItemID == "" {
		return
	}

	delay := r.startVerifyDelayOrDefault()
	maxAttempts := r.startVerifyMaxAttemptsOrDefault()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Wait out the start window, but abandon at once if the agent exits —
		// there is no PTY to renudge and no work will ever be claimed.
		select {
		case <-a.done:
			return
		case <-time.After(delay):
		}

		started, err := verifier(a.WorkItemID)
		if err != nil {
			// mg state unreadable: inconclusive. Do NOT renudge blind — a stray
			// CR into a working agent is worse than leaving recovery to the stall
			// watcher and the mayor's own unstarted-check. Stop here.
			log.Printf("agent %s: start-verify query for %s failed: %v — skipping auto-renudge",
				a.Name, a.WorkItemID, err)
			return
		}
		if started {
			return // work item claimed — healthy start, nothing to do.
		}

		// HARD unstarted-signal: the item is still in available/. The kickoff
		// nudge did not take. Re-deliver a bare submit terminator (CR) to flush
		// any paste-buffered kickoff without injecting a stray character.
		log.Printf("agent %s: work item %s still unclaimed %s after nudge (attempt %d/%d) — re-delivering submit terminator",
			a.Name, a.WorkItemID, delay, attempt, maxAttempts)
		if err := a.Nudge(""); err != nil {
			log.Printf("agent %s: auto-renudge failed: %v", a.Name, err)
			return
		}
		emitAutoRenudge(a, attempt, maxAttempts)
	}
}

// emitAutoRenudge records an auto_renudge event for one post-spawn recovery
// keystroke, so a spawn wave that needed re-nudging is visible in the event log
// (a per-spawn log line alone was invisible for the whole #76 sentinel episode —
// see mg-ce4c). Best-effort: events.Emit never propagates errors.
func emitAutoRenudge(a *Agent, attempt, maxAttempts int) {
	events.Emit(context.Background(), events.Event{
		EventType: "auto_renudge",
		Agent:     "pogod",
		Details: map[string]any{
			"to":           a.eventAgent(),
			"work_item_id": a.WorkItemID,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
			"reason":       "work_item_unclaimed",
		},
	})
}
