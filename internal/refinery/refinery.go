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
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	// StatePath is where refinery state (queue, history, lost list) is
	// persisted so it survives pogod restarts. Empty disables persistence
	// (used by most unit tests). Default: ~/.pogo/refinery-state.json
	StatePath string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Enabled:      true,
		PollInterval: DefaultPollInterval,
		WorktreeDir:  filepath.Join(home, ".pogo", "refinery", "worktrees"),
		MacguffinDir: filepath.Join(home, ".macguffin", "work"),
		StatePath:    filepath.Join(home, ".pogo", "refinery-state.json"),
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
	AutoCreateTargetRef bool      `json:"auto_create_target_ref,omitempty"`
	SubmitTime          time.Time `json:"submit_time"`
	DoneTime            time.Time `json:"done_time,omitempty"`
	Error               string    `json:"error,omitempty"`
	GateOutput          string    `json:"gate_output,omitempty"`
	// AlreadyMerged marks an MR whose branch had already landed on the target
	// before processing began (a re-submit of a merged branch, gh #34). The
	// MR resolves as StatusMerged — so poll loops keying off status terminate
	// normally — but gates, push, and deploy were skipped as no-ops.
	AlreadyMerged bool `json:"already_merged,omitempty"`
	// DeployError is set when a post-merge deploy hook ran and failed. The
	// merge itself still succeeded (Status remains StatusMerged); deploy
	// failure is surfaced for diagnostics, not rolled back. Empty when no
	// deploy was configured or the deploy succeeded.
	DeployError      string `json:"deploy_error,omitempty"`
	FailureCount     int    `json:"failure_count"`
	ThresholdReached bool   `json:"threshold_reached,omitempty"`
}

// OnMerged is called when a branch is successfully merged.
type OnMerged func(mr *MergeRequest)

// OnFailed is called when a merge attempt fails quality gates.
type OnFailed func(mr *MergeRequest)

// OnSubmit is called when a merge request is submitted to the queue.
// This is used by pogod to clean up polecat worktrees so the branch
// is no longer marked as "checked out" in the source repo.
type OnSubmit func(mr *MergeRequest)

// Refinery is the merge queue loop.
type Refinery struct {
	cfg Config

	mu            sync.Mutex
	queue         []*MergeRequest          // ordered FIFO
	history       []*MergeRequest          // completed (merged or failed)
	byID          map[string]*MergeRequest // all requests by ID
	failureCounts map[string]int           // consecutive failure count per author

	// processing is the single in-flight item between dequeue and history
	// append (the queue loop is single-threaded). Tracked so the persisted
	// snapshot never has an MR in limbo: an item is always in exactly one of
	// queue, processing, or history.
	processing *MergeRequest
	// recovered holds a processing item loaded from the state file. It is
	// resolved (ancestor probe) at the top of Start, once callbacks are
	// wired — see resolveRecovered.
	recovered *MergeRequest
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
	if err := os.MkdirAll(cfg.WorktreeDir, 0755); err != nil {
		return nil, fmt.Errorf("create worktree dir: %w", err)
	}
	r := &Refinery{
		cfg:           cfg,
		byID:          make(map[string]*MergeRequest),
		failureCounts: make(map[string]int),
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
	return r, nil
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
	// processing item so `refinery show` answers correctly from the moment
	// the API is up.
	r.queue = st.Queue
	r.history = st.History
	if st.FailureCounts != nil {
		r.failureCounts = st.FailureCounts
	}
	for _, mr := range r.queue {
		r.byID[mr.ID] = mr
	}
	for _, mr := range r.history {
		r.byID[mr.ID] = mr
	}
	if st.Processing != nil {
		// Resolved by resolveRecovered at Start — do not blindly re-run: the
		// crash may have happened after `git push` landed the merge.
		r.recovered = st.Processing
		r.byID[st.Processing.ID] = st.Processing
	}
	r.pruned = st.PrunedIDs

	// Lost entries age out after lostMaxRestarts pogod restarts.
	for _, le := range st.Lost {
		le.Restarts++
		if le.Restarts <= lostMaxRestarts {
			r.lost = append(r.lost, le)
		}
	}

	log.Printf("refinery: restored state from %s (queue=%d history=%d lost=%d, in-flight=%v)",
		r.store.path, len(r.queue), len(r.history), len(r.lost), r.recovered != nil)
	return nil
}

// saveStateLocked persists the current state to disk. Must be called with mu
// held. Persistence errors are logged, not propagated — a full disk must not
// wedge the merge queue.
func (r *Refinery) saveStateLocked() {
	if r.store == nil {
		return
	}
	processing := r.processing
	if processing == nil {
		processing = r.recovered
	}
	st := &persistedState{
		Queue:         r.queue,
		Processing:    processing,
		History:       r.history,
		FailureCounts: r.failureCounts,
		Lost:          r.lost,
		PrunedIDs:     r.pruned,
	}
	if err := r.store.save(st); err != nil {
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

// SetOnSubmit sets the callback for merge request submission.
// pogod uses this to unlink polecat worktrees so the branch is no
// longer marked as "checked out" in the source repo.
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

// OnSubmitFunc returns the current OnSubmit callback. Used by pogod's
// orchestration-restart path to carry the callback over to a fresh Refinery
// instance (without it, submits stop unlinking polecat worktrees after a
// restart).
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

	// Validate target ref before acquiring lock (shells out to git).
	if err := validateTargetRef(req.RepoPath, req.TargetRef); err != nil {
		if !req.AutoCreateTargetRef {
			return "", err
		}
		// Auto-create requested. Branch off the repo's default branch.
		// If the default branch *also* can't be located, surface the
		// original validation error — auto-create is a convenience, not
		// a way to paper over a broken repo.
		sourceRef, derr := detectDefaultBranch(req.RepoPath)
		if derr != nil {
			return "", fmt.Errorf("auto-create target_ref %q: detect default branch: %w (original: %v)", req.TargetRef, derr, err)
		}
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

	// Fire OnSubmit callback outside the lock. pogod uses this to unlink
	// the polecat's worktree so the branch is no longer "checked out".
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
// worth processing now. Held MRs (waiting on a QA gate) don't count — waking
// for those would busy-loop the gate check; they retry on the poll tick.
func (r *Refinery) wakeIfActionable() {
	r.mu.Lock()
	actionable := false
	for _, mr := range r.queue {
		if mr.Status != StatusHeld {
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
// origin/HEAD first (working clones with a populated remote), then HEAD itself
// (bare repos used in tests). Returns an error only when neither lookup works.
func detectDefaultBranch(repoPath string) (string, error) {
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").CombinedOutput()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if rest, ok := strings.CutPrefix(s, "origin/"); ok && rest != "" {
			return rest, nil
		}
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

	log.Printf("refinery: started (poll_interval=%s)", r.cfg.PollInterval)

	// Resolve any in-flight item recovered from the state file before the
	// first processNext. This runs here rather than in New so the OnMerged/
	// OnFailed callbacks (wired between New and Start) fire for a merge that
	// landed just before the crash.
	r.resolveRecovered()

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		r.processNext()
		r.pruneHistory()

		// Several submits can land while one MR is processing; their wakes
		// collapse into a single buffered signal. Re-arm it here so the loop
		// drains the queue back-to-back instead of one item per tick.
		r.wakeIfActionable()

		select {
		case <-ctx.Done():
			log.Printf("refinery: stopped")
			close(r.done)
			return
		case <-ticker.C:
			// next iteration
		case <-r.wakeCh:
			// submitted (or still-queued) work — next iteration now
		}
	}
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
}

// Queue returns a snapshot of queued merge requests.
func (r *Refinery) Queue() []MergeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]MergeRequest, len(r.queue))
	for i, mr := range r.queue {
		out[i] = *mr
	}
	return out
}

// History returns a snapshot of completed merge requests (most recent first).
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
}

// GetStatus returns the current refinery status.
func (r *Refinery) GetStatus() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{
		Enabled:      r.cfg.Enabled,
		Running:      r.cancel != nil,
		PollInterval: r.cfg.PollInterval.String(),
		QueueLen:     len(r.queue),
		HistoryLen:   len(r.history),
	}
}

// AuthorFailureCount returns the current consecutive failure count for an author.
func (r *Refinery) AuthorFailureCount(author string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failureCounts[author]
}

// Cancel removes a queued merge request from the queue without merging.
// Returns an error if the MR is not found or is not in a cancellable state.
func (r *Refinery) Cancel(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	mr, ok := r.byID[id]
	if !ok {
		return fmt.Errorf("merge request %q not found", id)
	}
	if mr.Status != StatusQueued {
		return fmt.Errorf("merge request %q has status %q and cannot be cancelled", id, mr.Status)
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
	return nil
}

// processNext takes the next queued item and processes it.
func (r *Refinery) processNext() {
	mr := r.dequeue()
	if mr == nil {
		return
	}

	// QA gate: check if a QA work item exists for this MR's author (work ID).
	result, qaItemID := r.checkQAGate(mr.Author)
	if result == QAGateHold {
		r.holdMergeRequest(mr, qaItemID)
		return
	}

	r.mu.Lock()
	mr.Status = StatusProcessing
	r.saveStateLocked()
	r.mu.Unlock()

	log.Printf("refinery: processing MR %s branch=%s", mr.ID, mr.Branch)

	gateOutput, deployErr, alreadyMerged, err := r.processMerge(mr)

	r.mu.Lock()
	mr.GateOutput = gateOutput
	mr.DeployError = deployErr
	mr.AlreadyMerged = alreadyMerged
	mr.DoneTime = time.Now()
	if err != nil {
		mr.Status = StatusFailed
		mr.Error = err.Error()
		if mr.Author != "" {
			r.failureCounts[mr.Author]++
			mr.FailureCount = r.failureCounts[mr.Author]
			if r.cfg.FailureThreshold > 0 && mr.FailureCount >= r.cfg.FailureThreshold {
				mr.ThresholdReached = true
				log.Printf("refinery: author %s reached failure threshold (%d consecutive failures)", mr.Author, mr.FailureCount)
			}
		}
		log.Printf("refinery: REJECTED MR %s branch=%s author=%s reason=%v (failure_count=%d)", mr.ID, mr.Branch, mr.Author, err, mr.FailureCount)
	} else {
		mr.Status = StatusMerged
		if mr.Author != "" {
			delete(r.failureCounts, mr.Author)
		}
		if alreadyMerged {
			log.Printf("refinery: MR %s resolved as already merged (no-op) branch=%s author=%s", mr.ID, mr.Branch, mr.Author)
		} else {
			log.Printf("refinery: MR %s merged successfully branch=%s author=%s", mr.ID, mr.Branch, mr.Author)
		}
	}
	r.history = append(r.history, mr)
	r.processing = nil
	r.pruneHistoryLocked()
	r.saveStateLocked()
	onMerged := r.onMerged
	onFailed := r.onFailed
	r.mu.Unlock()

	// Fire callbacks outside the lock
	if err != nil && onFailed != nil {
		onFailed(mr)
	} else if err == nil && onMerged != nil {
		onMerged(mr)
	}
}

// dequeue removes and returns the first queued item, or nil if empty.
// The item is tracked as r.processing so the persisted snapshot never has
// an MR in limbo between queue and history — a crash anywhere after this
// point resolves it via the recovery probe on next start.
func (r *Refinery) dequeue() *MergeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.queue) == 0 {
		return nil
	}

	mr := r.queue[0]
	r.queue = r.queue[1:]
	r.processing = mr
	r.saveStateLocked()
	return mr
}

// pruneHistory acquires the lock and prunes old history entries, persisting
// the result if anything changed.
func (r *Refinery) pruneHistory() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pruneHistoryLocked() {
		r.saveStateLocked()
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
