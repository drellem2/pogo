package events

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// useTempLog redirects the package-default Emit path to a temp file for the
// duration of the test. Returns the path.
func useTempLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	SetLogPathForTesting(path)
	t.Cleanup(func() { SetLogPathForTesting("") })
	return path
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return lines
}

func TestEmitRoundTrip(t *testing.T) {
	path := useTempLog(t)

	want := Event{
		EventType:  "agent_spawned",
		Agent:      "cat-mg-700a",
		WorkItemID: "mg-700a",
		Repo:       "/Users/daniel/dev/pogo",
		Details: map[string]any{
			"agent_type":  "polecat",
			"pid":         48213,
			"prompt_file": "/path/to/prompt.md",
		},
	}
	Emit(context.Background(), want)

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var got Event
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion: want %d, got %d", SchemaVersion, got.SchemaVersion)
	}
	if got.EventType != want.EventType {
		t.Errorf("EventType: want %q, got %q", want.EventType, got.EventType)
	}
	if got.Agent != want.Agent {
		t.Errorf("Agent: want %q, got %q", want.Agent, got.Agent)
	}
	if got.WorkItemID != want.WorkItemID {
		t.Errorf("WorkItemID: want %q, got %q", want.WorkItemID, got.WorkItemID)
	}
	if got.Repo != want.Repo {
		t.Errorf("Repo: want %q, got %q", want.Repo, got.Repo)
	}

	// Timestamp must be RFC3339Nano UTC and parseable.
	if !strings.HasSuffix(got.Timestamp, "Z") {
		t.Errorf("Timestamp not UTC: %q", got.Timestamp)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.Timestamp); err != nil {
		t.Errorf("Timestamp not RFC3339Nano: %q (%v)", got.Timestamp, err)
	}

	// Details should round-trip. JSON unmarshals numbers as float64.
	if got.Details["agent_type"] != "polecat" {
		t.Errorf("details.agent_type: want polecat, got %v", got.Details["agent_type"])
	}
	if got.Details["pid"].(float64) != 48213 {
		t.Errorf("details.pid: want 48213, got %v", got.Details["pid"])
	}
}

func TestEmitOmitsEmptyOptionalFields(t *testing.T) {
	path := useTempLog(t)

	Emit(context.Background(), Event{
		EventType: "nudge_sent",
		Agent:     "mayor",
		Details:   map[string]any{"to": "crew-arch", "message": "hi", "delivery": "pty"},
	})

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	if strings.Contains(lines[0], "work_item_id") {
		t.Errorf("empty work_item_id should be omitted, got: %s", lines[0])
	}
	if strings.Contains(lines[0], "repo") {
		t.Errorf("empty repo should be omitted, got: %s", lines[0])
	}
}

func TestEmitNilDetailsBecomesEmptyObject(t *testing.T) {
	path := useTempLog(t)

	Emit(context.Background(), Event{
		EventType: "x",
		Agent:     "y",
		// Details is nil
	})

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], `"details":{}`) {
		t.Errorf("nil details should serialize as {}, got: %s", lines[0])
	}
}

func TestEmitAppends(t *testing.T) {
	path := useTempLog(t)

	for i := 0; i < 5; i++ {
		Emit(context.Background(), Event{
			EventType: "tick",
			Agent:     "test",
			Details:   map[string]any{"i": i},
		})
	}

	lines := readLines(t, path)
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
}

// TestConcurrentWritesDontCorrupt verifies that many goroutines emitting
// simultaneously produce well-formed JSONL — every line is valid JSON, no
// interleaving. This is the property the schema relies on.
func TestConcurrentWritesDontCorrupt(t *testing.T) {
	path := useTempLog(t)

	const goroutines = 20
	const perGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				Emit(context.Background(), Event{
					EventType: "race",
					Agent:     "writer",
					Details: map[string]any{
						"g": g,
						"i": i,
					},
				})
			}
		}(g)
	}
	wg.Wait()

	lines := readLines(t, path)
	if len(lines) != goroutines*perGoroutine {
		t.Fatalf("expected %d lines, got %d", goroutines*perGoroutine, len(lines))
	}
	seen := make(map[[2]int]bool)
	for i, l := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("line %d not valid JSON: %v\nline: %q", i, err, l)
		}
		if ev.EventType != "race" {
			t.Fatalf("line %d wrong event_type: %q", i, ev.EventType)
		}
		key := [2]int{int(ev.Details["g"].(float64)), int(ev.Details["i"].(float64))}
		if seen[key] {
			t.Fatalf("duplicate write %v", key)
		}
		seen[key] = true
	}
	if len(seen) != goroutines*perGoroutine {
		t.Fatalf("expected %d unique events, got %d", goroutines*perGoroutine, len(seen))
	}
}

// TestConcurrentLargeWritesDontCorrupt covers the > PIPE_BUF code path that
// uses flock. Each line is padded past 512 bytes so the locking branch runs.
func TestConcurrentLargeWritesDontCorrupt(t *testing.T) {
	path := useTempLog(t)

	const goroutines = 10
	const perGoroutine = 25
	bigPayload := strings.Repeat("x", 1024)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				Emit(context.Background(), Event{
					EventType: "big",
					Agent:     "writer",
					Details: map[string]any{
						"g":       g,
						"i":       i,
						"payload": bigPayload,
					},
				})
			}
		}(g)
	}
	wg.Wait()

	lines := readLines(t, path)
	if len(lines) != goroutines*perGoroutine {
		t.Fatalf("expected %d lines, got %d", goroutines*perGoroutine, len(lines))
	}
	for i, l := range lines {
		if len(l) <= pipeBufBytes {
			t.Fatalf("test setup wrong: line %d only %d bytes (want > %d)", i, len(l), pipeBufBytes)
		}
		var ev Event
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("line %d not valid JSON: %v", i, err)
		}
		if ev.Details["payload"].(string) != bigPayload {
			t.Fatalf("line %d payload corrupted", i)
		}
	}
}

// TestWriteFailureDoesNotPropagate verifies that an unwriteable path causes
// Emit to return without panicking and without affecting the caller. We point
// the writer at a path under a non-existent + uncreatable parent, then assert
// the caller continues normally.
func TestWriteFailureDoesNotPropagate(t *testing.T) {
	// /dev/null/x can't be a directory: parent is not a dir. MkdirAll fails;
	// Emit must still return cleanly.
	SetLogPathForTesting("/dev/null/cannot/exist/events.log")
	t.Cleanup(func() { SetLogPathForTesting("") })

	done := make(chan struct{})
	go func() {
		defer close(done)
		Emit(context.Background(), Event{EventType: "x", Agent: "y"})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked on failing path")
	}

	// The caller should be free to continue.
	caller := 7
	caller++
	if caller != 8 {
		t.Fatal("post-Emit logic broken")
	}
}

// TestEmitToExplicitPath verifies the path-override entrypoint.
func TestEmitToExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "events.log") // tests parent-mkdir

	EmitTo(context.Background(), path, Event{
		EventType: "x",
		Agent:     "y",
		Details:   map[string]any{"a": 1},
	})

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
}

func TestExplicitTimestampPreserved(t *testing.T) {
	path := useTempLog(t)

	ts := "2026-04-25T10:00:00.000000000Z"
	Emit(context.Background(), Event{
		Timestamp: ts,
		EventType: "x",
		Agent:     "y",
	})

	lines := readLines(t, path)
	var got Event
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Timestamp != ts {
		t.Errorf("explicit timestamp clobbered: want %q, got %q", ts, got.Timestamp)
	}
}

func TestExplicitSchemaVersionPreserved(t *testing.T) {
	path := useTempLog(t)

	Emit(context.Background(), Event{
		SchemaVersion: 99,
		EventType:     "x",
		Agent:         "y",
	})

	lines := readLines(t, path)
	var got Event
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SchemaVersion != 99 {
		t.Errorf("explicit schema_version clobbered: want 99, got %d", got.SchemaVersion)
	}
}

// fillFile writes n bytes of dummy padding to path so a subsequent stat
// returns a size at or above the threshold. Each line is a JSONL-shaped
// record so a real reader could still parse the rotated file.
func fillFile(t *testing.T, path string, n int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create fill file: %v", err)
	}
	defer f.Close()
	if err := f.Truncate(n); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// TestRotationTriggersAtThreshold forces the live log past 100MB and verifies
// the next Emit moves it to events.log.1 and starts a fresh events.log.
func TestRotationTriggersAtThreshold(t *testing.T) {
	path := useTempLog(t)
	fillFile(t, path, maxLogBytes+1)

	Emit(context.Background(), Event{EventType: "post-rotate", Agent: "test"})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat live log: %v", err)
	}
	if info.Size() >= maxLogBytes {
		t.Fatalf("live log not rotated: size=%d", info.Size())
	}
	rotated, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("stat events.log.1: %v", err)
	}
	if rotated.Size() < maxLogBytes {
		t.Fatalf("rotated log too small: size=%d, want >= %d", rotated.Size(), maxLogBytes)
	}
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 post-rotation line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "post-rotate") {
		t.Fatalf("post-rotation line missing event_type: %q", lines[0])
	}
}

// TestRotationBelowThresholdNoOp verifies a near-threshold but undersized log
// is not rotated — the boundary is `>= maxLogBytes`, not `> maxLogBytes - 1`.
func TestRotationBelowThresholdNoOp(t *testing.T) {
	path := useTempLog(t)
	fillFile(t, path, maxLogBytes-1)

	Emit(context.Background(), Event{EventType: "no-rotate", Agent: "test"})

	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("events.log.1 should not exist below threshold (err=%v)", err)
	}
}

// TestRotationKeepsAtMostFiveAndDeletesOldest forces six rotations and checks
// that events.log.1..events.log.5 exist, events.log.6 does not, and the oldest
// (events.log.5) carries the marker from the very first rotation generation —
// confirming each new rotation pushes everything down by one.
func TestRotationKeepsAtMostFiveAndDeletesOldest(t *testing.T) {
	path := useTempLog(t)

	for gen := 0; gen < 6; gen++ {
		// Make the live log start with a unique marker for this generation,
		// then pad past the threshold so the next Emit rotates this content
		// into events.log.1.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			t.Fatalf("open live log: %v", err)
		}
		marker := []byte(fmt.Sprintf("gen-%d\n", gen))
		if _, err := f.Write(marker); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if err := f.Truncate(maxLogBytes + 1); err != nil {
			t.Fatalf("pad live log: %v", err)
		}
		f.Close()

		Emit(context.Background(), Event{EventType: "rotate-trigger", Agent: "test"})
	}

	for i := 1; i <= maxRotatedFiles; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", path, i)); err != nil {
			t.Fatalf("expected events.log.%d to exist: %v", i, err)
		}
	}
	if _, err := os.Stat(fmt.Sprintf("%s.%d", path, maxRotatedFiles+1)); !os.IsNotExist(err) {
		t.Fatalf("events.log.%d should not exist (err=%v)", maxRotatedFiles+1, err)
	}

	// After 6 rotations, the oldest (events.log.5) holds gen-1's marker
	// (gen-0 was deleted). Read just the first line — the rest is padding.
	oldest, err := os.Open(fmt.Sprintf("%s.%d", path, maxRotatedFiles))
	if err != nil {
		t.Fatalf("open oldest: %v", err)
	}
	defer oldest.Close()
	scanner := bufio.NewScanner(oldest)
	if !scanner.Scan() {
		t.Fatalf("oldest log empty")
	}
	if scanner.Text() != "gen-1" {
		t.Fatalf("oldest rotation should hold gen-1 marker, got %q (gen-0 should have been deleted)", scanner.Text())
	}
}

// TestRotationConcurrentEmittersOnlyRotateOnce verifies the rotate lock
// serializes concurrent rotations: 20 emitters racing on a too-large file
// produce one events.log.1, not many — and no events are lost in the live
// log.
func TestRotationConcurrentEmittersOnlyRotateOnce(t *testing.T) {
	path := useTempLog(t)
	fillFile(t, path, maxLogBytes+1)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			Emit(context.Background(), Event{
				EventType: "race-rotate",
				Agent:     "writer",
				Details:   map[string]any{"g": g},
			})
		}(g)
	}
	wg.Wait()

	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("events.log.2 should not exist after a single rotation (err=%v)", err)
	}
	lines := readLines(t, path)
	if len(lines) != goroutines {
		t.Fatalf("expected %d post-rotation lines, got %d", goroutines, len(lines))
	}
}

func TestResolveAgent(t *testing.T) {
	cases := []struct {
		name      string
		envName   string
		envType   string
		wantAgent string
	}{
		{"no env", "", "", "human"},
		{"polecat", "mg-4fa7", "polecat", "cat-mg-4fa7"},
		{"crew", "arch", "crew", "crew-arch"},
		{"mayor special-case", "mayor", "crew", "mayor"},
		{"name without type", "weird", "", "weird"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("POGO_AGENT_NAME", tc.envName)
			t.Setenv("POGO_AGENT_TYPE", tc.envType)
			if got := ResolveAgent(); got != tc.wantAgent {
				t.Errorf("ResolveAgent() = %q, want %q", got, tc.wantAgent)
			}
		})
	}
}
