package agent

import (
	"sort"
	"strings"
)

// Who is working which item, for readers that must not mistake a worked item for
// a neglected one (mg-1a8a).
//
// THE DEFECT. pogod claims a work item at spawn (mg-7254), but that claim FAILS
// OPEN on a not-found or unreadable store: the polecat is dispatched anyway and
// the item stays in available/. From there every reader that infers ownership
// from item status is wrong in the same direction — stall-watch reports the item
// as neglected, priority-wake urges a coordinator to dispatch it, and a
// coordinator acting on that nag puts a SECOND polecat on work already in
// progress. Two branches touching the same files is the concurrent-edit shape
// that has cost this fleet repeated rebase conflicts.
//
// The claim field cannot carry the distinction, which is why the answer is a
// second source rather than a better claim: pogod already knows which polecats
// are alive and which item each was dispatched at, and that knowledge is
// independent of whether the claim stuck.

// InFlightWorkItem names the worker on a work item and the evidence for it.
type InFlightWorkItem struct {
	// Polecat is the worker's agent name.
	Polecat string
	// Evidence is how this pogod knows: InFlightFromRegistry when the worker is
	// in this process's live registry, InFlightFromWitness when it is known only
	// from the persisted witness (a polecat that outlived an earlier pogod).
	//
	// It is carried because the two are not equally strong. A registry entry is
	// this process's own bookkeeping; a witness entry is a pid/start-time match
	// that counts a polecat as live when its identity merely could not be
	// DISPROVED. A reader told "do not dispatch, a worker is on it" is entitled
	// to know which of those it is looking at.
	Evidence string
}

// Evidence values for InFlightWorkItem.
const (
	InFlightFromRegistry = "registry"
	InFlightFromWitness  = "witness"
)

// WorkItemsInFlight maps every work item a live polecat is on to that polecat.
//
// # It is a union, for the reason RepoOccupancyFor's count is
//
// The in-memory registry is authoritative while this pogod has run continuously
// and is EMPTY after a restart, permanently — it has no adopt path (mg-13a3).
// The persisted witness survives a restart. Reading the registry alone would
// report every survivor's item as unworked on every redeploy, which is exactly
// when survivors exist; reading the witness alone would miss a polecat spawned
// seconds ago whose witness write has not landed. The registry wins a
// disagreement because it is this process's own record of the dispatch.
//
// # The error is returned, not folded into an empty map
//
// A witness read error yields the registry-derived answer AND the error, so the
// caller can act on what is known while SAYING the picture may be incomplete.
// That split matters here: an incomplete map means some worked item still reads
// as neglected — the pre-fix behaviour, loud rather than silent — whereas
// discarding the registry half would suppress nothing and lose the half we had.
// No caller may render the error as "nothing is in flight".
func (r *Registry) WorkItemsInFlight() (map[string]InFlightWorkItem, error) {
	out := make(map[string]InFlightWorkItem)

	witnessed, err := WitnessedPolecatWorkItems()
	for name, id := range witnessed {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = InFlightWorkItem{Polecat: name, Evidence: InFlightFromWitness}
		}
	}
	// Registry last so it overwrites a witness entry for the same item: same
	// polecat in the ordinary case, and where they disagree the live registry is
	// the record of the dispatch actually in progress.
	for _, p := range r.Polecats() {
		if id := strings.TrimSpace(p.WorkItemID); id != "" {
			out[id] = InFlightWorkItem{Polecat: p.Name, Evidence: InFlightFromRegistry}
		}
	}
	return out, err
}

// InFlightWorkItemIDs returns the ids in m, sorted. A convenience for callers
// stamping the set into an event or a message, where a stable order is the
// difference between a countable record and one that reorders every tick.
func InFlightWorkItemIDs(m map[string]InFlightWorkItem) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
