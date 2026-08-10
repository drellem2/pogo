package main

import (
	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// newStallWorkers lets the stall watcher ask which work items already have a
// live worker on them, so an item whose claim failed open at spawn is not
// reported as neglected while a polecat works it (mg-1a8a).
//
// It closes over the registry's own union of the in-memory agent list and the
// persisted polecat witness (WorkItemsInFlight), rather than counting workers
// here, for the reason newStallCapacity closes over RepoOccupancyFor: a second
// implementation of "who is alive" would drift from the first, and the drift
// would show up as advice about a fleet pogod sees differently.
//
// # known=false is reserved for no information at all
//
// A witness read error is UNCERTAINTY, not ignorance: the registry half still
// answers, and its answer is the one that covers every polecat this pogod
// spawned. Reporting the whole probe as unknown there would discard live
// evidence and put the two dispatch notices back to guessing from item status —
// this defect, one layer down. The unknown case is the one where nothing could
// be established, which for this probe is a registry that answered nothing AND a
// witness that could not be read.
func newStallWorkers(reg *agent.Registry) stallwatch.Workers {
	return stallwatch.WorkersFunc(func() (stallwatch.WorkInFlight, bool) {
		items, err := reg.WorkItemsInFlight()
		return stallWorkersFrom(items, err, reg.Polecats())
	})
}

// stallWorkersFrom is the translation, split out from the closure so the rule
// above — which failure state is "uncertain" and which is "unknown" — is
// testable without a live fleet.
//
// pids come from the registry rather than the witness because the notice uses
// them for `mg claim --pid`, and a witness pid is a pid we could not disprove;
// naming it in a repair command would invite restamping a claim onto a process
// that is not the worker. An unknown pid renders as a placeholder, which is
// honest and still actionable.
func stallWorkersFrom(items map[string]agent.InFlightWorkItem, err error, live []agent.PolecatInfo) (stallwatch.WorkInFlight, bool) {
	if err != nil && len(items) == 0 {
		// Nothing from either source: the witness is unreadable and this pogod's
		// registry knows of no worker. That is no information, not "idle".
		return stallwatch.WorkInFlight{}, false
	}

	pids := make(map[string]int, len(live))
	for _, p := range live {
		pids[p.Name] = p.PID
	}

	flight := stallwatch.WorkInFlight{Items: make(map[string]stallwatch.InFlightWorker, len(items))}
	for id, w := range items {
		flight.Items[id] = stallwatch.InFlightWorker{
			Name:     w.Polecat,
			PID:      pids[w.Polecat],
			Evidence: w.Evidence,
		}
	}
	if err != nil {
		flight.Uncertain = "the persisted polecat witness could not be read (" + err.Error() +
			"), so polecats that outlived an earlier pogod are missing from this attribution"
	}
	return flight, true
}
