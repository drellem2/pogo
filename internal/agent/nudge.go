package agent

import (
	"context"
	"fmt"
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
)

// DefaultIdleThreshold is how long the PTY output must be quiet before
// the agent is considered idle.
const DefaultIdleThreshold = 2 * time.Second

// DefaultNudgeTimeout is how long to wait for idle before giving up.
const DefaultNudgeTimeout = 30 * time.Second

// IsIdle returns true if no output has been written to the agent's PTY
// for at least the given duration. An agent with no output yet (just spawned)
// is not considered idle.
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

// NudgeWithMode delivers a message to the agent's PTY using the specified mode.
// In wait-idle mode, it blocks until the agent is idle (or timeout expires).
// In immediate mode, it writes directly (same as Nudge).
func (a *Agent) NudgeWithMode(msg string, mode NudgeMode, timeout time.Duration) error {
	if mode == NudgeImmediate {
		if err := a.Nudge(msg); err != nil {
			return err
		}
		emitNudgeSent(a, msg, "immediate")
		return nil
	}

	// Wait-idle mode: wait for quiescence, then deliver
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := a.WaitIdle(ctx, DefaultIdleThreshold); err != nil {
		return fmt.Errorf("wait for idle: %w", err)
	}

	if err := a.Nudge(msg); err != nil {
		return err
	}
	emitNudgeSent(a, msg, "idle")
	return nil
}

// emitNudgeSent records a nudge_sent event for a successful PTY delivery.
// Sender is "pogod" — the process actually writing the bytes — since the
// originating agent identity isn't plumbed through this call site in v1.
// Best-effort: events.Emit never propagates errors.
func emitNudgeSent(a *Agent, msg, mode string) {
	events.Emit(context.Background(), events.Event{
		EventType: "nudge_sent",
		Agent:     "pogod",
		Details: map[string]any{
			"to":       a.eventAgent(),
			"message":  msg,
			"delivery": "pty",
			"mode":     mode,
		},
	})
}
