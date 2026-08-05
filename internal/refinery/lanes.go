package refinery

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"time"
)

// DefaultMaxConcurrentMerges is how many merge requests the refinery runs at
// once when nothing configures a limit.
//
// It is 2, not "one per repo", because the bound this number sets is not a
// bound on correctness — the lane rule below already guarantees that — it is a
// bound on how much of the host the refinery is willing to commit to gates.
// A quality gate is the most expensive thing pogod runs (build.sh compiles and
// runs a full test suite), the host is shared with the polecat fleet, and gates
// running against each other inflate one another's wall time until a gate
// timeout starts failing branches that were fine. That failure mode is already
// documented and already annotated by the contention record (see
// gatecontention.go) precisely because it has happened.
//
// Two is what the incident that unparked this design needs: the queue that sat
// 70 minutes behind one slot-holder spanned exactly two repos, and one lane per
// repo drains it. Raising it buys parallelism across more repos at the cost of
// gate contention; setting it to 1 restores the historic single-slot refinery
// exactly, which is the intended rollback.
const DefaultMaxConcurrentMerges = 2

// laneKey returns the key identifying the resource a merge contends for.
//
// It is deliberately the repo BASENAME rather than the full path, because that
// is what ensureWorktree uses to name the refinery's private clone
// (WorktreeDir/<basename>). Two checkouts of different repos that happen to
// share a basename therefore share one clone, and giving them separate lanes
// would put two merges into a directory only one of them can own — a rebase in
// one would be clobbered by a checkout in the other. Keying on the clone means
// the lane rule is derived from the shared resource instead of hoping the two
// agree.
func laneKey(repoPath string) string {
	return filepath.Base(filepath.Clean(repoPath))
}

// RepoLane returns the lane a repo path's merges run in. Exported so a client
// grouping merge requests by what they contend for uses the refinery's own
// rule instead of reimplementing it — two answers to "do these two merges
// block each other?" is one answer too many, and the one in the CLI would be
// the one nobody noticed had drifted.
func RepoLane(repoPath string) string {
	return laneKey(repoPath)
}

// lane is one serial merge slot, owned by a repo for as long as a merge for
// that repo is in flight. Merges in different lanes run concurrently; merges in
// the same lane are strictly FIFO, because they share the refinery's clone of
// the repo AND they rebase onto the same target ref, so the second one's answer
// depends on the first one's outcome.
type lane struct {
	// key is the laneKey the lane is registered under.
	key string
	// mr is the merge request holding the lane.
	mr *MergeRequest
	// ctx/cancel are the per-merge cancellation handle. They are per-lane
	// rather than per-refinery so that cancelling one repo's merge cannot kill
	// another repo's gate — with a single shared cancel func, adding
	// concurrency would have silently turned Cancel into a broadcast.
	ctx    context.Context
	cancel context.CancelFunc
	// cancelRequested records that someone asked this merge to stop.
	cancelRequested bool
}

// LaneStatus reports one in-flight merge, with the repo whose lane it holds.
//
// The repo is on the record because the incident this design answers was
// unreadable without it: twelve merge requests waited on one gate belonging to
// a repo that none of the twelve shared, and no view said so. A count of
// in-flight merges answers "is anything happening"; only the repo answers "is
// anything happening for MY repo", which is the question a queued author has.
type LaneStatus struct {
	Repo   string    `json:"repo"`
	ID     string    `json:"id"`
	Branch string    `json:"branch"`
	Author string    `json:"author,omitempty"`
	Since  time.Time `json:"since,omitempty"`
}

// maxLanes returns the configured concurrency, clamped to at least 1.
func (r *Refinery) maxLanes() int {
	if r.cfg.MaxConcurrentMerges > 0 {
		return r.cfg.MaxConcurrentMerges
	}
	return DefaultMaxConcurrentMerges
}

// inFlightLocked returns the in-flight merge requests, longest-running first.
// Must be called with mu held.
//
// The order is by StartTime rather than map order so that every view built on
// it is stable across polls. A view that reorders itself on each read is the
// same reporting defect as one that omits rows: the reader cannot tell change
// from noise.
func (r *Refinery) inFlightLocked() []*MergeRequest {
	out := make([]*MergeRequest, 0, len(r.lanes))
	for _, ln := range r.lanes {
		out = append(out, ln.mr)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartTime.Equal(out[j].StartTime) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartTime.Before(out[j].StartTime)
	})
	return out
}

// laneHoldingLocked returns the lane whose merge request has the given ID, or
// nil. Must be called with mu held.
func (r *Refinery) laneHoldingLocked(id string) *lane {
	for _, ln := range r.lanes {
		if ln.mr != nil && ln.mr.ID == id {
			return ln
		}
	}
	return nil
}

// claimLane picks the first queued merge request whose repo lane is free,
// removes it from the queue, stamps it in flight and installs its lane.
// Returns (nil, nil) when nothing can start right now — either every lane is
// taken, concurrency is at its cap, or the queue holds only work for repos
// already merging.
//
// examined is a set of merge-request IDs to skip; a caller scanning for several
// dispatchable items passes it so an item put back on QA hold is not picked up
// again in the same pass, which would loop forever. Nil is fine for a single
// claim.
//
// Ordering: the queue is scanned front to back, so within one repo the order is
// exactly the submit order it has always been. Across repos an item whose lane
// is busy is passed over for a later item whose lane is free — that overtaking
// IS the change, and it is bounded: it can only ever be by a merge that could
// not have contended with the one it passed.
func (r *Refinery) claimLane(examined map[string]bool) (*lane, *MergeRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopping || len(r.lanes) >= r.maxLanes() {
		return nil, nil
	}
	for i, mr := range r.queue {
		if examined[mr.ID] {
			continue
		}
		key := laneKey(mr.RepoPath)
		if _, busy := r.lanes[key]; busy {
			continue
		}
		r.queue = append(r.queue[:i], r.queue[i+1:]...)
		mr.StartTime = time.Now()
		mr.Status = StatusProcessing
		ln := r.beginLaneLocked(key, mr)
		r.saveStateLocked()
		return ln, mr
	}
	return nil, nil
}

// beginLaneLocked registers a lane for mr with a fresh cancellation handle.
// Must be called with mu held.
func (r *Refinery) beginLaneLocked(key string, mr *MergeRequest) *lane {
	ctx, cancel := context.WithCancel(context.Background())
	ln := &lane{key: key, mr: mr, ctx: ctx, cancel: cancel}
	if r.lanes == nil {
		r.lanes = make(map[string]*lane)
	}
	r.lanes[key] = ln
	return ln
}

// endLaneLocked releases the lane and tears down its cancellation handle.
// Must be called with mu held.
func (r *Refinery) endLaneLocked(ln *lane) {
	if ln == nil {
		return
	}
	if ln.cancel != nil {
		ln.cancel()
	}
	// Only delete the lane if it is still ours: a released lane can already
	// have been re-claimed by the next merge for the same repo.
	if cur, ok := r.lanes[ln.key]; ok && cur == ln {
		delete(r.lanes, ln.key)
	}
}

// dispatchReady starts every merge that can run right now, then returns. It is
// what the queue loop calls in place of the old "process one item and block".
//
// It does NOT wait for the merges it starts. That is the whole point: the loop
// stays free to accept submissions, serve views and start work for other repos
// while a long gate runs.
func (r *Refinery) dispatchReady() {
	examined := make(map[string]bool)
	for {
		ln, mr := r.claimLane(examined)
		if ln == nil {
			return
		}
		examined[mr.ID] = true
		if r.holdForQA(ln, mr) {
			continue
		}
		r.laneWG.Add(1)
		go func(ln *lane, mr *MergeRequest) {
			defer r.laneWG.Done()
			r.runLane(ln, mr)
			// A freed lane is new capacity; wake the loop so the next merge
			// for this repo starts now rather than on the poll tick.
			r.wake()
		}(ln, mr)
	}
}

// holdForQA runs the QA gate for a claimed merge request and, when QA is not
// done, returns it to the queue and releases its lane. Reports whether it was
// held.
//
// The check runs outside the refinery lock (it walks the macguffin work
// directory) but with the lane already claimed, so a second merge for the same
// repo cannot slip past a held one and reorder that repo's queue.
func (r *Refinery) holdForQA(ln *lane, mr *MergeRequest) bool {
	result, qaItemID := r.checkQAGate(mr.Author)
	if result != QAGateHold {
		return false
	}
	r.holdMergeRequest(mr, qaItemID)
	return true
}

// runLane runs the merge pipeline for one lane's merge request to completion in
// the calling goroutine, resolves the request into history and fires the
// terminal callback. It is the body both the concurrent dispatcher and the
// synchronous processNext share.
func (r *Refinery) runLane(ln *lane, mr *MergeRequest) {
	log.Printf("refinery: processing MR %s branch=%s repo-lane=%s (%d/%d lanes busy)",
		mr.ID, mr.Branch, ln.key, r.laneCount(), r.maxLanes())

	outcome, err := r.processMerge(mr)

	r.mu.Lock()
	r.endLaneLocked(ln)
	mr.GateOutput = outcome.GateOutput
	mr.DeployError = outcome.DeployError
	mr.PostMergeError = outcome.PostMergeError
	mr.MergedSHA = outcome.MergedSHA
	mr.AlreadyMerged = outcome.AlreadyMerged
	mr.DoneTime = time.Now()
	alreadyMerged := outcome.AlreadyMerged
	if isCancelled(err) {
		// Cancelled, not failed. Neither the author's failure streak nor the
		// failed callback applies: the merge did not fail on its merits, and
		// firing onFailed here would reopen a work item on an operator
		// action rather than on a defect in the branch (mg-8595).
		mr.Status = StatusCancelled
		mr.Error = err.Error()
		log.Printf("refinery: MR %s cancelled mid-flight branch=%s author=%s (%v)", mr.ID, mr.Branch, mr.Author, err)
	} else if err != nil {
		mr.Status = StatusFailed
		mr.Error = err.Error()
		// The author's consecutive-failure streak counts DEFECTS only (mg-e5c2).
		// The streak feeds an escalation whose advice is "consider stopping the
		// polecat or reassigning the work item" — advice about the author. A
		// suppressed-DNS window is not evidence about an author, and on
		// 2026-08-05 it would have tripped that threshold for ten of them at
		// once. Infrastructure and contention failures are still recorded, still
		// mailed, and still fire onFailed; they just do not accumulate into a
		// verdict on whoever happened to be at the head of the queue.
		countsAgainstAuthor := mr.FailureClass == "" || mr.FailureClass == ClassDefect
		if mr.Author != "" && countsAgainstAuthor {
			r.failureCounts[mr.Author]++
			mr.FailureCount = r.failureCounts[mr.Author]
			if r.cfg.FailureThreshold > 0 && mr.FailureCount >= r.cfg.FailureThreshold {
				mr.ThresholdReached = true
				log.Printf("refinery: author %s reached failure threshold (%d consecutive failures)", mr.Author, mr.FailureCount)
			}
		} else if mr.Author != "" {
			mr.FailureCount = r.failureCounts[mr.Author]
			log.Printf("refinery: MR %s failed as %s — NOT counted against author %s's failure streak (still %d); this establishes nothing about the branch",
				mr.ID, mr.FailureClass, mr.Author, mr.FailureCount)
		}
		log.Printf("refinery: REJECTED MR %s branch=%s author=%s status=%s attempts=%d reason=%v (author_failure_streak=%d)",
			mr.ID, mr.Branch, mr.Author, mr.StatusLabel(), mr.AttemptCount, err, mr.FailureCount)
	} else {
		mr.Status = StatusMerged
		if mr.Author != "" {
			delete(r.failureCounts, mr.Author)
		}
		if alreadyMerged {
			log.Printf("refinery: MR %s resolved as already merged (no-op) branch=%s author=%s sha=%s", mr.ID, mr.Branch, mr.Author, mr.MergedSHA)
		} else {
			log.Printf("refinery: MR %s merged successfully branch=%s author=%s sha=%s", mr.ID, mr.Branch, mr.Author, mr.MergedSHA)
		}
		// The merge succeeded and its declared follow-on did not. Say so at
		// merged-level volume: the status stays "merged", so a reader keying
		// only off status would otherwise see unqualified success (mg-6879).
		if mr.PostMergeError != "" {
			log.Printf("refinery: MR %s merged but its POST-MERGE STEP FAILED branch=%s author=%s: %s — the work item will NOT be marked done", mr.ID, mr.Branch, mr.Author, mr.PostMergeError)
		}
	}
	r.history = append(r.history, mr)
	r.pruneHistoryLocked()
	r.saveStateLocked()
	onMerged := r.onMerged
	onFailed := r.onFailed
	r.mu.Unlock()

	// Fire callbacks outside the lock. A cancelled MR fires neither: it did
	// not merge, and it did not fail on its merits.
	switch {
	case isCancelled(err):
	case err != nil && onFailed != nil:
		onFailed(mr)
	case err == nil && onMerged != nil:
		onMerged(mr)
	}
}

// laneCount returns how many merges are in flight.
func (r *Refinery) laneCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lanes)
}

// InFlight returns one row per in-flight merge, longest-running first.
func (r *Refinery) InFlight() []LaneStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	mrs := r.inFlightLocked()
	out := make([]LaneStatus, 0, len(mrs))
	for _, mr := range mrs {
		out = append(out, LaneStatus{
			Repo:   laneKey(mr.RepoPath),
			ID:     mr.ID,
			Branch: mr.Branch,
			Author: mr.Author,
			Since:  mr.StartTime,
		})
	}
	return out
}
