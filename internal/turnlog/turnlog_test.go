package turnlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatAndParseRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 11, 20, 41, 7, 0, time.UTC)
	line := FormatLine("mayor", at, "cycle 412: dispatched 2, reaped 1")
	if !strings.HasSuffix(line, "\n") {
		t.Errorf("line must end in a newline: %q", line)
	}
	if got, want := line, "2026-08-11T20:41:07Z mayor cycle 412: dispatched 2, reaped 1\n"; got != want {
		t.Errorf("FormatLine = %q, want %q", got, want)
	}
	e, err := ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if e.Agent != "mayor" || !e.At.Equal(at) || e.Note != "cycle 412: dispatched 2, reaped 1" {
		t.Errorf("round trip lost data: %+v", e)
	}
}

// TestFormatLineIsChronologicalUnderLexicalSort pins the reason the timestamp
// comes first: a reader must be able to trust `tail -1` without parsing the
// whole file.
func TestFormatLineIsChronologicalUnderLexicalSort(t *testing.T) {
	base := time.Date(2026, 8, 11, 23, 59, 59, 0, time.UTC)
	prev := ""
	for i := 0; i < 5; i++ {
		line := FormatLine("a", base.Add(time.Duration(i)*time.Hour), "")
		if prev != "" && !(prev < line) {
			t.Fatalf("lexical order is not chronological: %q then %q", prev, line)
		}
		prev = line
	}
}

// TestFormatLineFlattensNotes: a note is free text from an agent, and an
// embedded newline would forge an extra turn — one line, one completed turn is
// the only property this file has.
func TestFormatLineFlattensNotes(t *testing.T) {
	line := FormatLine("pa", time.Now(), "did a thing\n2026-01-01T00:00:00Z mayor forged")
	if strings.Count(line, "\n") != 1 {
		t.Errorf("a note forged an extra line: %q", line)
	}
}

func TestParseLineRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "mayor", "not-a-timestamp mayor note", "2026-08-11 mayor"} {
		if _, err := ParseLine(in); err == nil {
			t.Errorf("ParseLine(%q) accepted a malformed line", in)
		}
	}
}

func TestAppendAndLast(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := AppendIn(root, "doctor", "turn", t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	e, err := LastIn(root, "doctor")
	if err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(2 * time.Minute); !e.At.Equal(want) {
		t.Errorf("Last returned %s, want the newest entry %s", e.At, want)
	}
}

// TestLastOnMissingFileIsNotExist is the contract every consumer keys on: an
// agent with no turnlog is os.ErrNotExist, and that must be read as RED. It is
// the state mayor, pa and architect were in.
func TestLastOnMissingFileIsNotExist(t *testing.T) {
	root := t.TempDir()
	_, err := LastIn(root, "mayor")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing turnlog gave %v, want os.ErrNotExist", err)
	}
	// An existing but EMPTY file is the same state and must not read as a
	// successful lookup with a zero time.
	if err := os.WriteFile(filepath.Join(root, "mayor.log"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LastIn(root, "mayor"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("empty turnlog gave %v, want os.ErrNotExist", err)
	}
}

// TestLastReadsOnlyTheTail and still finds the newest entry in a file far
// larger than the tail window.
func TestLastReadsOnlyTheTail(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	n := 2000 // ~100KB, well past tailBytes
	for i := 0; i < n; i++ {
		if err := AppendIn(root, "pm-pogo", "sweep", t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(PathIn(root, "pm-pogo"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= tailBytes {
		t.Fatalf("fixture too small to exercise the tail read: %d bytes", info.Size())
	}
	e, err := LastIn(root, "pm-pogo")
	if err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(time.Duration(n-1) * time.Minute); !e.At.Equal(want) {
		t.Errorf("Last = %s, want %s", e.At, want)
	}
}

// TestAppendRefusesAnUnnamedAgent: a line under no name, or under a name with a
// path separator in it, is not a missing signal — it is a false one.
func TestAppendRefusesAnUnnamedAgent(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", "   ", "a/b", "a b"} {
		if err := AppendIn(root, name, "n", time.Now()); err == nil {
			t.Errorf("AppendIn accepted agent name %q", name)
		}
	}
}

// TestDirIsFlatAndTierFree pins the convention mg-a270 asked for: uniform
// across all crew, with the PM tier not special-cased. A path carrying the tier
// would carry forward the accident that only the PMs are instrumented.
func TestDirIsFlatAndTierFree(t *testing.T) {
	for _, name := range []string{"mayor", "pa", "architect", "pm-pogo", "pm-onethird"} {
		got := Path(name)
		if filepath.Dir(got) != Dir() {
			t.Errorf("%s: turnlog at %s, want it directly under %s", name, got, Dir())
		}
		if filepath.Base(got) != name+".log" {
			t.Errorf("%s: basename %s, want %s.log", name, filepath.Base(got), name)
		}
	}
}
