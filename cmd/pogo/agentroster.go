package main

import (
	"fmt"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
)

// rosterFn is the roster read `pogo agent list` performs, indirected so a test
// can substitute a fixture instead of a live pogod. Production binds
// client.AgentRoster.
var rosterFn = client.AgentRoster

// printAbsentFooter names the configured crew agents that are NOT in the
// registry, under the `pogo agent list` output.
//
// The listing above it is a registry view, and a registry holds the agents pogod
// is running. So an agent that was stopped is not a row with a bad value in it —
// it is no row at all, and a reader cannot tell "this agent is down" from "this
// agent was never configured on this machine". mg-7d20: crew-doctor was stopped
// on 2026-08-10 as part of an auth-incident cleanup and stayed down 2 days 21
// hours, and `pogo agent list` did not show it at all, "not even as parked. An
// absent member cannot appear in a roster."
//
// This footer is deliberately the SMALLEST change that closes that: it adds no
// rows to the listing and nothing to --json, so the eight callers that consume
// the registry array — `pogo gc`, checkturns, checkorphans, checkstranded among
// them — keep the contract they already rely on, in which every element has a
// process behind it.
//
// IT IS BEST-EFFORT AND SAYS SO WHEN IT FAILS. A roster pogod could not compute
// prints a line naming the failure rather than nothing: silence here would read
// as "everybody is present", which is the exact reading this whole ticket exists
// to make impossible. What it must never do is fail the command — `pogo agent
// list` answering about the registry is still a correct answer to the question
// it was asked.
func printAbsentFooter() {
	rep, err := rosterFn()
	if err != nil {
		fmt.Printf("\n(roster check unavailable: %v — this listing shows the registry only,\n"+
			" so a configured agent that is not running would not appear above)\n", err)
		return
	}
	if rep == nil || len(rep.Absent) == 0 {
		return
	}
	fmt.Printf("\n%d configured crew agent(s) are NOT in this listing — not running, not parked:\n",
		len(rep.Absent))
	for _, m := range rep.Absent {
		fmt.Printf("  %-20s  %s\n", m.Name, absentNote(m))
	}
	fmt.Println("  (pogo agent roster — full view)")
}

// absentNote renders the one thing that decides whether an absence is a fault:
// what the agent's own frontmatter asked for.
func absentNote(m agent.RosterMember) string {
	switch m.Class {
	case agent.RosterSupervised:
		return "auto_start = true — should have started at boot"
	case agent.RosterOnDemand:
		return "auto_start = false — on-demand; nothing will bring it back"
	case agent.RosterUnclassifiable:
		return "prompt unreadable — cannot say what was wanted"
	default:
		return string(m.Class)
	}
}
