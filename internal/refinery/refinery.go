// Package refinery implements a deterministic merge queue loop inside pogod.
//
// The refinery is NOT an agent. It is a mechanical loop that picks up
// merge-ready branches from polecats, runs quality gates (build, test, lint),
// and either fast-forward merges to main or rejects with notification.
//
// It maintains its own git worktrees under ~/.pogo/refinery/worktrees/ and
// never touches agent or user working directories.
package refinery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/hostload"
)

// DefaultPollInterval is how often the refinery checks for new merge requests
// when nothing wakes it sooner. Submit signals the loop directly, so the poll
// is a backstop (held-MR retries, missed wakes), not the pickup latency.
const DefaultPollInterval = 30 * time.Second

// Default history limits.
const (
	DefaultMaxHistoryLen = 100
	DefaultMaxHistoryAge = 7 * 24 * time.Hour // 7 days
)

// DefaultFailureThreshold is the number of consecutive failures for the same
// author before the refinery flags the MR for escalation.
const DefaultFailureThreshold = 3

// Config holds refinery configuration.
type Config struct {
	// Enabled controls whether the refinery loop runs.
	Enabled bool
	// PollInterval is how often the loop checks for queued items.
	PollInterval time.Duration
	// WorktreeDir is where the refinery creates git worktrees.
	// Default: ~/.pogo/refinery/worktrees/
	WorktreeDir string
	// MaxHistoryLen is the maximum number of completed merge requests to keep.
	// Zero means use DefaultMaxHistoryLen. Negative means unlimited.
	MaxHistoryLen int
	// MaxHistoryAge is the maximum age of completed merge requests to keep.
	// Zero means use DefaultMaxHistoryAge. Negative means no age limit.
	MaxHistoryAge time.Duration
	// MacguffinDir is the path to the macguffin work directory (e.g. ~/.macguffin/work).
	// If empty, the QA gate is disabled.
	MacguffinDir string
	// FailureThreshold is the number of consecutive failures for the same
	// author before the MR is flagged for escalation (ThresholdReached is set).
	// Zero means use DefaultFailureThreshold. Negative means disable threshold.
	FailureThreshold int
	// MaxConcurrentMerges bounds how many merge requests are processed at
	// once. Merges are partitioned into per-repo lanes (see lanes.go): two
	// merges for the same repo are never concurrent whatever this is set to,
	// and this caps how many DIFFERENT repos may merge at the same time.
	// Zero means DefaultMaxConcurrentMerges. One restores the historic
	// single-slot refinery exactly.
	MaxConcurrentMerges int
	// StatePath is where refinery state (queue, history, lost list) is
	// persisted so it survives pogod restarts. Empty disables persistence
	// (used by most unit tests). Default: ~/.pogo/refinery-state.json
	StatePath string
}

// DefaultConfig returns a Config with sensible defaults. Pogo state paths
// derive from the pogo state dir ($POGO_HOME, default ~/.pogo) so an isolated
// daemon never touches the real user's refinery state (mg-3dc3);
// MacguffinDir stays HOME-derived because ~/.macguffin belongs to mg, not
// pogo.
func DefaultConfig() Config {
	pogoHome := config.PogoHome()
	home, _ := os.UserHomeDir()
	return Config{
		Enabled:      true,
		PollInterval: DefaultPollInterval,
		WorktreeDir:  filepath.Join(pogoHome, "refinery", "worktrees"),
		MacguffinDir: filepath.Join(home, ".macguffin", "work"),
		StatePath:    filepath.Join(pogoHome, "refinery-state.json"),
	}
}

// MergeStatus represents the outcome of a merge attempt.
type MergeStatus string

const (
	StatusQueued     MergeStatus = "queued"
	StatusProcessing MergeStatus = "processing"
	StatusMerged     MergeStatus = "merged"
	StatusFailed     MergeStatus = "failed"
	StatusCancelled  MergeStatus = "cancelled"
	// StatusLost marks an MR that restart recovery could not carry forward
	// (state file unreadable, branch deleted, remote unreachable). Surfaced
	// via HTTP 410 Gone so callers can distinguish it from "never existed"
	// and auto-resubmit.
	StatusLost MergeStatus = "lost"
)

// MergeRequest represents a branch submitted for merging.
type MergeRequest struct {
	ID        string      `json:"id"`
	RepoPath  string      `json:"repo_path"`
	Branch    string      `json:"branch"`
	TargetRef string      `json:"target_ref"` // e.g. "main"
	Author    string      `json:"author"`     // agent name that submitted
	Status    MergeStatus `json:"status"`
	// AutoCreateTargetRef opts the request into branching the target ref off
	// the repo's default branch when it does not yet exist on origin. Off by
	// default — the safe behaviour (typo in target_ref → error) stays the
	// default. Submitters that genuinely want a new feature branch carved
	// off the default must set this explicitly.
	AutoCreateTargetRef bool `json:"auto_create_target_ref,omitempty"`
	// DeferDone opts the submitter into owning its own post-merge lifecycle
	// (gh drellem2/pogo #81). When true, pogod's OnMerged reap path skips the
	// auto-done + auto-stop it would otherwise apply the instant a polecat's
	// branch merges — a --branch (PR-flow) polecat still has post-merge work to
	// do (open the PR, run verify checks, mail the PR URL) and must not be
	// killed mid-flow. The polecat calls `mg done` itself when its full flow
	// finishes. A bounded backstop reaps + escalates a deferred polecat that
	// never completes, so this does not regress the gh #34/#35 slot guarantees.
	// Off by default — the auto-done/auto-stop path stays the default.
	DeferDone bool `json:"defer_done,omitempty"`
	// PRFlow marks a merge request whose target is an *integration* branch
	// rather than the repo's default branch (mg-7746). Such a merge is one step
	// of a pull-request flow: the deliverable is a PR from the integration
	// branch to the default branch, which the submitting polecat has not opened
	// yet. Completion is that PR, not this merge — so pogod's reap path treats
	// PRFlow exactly like DeferDone: no auto-done, no auto-stop, bounded
	// backstop armed.
	//
	// Derived by Submit from the repo's default branch, never taken from the
	// submitter: the bug this fixes went unnoticed for three occurrences
	// precisely because it depended on a caller remembering to pass a flag.
	// When the default branch cannot be determined the field stays false and
	// the historic fast path applies.
	PRFlow     bool      `json:"pr_flow,omitempty"`
	SubmitTime time.Time `json:"submit_time"`
	// StartTime is when this request left the queue and became the in-flight
	// one. Zero while it is still pending. It is persisted, so it survives a
	// restart and still answers "how long has this been going" afterwards —
	// and it is separate from Progress.StartTime, which times the current
	// GATE, not the whole merge.
	StartTime  time.Time `json:"start_time,omitempty"`
	DoneTime   time.Time `json:"done_time,omitempty"`
	Error      string    `json:"error,omitempty"`
	GateOutput string    `json:"gate_output,omitempty"`
	// AlreadyMerged marks an MR whose branch had already landed on the target
	// before processing began (a re-submit of a merged branch, gh #34). The
	// MR resolves as StatusMerged — so poll loops keying off status terminate
	// normally — but gates, push, and deploy were skipped as no-ops.
	AlreadyMerged bool `json:"already_merged,omitempty"`
	// DeployError is set when a post-merge deploy hook ran and failed. The
	// merge itself still succeeded (Status remains StatusMerged); deploy
	// failure is surfaced for diagnostics, not rolled back. Empty when no
	// deploy was configured or the deploy succeeded.
	DeployError string `json:"deploy_error,omitempty"`
	// MergedSHA is the commit the branch actually landed as on the target ref
	// — the tip of the target after the fast-forward, as observed by the
	// refinery immediately after it pushed.
	//
	// The refinery always knew this value and used to throw it away: it went
	// into the merged event, the PR-close comment and the deploy hook's
	// worktree, and never onto the MergeRequest, so no actor downstream of the
	// merge could name the commit (mg-6879). That is the gap that made
	// post-merge work impossible to place anywhere but the authoring worker:
	// the worker's own `git rev-parse` was the only reachable answer, and the
	// worker is the one actor guaranteed not to outlive the merge.
	//
	// On the already-merged no-op path this is the branch tip, which the probe
	// has just confirmed is an ancestor of the target — i.e. still the commit
	// the branch's content landed as. Empty only when git could not be asked.
	MergedSHA string `json:"merged_sha,omitempty"`
	// PostMergeTag names a git tag the REFINERY creates on MergedSHA and pushes
	// after a successful merge, when the submitter declares one
	// (`--post-merge-tag`). It exists because of a constraint that no other
	// actor satisfies (mg-6879).
	//
	// A release cut has to tag the commit its own version bump landed as. That
	// requires seeing the merged SHA and outliving the merge, and the two
	// obvious candidates each have exactly one of those properties: the
	// authoring polecat sees the SHA but is reaped when its merge lands, while
	// a pre-merge script (bump-version.sh --tag, mg-cef7) outlives the merge
	// but can only tag a SHA the refinery is about to rewrite in its rebase.
	// So the naive fix for either defect is the other defect. The refinery has
	// both properties — it computes MergedSHA and it lives in pogod — which is
	// why the step belongs here rather than in the worker's sequence.
	//
	// Running it inside the merge pipeline (see processMerge) also removes the
	// race rather than deferring it: the whole step completes before OnMerged
	// fires, so there is no window in which the reap can land between the merge
	// and the tag.
	PostMergeTag string `json:"post_merge_tag,omitempty"`
	// PostMergeError is set when a declared post-merge step ran and failed. The
	// merge itself still succeeded (Status stays StatusMerged) — the tag is
	// pushed after the branch has landed on origin and nothing unwinds it.
	//
	// It is load-bearing rather than diagnostic, which is what separates it
	// from DeployError: pogod's reap consults it and refuses to mark the work
	// item done when it is set (see resolvePostMergeWork). The defect this
	// closes was silent — an item read `done` with exit_code=0 while its
	// deliverable did not exist — so a post-merge step that fails must not be
	// able to resolve as completion.
	PostMergeError string `json:"post_merge_error,omitempty"`
	// Verdict is the AUTHOR'S OWN result for its work item, handed over at
	// submit time so that it survives the merge (mg-dfea). It is a JSON object
	// and it is carried verbatim: the refinery does not read it, validate its
	// contents, or act on it.
	//
	// It exists because the author has no other reachable moment to record one.
	// pogod closes the work item the instant the branch merges and stops the
	// polecat ~0.5s later, so the polecat's own `mg done --result` arrives after
	// the item is already `done` — and mg REFUSES a second done rather than
	// clobbering the first. Nothing is overwritten; the author is simply
	// preempted, and its verdict has nowhere to land. Measured over the live
	// store on 2026-08-06: 139 of 149 landed items carried a refinery-authored
	// sidecar and 10 carried any field beyond branch/mr/target.
	//
	// Submit time is the only moment that works. Before the merge the author
	// cannot call `mg done` (that would close the item early); after the merge
	// it is already closed and usually already stopped. Its verdict is about
	// the work it did, not about the merge, so it is knowable at submit.
	Verdict json.RawMessage `json:"verdict,omitempty"`
	// FailureCount is the AUTHOR's consecutive-failure streak, not this merge's
	// attempt count. The two were confused during the 2026-08-05 incident, where
	// every one of thirty-one failures read `failure_count=1` and that was taken
	// to mean one attempt had been made. It did mean that — but only by accident,
	// because no retry existed. AttemptCount is the attempt count (mg-e5c2).
	FailureCount     int  `json:"failure_count"`
	ThresholdReached bool `json:"threshold_reached,omitempty"`
	// AttemptCount is how many attempts this merge request took, failed and
	// successful. Attempts holds one verbatim record per FAILED attempt: the
	// transport it was made over, the git command as invoked, git's raw output,
	// the class, and whether a retry followed — and if not, why not.
	//
	// "Failed once" and "failed after three attempts" are different records
	// here, which is the whole point: on 2026-08-05 the log and the mail could
	// not tell them apart, so nobody could tell whether a retry policy existed.
	AttemptCount int              `json:"attempt_count,omitempty"`
	Attempts     []AttemptFailure `json:"attempts,omitempty"`
	// FailureClass is the classification of the TERMINAL failure, empty on a
	// merged request. It is the field that separates "your code is wrong" from
	// "the network hiccuped" without reading the error text — see StatusLabel.
	FailureClass FailureClass `json:"failure_class,omitempty"`
	// NotRetriedReason states why no further attempt was made. Always set on a
	// terminal failure, including the retryable classes that ran out of budget,
	// so the absence of a retry is legible rather than looking like a policy
	// that does not exist (mg-e5c2, requirement 3).
	NotRetriedReason string `json:"not_retried_reason,omitempty"`
	// RecoveredOnAttempt names the attempt that won when earlier ones failed —
	// zero when the first attempt succeeded. A retried success that does not say
	// so turns a flaky host into an invisible one.
	RecoveredOnAttempt  int     `json:"recovered_on_attempt,omitempty"`
	RetryBackoffSeconds float64 `json:"retry_backoff_seconds,omitempty"`
	// Progress is the liveness record for the long-running step this MR is in
	// (quality-gates). It is refreshed on a bounded interval by the goroutine
	// running that step, so a reader can tell a gate that is running from one
	// whose runner is gone — see StepProgress for what each field does and
	// does not discriminate. Nil before the gates start; retained afterwards
	// with EndTime set, as the record of how long they took.
	Progress *StepProgress `json:"progress,omitempty"`
}

// StatusLabel is the status as a HUMAN reads it, with the failure class folded
// in: `failed(infrastructure)` rather than a bare `failed` (mg-e5c2).
//
// A coordinator triaging a queue sees status first, and on 2026-08-05 thirty-one
// bare `failed` rows invited dispatching thirty-one fixes for defects that did
// not exist; only reading each error line prevented it. This is the token that
// stops that being necessary — `pogo refinery show`, `refinery history` and the
// failure mail all render this rather than Status.
//
// The machine-readable Status field is deliberately UNCHANGED. Every polecat in
// the fleet polls `pogo refinery show --json | jq -r .status` and breaks out of
// its wait loop on the literal strings "merged"/"failed"/"lost"; a new token
// there would leave a polecat spinning through a failure it was supposed to
// report, which is a worse loss than the triage confusion this fixes. The class
// travels as its own field (`failure_class`) for machines and inside this label
// for humans.
func (m *MergeRequest) StatusLabel() string {
	if m.Status != StatusFailed || m.FailureClass == "" || m.FailureClass == ClassDefect {
		return string(m.Status)
	}
	return fmt.Sprintf("%s(%s)", m.Status, m.FailureClass)
}

// OnMerged is called when a branch is successfully merged.
type OnMerged func(mr *MergeRequest)

// OnFailed is called when a merge attempt fails quality gates.
type OnFailed func(mr *MergeRequest)

// OnSubmit is called when a merge request is submitted to the queue —
// at StatusQueued, BEFORE any merge is attempted.
//
// It has no user today. pogod formerly used it to unlink the submitting
// polecat's worktree; that stranded the polecat in a non-git directory on
// every merge failure and was deleted (gh #88). A callback wired here fires
// on a merge request whose outcome is not yet known, so it must not take any
// action that a failed merge would need undone — in particular it must not
// touch the author's worktree.
type OnSubmit func(mr *MergeRequest)

// Refinery is the merge queue loop.
type Refinery struct {
	cfg Config

	mu            sync.Mutex
	queue         []*MergeRequest          // ordered FIFO
	history       []*MergeRequest          // completed (merged or failed)
	byID          map[string]*MergeRequest // all requests by ID
	failureCounts map[string]int           // consecutive failure count per author

	// lanes holds the in-flight merge requests, keyed by laneKey — at most one
	// per repo, at most maxLanes() in total. An item is always in exactly one
	// of queue, lanes, or history, so the persisted snapshot never has an MR in
	// limbo.
	//
	// This used to be a single `processing *MergeRequest`, because the queue
	// loop ran one merge at a time for every repo on the host. That is the
	// serialisation mg-37ad removed: a lane's occupant blocks only merges that
	// share its git clone and its target ref.
	lanes map[string]*lane
	// laneWG counts running lane goroutines so Stop can wait them out. Without
	// it, cancelling the loop's context would return from Start while merges
	// were still pushing — and pogod builds a REPLACEMENT Refinery from the
	// state file on restart, so two refineries would be driving the same clone.
	laneWG sync.WaitGroup
	// stopping blocks new dispatches once shutdown has begun. In-flight lanes
	// are allowed to finish; nothing new is started.
	stopping bool
	// recovered holds in-flight items loaded from the state file. They are
	// resolved (ancestor probe) at the top of Start, once callbacks are
	// wired — see resolveRecovered.
	recovered []*MergeRequest
	// lost records MRs that restart recovery could not carry forward.
	lost []LostEntry
	// pruned is a ring of MR IDs removed from history by pruning, kept so
	// lookups can answer "pruned from history" instead of "not found".
	pruned []string

	// store persists state across restarts; nil when cfg.StatePath is empty.
	store *store

	onMerged OnMerged
	onFailed OnFailed
	onSubmit OnSubmit

	// nowFunc is used for time-based pruning; defaults to time.Now.
	// Override in tests to control time.
	nowFunc func() time.Time

	// heartbeatInterval overrides gateHeartbeatInterval. Zero means use the
	// package default; tests set it short so a beat is observable.
	heartbeatInterval time.Duration

	// loadSampler measures how much of the host the fleet is holding while a
	// gate runs, so a slow gate can be told apart from a slow change. Nil
	// disables sampling entirely — the gate still runs and still reports
	// everything it reported before, it just says nothing about the host.
	// See setLoadSampler.
	loadSampler hostload.Sampler

	// The cancellation handle that lets Cancel reach a PROCESSING merge request
	// instead of refusing it lives on the lane (see lanes.go), not here. It
	// used to be one shared context.CancelFunc on the Refinery, which was
	// correct while exactly one merge could be in flight and would have become
	// a broadcast the moment a second one could: cancelling one repo's merge
	// would have killed another repo's gate.

	// wakeCh wakes the queue loop ahead of the poll tick. Buffered with
	// capacity 1 so signalling never blocks and concurrent signals collapse
	// into a single wake.
	wakeCh chan struct{}

	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a new Refinery with the given config.
func New(cfg Config) (*Refinery, error) {
	if cfg.WorktreeDir == "" {
		return nil, fmt.Errorf("worktree dir is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.MaxHistoryLen == 0 {
		cfg.MaxHistoryLen = DefaultMaxHistoryLen
	}
	if cfg.MaxHistoryAge == 0 {
		cfg.MaxHistoryAge = DefaultMaxHistoryAge
	}
	if cfg.FailureThreshold == 0 {
		cfg.FailureThreshold = DefaultFailureThreshold
	}
	if cfg.MaxConcurrentMerges == 0 {
		cfg.MaxConcurrentMerges = DefaultMaxConcurrentMerges
	}
	if cfg.MaxConcurrentMerges < 1 {
		return nil, fmt.Errorf("max_concurrent_merges must be at least 1, got %d "+
			"(1 is the historic single-slot refinery; there is no setting that means 'merge nothing')", cfg.MaxConcurrentMerges)
	}
	if err := os.MkdirAll(cfg.WorktreeDir, 0755); err != nil {
		return nil, fmt.Errorf("create worktree dir: %w", err)
	}
	r := &Refinery{
		cfg:           cfg,
		byID:          make(map[string]*MergeRequest),
		failureCounts: make(map[string]int),
		lanes:         make(map[string]*lane),
		done:          make(chan struct{}),
		nowFunc:       time.Now,
		wakeCh:        make(chan struct{}, 1),
	}
	if cfg.StatePath != "" {
		r.store = &store{path: cfg.StatePath}
		if err := r.loadState(); err != nil {
			return nil, err
		}
	}
	// The refinery runs inside pogod, and every agent on this host is a
	// descendant of that process, so our own pid is the fleet root. Attribution
	// is by subtree rather than by agent on purpose: the instance that prompted
	// this was ONE polecat that had forked three compute processes, and any
	// count of agents reads that as an idle host.
	r.loadSampler = &hostload.Reader{Roots: []int{os.Getpid()}}
	return r, nil
}

// setLoadSampler replaces the host-load sampler, or disables sampling when
// nil. Tests use it to feed a known host rather than the one they run on: a
// saturated host and an idle one are both states a test needs and neither is
// one it can create.
func (r *Refinery) setLoadSampler(s hostload.Sampler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadSampler = s
}

// loadState restores persisted state from cfg.StatePath. A missing file is a
// clean first start. A corrupt file is moved aside (evidence preserved) and
// the refinery starts empty rather than staying down. A file written by a
// newer binary is a hard error — refusing to run means refusing to overwrite
// state we don't understand.
func (r *Refinery) loadState() error {
	st, err := r.store.load()
	if err != nil {
		if errors.Is(err, errStateCorrupt) {
			backup := r.store.path + ".corrupt"
			log.Printf("refinery: %v — moving aside to %s and starting empty", err, backup)
			if mvErr := os.Rename(r.store.path, backup); mvErr != nil {
				log.Printf("refinery: failed to back up corrupt state file: %v", mvErr)
			}
			return nil
		}
		return err
	}
	if st == nil {
		return nil
	}

	// Rebuild in-memory state. byID indexes queue + history + the recovered
	// in-flight items so `refinery show` answers correctly from the moment
	// the API is up.
	r.history = st.History
	if st.FailureCounts != nil {
		r.failureCounts = st.FailureCounts
	}

	// In-flight items come from ProcessingLanes (written since mg-37ad) and
	// from the single Processing slot (written by every pogod before it). Both
	// are read so a state file straddling the change loads without loss.
	// Resolved by resolveRecovered at Start — never blindly re-run: the crash
	// may have happened after `git push` landed the merge.
	inFlight := make(map[string]bool)
	for _, mr := range st.ProcessingLanes {
		if mr == nil || inFlight[mr.ID] {
			continue
		}
		inFlight[mr.ID] = true
		r.recovered = append(r.recovered, mr)
		r.byID[mr.ID] = mr
	}
	if st.Processing != nil && !inFlight[st.Processing.ID] {
		inFlight[st.Processing.ID] = true
		r.recovered = append(r.recovered, st.Processing)
		r.byID[st.Processing.ID] = st.Processing
	}

	// The persisted queue MIRRORS the in-flight items (see saveStateLocked) so
	// that a pogod predating lanes still finds them. This binary has them from
	// ProcessingLanes already, so drop the mirror rather than queue a duplicate
	// of a merge that is about to be recovered.
	r.queue = make([]*MergeRequest, 0, len(st.Queue))
	for _, mr := range st.Queue {
		if mr == nil || inFlight[mr.ID] {
			continue
		}
		r.queue = append(r.queue, mr)
		r.byID[mr.ID] = mr
	}
	for _, mr := range r.history {
		r.byID[mr.ID] = mr
	}
	r.pruned = st.PrunedIDs

	// Lost entries age out after lostMaxRestarts pogod restarts.
	for _, le := range st.Lost {
		le.Restarts++
		if le.Restarts <= lostMaxRestarts {
			r.lost = append(r.lost, le)
		}
	}

	log.Printf("refinery: restored state from %s (queue=%d history=%d lost=%d in-flight=%d)",
		r.store.path, len(r.queue), len(r.history), len(r.lost), len(r.recovered))
	return nil
}

// saveStateLocked snapshots the current state and hands it to the store's
// writer. Must be called with mu held. Persistence errors are logged, not
// propagated — a full disk must not wedge the merge queue.
//
// It MARSHALS here and writes nowhere: the disk work (write, fsync, rename)
// runs on the store's own goroutine, so mu is released the moment this returns
// (mg-538e). Marshalling stays under mu because that is the consistency
// boundary — the MergeRequest pointers in the snapshot are live objects other
// goroutines mutate.
//
// A caller that must not return until the change is durable calls flushState
// AFTER releasing mu. Waiting for the write while still holding mu would
// reinstate exactly the hold this split removes.
func (r *Refinery) saveStateLocked() {
	if r.store == nil {
		return
	}
	// In-flight items are whatever holds a lane, plus anything still awaiting
	// the recovery probe (Start has not run resolveRecovered yet).
	inFlight := r.inFlightLocked()
	inFlight = append(inFlight, r.recovered...)

	// The queue is written as pending items PLUS a copy of every in-flight
	// item at the head, marked queued.
	//
	// That mirror is the downgrade path, and it is not decoration. A pogod
	// predating per-repo lanes reads only `processing` (one slot) and `queue`.
	// Handed a file with three merges in flight it would keep at most one and
	// silently drop the rest — merge requests lost from a redesign of the thing
	// that merges. With the mirror it re-queues all three instead, and the
	// already-merged probe at the top of processMerge (gh #34) makes re-running
	// one that had landed a no-op. This binary strips the mirror on load.
	queue := make([]*MergeRequest, 0, len(r.queue)+len(inFlight))
	for _, mr := range inFlight {
		cp := *mr
		cp.Status = StatusQueued
		cp.StartTime = time.Time{}
		queue = append(queue, &cp)
	}
	queue = append(queue, r.queue...)

	st := &persistedState{
		Queue:           queue,
		ProcessingLanes: inFlight,
		History:         r.history,
		FailureCounts:   r.failureCounts,
		Lost:            r.lost,
		PrunedIDs:       r.pruned,
	}
	data, err := r.store.marshal(st)
	if err != nil {
		log.Printf("refinery: failed to marshal state: %v", err)
		return
	}
	r.store.enqueue(data)
}

// flushState blocks until every state change persisted so far is durable on
// disk. It is the write-through half of saveStateLocked and restores the
// pre-mg-538e guarantee that an unclean death after a public API call still
// finds the change in the state file.
//
// **MUST NOT be called with r.mu held.** It waits on an fsync; doing that
// under r.mu is the defect this repair exists to remove, and a fix that
// reintroduced it one layer up would not be a fix (see
// TestStateWriteDoesNotHoldRefineryMutex, which asserts the property rather
// than the implementation).
//
// The high-frequency gate-heartbeat saves deliberately do NOT flush: a
// heartbeat lost to a crash costs nothing, and paying an fsync per beat is
// what made the state write hot in the first place.
func (r *Refinery) flushState() {
	if r.store == nil {
		return
	}
	if err := r.store.flush(); err != nil {
		log.Printf("refinery: failed to persist state: %v", err)
	}
}

// SetOnMerged sets the callback for successful merges.
func (r *Refinery) SetOnMerged(fn OnMerged) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onMerged = fn
}

// SetOnFailed sets the callback for failed merge attempts.
func (r *Refinery) SetOnFailed(fn OnFailed) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onFailed = fn
}

// SetOnSubmit sets the callback for merge request submission. See OnSubmit
// for the constraint any callback wired here must respect.
func (r *Refinery) SetOnSubmit(fn OnSubmit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onSubmit = fn
}

// OnMergedFunc returns the current OnMerged callback.
func (r *Refinery) OnMergedFunc() OnMerged {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onMerged
}

// OnFailedFunc returns the current OnFailed callback.
func (r *Refinery) OnFailedFunc() OnFailed {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onFailed
}

// OnSubmitFunc returns the current OnSubmit callback, so a caller replacing a
// Refinery instance can carry it over.
func (r *Refinery) OnSubmitFunc() OnSubmit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.onSubmit
}

// Submit adds a merge request to the queue. Returns the assigned ID.
// Validates that the target ref exists in the repo before queuing.
func (r *Refinery) Submit(req MergeRequest) (string, error) {
	if req.RepoPath == "" {
		return "", fmt.Errorf("repo_path is required")
	}
	if req.Branch == "" {
		return "", fmt.Errorf("branch is required")
	}
	if req.TargetRef == "" {
		req.TargetRef = "main"
	}
	// Reject a malformed post-merge tag now, while the submitter is still
	// running and a retry costs nothing. The same mistake caught after the
	// merge lands costs a half-finished release, because the merge is not
	// unwound (mg-6879).
	if req.PostMergeTag != "" {
		if ok, why := validTagName(req.PostMergeTag); !ok {
			return "", fmt.Errorf("post_merge_tag %q is not a valid git tag name: %s", req.PostMergeTag, why)
		}
	}
	// Same reasoning for the verdict, and more sharply: it is checked here
	// because here is the last moment the AUTHOR is still running and can be
	// told. A verdict rejected after the merge would be a verdict destroyed by
	// the machinery meant to record it, which is the defect this field closes.
	if len(req.Verdict) > 0 {
		if err := ValidateVerdict(req.Verdict); err != nil {
			return "", fmt.Errorf("verdict: %w", err)
		}
	}

	// And the branch — the subject of the operation, and until mg-586d the only
	// argument checked for non-emptiness and nothing else. Validated FIRST of
	// the git-shelling checks, so a doomed submit does not get a target ref
	// auto-created for it on the way out. See validateSubmitBranch for why the
	// predicate is origin/<branch> by name rather than gh#134's durability one.
	if err := validateSubmitBranch(req.RepoPath, req.Branch); err != nil {
		return "", err
	}

	// Resolve the repo's default branch once: it decides both where an
	// auto-created target ref is carved from and whether this merge is a
	// PR-flow integration step (see PRFlow).
	defaultBranch, defaultBranchErr := detectDefaultBranch(req.RepoPath)

	// Validate target ref before acquiring lock (shells out to git).
	if err := validateTargetRef(req.RepoPath, req.TargetRef); err != nil {
		if !req.AutoCreateTargetRef {
			return "", err
		}
		// Auto-create requested. Branch off the repo's default branch.
		// If the default branch *also* can't be located, surface the
		// original validation error — auto-create is a convenience, not
		// a way to paper over a broken repo.
		if defaultBranchErr != nil {
			return "", fmt.Errorf("auto-create target_ref %q: detect default branch: %w (original: %v)", req.TargetRef, defaultBranchErr, err)
		}
		sourceRef := defaultBranch
		if sourceRef == req.TargetRef {
			return "", err
		}
		if cerr := createTargetRef(req.RepoPath, req.TargetRef, sourceRef); cerr != nil {
			return "", fmt.Errorf("auto-create target_ref %q from %q: %w", req.TargetRef, sourceRef, cerr)
		}
		log.Printf("refinery: auto-created target_ref %q from %q in %s", req.TargetRef, sourceRef, req.RepoPath)
		if rverr := validateTargetRef(req.RepoPath, req.TargetRef); rverr != nil {
			return "", fmt.Errorf("auto-created target_ref %q but post-validation failed: %w", req.TargetRef, rverr)
		}
	}

	// Classify the merge (mg-7746). A target that is not the repo's default
	// branch is an integration branch, so merging is an intermediate step of a
	// PR flow and NOT completion. Always derived, never trusted from the
	// submitter — the whole failure mode this fixes was a caller not passing a
	// flag. If the default branch is unknowable, keep the historic fast path:
	// guessing "PR flow" for every merge in an odd repo would strand items
	// claimed on the backstop instead.
	req.PRFlow = false
	if defaultBranchErr != nil {
		log.Printf("refinery: could not determine the default branch of %s (%v) — treating target %q as completion (no PR-flow deferral)", req.RepoPath, defaultBranchErr, req.TargetRef)
	} else if req.TargetRef != defaultBranch {
		req.PRFlow = true
		log.Printf("refinery: MR for branch %s targets integration branch %q (default is %q) — PR flow: the merge is an integration step, completion is the PR the author still has to open (mg-7746)", req.Branch, req.TargetRef, defaultBranch)
	}

	// Registered BEFORE the lock so it runs AFTER the unlock (defers are LIFO).
	// Submit promises write-through: a pogod that dies right after it returns
	// must still find the queued MR on restart. That promise is now kept out
	// here, with r.mu released, instead of by fsyncing under it (mg-538e).
	defer r.flushState()
	r.mu.Lock()
	defer r.mu.Unlock()

	req.ID = generateID()
	req.Status = StatusQueued
	req.SubmitTime = time.Now()

	r.queue = append(r.queue, &req)
	r.byID[req.ID] = &req
	r.saveStateLocked()
	onSubmit := r.onSubmit

	log.Printf("refinery: queued MR %s branch=%s repo=%s author=%s", req.ID, req.Branch, req.RepoPath, req.Author)

	// Wake the queue loop so pickup doesn't wait out the poll interval.
	r.wake()

	// Fire OnSubmit callback outside the lock. No user today — see OnSubmit.
	if onSubmit != nil {
		r.mu.Unlock()
		onSubmit(&req)
		r.mu.Lock()
	}

	return req.ID, nil
}

// wake signals the queue loop to run immediately instead of waiting for the
// next poll tick. Non-blocking: if a wake is already pending it is a no-op.
func (r *Refinery) wake() {
	select {
	case r.wakeCh <- struct{}{}:
	default:
	}
}

// wakeIfActionable signals the queue loop when the queue still holds an item
// that could START now. Two kinds of item are deliberately not actionable:
//
//   - Held MRs (waiting on a QA gate). Waking for those would busy-loop the
//     gate check; they retry on the poll tick.
//   - MRs whose repo lane is already busy, or that have no lane to run in
//     because concurrency is at its cap. Since mg-37ad the loop's dispatch is
//     non-blocking, so treating those as actionable would spin the loop hot:
//     dispatch would start nothing, re-arm the wake, and immediately run again.
//
// Both exclusions are the same rule — only re-arm for work the next dispatch
// could actually pick up.
func (r *Refinery) wakeIfActionable() {
	r.mu.Lock()
	actionable := false
	if !r.stopping && len(r.lanes) < r.maxLanes() {
		for _, mr := range r.queue {
			if mr.Status == StatusHeld {
				continue
			}
			if _, busy := r.lanes[laneKey(mr.RepoPath)]; busy {
				continue
			}
			actionable = true
			break
		}
	}
	r.mu.Unlock()
	if actionable {
		r.wake()
	}
}

// validateTargetRef checks that the target ref exists in the repo's remote.
// This prevents MRs from being queued with typos or wrong branch names that
// would silently fail during processing.
func validateTargetRef(repoPath, targetRef string) error {
	// Use git ls-remote to check if the ref exists on origin.
	// This works for both bare repos (used in tests) and regular repos.
	// GIT_TERMINAL_PROMPT=0 makes auth-required HTTPS remotes fail fast
	// rather than hang waiting for a username on stdin under launchd.
	cmd := exec.Command("git", "-C", repoPath, "ls-remote", "--heads", "origin", targetRef)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Distinguish auth failure from "no remote" — auth failures are
		// doomed and should be refused at submit time with an actionable
		// error rather than letting the MR run through the full pipeline
		// only to fail at push.
		if isAuthFailure(string(out)) {
			return formatPushAuthError(strings.TrimSpace(string(out)))
		}
		// ls-remote failed (e.g. no origin remote, or remote unreachable).
		// Fall back to checking local branches — bare repos used in tests
		// have no remote configured.
		cmd2 := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/heads/"+targetRef)
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			return fmt.Errorf("target_ref %q not found: %s", targetRef, strings.TrimSpace(string(out2)))
		}
		return nil
	}
	// ls-remote succeeded — empty output is a definitive "not found" answer
	// from the remote. Do NOT fall back to local rev-parse, because a stale
	// local branch must not mask a missing remote branch.
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("target_ref %q not found on origin in repo %s", targetRef, repoPath)
	}
	return nil
}

// detectDefaultBranch returns the default branch name for repoPath. It tries
// origin/HEAD first (working clones with a populated remote), then asks origin
// directly, then falls back to local HEAD (bare repos used in tests, and clones
// with no remote at all). Returns an error only when no lookup works.
//
// The remote probe matters because local HEAD is a poor proxy for "the repo
// default": in a working clone it is simply whatever branch is checked out, so
// a clone parked on a feature branch would report that branch as the default
// and every merge to main would look like a PR-flow integration step (mg-7746).
func detectDefaultBranch(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").CombinedOutput()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if rest, ok := strings.CutPrefix(s, "origin/"); ok && rest != "" {
			return rest, nil
		}
	}
	if ref, ok := remoteDefaultBranch(repoPath); ok {
		return ref, nil
	}
	out, err = exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "HEAD").CombinedOutput()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("could not determine default branch for %s", repoPath)
}

// remoteDefaultBranch asks origin which branch its HEAD points at. Used when
// refs/remotes/origin/HEAD is absent locally — it is only written by `git
// clone` and is easily lost. GIT_TERMINAL_PROMPT=0 so an auth-required HTTPS
// remote fails fast rather than hanging on stdin under launchd.
func remoteDefaultBranch(repoPath string) (string, bool) {
	cmd := exec.Command("git", "-C", repoPath, "ls-remote", "--symref", "origin", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ref:")
		if !ok {
			continue
		}
		// Format: "ref: refs/heads/main\tHEAD"
		fields := strings.Fields(rest)
		if len(fields) < 2 || fields[1] != "HEAD" {
			continue
		}
		if name, ok := strings.CutPrefix(fields[0], "refs/heads/"); ok && name != "" {
			return name, true
		}
	}
	return "", false
}

// createTargetRef creates targetRef pointing at sourceRef in repoPath. In a
// working clone it pushes origin/<source> to a new branch on origin; in a bare
// repo (the layout used by tests) it creates the branch directly via update-ref.
func createTargetRef(repoPath, targetRef, sourceRef string) error {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--is-bare-repository").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect %s: %v: %s", repoPath, err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "true" {
		shaOut, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/heads/"+sourceRef).CombinedOutput()
		if err != nil {
			return fmt.Errorf("source_ref %q not found in %s: %s", sourceRef, repoPath, strings.TrimSpace(string(shaOut)))
		}
		sha := strings.TrimSpace(string(shaOut))
		if upOut, err := exec.Command("git", "-C", repoPath, "update-ref", "refs/heads/"+targetRef, sha).CombinedOutput(); err != nil {
			return fmt.Errorf("update-ref %s in %s: %v: %s", targetRef, repoPath, err, strings.TrimSpace(string(upOut)))
		}
		return nil
	}

	// Working clone path: make sure we have origin/<source> locally, then
	// push it to a new branch on origin. GIT_TERMINAL_PROMPT=0 so an
	// auth-required HTTPS remote fails fast instead of hanging.
	fetchCmd := exec.Command("git", "-C", repoPath, "fetch", "origin", sourceRef)
	fetchCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if fOut, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fetch source_ref %q from origin: %v: %s", sourceRef, err, strings.TrimSpace(string(fOut)))
	}
	pushCmd := exec.Command("git", "-C", repoPath, "push", "origin",
		"refs/remotes/origin/"+sourceRef+":refs/heads/"+targetRef)
	pushCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if pOut, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("push %s:%s to origin: %v: %s", sourceRef, targetRef, err, strings.TrimSpace(string(pOut)))
	}
	return nil
}

// Start begins the merge queue loop. Blocks until ctx is cancelled or Stop is called.
func (r *Refinery) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	log.Printf("refinery: started (poll_interval=%s max_concurrent_merges=%d, one lane per repo)",
		r.cfg.PollInterval, r.maxLanes())

	// Resolve any in-flight items recovered from the state file before the
	// first dispatch. This runs here rather than in New so the OnMerged/
	// OnFailed callbacks (wired between New and Start) fire for a merge that
	// landed just before the crash.
	r.resolveRecovered()

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		// Start everything that can run now and return; the merges themselves
		// run on their own goroutines. The loop must not block on a merge —
		// that block WAS the defect (mg-37ad): a gate for one repo held every
		// other repo's merges behind it for as long as it ran.
		r.dispatchReady()
		r.pruneHistory()

		// Several submits can land while merges are running; their wakes
		// collapse into a single buffered signal. Re-arm it here so the loop
		// drains the queue back-to-back instead of one item per tick.
		r.wakeIfActionable()

		select {
		case <-ctx.Done():
			r.shutdown()
			return
		case <-ticker.C:
			// next iteration
		case <-r.wakeCh:
			// submitted (or still-queued) work, or a lane just freed up
		}
	}
}

// shutdown stops dispatching and waits for the merges already in flight.
//
// It waits rather than cancelling, which preserves exactly what Stop did when
// the loop was serial: the loop was inside the merge, so stopping the refinery
// always meant "finish the merge you are on, then stop". A long gate therefore
// still makes Stop slow — that is the pre-existing cost of not abandoning a
// merge halfway.
//
// Waiting is also what keeps a restart safe. pogod builds a REPLACEMENT
// Refinery from the state file this one flushes (see SetRefineryStarter), so
// returning while lanes were still pushing would put two refineries on the same
// clone with the same branches.
func (r *Refinery) shutdown() {
	r.mu.Lock()
	r.stopping = true
	inFlight := len(r.lanes)
	r.mu.Unlock()

	if inFlight > 0 {
		log.Printf("refinery: stopping — waiting for %d in-flight merge(s) to finish "+
			"(a merge is never abandoned halfway; this can take as long as its quality gate)", inFlight)
	}
	r.laneWG.Wait()
	log.Printf("refinery: stopped")
	close(r.done)
}

// Stop signals the refinery loop to exit and flushes state to disk.
// The final flush is belt-and-braces — writes are already per-mutation —
// but guarantees the file reflects the latest state on graceful shutdown
// (including the orchestration-restart path, which builds a fresh Refinery
// from this file).
func (r *Refinery) Stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}
	r.mu.Lock()
	r.saveStateLocked()
	r.mu.Unlock()
	// Shutdown is the one place the flush is load-bearing rather than
	// belt-and-braces: pogod builds a REPLACEMENT Refinery from this file.
	r.flushState()
}

// Queue returns a snapshot of PENDING merge requests. It deliberately excludes
// the one being processed — callers that want the whole pipeline want
// QueueWithProcessing.
func (r *Refinery) Queue() []MergeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]MergeRequest, len(r.queue))
	for i, mr := range r.queue {
		out[i] = *mr
	}
	return out
}

// QueueWithProcessing returns the whole pipeline: the in-flight merge requests
// first (Status is StatusProcessing and each carries its Progress record),
// longest-running first, then the pending ones in order.
//
// There can now be more than one in-flight row — one per repo lane — and that
// is the other half of what mg-37ad fixes. The incident that unparked it was
// unreadable precisely because the queue could not say WHICH repo was holding
// things up; a list that leads with every running merge, each identifiable by
// its branch and repo, is what makes "my repo is not blocked, it is just
// behind" a thing an operator can see.
//
// This is what /refinery/queue serves, and the omission it fixes is mg-0c51. A
// busy refinery used to render as a static list of `status=queued` rows with
// nothing running — byte-for-byte identical to a wedged one, and identical
// across repeated polls, because the row that was actually moving was the one
// row not in the list. An operator who nearly reported a stall was looking at a
// gate that was two minutes into a healthy run.
//
// The in-flight row leads rather than trails so that "something is happening"
// is the first thing read, and so a pending row's position in the slice is its
// position in the line.
func (r *Refinery) QueueWithProcessing() []MergeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	inFlight := r.inFlightLocked()
	out := make([]MergeRequest, 0, len(r.queue)+len(inFlight))
	for _, mr := range inFlight {
		out = append(out, *mr)
	}
	for _, mr := range r.queue {
		out = append(out, *mr)
	}
	return out
}

// History returns a snapshot of retained completed merge requests, OLDEST
// FIRST (append order, as pruneHistoryLocked's own comment says).
//
// It is not an archive and must not be read as one. Entries past
// MaxHistoryLen / MaxHistoryAge have been deleted by pruneHistoryLocked, so a
// short result means "this is what is retained", never "this is all that
// happened" — and because the cap is a count, the truncation point moves with
// merge volume rather than sitting at a stable age. Use HistoryFromLog to ask
// about a window wider than retention.
func (r *Refinery) History() []MergeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]MergeRequest, len(r.history))
	for i, mr := range r.history {
		out[i] = *mr
	}
	return out
}

// Get returns a merge request by ID, or nil if not found.
func (r *Refinery) Get(id string) *MergeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	mr, ok := r.byID[id]
	if !ok {
		return nil
	}
	copy := *mr
	return &copy
}

// Status returns a summary of the refinery state.
type Status struct {
	Enabled      bool   `json:"enabled"`
	Running      bool   `json:"running"`
	PollInterval string `json:"poll_interval"`
	QueueLen     int    `json:"queue_len"`
	HistoryLen   int    `json:"history_len"`
	// MaxHistoryLen and MaxHistoryAge report the retention that BOUNDS
	// HistoryLen. They are reported so a client can say "showing 100 of a
	// window that stops at 100" instead of printing 100 rows that look like all
	// of them — HistoryLen alone cannot distinguish a full window from a
	// truncated one, which is the whole defect in mg-e9ee.
	//
	// The client reads these rather than referencing DefaultMaxHistoryLen so
	// the number it prints is the running daemon's, an observation, and not a
	// constant the client hopes still matches.
	//
	// They are POINTERS so that "this daemon did not report a cap" is a
	// distinct value from "this daemon has no cap". A client talking to a pogod
	// older than mg-e9ee decodes nil, and must say the truncation state is
	// UNKNOWN — a zero decoded as "no cap" would put the silence straight back,
	// which is the failure this field exists to remove. A negative
	// MaxHistoryLen means that dimension genuinely does not prune. Age is a
	// duration string ("168h0m0s").
	MaxHistoryLen *int    `json:"max_history_len,omitempty"`
	MaxHistoryAge *string `json:"max_history_age,omitempty"`

	// Processing is the ID of the LONGEST-RUNNING in-flight merge request,
	// empty when the refinery is idle. QueueLen counts only PENDING requests
	// and never includes it, so QueueLen alone cannot tell a refinery that is
	// chewing through a merge from one that has stopped — which is exactly the
	// pair of states mg-0c51 was filed about.
	//
	// Since per-repo lanes (mg-37ad) there can be several. This field is kept,
	// and kept meaning "one of them", so a client older than that change still
	// reports a busy refinery as busy instead of as idle. It is NOT the whole
	// answer any more: read InFlight, and read ProcessingCount before
	// concluding anything about how much is running.
	Processing string `json:"processing,omitempty"`
	// ProcessingSince is when that merge request entered processing.
	ProcessingSince time.Time `json:"processing_since,omitempty"`
	// ProcessingCount is how many merges are in flight across all lanes.
	ProcessingCount int `json:"processing_count"`
	// InFlight is one row per in-flight merge, longest-running first, each
	// naming the repo whose lane it holds.
	InFlight []LaneStatus `json:"in_flight,omitempty"`
	// MaxConcurrentMerges is the lane cap this daemon is running with, so a
	// reader can tell "nothing else can start" from "nothing else is waiting".
	MaxConcurrentMerges int `json:"max_concurrent_merges,omitempty"`
}

// HistoryTruncation is what a client can say about whether history is complete.
type HistoryTruncation int

const (
	// HistoryTruncationUnknown means the daemon did not report its retention,
	// so whether rows are missing cannot be determined. It is NOT "not
	// truncated": a client must say so rather than fall silent.
	HistoryTruncationUnknown HistoryTruncation = iota
	// HistoryTruncationNone means retention was reported and has not bitten.
	HistoryTruncationNone
	// HistoryTruncationAtCap means the count cap has bitten: completed merge
	// requests have been DELETED to make room (see pruneHistoryLocked), so they
	// are gone from the refinery and only the event log still has them.
	HistoryTruncationAtCap
)

// HistoryTruncation classifies the retained window against the reported cap.
func (s Status) HistoryTruncation() HistoryTruncation {
	if s.MaxHistoryLen == nil {
		return HistoryTruncationUnknown
	}
	if *s.MaxHistoryLen > 0 && s.HistoryLen >= *s.MaxHistoryLen {
		return HistoryTruncationAtCap
	}
	return HistoryTruncationNone
}

// RetentionSummary renders the retention bound as one short phrase, or says
// plainly that it was not reported.
func (s Status) RetentionSummary() string {
	if s.MaxHistoryLen == nil {
		return "not reported by this pogod"
	}
	age := "no age cap"
	if s.MaxHistoryAge != nil && *s.MaxHistoryAge != "" {
		age = *s.MaxHistoryAge
	}
	if *s.MaxHistoryLen <= 0 {
		return fmt.Sprintf("no count cap / %s", age)
	}
	return fmt.Sprintf("max %d entries / %s", *s.MaxHistoryLen, age)
}

// GetStatus returns the current refinery status.
func (r *Refinery) GetStatus() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	maxLen := r.cfg.MaxHistoryLen
	maxAge := r.cfg.MaxHistoryAge.String()
	st := Status{
		Enabled:             r.cfg.Enabled,
		Running:             r.cancel != nil,
		PollInterval:        r.cfg.PollInterval.String(),
		QueueLen:            len(r.queue),
		HistoryLen:          len(r.history),
		MaxHistoryLen:       &maxLen,
		MaxHistoryAge:       &maxAge,
		MaxConcurrentMerges: r.maxLanes(),
	}
	inFlight := r.inFlightLocked()
	st.ProcessingCount = len(inFlight)
	for _, mr := range inFlight {
		st.InFlight = append(st.InFlight, LaneStatus{
			Repo:   laneKey(mr.RepoPath),
			ID:     mr.ID,
			Branch: mr.Branch,
			Author: mr.Author,
			Since:  mr.StartTime,
		})
	}
	if len(inFlight) > 0 {
		st.Processing = inFlight[0].ID
		st.ProcessingSince = inFlight[0].StartTime
	}
	return st
}

// AuthorFailureCount returns the current consecutive failure count for an author.
func (r *Refinery) AuthorFailureCount(author string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failureCounts[author]
}

// Cancel stops a merge request. A queued MR is removed from the queue and
// resolved as cancelled immediately. A processing MR has its running quality
// gate killed and stops at the next step boundary — see CancelOutcome for why
// those two are reported as different things.
//
// Reaching a processing MR is the third item of mg-8595: cancel used to refuse
// anything not queued, which meant it worked only on the state least in need
// of it, and a genuinely hung gate had no recovery short of restarting pogod.
// It is best-effort by nature — a merge that has already pushed to the target
// has landed, and Cancel will not pretend otherwise.
//
// Returns an error if the MR is not found or has already finished.
func (r *Refinery) Cancel(id string) (CancelOutcome, error) {
	// After the unlock (LIFO): an operator's cancel is write-through, but the
	// wait for disk happens with r.mu released (mg-538e).
	defer r.flushState()
	r.mu.Lock()
	defer r.mu.Unlock()

	mr, ok := r.byID[id]
	if !ok {
		return "", fmt.Errorf("merge request %q not found", id)
	}
	if mr.Status == StatusProcessing {
		ln := r.laneHoldingLocked(id)
		if ln == nil || ln.cancel == nil {
			// Persisted as processing but not actually in flight here — a
			// restart recovery case. Nothing to kill, and claiming otherwise
			// would be a lie.
			return "", fmt.Errorf("merge request %q reads as processing but is not in flight in this "+
				"daemon (likely recovered from a restart); it will be resolved by the recovery probe", id)
		}
		// Only this lane's gate is killed. Other repos' merges keep running,
		// which is the point of giving each lane its own cancel handle.
		r.requestInFlightCancelLocked(ln)
		return CancelRequestedInFlight, nil
	}
	if mr.Status != StatusQueued {
		return "", fmt.Errorf("merge request %q has status %q, which is already final and cannot be cancelled", id, mr.Status)
	}

	// Remove from queue.
	for i, q := range r.queue {
		if q.ID == id {
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			break
		}
	}

	mr.Status = StatusCancelled
	mr.DoneTime = time.Now()
	r.history = append(r.history, mr)
	r.saveStateLocked()

	log.Printf("refinery: cancelled MR %s branch=%s author=%s", mr.ID, mr.Branch, mr.Author)
	return CancelRemovedFromQueue, nil
}

// processNext takes the next dispatchable item and processes it TO COMPLETION
// IN THE CALLING GOROUTINE.
//
// The queue loop no longer calls this — it calls dispatchReady, which starts
// each lane on its own goroutine. This remains as the synchronous single-item
// entry point, which is what a caller wanting "run one merge and tell me the
// outcome" needs, and it runs the same lane body the dispatcher does.
func (r *Refinery) processNext() {
	ln, mr := r.claimLane(nil)
	if ln == nil {
		return
	}
	if r.holdForQA(ln, mr) {
		return
	}
	r.runLane(ln, mr)
}

// dequeue claims the first dispatchable item and returns it, or nil if nothing
// can start. The item holds a lane from this point on, so the persisted
// snapshot never has an MR in limbo between queue and history — a crash
// anywhere after this point resolves it via the recovery probe on next start.
func (r *Refinery) dequeue() *MergeRequest {
	_, mr := r.claimLane(nil)
	return mr
}

// pruneHistory acquires the lock and prunes old history entries, persisting
// the result if anything changed.
//
// The persist is flushed — with the lock released — because the pruned-ID ring
// is the whole reason `refinery show` can answer "pruned from history" instead
// of "not found", and that distinction has to survive a restart. Only a prune
// that actually changed something pays for it, so this is not an fsync per
// loop iteration.
func (r *Refinery) pruneHistory() {
	r.mu.Lock()
	changed := r.pruneHistoryLocked()
	if changed {
		r.saveStateLocked()
	}
	r.mu.Unlock()
	if changed {
		r.flushState()
	}
}

// pruneHistoryLocked removes old entries from history. Must be called with mu held.
// It enforces both the count limit (MaxHistoryLen) and age limit (MaxHistoryAge).
// Pruned IDs are remembered in a bounded ring so lookups can answer "pruned
// from history" instead of a bare "not found". Returns whether anything was
// pruned; the caller is responsible for persisting.
func (r *Refinery) pruneHistoryLocked() bool {
	pruned := false
	// Age-based pruning (history is append-order, oldest first).
	if r.cfg.MaxHistoryAge > 0 {
		cutoff := r.nowFunc().Add(-r.cfg.MaxHistoryAge)
		i := 0
		for i < len(r.history) && r.history[i].DoneTime.Before(cutoff) {
			delete(r.byID, r.history[i].ID)
			r.recordPrunedLocked(r.history[i].ID)
			i++
		}
		if i > 0 {
			r.history = r.history[i:]
			pruned = true
		}
	}

	// Count-based pruning.
	if r.cfg.MaxHistoryLen > 0 && len(r.history) > r.cfg.MaxHistoryLen {
		excess := len(r.history) - r.cfg.MaxHistoryLen
		for _, mr := range r.history[:excess] {
			delete(r.byID, mr.ID)
			r.recordPrunedLocked(mr.ID)
		}
		r.history = r.history[excess:]
		pruned = true
	}
	return pruned
}

// recordPrunedLocked appends an ID to the pruned ring, evicting the oldest
// entries beyond prunedRingCap. Must be called with mu held.
func (r *Refinery) recordPrunedLocked(id string) {
	r.pruned = append(r.pruned, id)
	if len(r.pruned) > prunedRingCap {
		r.pruned = r.pruned[len(r.pruned)-prunedRingCap:]
	}
}

// LostInfo returns the lost-list entry for id, or nil if the ID is not on the
// lost list. A lost MR is one that restart recovery could not carry forward.
func (r *Refinery) LostInfo(id string) *LostEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.lost {
		if r.lost[i].ID == id {
			le := r.lost[i]
			return &le
		}
	}
	return nil
}

// WasPruned reports whether id was removed from history by pruning (age or
// count limits), as opposed to never having existed.
func (r *Refinery) WasPruned(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.pruned {
		if p == id {
			return true
		}
	}
	return false
}
