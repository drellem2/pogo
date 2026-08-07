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
			want:   "no crew prompt declares auto_start = true",
		},
		{
			name:   "already full and unconfigured",
			report: server.StartReport{Mode: "full", AlreadyFull: true, AgentStartSkipped: "no config file; this daemon is not configured for orchestration"},
			want:   "no config file",
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

// TestRestartLinesNameTheCrewWhenAlreadyFull is the rendering half of mg-060c.
//
// The AlreadyFull branch used to return exactly one line — "nothing was
// stopped, so nothing was restarted" — and stop. That sentence is a true
// statement about the MODE and says nothing whatever about the crew, so the
// operator whose mayor had died read it as confirmation that all was well. The
// state it was printed in is the one the command exists to fix.
func TestRestartLinesNameTheCrewWhenAlreadyFull(t *testing.T) {
	out := joined(orchestrationRestartLines(server.StartReport{
		Mode:                 "full",
		AlreadyFull:          true,
		AgentsStarted:        []string{"mayor"},
		AgentsAlreadyRunning: []string{"pm-pogo"},
	}))

	if !strings.Contains(out, "mayor") {
		t.Errorf("an already-full start that recovered the mayor does not name it:\n%s", out)
	}
	if !strings.Contains(out, "pm-pogo") {
		t.Errorf("output does not name the crew that was already up:\n%s", out)
	}
	// The refinery was not restarted, and must not be reported as though the
	// daemon had no refinery starter — that reads as a misconfiguration.
	if strings.Contains(out, "NOT restarted (the daemon has no refinery starter") {
		t.Errorf("an already-full start blames a missing refinery starter:\n%s", out)
	}
	if !strings.Contains(out, "already full") {
		t.Errorf("output does not say the mode was left alone:\n%s", out)
	}
}

// The summary is the --json `message`, and it must carry the crew count in both
// branches. "already in full mode; nothing restarted" withheld the one number a
// caller needs to tell a recovered fleet from an untouched one.
func TestRestartSummaryCountsTheCrewWhenAlreadyFull(t *testing.T) {
	got := orchestrationRestartSummary(server.StartReport{
		Mode:          "full",
		AlreadyFull:   true,
		AgentsStarted: []string{"mayor"},
	})
	for _, want := range []string{"already in full mode", "1 crew agents started"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not contain %q", got, want)
		}
	}
}
