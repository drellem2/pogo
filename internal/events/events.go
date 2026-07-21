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
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// SchemaVersion is the on-disk schema version. See docs/event-log.md.
const SchemaVersion = 1

// pipeBufBytes is the conservative PIPE_BUF size (macOS = 512). Lines at or
// below this size are atomic against concurrent appenders without locking;
// longer lines take an advisory flock.
const pipeBufBytes = 512

// maxLogBytes is the size threshold above which Emit rotates the log. Checked
// inline on every write via a cheap stat — the os.Stat is microseconds and the
// rotation cost is paid once per ~100MB of events.
const maxLogBytes = 100 * 1024 * 1024

// maxRotatedFiles is the number of rotated files retained alongside the live
// log. With maxRotatedFiles=5 the on-disk footprint is bounded at roughly
// 6 × maxLogBytes (live + .1..maxRotatedFiles).
const maxRotatedFiles = 5

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

	testPathOnce sync.Once
	testPath     string
)

// DefaultLogPath returns events.log under the pogo state dir ($POGO_HOME,
// default ~/.pogo).
func DefaultLogPath() (string, error) {
	pathOnce.Do(func() {
		defaultPath = filepath.Join(config.PogoHome(), "events.log")
	})
	return defaultPath, defaultErr
}

// SetLogPathForTesting overrides the path used by Emit. Pass "" to restore the
// default — which under a test binary is this binary's private sandbox log, not
// the live ~/.pogo/events.log. Intended only for tests.
func SetLogPathForTesting(path string) {
	overrideMu.Lock()
	overridePath = path
	overrideMu.Unlock()
}

// ResolveAgent returns the canonical agent identity from POGO_AGENT_NAME and
// POGO_AGENT_TYPE env vars per the identity convention in docs/event-log.md.
// coordinator is the configured coordinator agent name ([agents] coordinator);
// empty falls back to the default, "mayor":
//
//   - crew named after the coordinator → bare coordinator name (e.g. "mayor")
//   - other crew                       → "crew-<name>"
//   - polecat                          → "cat-<name>"
//   - no env                           → "human"
func ResolveAgent(coordinator string) string {
	if coordinator == "" {
		coordinator = "mayor"
	}
	name := os.Getenv("POGO_AGENT_NAME")
	if name == "" {
		return "human"
	}
	switch os.Getenv("POGO_AGENT_TYPE") {
	case "crew":
		if name == coordinator {
			return name
		}
		return "crew-" + name
	case "polecat":
		return "cat-" + name
	default:
		return name
	}
}

// resolvePath returns the path Emit appends to.
//
// The log is test-safe by DEFAULT, not by remembering (mg-da48, ratified at
// ARCHITECTURE.md:433). Under a test binary the live ~/.pogo/events.log is not
// reachable from this function at all: an empty override resolves to a
// per-process sandbox, never to DefaultLogPath().
//
// This package shipped the opposite — `if p != "" { return p }; return
// DefaultLogPath()` — so the ZERO VALUE resolved to the live log and any test
// that did not explicitly set an override wrote to the operator's real audit
// log. events.log was polluted twice on that shape: mg-e06d (three weeks of
// phantom schedule_removed records, documented as a permanent aggregate cutoff
// at docs/event-log.md:16) and again on 2026-07-20 (six phantom auto_renudge
// rows during mg-2437's development). Both prior fixes were point fixes
// elsewhere and left this default untouched.
//
// The lesson is witness.go's, and its comment already names events.log as the
// victim: an opt-in guard is only ever remembered by the tests that least need
// it. internal/refinery and internal/agent remember to sandbox because the log
// is near their subject; a new test file that emits an event incidentally has
// no reason to know this store exists. So the default is the sandbox and there
// is nothing to remember.
//
// The explicit override still works and is still worth setting: one test
// picking its own path is isolation from OTHER TESTS, a different and
// legitimate question that this default does not answer.
//
// The branch is decided by testing.Testing(), i.e. by whether OUR binary is a
// test binary. A test that boots real pogod as a SUBPROCESS is unaffected —
// that child is not a test binary and resolves POGO_HOME as production does,
// which is correct.
func resolvePath() (string, error) {
	overrideMu.RLock()
	p := overridePath
	overrideMu.RUnlock()
	if p != "" {
		return p, nil
	}
	if testing.Testing() {
		return testDefaultLogPath(), nil
	}
	return DefaultLogPath()
}

// testDefaultLogPath returns this test binary's private events log.
//
// It never returns a path under PogoHome, and it has no error return, because
// there is no failure here that could justify handing back the live log: a test
// that cannot get a temp dir must fail to write ANYWHERE rather than succeed at
// appending a phantom record to the fleet's audit spine. Emit is best-effort
// and swallows write errors, so the fallback is another unwritable-at-worst
// temp path — never config.PogoHome().
func testDefaultLogPath() string {
	testPathOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pogo-test-events-*")
		if err != nil {
			// Still not the live log. A bad path here loses the test's own
			// events; a live-log record outlives every process that saw it.
			dir = filepath.Join(os.TempDir(), fmt.Sprintf("pogo-test-events-%d", os.Getpid()))
		}
		testPath = filepath.Join(dir, "events.log")
	})
	return testPath
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

	if info, err := os.Stat(path); err == nil && info.Size() >= maxLogBytes {
		if rotErr := rotate(path); rotErr != nil {
			fmt.Fprintf(os.Stderr, "events: rotate failed: %v\n", rotErr)
		}
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

// rotate shifts events.log -> events.log.1, events.log.1 -> events.log.2, ...,
// and deletes the file at maxRotatedFiles+1 if it spilled over. A separate
// lock file (events.log.rotate.lock) serializes rotation across processes so
// concurrent emitters don't double-rotate.
func rotate(path string) error {
	lockPath := path + ".rotate.lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open rotate lock: %w", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock rotate lock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat after lock: %w", err)
	}
	if info.Size() < maxLogBytes {
		return nil
	}

	oldest := fmt.Sprintf("%s.%d", path, maxRotatedFiles)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove oldest: %w", err)
	}
	for i := maxRotatedFiles - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", path, i)
		to := fmt.Sprintf("%s.%d", path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rename %s -> %s: %w", from, to, err)
		}
	}
	if err := os.Rename(path, path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rename live -> .1: %w", err)
	}
	return nil
}
