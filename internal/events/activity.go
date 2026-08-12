package events

// This file answers one question for every liveness detector on the box:
// does a log line recorded UNDER an agent's identity prove that the AGENT did
// something?
//
// Most of the time it does, and that is why the index every detector builds
// keys on Event.Agent. But the log is written by several processes, and pogod
// records some of its own interventions under the identity of the agent it
// intervened ON rather than under `pogod`. For those lines the answer is no —
// and worse than no, because pogod only performs them when something is WRONG
// with that agent. Counting them as recency inverts the instrument: the act
// that proves an agent is stuck is also the act that makes it look freshly
// alive.
//
// drellem2/pogo#138 is that inversion observed in production. `modal_dismissed`
// is emitted under the dismissed agent's identity (internal/claude's
// fireDismissal), wedgewatch's stall clock reads the newest line per identity,
// and the dismissal cooldown (5m) regenerates that line at twice the rate
// wedgewatch's marker hold-down (10m) needs to survive — so a modal the
// dismissal cannot clear suppresses the finding about it indefinitely.

// notAgentActivity is the EXCLUSION set: event types that carry an agent's
// identity but are not evidence the agent is alive.
//
// # Why an exclusion list and not an allow list
//
// An allow list fails CLOSED on every event type nobody remembered to add,
// including ones invented after this file was written. For a liveness index
// that means silently ageing an agent that is fine — a detector inventing a
// wedge out of its own incompleteness, which is the same class of bug one level
// up. The exclusion list fails OPEN: a new event type counts as activity, which
// is exactly the behaviour every one of these indexes had before this file
// existed. The cost of that default is one more inversion going unnoticed until
// somebody files it; the cost of the other default is a false wedge report for
// every event type ever added. This package prices the second higher.
//
// # Membership rule
//
// An event type belongs here when it is recorded under an agent's identity AND
// it fires BECAUSE that agent is stuck, failing, or unresponsive. That is the
// inversion: the reading gets fresher precisely when the subject is worse off.
//
// It does NOT belong here merely because pogod wrote it. `agent_spawned`,
// `agent_restarted`, `agent_stopped` and `agent_crashed` are pogod-authored and
// stay IN the index: they are genuine lifecycle transitions of the agent, and
// on this box a live agent's newest line is very often its own `agent_spawned`.
// Dropping those would leave most identities with no index entry at all, which
// reports them as unjudgeable rather than as fresh — a different failure, not a
// fix.
//
// Nor does it need to be here to be excluded from an index: pogod's nudge and
// scheduler traffic (`nudge_sent`, `auto_renudge`, `scheduler_fire_delivered`,
// …) is already attributed to `pogod` at the emit site, with the addressee in
// Details. That is the better fix where it is available, and this list is for
// the cases where the identity on the record genuinely is the agent's.
var notAgentActivity = map[string]bool{
	// pogod dismissed a modal that was eating this agent's input. It fires only
	// when the agent is behind a modal it did not clear itself — the
	// drellem2/pogo#138 case. The two aliases below are emitted alongside it by
	// fireDismissal for subscribers that grep the historical names, so all
	// three refresh the same key and all three are excluded.
	"modal_dismissed":            true,
	"rating_dialog_dismissed":    true,
	"rate_limit_modal_dismissed": true,

	// internal/synthwatch records this under the FAILING agent's identity when
	// its turns are failing synthetically (auth, rate limit, transport). It is
	// the same inversion with a different author, and on this host it is the
	// most frequent crew event there is — so leaving it in would keep the
	// defect live under the trigger that fires most often.
	"synthetic_failure_detected": true,
}

// CountsAsAgentActivity reports whether an event of this type, recorded under
// an agent's identity, is evidence that the agent itself is alive and working.
//
// It is the shared predicate for every last-seen index built over the event
// log — internal/wedgewatch's stall clock and internal/claude's events-stale
// gate today. Having one predicate is the point: those two indexes carried the
// identical no-exclusion rule, and fixing one while leaving the other would
// have left drellem2/pogo#138 live under a different trigger.
//
// Unknown and empty types count as activity. See notAgentActivity for why this
// direction is the safe one.
func CountsAsAgentActivity(eventType string) bool {
	return !notAgentActivity[eventType]
}
