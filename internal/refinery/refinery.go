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
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// DefaultPollInterval is how often the refinery checks for new merge requests.
const DefaultPollInterval = 30 * time.Second

// Config holds refinery configuration.
type Config struct {
	// Enabled controls whether the refinery loop runs.
	Enabled bool
	// PollInterval is how often the loop checks for queued items.
	PollInterval time.Duration
	// WorktreeDir is where the refinery creates git worktrees.
	// Default: ~/.pogo/refinery/worktrees/
	WorktreeDir string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Enabled:      true,
		PollInterval: DefaultPollInterval,
		WorktreeDir:  filepath.Join(home, ".pogo", "refinery", "worktrees"),
	}
}

// MergeStatus represents the outcome of a merge attempt.
type MergeStatus string

const (
	StatusQueued     MergeStatus = "queued"
	StatusProcessing MergeStatus = "processing"
	StatusMerged     MergeStatus = "merged"
	StatusFailed     MergeStatus = "failed"
)

// MergeRequest represents a branch submitted for merging.
type MergeRequest struct {
	ID         string      `json:"id"`
	RepoPath   string      `json:"repo_path"`
	Branch     string      `json:"branch"`
	TargetRef  string      `json:"target_ref"` // e.g. "main"
	Author     string      `json:"author"`     // agent name that submitted
	Status     MergeStatus `json:"status"`
	SubmitTime time.Time   `json:"submit_time"`
	DoneTime   time.Time   `json:"done_time,omitempty"`
	Error      string      `json:"error,omitempty"`
	GateOutput string      `json:"gate_output,omitempty"`
}

// OnMerged is called when a branch is successfully merged.
type OnMerged func(mr *MergeRequest)

// OnFailed is called when a merge attempt fails quality gates.
type OnFailed func(mr *MergeRequest)

// Refinery is the merge queue loop.
type Refinery struct {
	cfg Config

	mu      sync.Mutex
	queue   []*MergeRequest          // ordered FIFO
	history []*MergeRequest          // completed (merged or failed)
	byID    map[string]*MergeRequest // all requests by ID

	onMerged OnMerged
	onFailed OnFailed

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
	if err := os.MkdirAll(cfg.WorktreeDir, 0755); err != nil {
		return nil, fmt.Errorf("create worktree dir: %w", err)
	}
	return &Refinery{
		cfg:  cfg,
		byID: make(map[string]*MergeRequest),
		done: make(chan struct{}),
	}, nil
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

// Submit adds a merge request to the queue. Returns the assigned ID.
func (r *Refinery) Submit(req MergeRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if req.RepoPath == "" {
		return "", fmt.Errorf("repo_path is required")
	}
	if req.Branch == "" {
		return "", fmt.Errorf("branch is required")
	}
	if req.TargetRef == "" {
		req.TargetRef = "main"
	}

	req.ID = generateID()
	req.Status = StatusQueued
	req.SubmitTime = time.Now()

	r.queue = append(r.queue, &req)
	r.byID[req.ID] = &req

	log.Printf("refinery: queued MR %s branch=%s repo=%s author=%s", req.ID, req.Branch, req.RepoPath, req.Author)

	if err := mgEventAppend("refinery.submit",
		"item="+req.ID, "branch="+req.Branch, "author="+req.Author,
	); err != nil {
		log.Printf("refinery: event append failed: %v", err)
	}

	return req.ID, nil
}

// Start begins the merge queue loop. Blocks until ctx is cancelled or Stop is called.
func (r *Refinery) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	log.Printf("refinery: started (poll_interval=%s)", r.cfg.PollInterval)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		r.processNext()

		select {
		case <-ctx.Done():
			log.Printf("refinery: stopped")
			close(r.done)
			return
		case <-ticker.C:
			// next iteration
		}
	}
}

// Stop signals the refinery loop to exit.
func (r *Refinery) Stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}
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

// processNext takes the next queued item and processes it.
func (r *Refinery) processNext() {
	mr := r.dequeue()
	if mr == nil {
		return
	}

	r.mu.Lock()
	mr.Status = StatusProcessing
	r.mu.Unlock()

	log.Printf("refinery: processing MR %s branch=%s", mr.ID, mr.Branch)

	err := r.processMerge(mr)

	r.mu.Lock()
	mr.DoneTime = time.Now()
	if err != nil {
		mr.Status = StatusFailed
		mr.Error = err.Error()
		log.Printf("refinery: MR %s failed: %v", mr.ID, err)
	} else {
		mr.Status = StatusMerged
		log.Printf("refinery: MR %s merged successfully", mr.ID)
	}
	r.history = append(r.history, mr)
	onMerged := r.onMerged
	onFailed := r.onFailed
	r.mu.Unlock()

	// Append mg events
	if err != nil {
		if evErr := mgEventAppend("refinery.fail",
			"item="+mr.ID, "branch="+mr.Branch, "author="+mr.Author, "error="+mr.Error,
		); evErr != nil {
			log.Printf("refinery: event append failed: %v", evErr)
		}
	} else {
		if evErr := mgEventAppend("refinery.merge",
			"item="+mr.ID, "branch="+mr.Branch, "author="+mr.Author,
		); evErr != nil {
			log.Printf("refinery: event append failed: %v", evErr)
		}
	}

	// Fire callbacks outside the lock
	if err != nil && onFailed != nil {
		onFailed(mr)
	} else if err == nil && onMerged != nil {
		onMerged(mr)
	}
}

// dequeue removes and returns the first queued item, or nil if empty.
func (r *Refinery) dequeue() *MergeRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.queue) == 0 {
		return nil
	}

	mr := r.queue[0]
	r.queue = r.queue[1:]
	return mr
}

// mgEventAppend appends a structured event to the macguffin event log.
// eventType is the event type (e.g. "refinery.merge", "refinery.fail").
// kvs are key=value pairs to include as event fields.
func mgEventAppend(eventType string, kvs ...string) error {
	args := []string{"event", "append", eventType}
	for _, kv := range kvs {
		args = append(args, "--"+kv)
	}
	cmd := exec.Command("mg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mg event append failed: %s (%w)", string(out), err)
	}
	return nil
}
