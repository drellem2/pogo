package main

// The LIVE half of internal/progresswatch: the four measurements of mg-516e,
// each taken from the thing that actually knows the answer.
//
//	who is awake        pogod's agent registry (PTY last-write per worker)
//	who is writing      a walk of each worker's own worktree
//	who is computing    each worker's PROCESS SUBTREE, via internal/hostload
//	what has landed     the refinery's history and queue, plus done work items
//
// The detector itself is a pure function of the Snapshot this file builds, so
// nothing here decides anything: every judgement is in internal/progresswatch
// and every "could not measure" is carried as a field rather than as a zero.
// That split is deliberate — the tests that matter are of the judgement, and a
// judgement wired to live readers is a judgement nobody can test.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/hostload"
	"github.com/drellem2/pogo/internal/progresswatch"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/workitem"
)

// progressSampleTimeout bounds the whole sample. The CPU reading alone blocks
// for hostload.DefaultWindow and each worktree costs a walk, so a fleet of a
// dozen workers on a loaded box is seconds, not milliseconds. It runs off the
// heartbeat in a goroutine; the bound exists so a wedged filesystem cannot
// leave one accumulating per tick.
const progressSampleTimeout = 30 * time.Second

// fleetProgressSource builds the snapshot the detector judges.
//
// `queue` is a THUNK, not a pointer, for the reason refineryRepoActivity's is:
// an orchestration restart constructs a new *refinery.Refinery and reassigns
// the package variable, and a closure over the old pointer would go on
// answering from a refinery nobody is filling. `since` is the floor of the
// observable completion window — what makes an EMPTY history readable as
// "nothing in the 40m since pogod started" instead of as "nothing, ever".
func fleetProgressSource(reg *agent.Registry, queue func() *refinery.Refinery, since time.Time) progresswatch.SourceFunc {
	return func(now time.Time) (progresswatch.Snapshot, error) {
		if reg == nil {
			return progresswatch.Snapshot{}, errors.New("no agent registry: the worker population cannot be read")
		}
		ctx, cancel := context.WithTimeout(context.Background(), progressSampleTimeout)
		defer cancel()

		live := reg.WorkerProgressAt(now)
		snap := progresswatch.Snapshot{Now: now}
		roots := make([]int, 0, len(live))
		for _, w := range live {
			snap.Workers = append(snap.Workers, workerReading(w, now))
			if w.PID > 0 {
				roots = append(roots, w.PID)
			}
		}
		readWorkerCPU(ctx, &snap, roots)
		readFleetProgress(&snap, queue, since)
		return snap, nil
	}
}

// workerReading turns one registry row into the detector's Worker, asking the
// filesystem the one question the registry cannot answer.
func workerReading(w agent.WorkerProgress, now time.Time) progresswatch.Worker {
	out := progresswatch.Worker{
		Name:       w.Name,
		WorkItemID: w.WorkItemID,
		Age:        w.Age,
		PTYIdle:    w.PTYIdle,
		HasOutput:  w.HasOutput,
	}
	switch {
	case w.WorktreeDir == "":
		// A worker spawned with NoWorktree has no tree to be quiet in. That is
		// not an unwritten tree and must not be counted as one — its work goes
		// somewhere this detector cannot see, so it is unmeasurable here.
		out.WritesError = "spawned without a worktree"
	default:
		newest, err := gitgc.NewestWrite(w.WorktreeDir)
		switch {
		case err != nil:
			// Includes the tree having been reaped between the registry read
			// and the walk, which is the fleet making progress rather than a
			// fault — either way it is not evidence of quiet.
			out.WritesError = err.Error()
		case newest.IsZero():
			out.WritesKnown = true
		default:
			out.WritesKnown = true
			out.HasWrites = true
			if d := now.Sub(newest); d > 0 {
				out.WriteIdle = d
			}
		}
	}
	return out
}

// readWorkerCPU measures the worker SUBTREES. Subtrees, because a parent
// under-reports a fan-out workload — `go test` read 0.0% while its children
// burned 2–11% (mg-eb47) — and because a gating worker is silent on both PTY
// and worktree by construction, so this is the only member of the conjunction
// that can tell it from a blocked one.
func readWorkerCPU(ctx context.Context, snap *progresswatch.Snapshot, roots []int) {
	if len(roots) == 0 {
		// No workers, so the worker set consumes exactly nothing. That is a
		// measurement and not a blindness: attributing zero to an empty set is
		// the one case where the zero is certainly right.
		snap.CoresKnown = true
		snap.HostCores = runtime.NumCPU()
		return
	}
	r := hostload.Reader{Roots: roots}
	sample, err := r.Read(ctx)
	snap.HostCores = sample.Cores
	switch {
	case err != nil:
		snap.CoresError = err.Error()
	case !sample.Resolved():
		// Zeros that mean "this host cannot tell", not "the fleet is idle".
		snap.CoresError = sample.Unresolvable
	case !sample.Attributed:
		// Not one worker pid was in the process table. FleetCores is 0 because
		// nothing could be attributed, which is a different fact from idle.
		snap.CoresError = "no live worker process found in the process table"
	default:
		snap.CoresKnown = true
		snap.WorkerCores = sample.FleetCores
	}
	if snap.HostCores == 0 {
		// A failed sample carries no denominator, and a cores figure without
		// one is not a reading. hostload uses the same source for it.
		snap.HostCores = runtime.NumCPU()
	}
}

// readFleetProgress answers "when did this fleet last produce something".
//
// Three things count, and they are deliberately not just merges. A branch
// reaching the merge queue is a worker having finished its work; a work item
// closing is a piece of work landing whether or not it produced a merge (audits
// and investigations do not). Counting only merges would read a fleet of
// triage workers as producing nothing all night.
func readFleetProgress(snap *progresswatch.Snapshot, queue func() *refinery.Refinery, since time.Time) {
	snap.ProgressSince = since

	var errs []string
	read := false

	if queue != nil {
		if q := queue(); q != nil {
			read = true
			for _, mr := range q.History() {
				if mr.Status == refinery.StatusMerged && !mr.DoneTime.IsZero() {
					noteProgress(snap, mr.DoneTime, fmt.Sprintf("merge %s (%s) landed", mr.ID, mr.Branch))
				}
				noteProgress(snap, mr.SubmitTime, fmt.Sprintf("branch %s submitted by %s", mr.Branch, mr.Author))
			}
			for _, mr := range q.QueueWithProcessing() {
				noteProgress(snap, mr.SubmitTime, fmt.Sprintf("branch %s submitted by %s", mr.Branch, mr.Author))
			}
			st := q.GetStatus()
			snap.InFlight = st.Processing
			snap.InFlightSince = st.ProcessingSince
		}
	}

	items, err := workitem.List("done")
	if err != nil {
		errs = append(errs, "work items: "+err.Error())
	} else {
		read = true
		for _, it := range items {
			noteProgress(snap, it.CompletedAt, "work item "+it.ID+" done")
		}
	}

	snap.ProgressKnown = read
	if len(errs) > 0 {
		snap.ProgressError = joinNonEmpty(errs)
	}
	if !read && snap.ProgressError == "" {
		snap.ProgressError = "no refinery and no readable work items"
	}
}

// noteProgress keeps the most recent of the candidates, with what it was. A
// timestamp nobody can name is a number that cannot be chased.
func noteProgress(snap *progresswatch.Snapshot, at time.Time, what string) {
	if at.IsZero() || !at.After(snap.LastProgress) {
		return
	}
	snap.LastProgress = at
	snap.LastProgressWhat = what
}

func joinNonEmpty(parts []string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += "; "
		}
		out += p
	}
	return out
}
