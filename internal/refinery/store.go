package refinery

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// StateVersion is the current on-disk schema version for refinery-state.json.
// Loads refuse files written by a newer binary so an older pogod never
// silently clobbers state it doesn't understand.
const StateVersion = 1

// lostMaxRestarts is how many pogod restarts a lost entry survives before it
// is dropped from the state file. Lost entries exist so a polling polecat gets
// a distinct "lost" answer instead of "not found"; after a few restarts the
// polecat has either resubmitted or escalated, and the entry is stale.
const lostMaxRestarts = 3

// prunedRingCap bounds the ring of pruned-from-history MR IDs kept so
// `refinery show` can answer "pruned" instead of "not found".
const prunedRingCap = 256

// LostEntry records an MR that recovery could not carry forward across a
// restart. Enough context is kept for the author to resubmit.
type LostEntry struct {
	ID        string    `json:"id"`
	Branch    string    `json:"branch"`
	Author    string    `json:"author"`
	RepoPath  string    `json:"repo_path"`
	TargetRef string    `json:"target_ref"`
	Reason    string    `json:"reason"`
	LostTime  time.Time `json:"lost_time"`
	// Restarts counts pogod restarts survived; entries are dropped once it
	// exceeds lostMaxRestarts.
	Restarts int `json:"restarts"`
}

// persistedState is the wire format for ~/.pogo/refinery-state.json. The
// versioned envelope means future schema changes can be detected and migrated
// without ambiguity (same pattern as scheduler's schedules.json).
//
// byID is deliberately absent: it is rebuilt from queue+history+processing on
// load. Callbacks, config, and worktree clones are likewise not persisted.
type persistedState struct {
	Version int `json:"version"`
	// Queue is the pending items, PRECEDED BY a queued-looking copy of every
	// item in ProcessingLanes. That mirror exists for readers that predate
	// per-repo lanes and would otherwise drop in-flight merges on the floor;
	// this binary strips it on load. See saveStateLocked for the full argument.
	Queue []*MergeRequest `json:"queue"`
	// Processing is the pre-mg-37ad single in-flight slot, written when the
	// queue loop could only run one merge at a time. It is still READ so a
	// state file written by an older pogod carries its in-flight merge across
	// the upgrade; it is no longer WRITTEN. On load it is resolved via the
	// ancestor probe rather than blindly re-run — see resolveRecovered.
	Processing *MergeRequest `json:"processing,omitempty"`
	// ProcessingLanes is every in-flight item, one per repo lane.
	//
	// The schema VERSION is deliberately not bumped for this field. A version
	// bump makes an older pogod refuse the file outright ("newer than this
	// binary supports"), which would take the merge queue down on any rollback;
	// the format here stays loadable by those binaries without loss, because
	// they read the Queue mirror. Additive, and readable both ways, beats
	// detectable.
	ProcessingLanes []*MergeRequest `json:"processing_lanes,omitempty"`
	History         []*MergeRequest `json:"history"`
	FailureCounts   map[string]int  `json:"failure_counts,omitempty"`
	Lost            []LostEntry     `json:"lost,omitempty"`
	PrunedIDs       []string        `json:"pruned_ids,omitempty"`
}

// store handles persistence of refinery state to a single JSON file.
// Writes are atomic via temp-file + fsync + rename so a crashed pogod (or
// full disk) never leaves a half-written refinery-state.json behind.
//
// # The fsync is deliberately NOT reachable from Refinery.mu (mg-538e)
//
// Every persist used to run marshal + write + fsync + rename inside
// `saveStateLocked`, which its ~12 callers invoke with `Refinery.mu` held. An
// fsync is an unbounded hold: its duration is set by disk contention, not by
// the refinery. `pogo refinery queue` needs that same mutex
// (Refinery.QueueWithProcessing), which is how a busy disk turned into a
// hanging CLI while `pogo agent list` — a different mutex on a different
// object — answered instantly.
//
// The split is now: the caller marshals under Refinery.mu (that is the
// consistency boundary — the MergeRequest pointers are shared mutable state),
// hands the finished bytes to `enqueue`, and returns. A writer goroutine owned
// by this store does write/Sync/rename, serialized on `mu`. **Nothing on that
// path touches Refinery.mu**, and `mu` itself is never held across anything a
// reader of the queue needs.
//
// Durability at the API boundary is preserved by `flush`, which waits for the
// newest enqueued snapshot to reach disk. Callers that promise write-through
// (Submit, Stop, terminal lane resolution) call it AFTER releasing
// Refinery.mu; the high-frequency gate-heartbeat saves do not, because losing
// the last heartbeat of a crashed pogod costs nothing.
type store struct {
	path string

	// mu serializes the disk work — write, fsync, rename — and the load that
	// reads the same file. It is held only by the writer goroutine and by
	// load/save, never by anything holding Refinery.mu.
	mu sync.Mutex

	// qmu guards the hand-off queue below. It is held for O(1) bookkeeping
	// only: never across a write, never across an fsync, and never by a
	// goroutine that also holds Refinery.mu for anything but the enqueue
	// itself.
	qmu sync.Mutex
	// qcond is broadcast whenever doneSeq advances or the writer goes idle.
	qcond *sync.Cond
	// pending is the newest marshalled snapshot not yet written. Older
	// snapshots are dropped rather than queued: the file holds whole state, so
	// writing an intermediate version of it is pure cost. This is what makes a
	// burst of saves collapse into one write.
	pending []byte
	// pendSeq is the sequence number of `pending`, monotonically increasing
	// across every enqueue whether or not that snapshot survived coalescing.
	pendSeq uint64
	// doneSeq is the highest sequence number durably on disk.
	doneSeq uint64
	// writing reports whether a writer goroutine is draining the queue.
	writing bool
	// lastErr is the error from the most recent write attempt, reported by
	// flush. Persistence errors are logged rather than propagated everywhere
	// else — a full disk must not wedge the merge queue.
	lastErr error

	// beforeWrite, when non-nil, runs on the writer goroutine immediately
	// before the temp-file write. Tests use it to hold a write open and prove
	// the refinery mutex is free while it is (see
	// TestStateWriteDoesNotHoldRefineryMutex). Set once, before the first
	// enqueue.
	beforeWrite func()
}

// initQueue prepares the hand-off condition variable. Safe to call more than
// once; store values are constructed in several places (including tests).
func (s *store) initQueue() {
	s.qmu.Lock()
	if s.qcond == nil {
		s.qcond = sync.NewCond(&s.qmu)
	}
	s.qmu.Unlock()
}

// DefaultStatePath returns refinery-state.json under the pogo state dir
// ($POGO_HOME, default ~/.pogo). The error return is kept for call-site
// compatibility; it is always nil.
func DefaultStatePath() (string, error) {
	return filepath.Join(config.PogoHome(), "refinery-state.json"), nil
}

// errStateCorrupt wraps JSON parse failures so New can distinguish a corrupt
// file (recoverable: back it up and start empty) from a version-skew refusal
// (fatal: a newer binary owns this state).
var errStateCorrupt = errors.New("refinery: state file corrupt")

func (s *store) load() (*persistedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil, errors.New("refinery: store path unset")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", errStateCorrupt, s.path, err)
	}
	if st.Version == 0 {
		// Treat absent version as v1 so a hand-written state file
		// (development, debugging) round-trips without surprise.
		st.Version = StateVersion
	}
	if st.Version > StateVersion {
		return nil, fmt.Errorf("refinery: state version %d newer than this binary supports (%d) — refusing to overwrite", st.Version, StateVersion)
	}
	return &st, nil
}

// marshal renders st as the on-disk bytes. It touches no disk and takes no
// lock, so it is safe — and intended — to run under Refinery.mu: that is what
// makes the snapshot consistent with the state it was taken from.
func (s *store) marshal(st *persistedState) ([]byte, error) {
	st.Version = StateVersion
	if st.Queue == nil {
		st.Queue = []*MergeRequest{}
	}
	if st.History == nil {
		st.History = []*MergeRequest{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// enqueue hands a marshalled snapshot to the writer goroutine. It never blocks
// on disk; `flush` is how a caller waits for what it enqueued.
//
// It is the ONLY thing on the persist path a Refinery.mu holder may call.
func (s *store) enqueue(data []byte) {
	s.initQueue()
	s.qmu.Lock()
	s.pendSeq++
	s.pending = data
	start := !s.writing
	if start {
		s.writing = true
	}
	s.qmu.Unlock()
	if start {
		go s.drain()
	}
}

// drain writes queued snapshots until the queue is empty, one at a time,
// coalescing anything that arrived while the previous write was in flight.
// It runs on its own goroutine and never touches Refinery.mu.
func (s *store) drain() {
	for {
		s.qmu.Lock()
		data, seq := s.pending, s.pendSeq
		s.pending = nil
		if data == nil {
			s.writing = false
			s.qcond.Broadcast()
			s.qmu.Unlock()
			return
		}
		s.qmu.Unlock()

		err := s.writeBytes(data)
		if err != nil {
			log.Printf("refinery: failed to persist state: %v", err)
		}

		s.qmu.Lock()
		if seq > s.doneSeq {
			s.doneSeq = seq
		}
		s.lastErr = err
		s.qcond.Broadcast()
		s.qmu.Unlock()
	}
}

// flush blocks until every snapshot enqueued before the call is durable, and
// returns the last write error (nil when the newest write succeeded).
//
// MUST NOT be called with Refinery.mu held — waiting here is waiting on an
// fsync, and doing that under Refinery.mu is precisely the defect mg-538e
// repairs. See Refinery.flushState.
func (s *store) flush() error {
	s.initQueue()
	s.qmu.Lock()
	defer s.qmu.Unlock()
	want := s.pendSeq
	for s.doneSeq < want {
		s.qcond.Wait()
	}
	return s.lastErr
}

// save marshals and writes synchronously. It is the whole-operation form, used
// where there is no Refinery.mu to get out from under (tests, direct seeding).
func (s *store) save(st *persistedState) error {
	data, err := s.marshal(st)
	if err != nil {
		return err
	}
	return s.writeBytes(data)
}

// writeBytes atomically replaces the state file: temp-file, fsync, rename.
// The fsync lives here, under s.mu and nothing else.
func (s *store) writeBytes(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return errors.New("refinery: store path unset")
	}
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		cleanup()
		return err
	}
	return nil
}
