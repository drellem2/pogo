package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
)

// gcListAgentsFn is the registry read `pogo gc` performs, indirected so a test
// can drive gcLivePolecats without a live pogod. Production always uses
// client.ListAgents.
var gcListAgentsFn = client.ListAgents

// gcLivePolecats builds the do-not-touch set for `pogo gc` and the lines the
// command should print about how it was built. It returns an error only when the
// polecat witness is on disk but unreadable — see the caller, which declines to
// sweep on it.
//
// THE DEFECT THIS EXISTS TO CLOSE (mg-1403). This set used to come from
// client.ListAgents alone, i.e. from pogod's in-memory registry — which is EMPTY
// after a pogod restart, permanently, because the registry has no adopt/reattach
// path. That is mg-0130's exact shape, one caller over: `pogo gc --apply` against
// a restart-surviving, done-but-still-running polecat would sweep the worktree
// out from under it, because worktree removal is gated on the live set alone with
// no merge gate behind it.
//
// It was also SILENT, which is the part worth naming. The command warned when
// pogod was UNREACHABLE — but a restarted pogod is perfectly reachable and
// answers cheerfully with a registry that has forgotten everyone, and that is the
// case that matters. So the fix is the witness union (agent.LivePolecatSet, the
// same answer pogod's own sweep uses), and the witness-only survivors are NAMED
// in the output rather than silently folded in: an operator who runs --apply
// right after a restart should be able to see that the guard was doing something.
func gcLivePolecats() (map[string]bool, []string, error) {
	var notes []string

	// The registry half. Best-effort: an unreachable pogod is not fatal, because
	// the witness below answers for every polecat pogod ever spawned on this box,
	// and ticket status plus git's checked-out-branch protection still guard the
	// rest.
	var registry []string
	if agents, err := gcListAgentsFn(); err == nil {
		for _, a := range agents {
			if a.Type == agent.TypePolecat {
				registry = append(registry, a.Name)
			}
		}
	} else {
		notes = append(notes, fmt.Sprintf(
			"warning: could not reach pogod for the live-polecat list (%v);\n"+
				"         relying on the polecat witness, ticket status and git checkout state.", err))
	}

	live, err := agent.LivePolecatSet(registry)
	if err != nil {
		return nil, notes, err
	}

	inRegistry := make(map[string]bool, len(registry))
	for _, n := range registry {
		inRegistry[n] = true
	}
	var witnessOnly []string
	for n := range live {
		if !inRegistry[n] {
			witnessOnly = append(witnessOnly, n)
		}
	}
	sort.Strings(witnessOnly)
	if len(witnessOnly) > 0 {
		notes = append(notes, fmt.Sprintf(
			"note: %d live polecat(s) protected by the persisted witness alone, not by pogod's\n"+
				"      registry (pogod has restarted since they spawned): %s",
			len(witnessOnly), strings.Join(witnessOnly, ", ")))
	}
	return live, notes, nil
}
