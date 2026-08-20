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
