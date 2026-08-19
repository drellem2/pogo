package agent

import (
	"sort"
	"time"
)

// WorkerProgress is a snapshot of one live worker's identity plus the three
// facts a fleet-productivity detector needs about it: how long it has been
// alive, how long it has been quiet on its PTY, and where its worktree is so
// the caller can ask the filesystem whether anything came out of it.
//
// It is deliberately NOT an extension of PolecatActivity, for the reason that
// struct gives for not being an extension of PolecatInfo. PolecatActivity is
// the done-item reaper's input and its contract is "which item, and is it still
// talking"; giving it a pid and a path it does not read would make the reaper's
// dependencies wider than the reaper. This one carries the pid because CPU must
// be attributed by PROCESS SUBTREE — a parent under-reports a fan-out workload,
// measured at 0.0% while its children burned 2–11% (mg-eb47) — and the
// worktree path because the walk that reads it is slow and must happen OUTSIDE
// the registry lock.
//
// See internal/progresswatch for what the three facts are combined with, and
// why no one of them alone is worth reporting.
type WorkerProgress struct {
	// Name is the bare registry name; WorkItemID is the item it claimed, empty
	// for a worker spawned without one.
	Name       string
	WorkItemID string
	// PID is the worker process. It is the ROOT of the subtree whose CPU is the
	// question, never the process whose CPU is the answer.
	PID int
	// WorktreeDir is the git worktree the worker was given, empty when it was
	// spawned without one (NoWorktree). An empty path is not an unwritten tree
	// and a caller must report it as unmeasurable rather than as quiet.
	WorktreeDir string
	// Age is how long since the worker was spawned. A worker still reading its
	// ticket has written nothing and that is correct, so a consumer must have
	// this in order to decline to judge it.
	Age time.Duration
	// PTYIdle is how long since the worker last wrote to its PTY, meaningful
	// only when HasOutput. A worker that has NEVER written has an UNMEASURABLE
	// idle time, not a short one — seconds into spawn, or wedged before its
	// first turn on the mg-ce61 unsubmitted paste — so the two are separate
	// fields rather than a zero sentinel. PolecatActivity makes the same split
	// and diagnoseAgentAt inherits the same refusal.
	PTYIdle   time.Duration
	HasOutput bool
}

// WorkerProgressAt returns a snapshot of every live worker as of now, sorted by
// name so callers and their logs are deterministic.
//
// Everything it reports is a fact about the instant it was taken, and a worker
// can exit between the snapshot and the caller's use of it — so a consumer must
// tolerate the pid being gone and the worktree having been reaped by then.
// Neither is an error: a worktree that vanished because its worker finished is
// the fleet making progress.
func (r *Registry) WorkerProgressAt(now time.Time) []WorkerProgress {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []WorkerProgress
	for _, a := range r.agents {
		if a.Type != TypePolecat || !a.alive() {
			continue
		}
		w := WorkerProgress{
			Name:        a.Name,
			WorkItemID:  a.WorkItemID,
			PID:         a.PID,
			WorktreeDir: a.WorktreeDir,
		}
		if !a.StartTime.IsZero() {
			w.Age = now.Sub(a.StartTime)
		}
		if a.outputBuf != nil {
			if t := a.outputBuf.LastWriteTime(); !t.IsZero() {
				w.HasOutput = true
				w.PTYIdle = now.Sub(t)
			}
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
