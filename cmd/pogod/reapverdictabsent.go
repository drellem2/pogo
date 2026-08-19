package main

import (
	"context"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refinery"
)

// EventClosedWithoutVerdict is the event_type recorded when the auto-done path
// closes a work item whose merge request carried no author verdict (mg-c456).
//
// It exists because THE MOMENT OF LOSS WAS THE ONE MOMENT NOTHING WATCHED.
// doctor's census filed with mg-c456 — `pogo check-verdicts`, unfiltered over
// every filer on 2026-08-19 — reported 385 ROUTING + 1014 LOST rows in the live
// store, and doctor's own per-landing-date table put the accrual at roughly 10-80
// rows per working day, going to exactly zero across the 118h fleet outage of
// 2026-08-15..18 and resuming when throughput resumed. Those figures are
// doctor's, not re-derived here; what is re-derived on this branch is the
// sidecar-predicate measurement quoted in reap.go. The outage is what makes the
// gap a product of NORMAL operation rather than one incident's backlog.
// `check-verdicts` finds those rows afterwards by reconstruction and is
// deliberately report-only. Nothing counted them as they were made.
//
// AND THE REASON IS NOT A MISSING COUNTER, which is why this is an event at the
// close and not a metric bolted to an existing path. At the instant a verdict is
// lost the system's own answer is SUCCESS: the branch merged, the item closed,
// and the worker's later `mg done --result` was refused only because the item is
// already terminal — a refusal the protocol scores as normal completion. Every
// signal on that path reads healthy, so a wider or better-aimed version of the
// same check reports more success rather than the loss. What was needed was not a
// better reading of that path but an observation made OFF it, at the instant the
// close lands.
const EventClosedWithoutVerdict = "work_item_closed_without_verdict"

// recordCloseWithoutVerdict writes the durable record of one such close.
//
// It goes to events.log rather than only to log.Printf because pogod logs to
// INHERITED STDERR: a `log.Printf` in this file is not reliably a line anybody
// can go back and read, which is the same reason mg-2b71's completion notice is
// an event. A remedy for an unobserved loss that is itself only observable in a
// stream that may not exist tomorrow would be the defect wearing the fix's
// clothes.
//
// COVERAGE, stated because an unstated limit is how this lineage keeps getting
// re-measured wrong: this records closes THIS WRITER performed. A polecat that
// wins the race and writes a verdict-free result of its own is a real loss and is
// NOT counted here — the reap does not read the store back and cannot see it, and
// `pogo check-verdicts` remains the instrument for that population.
func recordCloseWithoutVerdict(mr *refinery.MergeRequest, a *agent.Agent, polecat bool) {
	worker := ""
	if a != nil {
		worker = a.Name
	}
	details := map[string]any{
		// The two facts this record asserts, and not a third. `worker_live_at_close`
		// separates "a worker was about to be stopped with its window shut" from a
		// hand-submitted branch whose submitter had no verdict either — different
		// findings, and folding them together is how a scope becomes wrong.
		"route":                "merge",
		"worker_live_at_close": polecat,
		"branch":               mr.Branch,
		"mr":                   mr.ID,
		"target":               mr.TargetRef,
	}
	if worker != "" {
		details["worker"] = worker
	}
	if mr.MergedSHA != "" {
		details["merged_sha"] = mr.MergedSHA
	}
	events.Emit(context.Background(), events.Event{
		EventType:  EventClosedWithoutVerdict,
		Agent:      "pogod",
		WorkItemID: mr.Author,
		Details:    details,
	})
}
