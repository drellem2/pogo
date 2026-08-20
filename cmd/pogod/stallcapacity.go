package main

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// newStallCapacity gives the stall watcher a reader for the per-repo worker cap
// (mg-dd77).
//
// It closes over the SAME Registry method the spawn point refuses on
// (RepoOccupancyFor), rather than recounting workers, for the reason the
// /agents/hostload endpoint attaches the same struct: an advisory count that could
// drift from the enforced one lets a reader plan against a host pogod sees
// differently. Here the reader is a coordinator being TOLD what to do, so the
// drift would show up as advice that is refused a second later.
//
// # When occupancy is UNKNOWN rather than merely uncertain
//
// The cap's own failure direction is to fail open — an unreadable witness lets
// the dispatch proceed — and the notice follows it: an uncertain count still
// reads as dispatchable, with the uncertainty carried alongside. But there is
// one state where "fails open" and "we know there is room" come apart. The
// in-memory registry is EMPTY after a restart, permanently (it has no adopt
// path, mg-13a3), so a witness error with an empty registry is not a low count
// — it is no information at all, and reporting it as free slots would be the
// same defect this fix is repairing, one layer down. That case answers
// known=false and the notice says so instead of naming a remedy.
func newStallCapacity(reg *agent.Registry) stallwatch.Capacity {
	return stallwatch.CapacityFunc(func(repo string) (stallwatch.RepoCapacity, bool) {
		return stallCapacityFrom(reg.RepoOccupancyFor(repo))
	})
}

// stallCapacityFrom is the translation, split out from the closure so the rule
// above — which of the cap's failure states is "uncertain" and which is
// "unknown" — is testable without a live fleet in the repo.
//
// AtCap is copied from WouldRefuse rather than recomputed from Count >= Cap.
// The two agree today, and the copy is what keeps them agreeing: a later
// refinement to the cap (a reserve, a grace slot) changes WouldRefuse, and a
// notice that had reimplemented the comparison would go on describing the old
// rule in perfectly confident prose.
func stallCapacityFrom(occ agent.RepoOccupancy) (stallwatch.RepoCapacity, bool) {
	c := stallwatch.RepoCapacity{
		Repo:       occ.Repo,
		Count:      occ.Count,
		Cap:        occ.Cap,
		Polecats:   occ.Polecats,
		AtCap:      occ.WouldRefuse,
		Unresolved: occ.Unresolvable,
	}
	// An unresolvable repo is the mg-cd4a case: the item names a repository
	// this host cannot identify, so no count was TAKEN. Reporting Count 0 as
	// free slots is the defect — the repository behind the name may be at its
	// cap, and it was, the night this was measured.
	if occ.Unresolvable != "" {
		return c, false
	}
	if occ.WitnessErr != "" && occ.Count == 0 {
		return c, false
	}
	var notes []string
	if occ.WitnessErr != "" {
		notes = append(notes, "the persisted polecat witness could not be read ("+occ.WitnessErr+
			"), so survivors of an earlier pogod are missing from the count")
	}
	if n := len(occ.Unattributed); n > 0 {
		notes = append(notes, fmt.Sprintf("%d live worker(s) could not be attributed to any repo: %s",
			n, strings.Join(occ.Unattributed, ", ")))
	}
	c.Uncertain = strings.Join(notes, "; ")
	return c, true
}
