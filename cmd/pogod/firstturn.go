package main

// This file holds the small pieces of the first-completed-turn floor's wiring
// that would otherwise bulk out main.go. The detector itself is
// internal/firstturn; the construction and the heartbeat hook are in main.go
// beside its sibling watchers.

// firstTurnUnarmedReason names WHICH dependency was missing when the floor could
// not be armed.
//
// A single "NOT armed" line covering two independent causes is the shape mg-7a20
// found in the activation audit: a statement whose denominator is unstated reads
// as more coverage than it had. The two causes have different remedies — a
// registry that did not load is an agent-side failure, a scheduler that did not
// load takes the completion evidence with it — and an operator reading this line
// during an outage should not have to guess which.
func firstTurnUnarmedReason(haveRegistry, haveScheduler bool) string {
	switch {
	case !haveRegistry && !haveScheduler:
		return "neither the agent registry nor the scheduler loaded, so there is no population AND no completion evidence"
	case !haveRegistry:
		return "the agent registry did not load, so there is no population to judge"
	default:
		return "the scheduler did not load, so there is no fire-completion evidence to judge against"
	}
}
