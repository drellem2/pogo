package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// NudgeMode controls how a nudge message is delivered.
type NudgeMode string

const (
	// NudgeWaitIdle waits for the agent to be idle before delivering.
	NudgeWaitIdle NudgeMode = "wait-idle"

	// NudgeImmediate writes directly to the PTY without waiting.
	NudgeImmediate NudgeMode = "immediate"

	// NudgeWaitReady waits for the provider's prompt-ready sentinel to appear
	// in PTY output (then for output to settle) before delivering. It is the
	// initial-nudge mode: the spawn-time gate must prove the harness's
	// interactive input loop is genuinely ready, not merely quiet, because a
	// harness is also quiet during pre-TUI startup (mg-ce61). When the
	// provider declares no sentinel, it falls back to NudgeWaitIdle semantics.
	NudgeWaitReady NudgeMode = "wait-ready"

	// NudgeConfirm delivers immediately and then proves delivery from the
	// harness's own submission receipts, escalating on absence. See
	// Agent.deliverConfirmed. It is the default for the nudge API and the
	// scheduler; it degrades to NudgeWaitIdle when the agent has no receipt
	// signal, so an agent spawned before the hook existed behaves exactly as
	// it did before.
	NudgeConfirm NudgeMode = "confirm"
)

// DefaultNudgeTimeout is how long to wait for idle before giving up.
const DefaultNudgeTimeout = 30 * time.Second

// receiptPollInterval is how often the confirm path re-reads the receipt file.
const receiptPollInterval = 100 * time.Millisecond

// minConfirmStep floors each escalation step's window, so a caller passing a
// very short timeout still gives the harness a realistic chance to submit
// before the next step fires.
const minConfirmStep = 1500 * time.Millisecond

var (
	// ErrNudgeQueued means the message was written to a harness that was
	// mid-turn and no submission receipt arrived. It is the one outcome pogod
	// cannot resolve either way, and the measurements behind that are worth
	// stating precisely (docs/investigations/confirmed-nudge-delivery-2026-07-29.md):
	//
	// Against a real Claude Code mid-turn, the message IS accepted and acted on
	// — the probe asked for "PONG" and got it — but no UserPromptSubmit hook
	// fires for a prompt taken while a turn is in flight. The receipt is
	// therefore BLIND to mid-turn delivery, not evidence against it. Waiting
	// longer does not help: a run watched the receipt for four minutes past the
	// end of the turn and it never moved, while the answer had been on screen
	// the whole time.
	//
	// So this is neither a success nor a failure, and pogod claims neither.
	// Nothing is retried: the message very probably landed, and a resend would
	// deliver one instruction twice — measured, in the same session, by
	// resending and watching it arrive a second time. An agent acting twice on
	// one instruction is a worse outcome than one that acted late.
	ErrNudgeQueued = errors.New("nudge written mid-turn; harness emits no receipt for it")

	// ErrNudgeUnconfirmed means the escalation ran to the end — the message,
	// then a bare return, then the message again — and the harness never
	// recorded a submission. Nobody received it. This is the outcome that used
	// to be reported as success.
	ErrNudgeUnconfirmed = errors.New("nudge not confirmed by the agent")
)

// IsIdle returns true if no output has been written to the agent's PTY
// for at least the given duration. An agent with no output yet (just spawned)
// is not considered idle.
//
// Caveat (mg-feb3): idleness is purely "time since last PTY write", so a
// CPU-starved harness that has stalled without emitting output reads as idle
// even though its interactive input loop is not yet listening. WaitForReady
// pairs this with the prompt-ready sentinel to resist that false-idle at the
// initial-nudge gate, but under a concurrent spawn wave the gate can still
// misfire and swallow the kickoff. The post-spawn auto-renudge watcher
// (verifyStartAndRenudge) is the failure-mode-agnostic backstop: it gates on the
// HARD started-signal (the work item leaving available/), never on quiescence,
// precisely because a quiescence re-check would reproduce this same false-idle.
func (a *Agent) IsIdle(quiescence time.Duration) bool {
	lastWrite := a.outputBuf.LastWriteTime()
	if lastWrite.IsZero() {
		return false
	}
	return time.Since(lastWrite) >= quiescence
}

// WaitIdle blocks until the agent's PTY output has been quiet for the given
// threshold, or the context is cancelled. Polls at half the threshold interval.
func (a *Agent) WaitIdle(ctx context.Context, quiescence time.Duration) error {
	// Check immediately
	if a.IsIdle(quiescence) {
		return nil
	}

	pollInterval := quiescence / 2
	if pollInterval < 100*time.Millisecond {
		pollInterval = 100 * time.Millisecond
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.done:
			return fmt.Errorf("agent %q exited while waiting for idle", a.Name)
		case <-ticker.C:
			if a.IsIdle(quiescence) {
				return nil
			}
		}
	}
}

// markPromptReady records that this agent's harness has been observed at a
// ready composer at least once. The record is sticky on purpose: the output
// ring buffer is bounded, so a busy agent scrolls its composer marker out of
// the retained window, and a readiness test that only re-scans the buffer would
// flip a working agent back to "not ready" (mg-c33e).
func (a *Agent) markPromptReady() {
	a.promptReadySeen.Store(true)
}

// hasReadySignal reports whether this agent's provider declares any
// prompt-ready marker at all. A provider without one (e.g. Codex, whose
// ratatui composer has no stable marker — see NudgeProfile.PromptReadySentinel)
// gives the start-verify fallback nothing to observe.
func (a *Agent) hasReadySignal() bool {
	if a.nudge.PromptReadySentinel != "" {
		return true
	}
	for _, alt := range a.nudge.PromptReadyAlternates {
		if alt != "" {
			return true
		}
	}
	return false
}

// sawPromptReady reports whether the harness has ever rendered a ready
// composer: either the initial-nudge path already recorded the sighting, or the
// marker is still present in the retained PTY output.
//
// This is a STRUCTURAL observation of the screen — "does the composer exist" —
// not the output-quiescence heuristic the package doc rejects. The distinction
// matters: quiescence misreads a CPU-starved process as ready (it is quiet
// because it is starved), whereas a starved process, a loading spinner, and the
// workspace-trust dialog all render no composer and so all read correctly as
// not-ready. See DefaultNudgeProfile's sentinel comment, which notes the hint is
// absent during exactly those screens.
func (a *Agent) sawPromptReady() bool {
	if a.promptReadySeen.Load() {
		return true
	}
	clean := StripANSI(a.outputBuf.Last(a.outputBuf.Len()))
	if a.nudge.PromptReadySentinel != "" && bytes.Contains(clean, []byte(a.nudge.PromptReadySentinel)) {
		a.markPromptReady()
		return true
	}
	for _, alt := range a.nudge.PromptReadyAlternates {
		if alt != "" && bytes.Contains(clean, []byte(alt)) {
			a.markPromptReady()
			return true
		}
	}
	return false
}

// WaitForReady blocks until the agent's PTY output contains the prompt-ready
// sentinel AND has since gone quiet for quiescence, or the context is
// cancelled. The sentinel proves the interactive input loop has rendered; the
// trailing quiescence proves rendering has settled, so a submitted nudge is
// re-tokenized instead of absorbed into an in-flight paste block (mg-ce61).
//
// It reports whether the sentinel was actually observed. On context timeout it
// returns (sentinelSeen, ctx.Err()) so the caller can decide whether to
// deliver anyway — the initial-nudge path delivers best-effort rather than
// dropping the nudge if the sentinel never appears (a harness UI change must
// degrade to no-worse-than the old wait-idle behavior, not a silent wedge).
func (a *Agent) WaitForReady(ctx context.Context, sentinels []string, quiescence time.Duration) (bool, error) {
	wants := make([][]byte, 0, len(sentinels))
	for _, s := range sentinels {
		if s != "" {
			wants = append(wants, []byte(s))
		}
	}
	seen := false

	check := func() bool {
		if !seen {
			clean := StripANSI(a.outputBuf.Last(a.outputBuf.Len()))
			for _, want := range wants {
				if bytes.Contains(clean, want) {
					seen = true
					// Record the sighting so it survives buffer eviction —
					// the start-verify fallback gates on it (mg-c33e).
					a.markPromptReady()
					break
				}
			}
		}
		return seen && a.IsIdle(quiescence)
	}

	if check() {
		return seen, nil
	}

	pollInterval := quiescence / 2
	if pollInterval < 100*time.Millisecond {
		pollInterval = 100 * time.Millisecond
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return seen, ctx.Err()
		case <-a.done:
			return seen, fmt.Errorf("agent %q exited while waiting for prompt-ready", a.Name)
		case <-ticker.C:
			if check() {
				return seen, nil
			}
		}
	}
}

// NudgeWithMode delivers a message to the agent's PTY using the specified mode.
// In wait-idle mode, it blocks until the agent is idle (or timeout expires).
// In wait-ready mode, it waits for the provider's prompt-ready sentinel (then
// quiescence) before delivering, falling back to wait-idle when no sentinel is
// configured. In immediate mode, it writes directly (same as Nudge).
func (a *Agent) NudgeWithMode(msg string, mode NudgeMode, timeout time.Duration) error {
	return a.NudgeWithModeCorrelated(msg, mode, timeout, "")
}

// NudgeWithModeCorrelated is NudgeWithMode with a correlation id stamped onto
// the emitted nudge_sent event.
//
// The scheduler passes the fire's completion token here (mg-a754). That makes
// nudge_sent, scheduler_fire_delivered and scheduler_fire_completed joinable on
// one key, which is the difference between "771 nudges were sent and 647 fires
// were delivered" — two true, unrelatable numbers, as the 2026-07-22 events log
// recorded them — and being able to follow a single fire from bytes-written to
// work-finished, or watch it stop at the former.
//
// corr is optional: an empty value emits exactly the pre-existing event shape,
// so callers with no fire to correlate (manual nudges, mail-check kickoffs) are
// unchanged.
func (a *Agent) NudgeWithModeCorrelated(msg string, mode NudgeMode, timeout time.Duration, corr string) error {
	if mode == NudgeImmediate {
		if err := a.Nudge(msg); err != nil {
			return err
		}
		emitNudgeSent(a, msg, "immediate", corr)
		return nil
	}

	// Confirmed delivery, when this agent can prove receipt. Without a receipt
	// signal there is nothing to escalate against, so it degrades to the
	// wait-idle path below — an agent spawned before the hook existed, or under
	// a provider that installs none, behaves exactly as it always did.
	if mode == NudgeConfirm {
		if !a.hasReceiptSignal() {
			mode = NudgeWaitIdle
		} else {
			return a.deliverConfirmed(msg, timeout, corr)
		}
	}

	if mode == NudgeWaitReady && a.nudge.PromptReadySentinel != "" {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		sentinels := append([]string{a.nudge.PromptReadySentinel}, a.nudge.PromptReadyAlternates...)
		seen, err := a.WaitForReady(ctx, sentinels, a.nudge.IdleThreshold)
		if err != nil {
			// Agent exited mid-wait: nothing to deliver to.
			if !errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("wait for prompt-ready: %w", err)
			}
			// Deadline hit. Deliver best-effort rather than dropping the
			// initial nudge — see WaitForReady's contract. Log what we saw so
			// a stale sentinel (harness UI change) is diagnosable.
			if seen {
				log.Printf("agent %s: prompt-ready sentinel seen but PTY never settled "+
					"within %s; delivering nudge anyway", a.Name, timeout)
			} else {
				log.Printf("agent %s: prompt-ready sentinel %q not seen within %s; "+
					"delivering nudge best-effort (sentinel may be stale)",
					a.Name, a.nudge.PromptReadySentinel, timeout)
			}
		}

		// Feed the fleet-wide drift detector. Both the settled (err == nil) and
		// the deadline path carry a meaningful gate result — seen is true only
		// when the sentinel actually matched. The non-deadline error above
		// returned early (agent died mid-wait: inconclusive, not recorded), so
		// every outcome that reaches here is a real spawn's ready-gate result.
		// A per-spawn log line was invisible for the whole #76 episode; this
		// turns a fleet-wide run of misses into one loud alert (mg-ce4c).
		RecordInitialNudgeReady(a.ProviderID(), a.nudge.PromptReadySentinel, seen)

		// The ready gate proves the composer rendered; it does not prove the
		// kickoff landed. Orc measured Claude still losing input a second after
		// it had finished painting, and this is the nudge a polecat's entire
		// existence hangs on — a dropped one is a polecat that claims nothing
		// and sits until somebody notices. Confirm it when we can. The budget
		// restarts here: the ready wait is over, and a full escalation cycle is
		// what the remaining question needs.
		if a.hasReceiptSignal() {
			return a.deliverConfirmed(msg, DefaultNudgeTimeout, corr)
		}

		if err := a.Nudge(msg); err != nil {
			return err
		}
		emitNudgeSent(a, msg, "ready", corr)
		return nil
	}

	// Wait-idle mode (or wait-ready with no configured sentinel): wait for
	// quiescence, then deliver.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := a.WaitIdle(ctx, a.nudge.IdleThreshold); err != nil {
		// Distinguish "agent never went quiet" (busy, or stuck redrawing —
		// e.g. a TUI corrupted by a bad attach resize) from a plain context
		// cancellation. The deadline-exceeded case still wraps the original
		// error so callers' errors.Is(err, context.DeadlineExceeded) holds.
		if errors.Is(err, context.DeadlineExceeded) {
			sinceWrite := time.Since(a.outputBuf.LastWriteTime()).Round(time.Millisecond)
			return fmt.Errorf("wait for idle: agent %q still producing output after %s "+
				"(last PTY write %s ago) — agent is busy or stuck redrawing: %w",
				a.Name, timeout, sinceWrite, err)
		}
		return fmt.Errorf("wait for idle: %w", err)
	}

	if err := a.Nudge(msg); err != nil {
		return err
	}
	emitNudgeSent(a, msg, "idle", corr)
	return nil
}

// MidTurn reports whether the agent is currently producing output — the state
// a working agent is in, and the state whose negation wait-idle demands.
//
// It is deliberately NOT !IsIdle(). IsIdle answers false for an agent that has
// never written anything at all, which is a just-spawned harness — the single
// most important case for the escalation below, since the startup drop is the
// failure Orc measured. Reading that as "mid-turn" would switch the escalation
// off at exactly the moment it is needed. Silence is not a turn.
func (a *Agent) MidTurn() bool {
	last := a.outputBuf.LastWriteTime()
	if last.IsZero() {
		return false
	}
	return time.Since(last) < a.nudge.IdleThreshold
}

// hasReceiptSignal reports whether this agent can prove delivery. It is set at
// spawn, and only when pogod installed a submission-receipt hook it could
// actually resolve — never inferred from the receipt file's existence, because
// a hook that was never installed and a hook that has fired zero times look
// identical on disk, and escalating against the first would resend a message
// the agent received perfectly well.
func (a *Agent) hasReceiptSignal() bool {
	return a.receiptFile != ""
}

// ReceiptFile returns where this agent's harness records the prompts it
// submits, or "" when this agent cannot prove delivery at all. Exported so
// diagnostics — and the live control in internal/providers — can read the same
// number the confirm path reads, rather than a second opinion about it.
func (a *Agent) ReceiptFile() string {
	return a.receiptFile
}

// awaitSubmit polls the receipt file until the count exceeds before, the window
// expires, or the agent exits. Returns the new count and whether it moved.
func (a *Agent) awaitSubmit(before int, window time.Duration) (int, bool) {
	deadline := time.Now().Add(window)
	for {
		n, err := CountSubmits(a.receiptFile)
		if err == nil && n > before {
			return n, true
		}
		if !time.Now().Before(deadline) {
			return before, false
		}
		select {
		case <-a.done:
			return before, false
		case <-time.After(receiptPollInterval):
		}
	}
}

// deliverConfirmed writes msg to the PTY and then proves the harness submitted
// it, escalating on absence rather than assuming success.
//
// The escalation order is the whole design and is not interchangeable:
//
//  1. The message. If a receipt arrives, done — the overwhelmingly common case,
//     and one that costs nothing extra.
//  2. A BARE RETURN. The measured failure is not usually a lost message; it is
//     a lost submit, leaving the text sitting unsent in the composer. A return
//     carries no content, so it submits whatever is loaded and CANNOT duplicate
//     anything. That is why it goes first.
//  3. The message again — only now, when a bare return has proved there was
//     nothing loaded to submit.
//  4. Refuse. Return ErrNudgeUnconfirmed instead of a success nobody can check.
//
// A mid-turn agent stops after step 1 with ErrNudgeQueued. Claude Code takes
// the prompt — it was measured answering one typed into the middle of a turn —
// but emits no UserPromptSubmit for it, so absence of a receipt is a blind spot
// rather than evidence, and steps 2–3 would re-deliver a message that landed.
//
// Note what this does NOT do: it never waits for idle. That precondition is the
// negation of the state a working agent is in, which is why a busy agent was
// unreachable. The guarantee wait-idle was standing in for — "do not type into
// a harness that will not process it" — is now carried by the receipt itself,
// which is a direct observation of processing rather than a proxy for it.
func (a *Agent) deliverConfirmed(msg string, timeout time.Duration, corr string) error {
	before, err := CountSubmits(a.receiptFile)
	if err != nil {
		return fmt.Errorf("read submission receipts for %q: %w", a.Name, err)
	}

	// Sampled BEFORE typing: the write echoes back through the tty and would
	// make every agent look mid-turn a moment later.
	busy := a.MidTurn()

	// Three escalation steps share the budget. A mid-turn agent gets one of
	// them and no more: the harness emits no receipt for a mid-turn prompt at
	// all, so a longer wait cannot turn that outcome into a confirmation — it
	// only blocks the caller. The one step is still worth spending, because
	// MidTurn can read true for an agent that merely blinked a spinner, and
	// that agent's receipt does arrive.
	step := timeout / 3
	if step < minConfirmStep {
		step = minConfirmStep
	}

	if err := a.Nudge(msg); err != nil {
		return err
	}
	if _, ok := a.awaitSubmit(before, step); ok {
		emitNudgeSent(a, msg, "confirm", corr)
		return nil
	}

	if busy {
		emitNudgeUnconfirmed(a, msg, "queued", corr)
		return fmt.Errorf("nudge to %q: written to a harness that was mid-turn, which "+
			"emits no submission receipt for such a prompt (%s); pogod can neither "+
			"confirm nor deny delivery and will not retry, because the message very "+
			"probably landed and a resend would deliver it twice: %w",
			a.Name, step, ErrNudgeQueued)
	}

	// Step 2: bare return. Submits loaded-but-unsent text; carries no content,
	// so it cannot duplicate an already-delivered message.
	log.Printf("agent %s: no submission receipt for nudge after %s; sending a bare return",
		a.Name, step)
	if err := a.Nudge(""); err != nil {
		return fmt.Errorf("bare return to %q: %w", a.Name, err)
	}
	if _, ok := a.awaitSubmit(before, step); ok {
		log.Printf("agent %s: bare return submitted the loaded message", a.Name)
		emitNudgeSent(a, msg, "confirm-bare-return", corr)
		return nil
	}

	// Step 3: the message again. The bare return proved nothing was loaded.
	log.Printf("agent %s: bare return submitted nothing; resending the message", a.Name)
	if err := a.Nudge(msg); err != nil {
		return fmt.Errorf("resend to %q: %w", a.Name, err)
	}
	if _, ok := a.awaitSubmit(before, step); ok {
		emitNudgeSent(a, msg, "confirm-resend", corr)
		return nil
	}

	emitNudgeUnconfirmed(a, msg, "refused", corr)
	return fmt.Errorf("nudge to %q: no submission receipt after the message, a bare "+
		"return, and a resend within %s — the agent did not receive it: %w",
		a.Name, timeout, ErrNudgeUnconfirmed)
}

// emitNudgeSent records a nudge_sent event for a successful PTY delivery.
// Sender is "pogod" — the process actually writing the bytes — since the
// originating agent identity isn't plumbed through this call site in v1.
// Best-effort: events.Emit never propagates errors.
func emitNudgeSent(a *Agent, msg, mode, corr string) {
	details := map[string]any{
		"to":       a.eventAgent(),
		"message":  msg,
		"delivery": "pty",
		"mode":     mode,
	}
	// Correlation id, when the caller has one. Present only for nudges that
	// belong to a larger transaction whose completion is separately observable
	// — today that is a scheduler fire, keyed by its completion token so
	// nudge_sent joins to scheduler_fire_completed (mg-a754).
	if corr != "" {
		details["fire_token"] = corr
	}
	events.Emit(context.Background(), events.Event{
		EventType: "nudge_sent",
		Agent:     "pogod",
		Details:   details,
	})
}

// emitNudgeUnconfirmed records a delivery pogod could NOT prove. It exists
// because the alternative — a nudge that fails quietly into a caller's error
// string — is the same shape of invisibility that let 647 delivered fires sit
// alongside a fleet where nothing consumed them: outcome is only auditable if
// it is in the log next to the successes. outcome is "queued" (written to a
// mid-turn harness, not retried) or "refused" (escalated to the end, nothing).
func emitNudgeUnconfirmed(a *Agent, msg, outcome, corr string) {
	details := map[string]any{
		"to":       a.eventAgent(),
		"message":  msg,
		"delivery": "pty",
		"mode":     "confirm",
		"outcome":  outcome,
	}
	if corr != "" {
		details["fire_token"] = corr
	}
	events.Emit(context.Background(), events.Event{
		EventType: "nudge_unconfirmed",
		Agent:     "pogod",
		Details:   details,
	})
}
