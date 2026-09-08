package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/health"
)

// TestHealthPogodLineNamesThePid pins the pid onto `pogo server status`, which
// is mg-cbee's replacement for a pattern match that cannot work.
//
// `pgrep`/`pkill` exclude the calling process and every one of its ancestors
// unless passed `-a` (`man pgrep`), and pogod is the ancestor of every agent it
// spawns. Measured 2026-08-20 from a worker shell: `pgrep -x pogod`,
// `pgrep -f pogod` and bare `pgrep pogod` all returned empty at exit 1 while
// `lsof -iTCP:10000 -sTCP:LISTEN` showed pogod serving on it, and
// `pgrep -ax pogod` returned the pid. Agents had no other way to ask.
func TestHealthPogodLineNamesThePid(t *testing.T) {
	line := formatHealthPogod(health.Pogod{Status: "ok", Mode: "full", Uptime: "57m49s", PID: 11579})
	for _, want := range []string{"pogod:", "ok", "mode=full", "uptime=57m49s", "pid=11579"} {
		if !strings.Contains(line, want) {
			t.Errorf("pogod health line missing %q: %s", want, line)
		}
	}
}

// TestHealthPogodLineNamesAnAbsentPidRatherThanPrintingZero is the RED arm, and
// it guards against this change reproducing the defect it was filed about.
//
// mg-cbee is a ticket about a reading that answered a different question at
// exit 0 and looked well-formed doing it. The obvious implementation here has
// the same shape: a daemon built before the field decodes to PID 0, and
// `pid=0` is a plausible-looking token a reader carries straight into `kill` or
// `ps`. So an unreported pid must be rendered as a NAMED absence with a working
// alternative attached, and `pid=0` must never appear.
func TestHealthPogodLineNamesAnAbsentPidRatherThanPrintingZero(t *testing.T) {
	line := formatHealthPogod(health.Pogod{Status: "ok", Mode: "full", Uptime: "1m"})
	if strings.Contains(line, "pid=0") {
		t.Errorf("an unreported pid rendered as `pid=0`, a number a reader will use: %s", line)
	}
	for _, want := range []string{"unreported", "predates", "lsof"} {
		if !strings.Contains(line, want) {
			t.Errorf("absent-pid rendering missing %q — it must name the absence and give a way to get the answer: %s", want, line)
		}
	}
}

// TestHealthPogodLineReportsDispatch pins the drain flag onto `pogo server
// status` (mg-5c4a).
//
// `draining=true` refuses ALL new polecat dispatch, and until this line nothing
// on the box reported it: /version answers, mode reads `full`, the agent roster
// looks normal, and the fleet silently does no work. On four consecutive nights
// (2026-09-04..07) a deploy killed by its own run deadline left the flag set,
// and each time a human happened to read /agents/drain the next morning. This
// is the instrument that is actually read.
func TestHealthPogodLineReportsDispatch(t *testing.T) {
	no, yes := false, true

	off := formatHealthPogod(health.Pogod{Status: "ok", Mode: "full", Uptime: "1m", PID: 1, Draining: &no})
	if !strings.Contains(off, "draining=false") {
		t.Errorf("the healthy value must print too — a token that appears only on failure cannot be told from one that was dropped: %s", off)
	}

	on := formatHealthPogod(health.Pogod{Status: "ok", Mode: "full", Uptime: "1m", PID: 1, Draining: &yes})
	for _, want := range []string{"draining=TRUE", "DISPATCH IS OFF", "/agents/drain"} {
		if !strings.Contains(on, want) {
			t.Errorf("a draining pogod must say so and carry the remedy, missing %q: %s", want, on)
		}
	}
}

// TestHealthPogodLineDoesNotInventADispatchState is the RED arm, and it guards
// this fix against the defect it was filed about.
//
// mg-5c4a is a ticket about instruments that state a happy-path outcome they
// did not observe. A daemon built before the field sends no `draining` at all,
// and rendering that as `draining=false` would be the same move: a confident
// answer over a question nobody asked the daemon, printed on the one line an
// operator is most likely to believe.
func TestHealthPogodLineDoesNotInventADispatchState(t *testing.T) {
	line := formatHealthPogod(health.Pogod{Status: "ok", Mode: "full", Uptime: "1m", PID: 1})
	if strings.Contains(line, "draining=false") || strings.Contains(line, "draining=TRUE") {
		t.Errorf("an unreported drain flag was rendered as a value: %s", line)
	}
	for _, want := range []string{"unreported", "predates", "/agents/drain"} {
		if !strings.Contains(line, want) {
			t.Errorf("absent-flag rendering missing %q — it must name the absence and give a way to get the answer: %s", want, line)
		}
	}
}
