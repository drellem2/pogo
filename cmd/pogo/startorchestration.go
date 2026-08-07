package main

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/server"
)

// This file renders pogod's StartReport for `pogo server start` when that
// command finds the daemon in index-only mode and restarts orchestration.
//
// It exists because the old output was one unconditional line — "Orchestration
// restarted" — printed with a zero exit whether or not a single agent came
// back. gh #108 is exactly that: the transition restored nothing, and the CLI
// could not tell the operator, because it was never told either. A green
// return is what the defect already produces, so naming the fleet is not
// cosmetic; it is the part of the fix an operator can actually see.

// orchestrationRestartLines renders the human-readable detail lines for a
// restart, one per fact. Every line names something: which agents came back,
// which were already up, which are parked, which failed, and what is
// deliberately not restored.
func orchestrationRestartLines(r server.StartReport) []string {
	if r.AlreadyFull {
		return []string{"Already in full mode — nothing was stopped, so nothing was restarted."}
	}

	var lines []string
	if r.RefineryRestarted {
		lines = append(lines, "Refinery: restarted.")
	} else {
		lines = append(lines, "Refinery: NOT restarted (the daemon has no refinery starter configured).")
	}

	if len(r.AgentsStarted) > 0 {
		lines = append(lines, fmt.Sprintf("Crew agents started (%d): %s",
			len(r.AgentsStarted), strings.Join(r.AgentsStarted, ", ")))
	}
	if len(r.AgentsAlreadyRunning) > 0 {
		lines = append(lines, fmt.Sprintf("Crew agents already running (%d): %s",
			len(r.AgentsAlreadyRunning), strings.Join(r.AgentsAlreadyRunning, ", ")))
	}
	if len(r.AgentsParked) > 0 {
		lines = append(lines, fmt.Sprintf("Crew agents left parked (%d): %s — down on purpose; wake with 'pogo agent wake <name>'.",
			len(r.AgentsParked), strings.Join(r.AgentsParked, ", ")))
	}
	for _, f := range r.AgentsFailed {
		lines = append(lines, fmt.Sprintf("Crew agent FAILED to start: %s — %s", f.Name, f.Error))
	}

	// The "nothing came back" cases, each said out loud with its reason. This
	// is the branch the reporter hit; silence here is the whole bug.
	if len(r.AgentsStarted) == 0 && len(r.AgentsAlreadyRunning) == 0 && len(r.AgentsFailed) == 0 {
		switch {
		case r.AgentStartSkipped != "":
			lines = append(lines, "No crew agents started: "+r.AgentStartSkipped)
		default:
			lines = append(lines, "No crew agents started — no crew prompt declares auto_start = true.")
		}
	} else if r.AgentStartSkipped != "" {
		lines = append(lines, "Crew auto-start sweep did not run: "+r.AgentStartSkipped)
	}

	// Always stated, including when the sweep restored a full crew. Polecats
	// are ephemeral and correctly stay gone; an operator who is not told that
	// reads their absence as more of the same defect.
	lines = append(lines, "Polecats are NOT restored — they are ephemeral and are re-dispatched per work item.")
	return lines
}

// orchestrationRestartSummary is the one-line form used as the `message` field
// in --json output, where the structured report sits beside it.
func orchestrationRestartSummary(r server.StartReport) string {
	if r.AlreadyFull {
		return "already in full mode; nothing restarted"
	}
	parts := []string{fmt.Sprintf("orchestration restarted; %d crew agents started", len(r.AgentsStarted))}
	if len(r.AgentsFailed) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed to start", len(r.AgentsFailed)))
	}
	if r.AgentStartSkipped != "" {
		parts = append(parts, "auto-start sweep skipped: "+r.AgentStartSkipped)
	}
	parts = append(parts, "polecats not restored")
	return strings.Join(parts, "; ")
}
