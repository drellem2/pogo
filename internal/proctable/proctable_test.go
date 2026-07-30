package proctable

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestParseCPUTime covers both dialects of the ps TIME column. The cases with a
// fractional part are darwin's; the whole-second ones are procps'. Both parse,
// and the fact that only one of them CARRIES sub-second information is what
// Source.Resolution exists to say — a parser cannot recover precision the
// column never had.
func TestParseCPUTime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"0:00.02", 20 * time.Millisecond, true},
		{"0:01.25", 1250 * time.Millisecond, true},
		{"1:30", 90 * time.Second, true},
		{"168:35.92", 168*time.Minute + 35*time.Second + 920*time.Millisecond, true},
		{"169:10.30", 169*time.Minute + 10300*time.Millisecond, true},
		{"2:03:04", 2*time.Hour + 3*time.Minute + 4*time.Second, true},
		{"12:34:56", 12*time.Hour + 34*time.Minute + 56*time.Second, true},
		{"3-02:00:00", 74 * time.Hour, true},
		{"2-03:04:05", 2*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second, true},
		{"", 0, false},
		{"nope", 0, false},
		{"garbage", 0, false},
		{"1:2:3:4", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseCPUTime(c.in)
		if ok != c.ok {
			t.Errorf("ParseCPUTime(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("ParseCPUTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParsePSSkipsMalformedLines(t *testing.T) {
	rows := ParsePS("  1 0 1 0:01.00\ngarbage\n  2 1 x 0:02.00\n  3 1 3 0:03.00\n")
	if len(rows) != 2 {
		t.Fatalf("ParsePS kept %d rows, want 2: %v", len(rows), rows)
	}
	if rows[0].PID != 1 || rows[1].PID != 3 {
		t.Errorf("unexpected rows: %v", rows)
	}
	if rows[0].PPID != 0 || rows[0].PGID != 1 || rows[0].CPU != time.Second {
		t.Errorf("row 0 misparsed: %+v", rows[0])
	}
}

// TestParseProcStat is the Linux reader's unit test, and it runs everywhere —
// deliberately. The bug this package fixes was a Linux-only behaviour that no
// darwin run could see, and a parser that is only exercised on the platform it
// targets repeats that shape one level down.
func TestParseProcStat(t *testing.T) {
	// A real-shaped line: pid 4242, 137 utime ticks + 63 stime ticks = 200
	// ticks = 2s at 10ms per tick.
	line := "4242 (sh) S 4200 4242 4242 0 -1 4194304 512 0 0 0 137 63 0 0 20 0 1 0 99 0 0\n"
	row, ok := parseProcStat(line)
	if !ok {
		t.Fatalf("parseProcStat refused a well-formed line")
	}
	if row.PID != 4242 || row.PPID != 4200 || row.PGID != 4242 {
		t.Errorf("identity misparsed: %+v", row)
	}
	if row.CPU != 2*time.Second {
		t.Errorf("CPU = %v, want 2s (137+63 ticks at %v)", row.CPU, procfsTick)
	}
}

// TestParseProcStatSurvivesAParenthesisedCommand guards the one field in
// /proc/<pid>/stat that cannot be split on: comm is unescaped and may contain
// spaces and parentheses, so a naive Fields() walk shifts every field after it
// and silently reads someone else's number as CPU time.
func TestParseProcStatSurvivesAParenthesisedCommand(t *testing.T) {
	line := "77 (weird ) name (x)) R 5 77 77 0 -1 0 0 0 0 0 10 5 0 0 20 0 1 0 1 0 0\n"
	row, ok := parseProcStat(line)
	if !ok {
		t.Fatalf("parseProcStat refused a legal parenthesised comm")
	}
	if row.PID != 77 || row.PPID != 5 || row.PGID != 77 {
		t.Errorf("identity misparsed: %+v", row)
	}
	if row.CPU != 150*time.Millisecond {
		t.Errorf("CPU = %v, want 150ms (10+5 ticks)", row.CPU)
	}
}

func TestParseProcStatRejectsRubbish(t *testing.T) {
	for _, s := range []string{"", "no parens here", "12 (sh) S 1", "x (sh) S 1 2 3 0 0 0 0 0 0 0 1 1"} {
		if _, ok := parseProcStat(s); ok {
			t.Errorf("parseProcStat accepted %q", s)
		}
	}
}

// TestReadProcfsWalksAFixtureTree exercises the directory walk on every
// platform by pointing it at a fake /proc.
func TestReadProcfsWalksAFixtureTree(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("1", "1 (init) S 0 1 1 0 -1 0 0 0 0 0 100 0 0 0 20 0 1 0 1 0 0\n")
	write("2", "2 (worker) R 1 1 1 0 -1 0 0 0 0 0 50 25 0 0 20 0 1 0 1 0 0\n")
	// A process that exited between the listing and the read: no stat file.
	write("3", "")
	// Not a pid at all — /proc is full of these.
	write("uptime-ish", "not a process")

	rows, err := readProcfs(root)
	if err != nil {
		t.Fatalf("readProcfs: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("read %d rows, want 2 (the departed pid and the non-pid entry must be skipped): %+v", len(rows), rows)
	}
	byPID := map[int]Row{}
	for _, r := range rows {
		byPID[r.PID] = r
	}
	if byPID[1].CPU != time.Second {
		t.Errorf("pid 1 CPU = %v, want 1s", byPID[1].CPU)
	}
	if byPID[2].CPU != 750*time.Millisecond {
		t.Errorf("pid 2 CPU = %v, want 750ms (50+25 ticks)", byPID[2].CPU)
	}
}

func TestReadProcfsRefusesAnEmptyTree(t *testing.T) {
	// Zero readable processes is not an empty host, it is a broken read. It
	// must surface as an error, because a caller that receives it as an empty
	// table computes a rate of zero and reports an idle machine.
	if _, err := readProcfs(t.TempDir()); err == nil {
		t.Error("readProcfs accepted a tree with no process entries")
	}
	if _, err := readProcfs(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("readProcfs accepted a missing root")
	}
}

// TestSourceResolutionGovernsTheUsableWindow is the assertion the CI failure
// turned on. A source whose CPU column is in whole seconds cannot answer a
// sub-second window, and must say so rather than let a caller divide zero by
// the window and call the result idle.
func TestSourceResolutionGovernsTheUsableWindow(t *testing.T) {
	coarse := Source{Name: "test-ps", Resolution: time.Second}
	fine := Source{Name: "test-procfs", Resolution: 10 * time.Millisecond}

	if coarse.CanResolve(400 * time.Millisecond) {
		t.Error("a whole-second column must not claim to resolve a 400ms window")
	}
	if reason := coarse.CannotResolve(400 * time.Millisecond); reason == "" {
		t.Error("an unresolvable window must come with a reason")
	} else {
		// The reason has to name all three numbers, or a reader cannot tell
		// whether to widen the window or change host.
		for _, want := range []string{"test-ps", "1s", "400ms", "5s"} {
			if !strings.Contains(reason, want) {
				t.Errorf("reason %q does not mention %q", reason, want)
			}
		}
	}
	if !coarse.CanResolve(5 * time.Second) {
		t.Error("a whole-second column must resolve a 5s window")
	}
	if !fine.CanResolve(400 * time.Millisecond) {
		t.Error("a 10ms column must resolve a 400ms window")
	}
	if reason := fine.CannotResolve(400 * time.Millisecond); reason != "" {
		t.Errorf("a resolvable window must return no reason, got %q", reason)
	}
	if fine.CanResolve(10 * time.Millisecond) {
		t.Error("even a 10ms column cannot resolve a single-tick window")
	}
}

// TestCurrentSourceMatchesThisPlatform states, per platform, which reader is
// expected and how precise it is. It is the test that would have caught the
// original failure: nothing else in the tree asserted that a Linux run has a
// usable resolution at all.
func TestCurrentSourceMatchesThisPlatform(t *testing.T) {
	src := Current()
	t.Logf("process-table source on %s: %s", runtime.GOOS, src)
	if src.Name == "" || src.Resolution <= 0 {
		t.Fatalf("detect() returned an unusable source: %+v", src)
	}
	switch runtime.GOOS {
	case "linux":
		// Every Linux this project supports has procfs; if one does not, the
		// fallback is honest but the gate/hostload windows will not resolve,
		// and this is where that gets noticed.
		if src.Name != linuxProcfs {
			t.Errorf("on Linux the source must be %s, got %q — sub-second windows will not resolve",
				linuxProcfs, src.Name)
		}
		if src.Resolution != procfsTick {
			t.Errorf("procfs resolution = %v, want %v", src.Resolution, procfsTick)
		}
	case "darwin":
		if src.Name != "darwin-ps" || src.Resolution != darwinPSTick {
			t.Errorf("on darwin the source must be darwin-ps at %v, got %+v", darwinPSTick, src)
		}
	}
	// Both supported platforms must resolve the windows their callers use: the
	// refinery's compressed test heartbeat (400ms) and hostload's default (1s).
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if !src.CanResolve(400 * time.Millisecond) {
			t.Errorf("%s cannot resolve a 400ms window; the gate CPU signal is blind here", src)
		}
	}
}

// TestReadReadsTheRealProcessTable is the positive control on whichever reader
// this platform selected. If the flags, the TIME format, or the procfs field
// offsets are wrong here, every CPU reading in the tree silently degrades and
// this is what notices.
func TestReadReadsTheRealProcessTable(t *testing.T) {
	rows, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rows) < 10 {
		t.Fatalf("read %d rows; the process table is not that small", len(rows))
	}
	self, found := os.Getpid(), false
	var mine Row
	for _, r := range rows {
		if r.PID == self {
			found, mine = true, r
		}
		if r.PID <= 0 || r.CPU < 0 {
			t.Fatalf("nonsense row: %+v", r)
		}
	}
	if !found {
		t.Fatalf("the table did not contain this test process (pid %d)", self)
	}
	if mine.PPID <= 0 {
		t.Errorf("this process reported ppid %d", mine.PPID)
	}

	// The column has to ADVANCE, not merely be present. A cumulative CPU
	// figure that never moves is indistinguishable from one that is not being
	// read at all, and "not being read" is exactly the failure this package
	// exists to prevent. So burn a known amount of CPU and require the reading
	// to change — which is also the only operation the callers ever perform on
	// this column.
	//
	// Asserting a non-zero absolute value instead would be wrong: this suite
	// runs in a few milliseconds, so a freshly started test binary can hold
	// genuinely zero whole ticks of CPU on a 10ms-resolution source.
	src := Current()
	spin(2 * src.MinWindow())
	after, err := Read()
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	var mineAfter Row
	for _, r := range after {
		if r.PID == self {
			mineAfter = r
		}
	}
	if mineAfter.CPU <= mine.CPU {
		t.Errorf("this process burned CPU for %s and its cumulative CPU column went %v -> %v; "+
			"%s is not reporting work", 2*src.MinWindow(), mine.CPU, mineAfter.CPU, src)
	}
}

// spin burns CPU in this process for d.
func spin(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
	}
}

func TestReadPSSurfacesAFailure(t *testing.T) {
	prev := execPS
	execPS = func(context.Context) ([]byte, error) { return nil, errors.New("boom") }
	t.Cleanup(func() { execPS = prev })
	if _, err := readPS(); err == nil {
		t.Fatal("readPS swallowed an exec failure")
	}
}
