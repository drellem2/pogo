package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/drellem2/pogo/internal/config"
)

// store handles persistence of the scheduler state to a single JSON file.
// Writes are atomic via temp-file + rename so a crashed pogod (or full disk)
// never leaves a half-written schedules.json behind.
type store struct {
	path string

	mu sync.Mutex
}

// onDisk is the wire format for ~/.pogo/schedules.json. Wrapping the entries
// in a versioned envelope means future schema changes can be detected and
// migrated without ambiguity.
type onDisk struct {
	Version   int     `json:"version"`
	Schedules []Entry `json:"schedules"`
}

// DefaultPath returns schedules.json under the pogo state dir ($POGO_HOME,
// default ~/.pogo). The error return is kept for call-site compatibility; it
// is always nil.
func DefaultPath() (string, error) {
	return filepath.Join(config.PogoHome(), "schedules.json"), nil
}

func (s *store) applyDefaults() {
	if s.path == "" {
		// best-effort; load() will surface the missing-home error if any.
		if p, err := DefaultPath(); err == nil {
			s.path = p
		}
	}
}

func (s *store) load() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil, errors.New("scheduler: store path unset")
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
	var disk onDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("scheduler: parse %s: %w", s.path, err)
	}
	if disk.Version == 0 {
		// Treat absent version as v1 so a hand-written schedules.json
		// (development, debugging) round-trips without surprise.
		disk.Version = StateVersion
	}
	if disk.Version > StateVersion {
		return nil, fmt.Errorf("scheduler: state version %d newer than this binary supports (%d) — refusing to overwrite", disk.Version, StateVersion)
	}
	return disk.Schedules, nil
}

func (s *store) save(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return errors.New("scheduler: store path unset")
	}
	if entries == nil {
		entries = []Entry{}
	}
	disk := onDisk{Version: StateVersion, Schedules: entries}
	data, err := json.MarshalIndent(disk, "", "  ")
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
