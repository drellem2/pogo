package events

import (
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

func mustWriteEvents(t *testing.T, path string, evs []Event) {
	t.Helper()
	for _, ev := range evs {
		EmitTo(context.Background(), path, ev)
	}
}

func TestFilterMatchTypeAndAgent(t *testing.T) {
	ev := Event{
		EventType: "agent_spawned",
		Agent:     "cat-mg-156b",
		Timestamp: "2026-04-25T10:00:00.000000000Z",
	}

	// Empty filter matches.
	if !(Filter{}).Match(ev) {
		t.Fatal("empty filter should match")
	}

	// Type match / mismatch.
	if !(Filter{Type: "agent_spawned"}).Match(ev) {
		t.Errorf("type=agent_spawned should match")
	}
	if (Filter{Type: "refinery_merged"}).Match(ev) {
		t.Errorf("type=refinery_merged should NOT match")
	}

	// Agent match / mismatch.
	if !(Filter{Agent: "cat-mg-156b"}).Match(ev) {
		t.Errorf("agent=cat-mg-156b should match")
	}
	if (Filter{Agent: "mayor"}).Match(ev) {
		t.Errorf("agent=mayor should NOT match")
	}
}

func TestFilterSinceMin(t *testing.T) {
	now := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	old := Event{Timestamp: now.Add(-2 * time.Hour).Format(time.RFC3339Nano), EventType: "x", Agent: "y"}
	recent := Event{Timestamp: now.Add(-30 * time.Minute).Format(time.RFC3339Nano), EventType: "x", Agent: "y"}

	cutoff := now.Add(-1 * time.Hour)
	f := Filter{SinceMin: cutoff}

	if f.Match(old) {
		t.Errorf("event 2h old should fail since=1h")
	}
	if !f.Match(recent) {
		t.Errorf("event 30m old should pass since=1h")
	}
}

func TestFilterSinceMinUnparseableTimestamp(t *testing.T) {
	f := Filter{SinceMin: time.Now().Add(-1 * time.Hour)}
	bad := Event{Timestamp: "not-a-timestamp", EventType: "x", Agent: "y"}
	if f.Match(bad) {
		t.Errorf("unparseable timestamp should fail SinceMin filter, not pass through")
	}
	// With SinceMin unset, unparseable timestamps don't matter.
	if !(Filter{}).Match(bad) {
		t.Errorf("zero filter should still match events with bad timestamps")
	}
}

func TestReadFilteredHonorsAllFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")

	now := time.Now().UTC()
	mustWriteEvents(t, path, []Event{
		{Timestamp: now.Add(-3 * time.Hour).Format(time.RFC3339Nano), EventType: "agent_spawned", Agent: "cat-mg-001"},
		{Timestamp: now.Add(-30 * time.Minute).Format(time.RFC3339Nano), EventType: "agent_spawned", Agent: "cat-mg-002"},
		{Timestamp: now.Add(-15 * time.Minute).Format(time.RFC3339Nano), EventType: "refinery_merged", Agent: "refinery"},
		{Timestamp: now.Add(-5 * time.Minute).Format(time.RFC3339Nano), EventType: "agent_stopped", Agent: "cat-mg-002"},
	})

	// since=1h drops the 3h-old event.
	got, err := ReadFiltered(path, Filter{SinceMin: now.Add(-1 * time.Hour)})
	if err != nil {
		t.Fatalf("ReadFiltered: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("since=1h: want 3 events, got %d", len(got))
	}

	// since=1h + type=agent_spawned narrows to 1 event.
	got, _ = ReadFiltered(path, Filter{SinceMin: now.Add(-1 * time.Hour), Type: "agent_spawned"})
	if len(got) != 1 || got[0].Agent != "cat-mg-002" {
		t.Errorf("since+type: want [cat-mg-002], got %+v", got)
	}

	// agent=cat-mg-002 returns both events for that agent (no time filter).
	got, _ = ReadFiltered(path, Filter{Agent: "cat-mg-002"})
	if len(got) != 2 {
		t.Errorf("agent=cat-mg-002: want 2 events, got %d", len(got))
	}
}

func TestReadFilteredMissingFile(t *testing.T) {
	got, err := ReadFiltered(filepath.Join(t.TempDir(), "nope.log"), Filter{})
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if got != nil {
		t.Errorf("missing file should yield nil events, got %+v", got)
	}
}

func TestReadFilteredSkipsMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	// Write a real event, a junk line, then another real event.
	if err := os.WriteFile(path, []byte(
		`{"schema_version":1,"timestamp":"2026-04-25T10:00:00Z","event_type":"x","agent":"a","details":{}}
this is not json
{"schema_version":1,"timestamp":"2026-04-25T10:00:01Z","event_type":"y","agent":"b","details":{}}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadFiltered(path, Filter{})
	if err != nil {
		t.Fatalf("ReadFiltered: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 events (malformed line skipped), got %d", len(got))
	}
}

func TestFormatPretty(t *testing.T) {
	ev := Event{
		Timestamp:  "2026-04-25T10:23:09.123456789Z",
		EventType:  "refinery_merged",
		Agent:      "refinery",
		WorkItemID: "mg-0241",
		Repo:       "/Users/daniel/dev/pogo",
		Details: map[string]any{
			"merge_request_id": "mr-9482",
			"branch":           "polecat-mg-0241",
		},
	}
	out := FormatPretty(ev)
	for _, want := range []string{
		"2026-04-25T10:23:09Z",
		"refinery_merged",
		"refinery",
		"work_item=mg-0241",
		"repo=/Users/daniel/dev/pogo",
		"branch=polecat-mg-0241",
		"merge_request_id=mr-9482",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatPretty missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatPrettyDetailsOrderingDeterministic(t *testing.T) {
	ev := Event{
		Timestamp: "2026-04-25T10:00:00Z",
		EventType: "x", Agent: "y",
		Details: map[string]any{"z": 1, "a": 2, "m": 3},
	}
	out := FormatPretty(ev)
	// Keys sorted: a, m, z.
	if strings.Index(out, "a=2") > strings.Index(out, "m=3") || strings.Index(out, "m=3") > strings.Index(out, "z=1") {
		t.Errorf("details not in sorted order: %s", out)
	}
}

func TestFormatPrettyOmitsEmptyOptionals(t *testing.T) {
	ev := Event{
		Timestamp: "2026-04-25T10:00:00Z",
		EventType: "x", Agent: "y",
	}
	out := FormatPretty(ev)
	if strings.Contains(out, "work_item=") {
		t.Errorf("should omit empty work_item: %s", out)
	}
	if strings.Contains(out, "repo=") {
		t.Errorf("should omit empty repo: %s", out)
	}
}

func TestFollowDeliversAppendedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(path, []byte(`{"event_type":"old"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var mu sync.Mutex
	var got []string
	done := make(chan error, 1)

	go func() {
		done <- Follow(path, 20*time.Millisecond, stop, func(line []byte) {
			mu.Lock()
			got = append(got, string(line))
			mu.Unlock()
		})
	}()

	// Give Follow a moment to seek to end so it skips the pre-existing line.
	time.Sleep(80 * time.Millisecond)

	// Append two new events; Follow should deliver both.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event_type":"new1"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event_type":"new2"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Wait for delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("Follow returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want 2 lines streamed, got %d: %v", len(got), got)
	}
	// Ensure both lines parse and we got "new1" / "new2", not "old".
	for i, line := range got {
		var ev struct {
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d not JSON: %v", i, err)
		}
		if ev.EventType == "old" {
			t.Errorf("Follow streamed pre-existing line; should seek to end")
		}
	}
}

func TestFollowCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	stop := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- Follow(path, 20*time.Millisecond, stop, func(line []byte) {})
	}()

	// Give Follow time to create the file.
	time.Sleep(80 * time.Millisecond)
	if _, err := os.Stat(path); err != nil {
		close(stop)
		<-done
		t.Fatalf("Follow should have created the missing file: %v", err)
	}

	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("Follow: %v", err)
	}
}

func TestFollowExitsOnStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Follow(path, 20*time.Millisecond, stop, func(line []byte) {})
	}()

	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Follow returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Follow did not exit on stop")
	}
}

func TestParseLineRejectsGarbage(t *testing.T) {
	if _, err := ParseLine([]byte("not json")); err == nil {
		t.Error("ParseLine should reject non-JSON")
	}
}

func TestScanFileStreamsInOrderAndSkipsMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	mustWriteEvents(t, path, []Event{
		{EventType: "a", Agent: "x", Timestamp: "2026-04-25T10:00:00.000000000Z"},
		{EventType: "b", Agent: "y", Timestamp: "2026-04-25T11:00:00.000000000Z"},
	})
	// Append a malformed line and a well-formed one after it: the bad line must
	// be skipped without aborting the scan.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not json\n" + `{"event_type":"c","agent":"z","timestamp":"2026-04-25T12:00:00.000000000Z","details":{}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var seen []string
	if err := ScanFile(path, func(ev Event) { seen = append(seen, ev.EventType) }); err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if got := strings.Join(seen, ","); got != "a,b,c" {
		t.Errorf("want a,b,c in file order, got %q", got)
	}
}

func TestScanFileMissingPathIsNotAnError(t *testing.T) {
	called := false
	if err := ScanFile(filepath.Join(t.TempDir(), "nope.log"), func(Event) { called = true }); err != nil {
		t.Errorf("missing path should mean 'no events yet', got %v", err)
	}
	if called {
		t.Error("visit should not be called for a missing log")
	}
}

// TestLogFilesChronologicalOrder pins the ordering contract LogFiles exists for:
// the highest-numbered rotated file is the OLDEST, so a caller walking the
// returned slice sees history forwards.
func TestLogFilesChronologicalOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	if got := LogFiles(path); len(got) != 0 {
		t.Errorf("no files yet, got %v", got)
	}

	for _, p := range []string{path, path + ".1", path + ".3"} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := LogFiles(path)
	want := []string{path + ".3", path + ".1", path}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: want %s, got %s", i, want[i], got[i])
		}
	}
}

// TestLogSpilledOnlyWhenTheLastSlotIsOccupied is the distinction a coverage
// claim rests on: an unrotated (or partly rotated) log has discarded nothing, so
// its first record is a beginning, not a cut.
func TestLogSpilledOnlyWhenTheLastSlotIsOccupied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	if LogSpilled(path) {
		t.Error("no log at all has not spilled")
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if LogSpilled(path) {
		t.Error("a live log with no rotated files has not spilled")
	}
	for i := 1; i < maxRotatedFiles; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s.%d", path, i), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if LogSpilled(path) {
			t.Fatalf("slot .%d filled but the last slot (.%d) is empty; nothing has been discarded yet", i, maxRotatedFiles)
		}
	}
	if err := os.WriteFile(fmt.Sprintf("%s.%d", path, maxRotatedFiles), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !LogSpilled(path) {
		t.Errorf("the last retained slot (.%d) is occupied; the next rotation drops it, so coverage is no longer guaranteed", maxRotatedFiles)
	}
}
