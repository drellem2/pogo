package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/server"
)

// The reporter's symptom was a command that returns "full mode" without saying
// which agents actually restarted. These tests assert the output NAMES things,
// because a message that merely stops lying — "restarted, possibly nothing" —
// would leave the same operator trap in place.

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestRestartLinesNameTheAgentsThatCameBack(t *testing.T) {
	out := joined(orchestrationRestartLines(server.StartReport{
		Mode:              "full",
		RefineryRestarted: true,
		AgentsStarted:     []string{"mayor", "pm-pogo"},
	}))

	for _, want := range []string{"mayor", "pm-pogo", "Refinery: restarted"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Polecats are NOT restored") {
		t.Errorf("output does not say polecats stay gone; their absence would read as the bug:\n%s", out)
	}
}

func TestRestartLinesSayWhyNothingStarted(t *testing.T) {
	cases := []struct {
		name   string
		report server.StartReport
		want   string
	}{
		{
			name:   "autostart disabled",
			report: server.StartReport{Mode: "full", AgentStartSkipped: "[agents] autostart = false"},
			want:   "[agents] autostart = false",
		},
		{
			name:   "no eligible prompts",
			report: server.StartReport{Mode: "full"},
			want:   "no crew prompt declares auto_start = true",
		},
		{
			name:   "already full",
			report: server.StartReport{Mode: "full", AlreadyFull: true},
			want:   "nothing was restarted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := joined(orchestrationRestartLines(tc.report))
			if !strings.Contains(out, tc.want) {
				t.Errorf("output does not explain the empty restart (%q):\n%s", tc.want, out)
			}
		})
	}
}

// A partial restore is not a success. The failing agent is named, with its
// error, on the line an operator reads.
func TestRestartLinesNameFailedAgents(t *testing.T) {
	out := joined(orchestrationRestartLines(server.StartReport{
		Mode:              "full",
		RefineryRestarted: true,
		AgentsStarted:     []string{"mayor"},
		AgentsFailed:      []server.AgentStartFailure{{Name: "gc", Error: "pty start: no ptys"}},
	}))

	if !strings.Contains(out, "gc") || !strings.Contains(out, "no ptys") {
		t.Errorf("output does not name the failed agent and its error:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("a failed spawn is not marked as a failure:\n%s", out)
	}
}

func TestRestartLinesNameParkedAndAlreadyRunning(t *testing.T) {
	out := joined(orchestrationRestartLines(server.StartReport{
		Mode:                 "full",
		RefineryRestarted:    true,
		AgentsAlreadyRunning: []string{"mayor"},
		AgentsParked:         []string{"sleepy"},
	}))

	if !strings.Contains(out, "already running") || !strings.Contains(out, "mayor") {
		t.Errorf("output does not name the agents that were already up:\n%s", out)
	}
	// A parked agent is down on purpose. Left unlabelled it reads as a
	// casualty of the restart.
	if !strings.Contains(out, "parked") || !strings.Contains(out, "sleepy") {
		t.Errorf("output does not name the parked agents:\n%s", out)
	}
}

func TestRestartSummaryCarriesTheCounts(t *testing.T) {
	got := orchestrationRestartSummary(server.StartReport{
		Mode:          "full",
		AgentsStarted: []string{"mayor", "pm-pogo"},
		AgentsFailed:  []server.AgentStartFailure{{Name: "gc", Error: "boom"}},
	})
	for _, want := range []string{"2 crew agents started", "1 failed", "polecats not restored"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not contain %q", got, want)
		}
	}
}
