package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/turnlog"
)

// fullRoster is a roster read in which every configured agent was in the
// population — the case where "every present agent" really is the fleet.
var fullRoster = turnCoverage{Configured: 1}

// TestRenderTurnReportDistinguishesCleanFromUnexamined. "Every present agent
// completed a turn" and "no agent was examined" both produce zero findings, and
// telling them apart is the single reading this whole ticket turns on: for
// twenty-two hours the fleet's instruments produced the second and everyone
// read it as the first.
func TestRenderTurnReportDistinguishesCleanFromUnexamined(t *testing.T) {
	empty := renderTurnReport(turnlog.Report{Dir: "/x/turnlog", MaxAge: "3h0m0s"}, fullRoster, false)
	if !strings.Contains(empty, "No agent was examined") || !strings.Contains(empty, "NOT a clean fleet") {
		t.Errorf("an empty population rendered as a pass:\n%s", empty)
	}

	now := time.Now().UTC()
	clean := renderTurnReport(turnlog.Report{
		Dir: "/x/turnlog", MaxAge: "3h0m0s", Live: 1,
		Agents: []turnlog.State{{Agent: "mayor", Verdict: turnlog.VerdictLive, Last: now.Add(-time.Minute), AgeSecs: 60}},
	}, fullRoster, false)
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
	}, fullRoster, false)
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

// cleanTurnReport is one live agent and nothing else wrong with it.
func cleanTurnReport() turnlog.Report {
	now := time.Now().UTC()
	return turnlog.Report{
		Dir: "/x/turnlog", MaxAge: "3h0m0s", Live: 1,
		Agents: []turnlog.State{{Agent: "mayor", Verdict: turnlog.VerdictLive, Last: now.Add(-time.Minute), AgeSecs: 60}},
	}
}

// TestRenderTurnReportNamesTheAbsent is mg-d88e's live proof, as a test. On
// 2026-09-06 check-turns examined six agents and closed with "Every present
// agent has completed a turn within the window" while doctor and representative
// were down — not silent rows, no rows at all. The absent must be named, and
// the clean line must stop reading as a fleet-wide green.
func TestRenderTurnReportNamesTheAbsent(t *testing.T) {
	cov := turnCoverage{
		Configured: 10,
		Parked:     []string{"pm-x", "pm-y"},
		Absent: []agent.RosterMember{
			{Name: "doctor", State: agent.RosterAbsent, Class: agent.RosterOnDemand},
			{Name: "representative", State: agent.RosterAbsent, Class: agent.RosterSupervised},
		},
	}
	out := renderTurnReport(cleanTurnReport(), cov, false)
	for _, want := range []string{
		"NOT EXAMINED", "doctor", "representative",
		"auto_start = false", "should have started at boot",
		"not examined: 2 parked: pm-x, pm-y",
		"Every present agent has completed a turn",
		"NOT a fleet-wide green",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// TestRenderTurnReportCompleteRosterAddsNothing: when every configured agent
// was examined, the coverage block must not add noise, or it becomes a line
// people learn to skip.
func TestRenderTurnReportCompleteRosterAddsNothing(t *testing.T) {
	out := renderTurnReport(cleanTurnReport(), turnCoverage{Configured: 6}, false)
	for _, bad := range []string{"NOT EXAMINED", "not examined", "fleet-wide", "roster"} {
		if strings.Contains(out, bad) {
			t.Errorf("a complete roster printed %q:\n%s", bad, out)
		}
	}
}

// TestRenderTurnReportRosterFailureIsSaidOutLoud: a roster that could not be
// read must not render like a complete one — that silence is the reading this
// ticket exists to remove.
func TestRenderTurnReportRosterFailureIsSaidOutLoud(t *testing.T) {
	out := renderTurnReport(cleanTurnReport(), turnCoverage{Error: "connection refused"}, false)
	for _, want := range []string{"roster check unavailable", "connection refused", "not a fleet-wide green"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	// And on the empty-population arm too, where the absence line matters most.
	empty := renderTurnReport(turnlog.Report{Dir: "/x/turnlog", MaxAge: "3h0m0s"},
		turnCoverage{Error: "connection refused"}, false)
	if !strings.Contains(empty, "roster check unavailable") {
		t.Errorf("empty population dropped the roster failure:\n%s", empty)
	}
}

// TestReadTurnCoverage reads through rosterFn — the same read `pogo agent
// list`'s footer uses — and keeps a failure rather than dropping it.
func TestReadTurnCoverage(t *testing.T) {
	withRoster(t, &agent.RosterReport{
		Configured: 10, Present: 6, Parked: 2,
		Absent: []agent.RosterMember{{Name: "doctor", State: agent.RosterAbsent, Class: agent.RosterOnDemand}},
		Members: []agent.RosterMember{
			{Name: "doctor", State: agent.RosterAbsent},
			{Name: "mayor", State: agent.RosterPresent},
			{Name: "pm-x", State: agent.RosterParked},
		},
	}, nil)
	cov := readTurnCoverage()
	if cov.Configured != 10 || len(cov.Absent) != 1 || cov.Absent[0].Name != "doctor" {
		t.Errorf("coverage = %+v", cov)
	}
	if len(cov.Parked) != 1 || cov.Parked[0] != "pm-x" {
		t.Errorf("parked = %v, want [pm-x]", cov.Parked)
	}
	if cov.complete() {
		t.Error("a roster with an absent member read as complete")
	}

	withRoster(t, nil, errors.New("connection refused"))
	if cov := readTurnCoverage(); cov.Error == "" || cov.complete() {
		t.Errorf("a failed roster read was dropped: %+v", cov)
	}
}

// TestTurnCensusJSONKeepsTheReportFlat: --json adds a `roster` object and must
// not move any field the report already had, so existing consumers of
// `check-turns --json` read the same keys.
func TestTurnCensusJSONKeepsTheReportFlat(t *testing.T) {
	raw, err := json.Marshal(turnCensus{Report: cleanTurnReport(), Roster: turnCoverage{
		Configured: 2, Absent: []agent.RosterMember{{Name: "doctor", State: agent.RosterAbsent}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"dir", "agents", "live", "findings", "roster"} {
		if _, ok := m[k]; !ok {
			t.Errorf("--json missing top-level %q: %s", k, raw)
		}
	}
	if !strings.Contains(string(m["roster"]), `"doctor"`) {
		t.Errorf("roster object does not name the absent agent: %s", m["roster"])
	}
}
