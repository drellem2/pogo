package events

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// padToRotation appends blank lines until path reaches the rotation threshold,
// so the next EmitTo performs a REAL rotation (rotate(), not a hand rename).
// Blank lines are skipped by every reader, so they add no events.
func padToRotation(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte{'\n'}, 1<<20)
	for left := int64(maxLogBytes) - info.Size(); left > 0; left -= int64(len(chunk)) {
		n := int64(len(chunk))
		if left < n {
			n = left
		}
		if _, err := f.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
	}
}

func emitAt(path, typ string, at time.Time) {
	EmitTo(context.Background(), path, Event{EventType: typ, Agent: "t", Timestamp: at.UTC().Format(time.RFC3339Nano)})
}

// TestReadWindowCrossesRotation is mg-50b9's shape: two matching events are
// written, the log rotates, and a --since window reaching back over the
// rotation must count them. The live file alone — what `pogo events list`
// used to read — holds none of them, and that control is asserted too so the
// test cannot pass by the rotation not having happened.
func TestReadWindowCrossesRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	now := time.Now()

	emitAt(path, "investigation_search", now.Add(-48*time.Hour))
	emitAt(path, "investigation_search", now.Add(-47*time.Hour))
	emitAt(path, "other", now.Add(-46*time.Hour))
	padToRotation(t, path)
	emitAt(path, "investigation_search", now.Add(-1*time.Hour)) // rotates first
	emitAt(path, "other", now.Add(-30*time.Minute))

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("precondition: the log did not rotate: %v", err)
	}
	f := Filter{Type: "investigation_search", SinceMin: now.Add(-72 * time.Hour)}

	live, err := ReadFiltered(path, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("control: the live file alone should hold 1 match, got %d", len(live))
	}

	w, err := ReadWindow(path, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Events) != 3 {
		t.Fatalf("window crossing the rotation: want 3 matches (2 in .1, 1 live), got %d", len(w.Events))
	}
	if w.Truncated {
		t.Error("nothing has been discarded (no .5), so a window reaching past the first record is complete, not truncated")
	}
	// Time order: rotated chunk first.
	for i := 1; i < len(w.Events); i++ {
		if w.Events[i].Timestamp < w.Events[i-1].Timestamp {
			t.Errorf("events out of time order at %d: %s before %s", i, w.Events[i-1].Timestamp, w.Events[i].Timestamp)
		}
	}

	// A window that starts after the live file's first record does not walk
	// the rotated one.
	w, err = ReadWindow(path, Filter{Type: "other", SinceMin: now.Add(-45 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Events) != 1 || len(w.Files) != 1 {
		t.Errorf("45m window: want 1 match from 1 file, got %d from %v", len(w.Events), w.Files)
	}
}

// writeChunk writes one retained file whose single record is at `at`.
func writeChunk(t *testing.T, p, typ string, at time.Time) {
	t.Helper()
	line := fmt.Sprintf(`{"schema_version":1,"timestamp":%q,"event_type":%q,"agent":"t","details":{}}`+"\n",
		at.UTC().Format(time.RFC3339Nano), typ)
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadWindowTruncatedPastOldestRetained: once rotation has discarded a
// chunk, a window reaching past the oldest retained record must say so — even,
// especially, when it found nothing.
func TestReadWindowTruncatedPastOldestRetained(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	now := time.Now()
	oldest := now.Add(-100 * time.Hour)
	for i := maxRotatedFiles; i >= 1; i-- {
		writeChunk(t, fmt.Sprintf("%s.%d", path, i), "other", now.Add(-time.Duration(i*20)*time.Hour))
	}
	writeChunk(t, path, "other", now.Add(-time.Hour))
	if !LogSpilled(path) {
		t.Fatal("precondition: slot .5 is filled, the log should read as spilled")
	}

	w, err := ReadWindow(path, Filter{Type: "investigation_search", SinceMin: now.Add(-200 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Events) != 0 {
		t.Fatalf("want 0 matches, got %d", len(w.Events))
	}
	if !w.Truncated {
		t.Error("a zero over a window reaching past discarded history must be flagged truncated, not returned clean")
	}
	if w.Floor.Sub(oldest).Abs() > time.Microsecond {
		t.Errorf("floor: want %s, got %s", oldest, w.Floor)
	}

	w, err = ReadWindow(path, Filter{SinceMin: now.Add(-50 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Truncated {
		t.Error("a window inside retained history is complete")
	}
	if len(w.Events) != 3 { // .2 (-40h), .1 (-20h), live (-1h)
		t.Errorf("50h window: want 3 events, got %d", len(w.Events))
	}

	w, err = ReadWindow(path, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if w.Truncated {
		t.Error("an unbounded read asks for everything retained and gets it; it is not truncated")
	}
	if len(w.Events) != maxRotatedFiles+1 {
		t.Errorf("unbounded: want %d events, got %d", maxRotatedFiles+1, len(w.Events))
	}
}
