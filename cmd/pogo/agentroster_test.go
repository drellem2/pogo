package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// captureStdout lives in checkstaleness_nofire_test.go. The footer's whole job
// is what a person sees, so these tests read the same bytes they would.

func withRoster(t *testing.T, rep *agent.RosterReport, err error) {
	t.Helper()
	prev := rosterFn
	rosterFn = func() (*agent.RosterReport, error) { return rep, err }
	t.Cleanup(func() { rosterFn = prev })
}

// TestAbsentFooter_NamesTheAgentThatIsNotInTheListing is mg-7d20's user-visible
// half: `pogo agent list` could not show a stopped crew agent at all, not even
// as parked, so a reader had no way to tell "doctor is down" from "doctor was
// never configured here".
func TestAbsentFooter_NamesTheAgentThatIsNotInTheListing(t *testing.T) {
	withRoster(t, &agent.RosterReport{
		Configured: 11, Present: 9, Parked: 1,
		Absent: []agent.RosterMember{{
			Name: "doctor", Identity: "crew-doctor", State: agent.RosterAbsent,
			Class: agent.RosterOnDemand,
		}},
	}, nil)

	out := captureStdout(t, printAbsentFooter)
	for _, want := range []string{"doctor", "auto_start = false", "pogo agent roster", "not running, not parked"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q, got:\n%s", want, out)
		}
	}
}

// TestAbsentFooter_SupervisedAbsenceReadsAsAFault: the class is the difference
// between "nobody has asked for it" and "pogod should have started this".
func TestAbsentFooter_SupervisedAbsenceReadsAsAFault(t *testing.T) {
	withRoster(t, &agent.RosterReport{
		Configured: 11, Present: 10,
		Absent: []agent.RosterMember{{
			Name: "pm-pogo", State: agent.RosterAbsent, Class: agent.RosterSupervised,
		}},
	}, nil)

	out := captureStdout(t, printAbsentFooter)
	if !strings.Contains(out, "should have started at boot") {
		t.Errorf("a supervised absence must read as a fault, got:\n%s", out)
	}
}

// TestAbsentFooter_CompleteRosterPrintsNothing: the footer must not add noise to
// the healthy case, or it becomes a line people learn to skip.
func TestAbsentFooter_CompleteRosterPrintsNothing(t *testing.T) {
	withRoster(t, &agent.RosterReport{Configured: 11, Present: 10, Parked: 1}, nil)

	if out := captureStdout(t, printAbsentFooter); out != "" {
		t.Errorf("a complete roster must print nothing, got:\n%s", out)
	}
}

// TestAbsentFooter_FailureIsSaidOutLoud. Silence on a failed roster read would
// be indistinguishable from "everybody is present" — the exact reading this
// ticket exists to make impossible.
func TestAbsentFooter_FailureIsSaidOutLoud(t *testing.T) {
	withRoster(t, nil, errors.New("connection refused"))

	out := captureStdout(t, printAbsentFooter)
	if !strings.Contains(out, "roster check unavailable") || !strings.Contains(out, "connection refused") {
		t.Errorf("a failed roster read must say so, got:\n%s", out)
	}
	if !strings.Contains(out, "registry only") {
		t.Errorf("the failure must say what the listing above it does and does not cover, got:\n%s", out)
	}
}
