package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/hookarm"
)

func running(name, state string) agent.AgentInfo {
	return agent.AgentInfo{Name: name, Status: agent.StatusRunning, MailWarn: state}
}

// TestZeroArmedIsLoud is the reading that was true on the day mg-d924 merged:
// the fix was in the tree, in main, and in force for nobody. The summary has to
// make that as unmissable as a warning, because the thing it is competing with
// is a fleet listing that already looks completely healthy.
func TestZeroArmedIsLoud(t *testing.T) {
	var buf bytes.Buffer
	printMailWarnSummary(&buf, []agent.AgentInfo{
		running("mayor", string(hookarm.StateOff)),
		running("architect", string(hookarm.StateOff)),
		running("pa", string(hookarm.StateOff)),
	})
	out := buf.String()
	if !strings.Contains(out, "0 of 3 running agent(s) ARMED") {
		t.Fatalf("the count is not stated plainly:\n%s", out)
	}
	if !strings.Contains(out, "architect") || !strings.Contains(out, "mayor") {
		t.Errorf("the unarmed agents are not named:\n%s", out)
	}
	if !strings.Contains(out, "RESTARTS") {
		t.Errorf("the output does not say what makes an unarmed agent armed:\n%s", out)
	}
}

// TestAnOldDaemonReportsUnavailableNotClear is this remedy checked against the
// defect it remedies. The report is served BY pogod, so a pogod too old to
// carry the field returns nothing for it — and nothing, rendered as a blank
// column and a missing summary, reads as "all fine". That is the same
// substitution of silence for an answer that the ticket is about, committed one
// level up by the instrument built to catch it.
func TestAnOldDaemonReportsUnavailableNotClear(t *testing.T) {
	var buf bytes.Buffer
	printMailWarnSummary(&buf, []agent.AgentInfo{
		running("mayor", ""),
		running("architect", ""),
	})
	out := buf.String()
	if !strings.Contains(out, "UNAVAILABLE") {
		t.Fatalf("a daemon that reports no state must not render as clear:\n%s", out)
	}
	if strings.Contains(out, "ARMED") {
		t.Errorf("an unmeasured fleet must not be counted as armed:\n%s", out)
	}
	if !strings.Contains(out, "predates mg-503d") {
		t.Errorf("the output does not say why the answer is missing:\n%s", out)
	}
}

func TestMixedStatesAreCountedAndExplained(t *testing.T) {
	var buf bytes.Buffer
	printMailWarnSummary(&buf, []agent.AgentInfo{
		running("p1", string(hookarm.StateArmed)),
		running("p2", string(hookarm.StateArmed)),
		running("mayor", string(hookarm.StatePending)),
		running("pa", string(hookarm.StateUnknown)),
	})
	out := buf.String()
	if !strings.Contains(out, "2 of 4 running agent(s) ARMED") {
		t.Fatalf("wrong count:\n%s", out)
	}
	for _, want := range []string{"pending", "unknown", "mayor", "pa"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// Armed agents are counted, not listed: the list is a worklist, and a
	// worklist that includes the agents needing nothing is one nobody reads.
	if strings.Contains(out, "p1, p2") {
		t.Errorf("armed agents should not be enumerated:\n%s", out)
	}
}

// TestParkedAndExitedAgentsAreNotInTheDenominator: neither has a session for a
// hook to be loaded into, so counting them would move the fraction without
// changing who is protected.
func TestParkedAndExitedAgentsAreNotInTheDenominator(t *testing.T) {
	var buf bytes.Buffer
	printMailWarnSummary(&buf, []agent.AgentInfo{
		running("p1", string(hookarm.StateArmed)),
		{Name: "pm-lineara", Status: agent.StatusParked},
		{Name: "old", Status: agent.StatusExited},
	})
	if out := buf.String(); !strings.Contains(out, "1 of 1 running agent(s) ARMED") {
		t.Fatalf("parked/exited agents leaked into the count:\n%s", out)
	}
}

// TestTheRowNeverRendersAnUnmeasuredAgentAsBlank: a blank cell reads as
// "nothing to report". What an empty state actually means is that nothing
// measured, and the two must not look the same in a listing people skim.
func TestTheRowNeverRendersAnUnmeasuredAgentAsBlank(t *testing.T) {
	if got := mailWarnCell(running("mayor", "")); got != "  mail-warn=?" {
		t.Fatalf("mailWarnCell for an unreported agent = %q, want a visible ?", got)
	}
	if got := mailWarnCell(running("mayor", string(hookarm.StateArmed))); got != "  mail-warn=armed" {
		t.Fatalf("mailWarnCell = %q", got)
	}
	if got := mailWarnCell(agent.AgentInfo{Name: "pm-lineara", Status: agent.StatusParked}); got != "" {
		t.Fatalf("a parked agent should carry no cell, got %q", got)
	}
}

// TestNoRunningAgentsPrintsNothing keeps the hook's own rule: a report that
// speaks when there is nothing to say is a report that gets skimmed.
func TestNoRunningAgentsPrintsNothing(t *testing.T) {
	var buf bytes.Buffer
	printMailWarnSummary(&buf, []agent.AgentInfo{{Name: "pm-lineara", Status: agent.StatusParked}})
	if buf.Len() != 0 {
		t.Fatalf("printed something with no running agents:\n%s", buf.String())
	}
}
