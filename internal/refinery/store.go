package refinery

import (
	"encoding/json"
	"errors"
	"fmt"
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
type store struct {
	path string

	mu sync.Mutex
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

func (s *store) save(st *persistedState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return errors.New("refinery: store path unset")
	}
	st.Version = StateVersion
	if st.Queue == nil {
		st.Queue = []*MergeRequest{}
	}
	if st.History == nil {
		st.History = []*MergeRequest{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
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
