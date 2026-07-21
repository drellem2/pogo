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
// It gates on the HARD started-signal — the agent's mg work item leaving
// available/ — whenever the agent carries a work item id. When it does not, it
// falls back to the READY-COMPOSER signal (mg-c33e); see startedSignal below
// for both, and for the cases where it still declines outright.
//
// The renudge is a bare CR (a.Nudge("") writes only the provider's
// SubmitTerminator), the payload least likely to corrupt a working agent's
// input, and it is delivered ONLY while the agent is provably unstarted —
// never on a quiescence heuristic (see the package doc).
func (r *Registry) verifyStartAndRenudge(a *Agent) {
	started, reason, ok := r.startedSignal(a)
	if !ok {
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

		hasStarted, err := started()
		if err != nil {
			// mg state unreadable: inconclusive. Do NOT renudge blind — a stray
			// CR into a working agent is worse than leaving recovery to the stall
			// watcher and the mayor's own unstarted-check. Stop here.
			log.Printf("agent %s: start-verify query for %s failed: %v — skipping auto-renudge",
				a.Name, a.WorkItemID, err)
			return
		}
		if hasStarted {
			return // started — healthy, nothing to do.
		}

		// HARD unstarted-signal. The kickoff nudge did not take. Re-deliver a
		// bare submit terminator (CR) to flush any paste-buffered kickoff
		// without injecting a stray character.
		if reason == reasonNoReadyComposer {
			log.Printf("agent %s: still shows no ready composer %s after nudge (attempt %d/%d) — re-delivering submit terminator",
				a.Name, delay, attempt, maxAttempts)
		} else {
			log.Printf("agent %s: work item %s still unclaimed %s after nudge (attempt %d/%d) — re-delivering submit terminator",
				a.Name, a.WorkItemID, delay, attempt, maxAttempts)
		}
		if err := a.Nudge(""); err != nil {
			log.Printf("agent %s: auto-renudge failed: %v", a.Name, err)
			return
		}
		emitAutoRenudge(a, attempt, maxAttempts, reason)
	}
}

// startedSignal picks the started-signal this agent can be watched on and
// returns it with the auto_renudge reason that names it. ok is false when the
// agent cannot be watched at all; the decline has already been reported.
//
// Two signals, strongest first:
//
//   - work_item_unclaimed — the item leaving available/. The polecat's first
//     protocol action, and the strongest available: it proves the kickoff nudge
//     was ACCEPTED, not merely that a composer rendered.
//   - no_ready_composer — the provider's prompt-ready sentinel has never
//     appeared. Used when there is no work item, which mg-c33e exists to close:
//     `--no-worktree` dispatch commonly carries no `--id` (it is optional), and
//     mg-560d proved that gap is load-bearing. Such a spawn's cwd is a
//     brand-new ~/.pogo/agents/<name>/, untrusted, so Claude Code raises the
//     workspace-trust dialog every time; the dialog renders no composer, the
//     ready sentinel never matches, and the kickoff nudge is never delivered.
//     560d measured that a bare CR — precisely what this watcher sends —
//     dismisses that dialog (dialog → composer at t=0.7s, nudge accepted). So
//     the recovery net could have rescued those polecats and, before this
//     change, declined to.
//
// The fallback is a STRUCTURAL observation of the screen, not the
// output-quiescence heuristic the package doc rejects at length. Quiescence
// misreads a CPU-starved harness as ready — it is quiet BECAUSE it is starved.
// "Has a composer ever rendered" does not: a starved process, a loading
// spinner, and the trust dialog all show no composer and so all read correctly
// as unstarted. DefaultNudgeProfile's sentinel comment says as much directly.
// The sighting is latched (Agent.markPromptReady) so a bounded output buffer
// scrolling the marker away cannot flip a working agent back to unstarted.
//
// It still declines, loudly per mg-2437, in three cases — the fix must not
// start renudging what the old early return legitimately covered:
//
//   - no start verifier wired. pogod wires one at startup, so this is a bare
//     registry (unit tests) or a daemon-wide fault; either way nothing on it is
//     watched and renudging on a private signal would misrepresent that.
//   - crew agents. They never carry a work item by design, are long-lived, and
//     are respawned and nudged on their own cycle. This is the same
//     polecat-vs-crew seam reportUnwatched already draws.
//   - no prompt-ready marker declared by the provider (e.g. Codex, whose
//     ratatui composer has no stable marker). Nothing to observe.
func (r *Registry) startedSignal(a *Agent) (started func() (bool, error), reason string, ok bool) {
	verifier := r.getStartVerifier()
	if verifier == nil {
		reportUnwatched(a, reasonNoStartVerifier)
		return nil, "", false
	}

	if a.WorkItemID != "" {
		return func() (bool, error) { return verifier(a.WorkItemID) }, reasonWorkItemUnclaimed, true
	}

	if a.Type != TypePolecat || !a.hasReadySignal() {
		reportUnwatched(a, reasonNoReadySignal)
		return nil, "", false
	}

	log.Printf("agent %s: no work item id — start-verification falls back to the ready-composer signal "+
		"(a screen that never renders a composer draws a recovery CR); re-dispatch with --id <work-item> for the stronger claim signal",
		a.Name)
	return func() (bool, error) { return a.sawPromptReady(), nil }, reasonNoReadyComposer, true
}

// Decline reasons reported by reportUnwatched, recorded as the `reason` detail
// on the agent_unwatched event so an operator can tell a per-dispatch gap ("this
// spawn had no --id") from a daemon-wide one ("nothing is wired to recover any
// spawn").
const (
	// reasonNoReadySignal: nothing observable to gate on. Since mg-c33e a
	// missing work item alone is no longer a decline — the ready-composer
	// fallback covers it — so this fires only when the provider declares no
	// prompt-ready marker either (or the agent is crew, where the report is a
	// no-op by design).
	reasonNoReadySignal   = "no_ready_signal"
	reasonNoStartVerifier = "no_start_verifier"
)

// auto_renudge reasons, naming which started-signal reported the agent
// unstarted. See Registry.startedSignal.
const (
	reasonWorkItemUnclaimed = "work_item_unclaimed"
	reasonNoReadyComposer   = "no_ready_composer"
)

// reportUnwatched announces that a freshly spawned POLECAT will get no
// start-verification at all — mg-2437, narrowed by mg-c33e.
//
// The gap mg-2437 made visible: `--no-worktree` in-place dispatch is exactly
// the shape documented as commonly carrying no work item, and `--id` is
// optional (cmd/pogo/main.go). Such a spawn got no started-verifier — not a
// degraded one, a structurally absent one — and nothing said so. mg-2437 made
// that audible but left the hole open, and mg-560d then proved the hole was
// load-bearing for the drellem2/macguffin#25 hang.
//
// mg-c33e CLOSED it: a no-work-item polecat is now watched on the
// ready-composer fallback (see startedSignal), so this report no longer fires
// for the mere absence of `--id`. What remains is the honest residue — a
// verifier-less daemon, or a provider that declares no prompt-ready marker at
// all. Those really do have nothing to gate on, and saying so is still the
// point. Same loud-decline shape as the prompt-sync decline in mg-f86c.
//
// It reports through BOTH a log line and an event on purpose: this package's own
// history is that a per-spawn log line alone stayed invisible for the entire #76
// sentinel episode (mg-ce4c), which is why the renudge emits an event too. A
// recovery net that is structurally absent warrants at least that much.
//
// Crew agents are deliberately exempt. They never carry a work item by design
// and are long-lived, respawned and nudged on their own cycle — alarming on
// every crew spawn would be noise that trains an operator to skip the line that
// matters.
func reportUnwatched(a *Agent, reason string) {
	if a.Type != TypePolecat {
		return
	}

	switch reason {
	case reasonNoStartVerifier:
		log.Printf("agent %s: UNWATCHED — no start verifier is wired, so no spawn on this daemon gets start-verification or auto-renudge; a failure to start will NOT be automatically recovered",
			a.Name)
	default:
		log.Printf("agent %s: UNWATCHED — no work item id AND this provider declares no prompt-ready marker, so neither the claim signal nor the ready-composer fallback can gate on anything; NO auto-renudge will recover a failed start; re-dispatch with --id <work-item> to get start-verification",
			a.Name)
	}

	events.Emit(context.Background(), events.Event{
		EventType: "agent_unwatched",
		Agent:     "pogod",
		Details: map[string]any{
			"to":     a.eventAgent(),
			"reason": reason,
		},
	})
}

// emitAutoRenudge records an auto_renudge event for one post-spawn recovery
// keystroke, so a spawn wave that needed re-nudging is visible in the event log
// (a per-spawn log line alone was invisible for the whole #76 sentinel episode —
// see mg-ce4c). Best-effort: events.Emit never propagates errors.
func emitAutoRenudge(a *Agent, attempt, maxAttempts int, reason string) {
	events.Emit(context.Background(), events.Event{
		EventType: "auto_renudge",
		Agent:     "pogod",
		Details: map[string]any{
			"to": a.eventAgent(),
			// Present but empty on the ready-composer path — the absence of a
			// work item is the whole reason that path exists (mg-c33e).
			"work_item_id": a.WorkItemID,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
			"reason":       reason,
		},
	})
}
