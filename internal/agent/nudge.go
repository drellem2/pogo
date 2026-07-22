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
)

// DefaultNudgeTimeout is how long to wait for idle before giving up.
const DefaultNudgeTimeout = 30 * time.Second

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
