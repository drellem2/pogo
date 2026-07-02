package refinery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	Version int             `json:"version"`
	Queue   []*MergeRequest `json:"queue"`
	// Processing is the single in-flight item (the queue loop is
	// single-threaded, so there is at most one). On load it is resolved via
	// the ancestor probe rather than blindly re-run — see resolveRecovered.
	Processing    *MergeRequest   `json:"processing,omitempty"`
	History       []*MergeRequest `json:"history"`
	FailureCounts map[string]int  `json:"failure_counts,omitempty"`
	Lost          []LostEntry     `json:"lost,omitempty"`
	PrunedIDs     []string        `json:"pruned_ids,omitempty"`
}

// store handles persistence of refinery state to a single JSON file.
// Writes are atomic via temp-file + fsync + rename so a crashed pogod (or
// full disk) never leaves a half-written refinery-state.json behind.
type store struct {
	path string

	mu sync.Mutex
}

// DefaultStatePath returns ~/.pogo/refinery-state.json.
func DefaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pogo", "refinery-state.json"), nil
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
