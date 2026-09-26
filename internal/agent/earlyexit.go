package agent

import (
	"context"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// EarlyExit describes a polecat that died during its cold start: an exit
// nobody asked for, inside the provider's own cold-start budget.
//
// It exists because of how drellem2/pogo#177 stayed invisible. Claude Code's
// trust dialog was answered "No, exit", the harness exited 1 about three seconds
// after spawn, and every record around it read healthy: the spawn had already
// returned ok (it answers the moment the process exists), the work item was
// claimed (pogod claims at spawn), and the worktree was reaped on exit like any
// finished polecat's. agent_crashed was written, but without the work item on
// it, so nothing tied a two-second life to the item now sitting claimed with no
// worker. That is the same shape as the 18-day outage — a signal that reads
// green over a dead worker (mg-c1e2, part 3).
//
// The HTTP spawn result cannot carry this: it is written before the harness
// has run long enough to die, and holding it open for a cold-start window
// would put that window on every dispatch. So the death is surfaced where it
// is observable — an agent_early_exit event keyed to the work item, and a
// coordinator notice from pogod's exit handler.
type EarlyExit struct {
	// ExitCode is the harness's exit status (-1 when killed by a signal).
	ExitCode int
	// After is how long the process lived.
	After time.Duration
	// Window is the cold-start budget the exit fell inside.
	Window time.Duration
	// ComposerSeen is whether the harness ever showed a ready composer. False
	// is the trust-dialog signature: the process died before it could accept
	// a prompt.
	ComposerSeen bool
	// LastOutput is the tail of the PTY ring, ANSI-stripped.
	LastOutput string
}

// coldStartWindow is the provider's own cold-start budget: the same bound the
// initial nudge waits for the composer, and the same bound the trust-dialog
// hooks watch for. One definition, so "early" means what the spawn path means
// by "still starting".
func (a *Agent) coldStartWindow() time.Duration {
	if a.nudge.InitialNudgeTimeout > 0 {
		return a.nudge.InitialNudgeTimeout
	}
	return DefaultNudgeProfile.InitialNudgeTimeout
}

// isEarlyExit is the decision, kept pure so it can be tested without a PTY.
// Only polecats: crew are long-lived and restarted on their own cycle, and a
// crew crash loop already has agent_crashed and agent_restarted. A requested
// stop is never early, however quick — `pogo agent stop` two seconds after a
// spawn is an operator's choice, not a dead worker. Exit status does not
// matter: Codex refusing its trust dialog exits 0.
func isEarlyExit(typ AgentType, stopRequested bool, lived, window time.Duration) bool {
	return typ == TypePolecat && !stopRequested && lived >= 0 && lived < window
}

// EarlyExit reports whether this agent's exit was a cold-start death, and its
// particulars. It is meaningful only once the agent has exited; before that it
// reports false.
func (a *Agent) EarlyExit() (EarlyExit, bool) {
	a.mu.Lock()
	exited := a.Status == StatusExited
	stopRequested := a.stopRequested
	lived := a.ExitTime.Sub(a.StartTime)
	code := a.ExitCode
	a.mu.Unlock()

	window := a.coldStartWindow()
	if !exited || !isEarlyExit(a.Type, stopRequested, lived, window) {
		return EarlyExit{}, false
	}
	return EarlyExit{
		ExitCode:     code,
		After:        lived,
		Window:       window,
		ComposerSeen: a.promptReadySeen.Load(),
		LastOutput:   string(StripANSI(a.outputBuf.Last(512))),
	}, true
}

// emitEarlyExit records agent_early_exit when the exit was a cold-start death.
// It is written IN ADDITION to agent_stopped / agent_crashed, not instead: those
// describe how the process ended, this one says the worker never got going,
// and it carries the work item they do not.
func (a *Agent) emitEarlyExit() {
	e, ok := a.EarlyExit()
	if !ok {
		return
	}
	details := map[string]any{
		"pid":                   a.PID,
		"exit_code":             e.ExitCode,
		"seconds_after_spawn":   e.After.Seconds(),
		"cold_start_window_sec": e.Window.Seconds(),
		"composer_seen":         e.ComposerSeen,
		"provider":              a.ProviderID(),
	}
	if e.LastOutput != "" {
		details["last_output"] = e.LastOutput
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "agent_early_exit",
		Agent:      a.eventAgent(),
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Details:    details,
	})
}
