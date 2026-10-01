package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// TestAgentListWorkCells pins the repo= and work-item=<id>(<status>) cells that
// let an operator tell a slot held by work from one held by a finished worker
// (drellem2/pogo#128).
func TestAgentListWorkCells(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   agent.AgentInfo
		want string
	}{
		{"crew agent: no repo, no item", agent.AgentInfo{}, ""},
		{
			"finished polecat shows repo and done",
			agent.AgentInfo{SourceRepo: "/dev/pogo", WorkItemID: "mg-bdd6", WorkItemStatus: "done"},
			"  repo=/dev/pogo  work-item=mg-bdd6(done)",
		},
		{
			"working polecat shows claimed",
			agent.AgentInfo{SourceRepo: "/dev/pogo", WorkItemID: "mg-9f4fb", WorkItemStatus: "claimed"},
			"  repo=/dev/pogo  work-item=mg-9f4fb(claimed)",
		},
		{
			// Unknown is not "not done": no parentheses rather than a guess.
			"unreadable status prints the id alone",
			agent.AgentInfo{SourceRepo: "/dev/pogo", WorkItemID: "mg-x"},
			"  repo=/dev/pogo  work-item=mg-x",
		},
		{
			"no-worktree polecat has no repo cell",
			agent.AgentInfo{WorkItemID: "mg-y", WorkItemStatus: "claimed"},
			"  work-item=mg-y(claimed)",
		},
	} {
		if got := agentListWorkCells(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
