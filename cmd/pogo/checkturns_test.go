package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/turnlog"
)

// TestRenderTurnReportDistinguishesCleanFromUnexamined. "Every present agent
// completed a turn" and "no agent was examined" both produce zero findings, and
// telling them apart is the single reading this whole ticket turns on: for
// twenty-two hours the fleet's instruments produced the second and everyone
// read it as the first.
func TestRenderTurnReportDistinguishesCleanFromUnexamined(t *testing.T) {
	empty := renderTurnReport(turnlog.Report{Dir: "/x/turnlog", MaxAge: "3h0m0s"}, false)
	if !strings.Contains(empty, "No agent was examined") || !strings.Contains(empty, "NOT a clean fleet") {
		t.Errorf("an empty population rendered as a pass:\n%s", empty)
	}

	now := time.Now().UTC()
	clean := renderTurnReport(turnlog.Report{
		Dir: "/x/turnlog", MaxAge: "3h0m0s", Live: 1,
		Agents: []turnlog.State{{Agent: "mayor", Verdict: turnlog.VerdictLive, Last: now.Add(-time.Minute), AgeSecs: 60}},
	}, false)
	if !strings.Contains(clean, "Every present agent has completed a turn") {
		t.Errorf("a genuinely clean report did not say so:\n%s", clean)
	}
	if strings.Contains(clean, "No agent was examined") {
		t.Errorf("clean and unexamined rendered the same:\n%s", clean)
	}
}

// TestRenderTurnReportNamesTheSilent. A `silent` agent has two causes with
// opposite responses — it has completed no turn since starting, or it is
// running a prompt rendered before this artifact existed — and the report must
// not let a reader collapse them.
func TestRenderTurnReportNamesTheSilent(t *testing.T) {
	out := renderTurnReport(turnlog.Report{
		Dir: "/x/turnlog", MaxAge: "3h0m0s", Silent: 1, Findings: 1,
		Agents: []turnlog.State{{
			Agent: "architect", Verdict: turnlog.VerdictSilent,
			Detail: "no turn-completion artifact exists for this agent",
		}},
	}, false)
	for _, want := range []string{"architect", "silent", "never", "check its uptime"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	// It must not recommend a restart. An agent failing every turn in 10ms is
	// not wedged, and restarting destroys the transcript that says which it is.
	if strings.Contains(out, "pogo agent stop") {
		t.Errorf("the report recommends a restart; it should route to diagnose:\n%s", out)
	}
}

func TestShortDur(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:          "30s",
		90 * time.Second:          "1m",
		22 * time.Hour:            "22h00m",
		50 * time.Hour:            "2d",
		time.Hour + 5*time.Minute: "1h05m",
	}
	for in, want := range cases {
		if got := shortDur(in); got != want {
			t.Errorf("shortDur(%s) = %q, want %q", in, got, want)
		}
	}
}
