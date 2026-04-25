// Package events implements the append-only JSONL writer for ~/.pogo/events.log.
//
// See docs/event-log.md for the schema. This package provides Emit, the
// best-effort writer that crew agents, polecats, the refinery, and mg use to
// record observable system events. Writes are atomic for short lines (POSIX
// guarantees O_APPEND writes ≤ PIPE_BUF are atomic against other appenders);
// longer lines take an advisory flock to avoid interleaving.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// SchemaVersion is the on-disk schema version. See docs/event-log.md.
const SchemaVersion = 1

// pipeBufBytes is the conservative PIPE_BUF size (macOS = 512). Lines at or
// below this size are atomic against concurrent appenders without locking;
// longer lines take an advisory flock.
const pipeBufBytes = 512

// Event is one envelope record in the log. Per docs/event-log.md, SchemaVersion
// and Timestamp are auto-populated by Emit if zero. WorkItemID and Repo are
// omitted when empty; Details is always emitted (as {} if nil).
type Event struct {
	SchemaVersion int            `json:"schema_version"`
	Timestamp     string         `json:"timestamp"`
	EventType     string         `json:"event_type"`
	Agent         string         `json:"agent"`
	WorkItemID    string         `json:"work_item_id,omitempty"`
	Repo          string         `json:"repo,omitempty"`
	Details       map[string]any `json:"details"`
}

var (
	pathOnce    sync.Once
	defaultPath string
	defaultErr  error

	overrideMu   sync.RWMutex
	overridePath string
)

// DefaultLogPath returns ~/.pogo/events.log.
func DefaultLogPath() (string, error) {
	pathOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			defaultErr = err
			return
		}
		defaultPath = filepath.Join(home, ".pogo", "events.log")
	})
	return defaultPath, defaultErr
}

// SetLogPathForTesting overrides the path used by Emit. Pass "" to restore the
// default. Intended only for tests.
func SetLogPathForTesting(path string) {
	overrideMu.Lock()
	overridePath = path
	overrideMu.Unlock()
}

func resolvePath() (string, error) {
	overrideMu.RLock()
	p := overridePath
	overrideMu.RUnlock()
	if p != "" {
		return p, nil
	}
	return DefaultLogPath()
}

// Emit appends a single event to the log. It is best-effort: failures are
// reported on stderr but never returned, and Emit will not panic the caller.
//
// Emit fills SchemaVersion (1) and Timestamp (UTC RFC3339Nano) when those
// fields are zero. Details is replaced with {} if nil so the on-disk record
// always carries an object per the schema.
//
// ctx is reserved for future use (e.g. HTTP fan-out once F4 lands). Emit does
// not currently honor cancellation — local file appends are too short to be
// worth interrupting.
func Emit(ctx context.Context, event Event) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "events: panic in Emit: %v\n", r)
		}
	}()
	if err := emit(event); err != nil {
		fmt.Fprintf(os.Stderr, "events: emit failed: %v\n", err)
	}
}

// EmitTo is Emit with an explicit path. Used by tests and callers that have
// a non-default log location. Same best-effort semantics as Emit.
func EmitTo(ctx context.Context, path string, event Event) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "events: panic in EmitTo: %v\n", r)
		}
	}()
	if err := writeEvent(path, event); err != nil {
		fmt.Fprintf(os.Stderr, "events: emit failed: %v\n", err)
	}
}

func emit(event Event) error {
	path, err := resolvePath()
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	return writeEvent(path, event)
}

func writeEvent(path string, event Event) error {
	if event.SchemaVersion == 0 {
		event.SchemaVersion = SchemaVersion
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if event.Details == nil {
		event.Details = map[string]any{}
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	line := append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	locked := false
	if len(line) > pipeBufBytes {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err == nil {
			locked = true
		}
	}
	_, writeErr := f.Write(line)
	if locked {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	if writeErr != nil {
		return fmt.Errorf("write: %w", writeErr)
	}
	return nil
}
