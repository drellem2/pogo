package main

import (
	"github.com/drellem2/pogo/internal/agent"
)

// agentListWorkCells renders the trailing repo= and work-item= cells of a
// `pogo agent list` row (drellem2/pogo#128).
//
// They are here because the per-repo dispatch cap counts workers BY REPO and
// leaves out a worker whose item is already done, and before this an operator
// could see neither fact: `pogo agent list` named no repo and no item status, so
// "slot held by work" and "slot held by a finished agent" read identically.
//
// The status sits in parentheses on the work item it describes. It is omitted
// when pogod did not report one — an unreadable store, or a pogod older than
// the field — because an empty status is "unknown", and printing it as
// anything would claim a state nobody read.
func agentListWorkCells(a agent.AgentInfo) string {
	out := ""
	if a.SourceRepo != "" {
		out += "  repo=" + a.SourceRepo
	}
	if a.WorkItemID != "" {
		out += "  work-item=" + a.WorkItemID
		if a.WorkItemStatus != "" {
			out += "(" + a.WorkItemStatus + ")"
		}
	}
	return out
}
