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
	"io/fs"
	"os"
	"runtime"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/hostload"
	"github.com/drellem2/pogo/internal/progresswatch"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/workitem"
)

// The sample has TWO budgets, one per expensive term, and they are separate so
// that neither can spend the other's (drellem2/pogo#156).
//
// Until #156 there was one 30s context for the whole sample, and it was spent
// in program order: the worktree walks ran first and observed no context at
// all, and readWorkerCPU ran last on whatever was left. On a fleet holding two
// JS monorepo worktrees the walks took 53s, so the CPU read was handed an
// already-expired context, failed in milliseconds with "context deadline
// exceeded", and the computing term read "worker CPU unmeasurable" for 11
// hours — the one term that tells a gating worker from a blocked one. The walk
// time was not even bounded by the 30s: nothing on that path checked it.
//
// Now the CPU read runs FIRST, on its own context, and the walks share a
// separate budget that every walk observes.

// progressCPUTimeout bounds the worker-CPU read. The read is two process-table
// snapshots hostload.DefaultWindow apart — measured at 1.06s on a 10-core host
// (mg-edc2) — so ten windows leaves an order of magnitude for a loaded box's
// ps without letting a wedged one hold the sample.
const progressCPUTimeout = 10 * hostload.DefaultWindow

// progressWalkTimeout bounds all of a sample's worktree walks together. It
// runs off the heartbeat in a goroutine; the bound exists so a wedged
// filesystem cannot leave one accumulating per tick, and so a sweep finishes
// well inside progresswatch.DefaultInterval (5m).
//
// Each worktree's walk gets a SHARE of what is left, not a constant — see
// walkShare.
const progressWalkTimeout = 30 * time.Second

// walkShare is one worktree's sub-budget: what is left of the walk budget
// divided by the number of worktrees still to walk.
//
// It is DERIVED rather than a per-tree constant on purpose. Walk cost is
// dominated by a per-entry constant measured at ~3us/entry on this host and
// ~127us/entry on the deployment that reported #156 — about 50x apart — so any
// constant tuned here would be wrong by more than an order of magnitude there.
// A fair share of the remainder needs no such constant: it is fixed by the
// total budget and the population, and a tree that finishes early hands its
// unused share forward to the trees after it, so a fleet of small trees never
// trips it and one huge tree cannot starve the rest.
func walkShare(ctx context.Context, remaining int) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok || remaining < 1 {
		return progressWalkTimeout
	}
	left := time.Until(deadline)
	if left <= 0 {
		return 0
	}
	return left / time.Duration(remaining)
}

// fleetProgressSource builds the snapshot the detector judges.
//
// `queue` is a THUNK, not a pointer, for the reason refineryRepoActivity's is:
// an orchestration restart constructs a new *refinery.Refinery and reassigns
// the package variable, and a closure over the old pointer would go on
// answering from a refinery nobody is filling. `since` is the floor of the
// observable completion window — what makes an EMPTY history readable as
// "nothing in the 40m since pogod started" instead of as "nothing, ever".
// `skipDirs` is [progress_watch] skip_dirs: the directory names the worktree
// walk prunes (see gitgc.NewestWrite for why only this caller opts in).
func fleetProgressSource(reg *agent.Registry, queue func() *refinery.Refinery, since time.Time, skipDirs []string) progresswatch.SourceFunc {
	return func(now time.Time) (progresswatch.Snapshot, error) {
		if reg == nil {
			return progresswatch.Snapshot{}, errors.New("no agent registry: the worker population cannot be read")
		}
		snap := sampleWorkers(now, reg.WorkerProgressAt(now), progressCPUTimeout, progressWalkTimeout, skipDirs)
		readFleetProgress(&snap, queue, since)
		return snap, nil
	}
}

// sampleWorkers takes the two per-worker terms under their own budgets. The
// budgets are parameters so a test can starve one and watch the other survive,
// which is the property #156 lost and no test pinned.
func sampleWorkers(now time.Time, live []agent.WorkerProgress, cpuBudget, walkBudget time.Duration, skipDirs []string) progresswatch.Snapshot {
	snap := progresswatch.Snapshot{Now: now}
	roots := make([]int, 0, len(live))
	for _, w := range live {
		if w.PID > 0 {
			roots = append(roots, w.PID)
		}
	}

	// CPU first, on its own budget: `roots` derives only from the registry,
	// so nothing about the walks is needed to take it.
	cpuCtx, cpuCancel := context.WithTimeout(context.Background(), cpuBudget)
	readWorkerCPU(cpuCtx, &snap, roots)
	cpuCancel()

	walkCtx, walkCancel := context.WithTimeout(context.Background(), walkBudget)
	defer walkCancel()
	for i, w := range live {
		wctx, wcancel := context.WithTimeout(walkCtx, walkShare(walkCtx, len(live)-i))
		snap.Workers = append(snap.Workers, workerReading(wctx, w, now, skipDirs))
		wcancel()
	}
	return snap
}

// workerReading turns one registry row into the detector's Worker, asking the
// filesystem the one question the registry cannot answer. ctx bounds the walk;
// skipDirs is passed through to gitgc.NewestWrite.
func workerReading(ctx context.Context, w agent.WorkerProgress, now time.Time, skipDirs []string) progresswatch.Worker {
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
	case worktreeGone(w.WorktreeDir):
		// A worker that is REGISTERED and pid-alive, holding a path that is not
		// there. WorkerProgressAt records WorktreeDir at spawn and never
		// re-checks it, so this is the one place the fact can be noticed.
		//
		// It stays WritesKnown==false — an absent tree is not an unwritten one —
		// but the extra flag is what keeps it OFF the blind line: the detector
		// reports it as its own state rather than as a measurement it failed to
		// take. See Reading.WorktreeGone.
		out.WorktreeGone = true
		out.WritesError = "worktree no longer exists: " + w.WorktreeDir
	default:
		newest, err := gitgc.NewestWrite(ctx, w.WorktreeDir, skipDirs)
		switch {
		case err != nil && ctx.Err() != nil:
			// The walk ran out of its share of the sample budget. That is a
			// blindness about THIS worker's tree, attributed to it, rather
			// than — as before #156 — the whole fleet's CPU term going blind
			// for a tree it never touched. No partial maximum: see
			// gitgc.NewestWrite.
			out.WritesError = "worktree walk over its share of the sample budget: " + err.Error()
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

// worktreeGone answers whether the recorded worktree ROOT is absent, and only
// that.
//
// It asks the filesystem about the root rather than pattern-matching ENOENT out
// of gitgc.NewestWrite's error, because those are two different facts that
// produce the same error value: a file removed three levels down while the walk
// was in it also yields fs.ErrNotExist, and that is a race against ordinary
// agent work, not a reaped tree.
//
// Every other stat failure — EACCES on the root above all — answers FALSE here
// and falls through to the walk, which reports it as a blindness. That is
// correct and is the boundary worth stating: "I could not look" belongs on the
// blind line, and only "I looked, and it is not there" belongs on the new one.
func worktreeGone(dir string) bool {
	_, err := os.Stat(dir)
	return errors.Is(err, fs.ErrNotExist)
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
