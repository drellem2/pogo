package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/heartwatch"
)

// The probe runs in `go test ./...` so every merge exercises the failing arm.
// A liveness check that has never been observed firing is a presence check
// until proven otherwise — and this one spent its whole life in that state, was
// finally measured, and had not fired for two agents across fourteen days
// (mg-d616).
func TestCheckHeartbeatsProbePasses(t *testing.T) {
	if code := heartbeatProbeVerdict(false); code != 0 {
		t.Fatalf("heartbeatProbeVerdict = %d, want 0 — the positive control for check-heartbeats "+
			"failed, so a green census from this command means nothing", code)
	}
}

// An empty population must SAY it examined nobody. Zero examined produces zero
// findings, which is the same green as a healthy fleet.
func TestRenderHeartbeatReportSaysZeroExaminedOutLoud(t *testing.T) {
	out := renderHeartbeatReport(heartwatch.Report{
		Root:         "/fixture/agents",
		StallAfter:   heartwatch.DefaultStallAfter.String(),
		RestartAfter: heartwatch.DefaultRestartAfter.String(),
	}, false)

	if !strings.Contains(out, "population 0 agent(s)") {
		t.Errorf("output does not print the population count:\n%s", out)
	}
	if !strings.Contains(out, "NOT a clean fleet") {
		t.Errorf("an empty population did not say it was not a clean fleet:\n%s", out)
	}
	if strings.Contains(out, "Every present crew agent has a fresh heartbeat") {
		t.Errorf("an empty population reported the fleet healthy:\n%s", out)
	}
}

// A `missing` row must be rendered as a finding with its caveat, not as an
// omission.
func TestRenderHeartbeatReportExplainsMissing(t *testing.T) {
	out := renderHeartbeatReport(heartwatch.Report{
		Root:         "/fixture/agents",
		StallAfter:   heartwatch.DefaultStallAfter.String(),
		RestartAfter: heartwatch.DefaultRestartAfter.String(),
		Examined:     1,
		Missing:      1,
		Findings:     1,
		Agents: []heartwatch.State{{
			Agent: "pa", Verdict: heartwatch.VerdictMissing,
			Detail: "present, and publishes no sweep.log under any known path",
		}},
	}, false)

	if !strings.Contains(out, "1 CREW HEARTBEAT(S) ARE NOT FRESH") {
		t.Errorf("missing row was not counted as a finding:\n%s", out)
	}
	if !strings.Contains(out, "not a healthy reading") {
		t.Errorf("output does not say a missing heartbeat is not health:\n%s", out)
	}
}

// A clean census still prints the population, and says nothing about action.
func TestRenderHeartbeatReportCleanCensus(t *testing.T) {
	now := time.Now()
	out := renderHeartbeatReport(heartwatch.Report{
		Root:         "/fixture/agents",
		StallAfter:   heartwatch.DefaultStallAfter.String(),
		RestartAfter: heartwatch.DefaultRestartAfter.String(),
		Examined:     2,
		Fresh:        2,
		Agents: []heartwatch.State{
			{Agent: "mayor", Verdict: heartwatch.VerdictFresh, Last: now.Add(-time.Minute), AgeSecs: 60},
			{Agent: "pm-pogo", Verdict: heartwatch.VerdictFresh, Last: now.Add(-2 * time.Minute), AgeSecs: 120},
		},
	}, false)

	if !strings.Contains(out, "population 2 agent(s)") {
		t.Errorf("output does not print the population count:\n%s", out)
	}
	if !strings.Contains(out, "Every present crew agent has a fresh heartbeat") {
		t.Errorf("clean census did not report clean:\n%s", out)
	}
}

// Wake suppression is reported by the CLI rather than applied by it: this
// command is the one a human runs when they want the reading regardless.
func TestRenderHeartbeatReportNotesWakeSuppressionWithoutApplyingIt(t *testing.T) {
	now := time.Now().UTC()
	out := renderHeartbeatReport(heartwatch.Report{
		Root:           "/fixture/agents",
		StallAfter:     heartwatch.DefaultStallAfter.String(),
		RestartAfter:   heartwatch.DefaultRestartAfter.String(),
		Examined:       1,
		RestartDue:     1,
		Findings:       1,
		WakeSuppressed: true,
		WokeAt:         now.Add(-3 * time.Minute),
		Agents: []heartwatch.State{{
			Agent: "pm-riemann", Verdict: heartwatch.VerdictRestartDue,
			Last: now.Add(-5 * time.Hour), AgeSecs: (5 * time.Hour).Seconds(),
		}},
	}, false)

	if !strings.Contains(out, "system_wake") {
		t.Errorf("output does not mention the wake:\n%s", out)
	}
	if !strings.Contains(out, "reading above stands") {
		t.Errorf("output implies the wake erased the reading:\n%s", out)
	}
	if !strings.Contains(out, "1 CREW HEARTBEAT(S) ARE NOT FRESH") {
		t.Errorf("wake suppression hid the finding from the CLI:\n%s", out)
	}
}
