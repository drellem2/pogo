package main

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/strandedwork"
)

// Who gets told a merge failed (mg-1fcc).
//
// The refinery addressed its MERGE FAILED notice to `mr.Author`, which for a
// polecat is the WORK ITEM id ("mg-32e3"). The running agent is named after
// itself ("c32e3"), and mg canonicalizes only the "mg-" prefix — so "mg-32e3"
// resolves to the box `32e3` and the agent reads `c32e3`. Two different boxes.
// On 2026-08-10 two such notices sat unread in `32e3` and `db58` while both
// agents were alive and reading their own inboxes.
//
// # This is a REDUNDANCY repair, and the scope is deliberately narrow
//
// Nothing was stranded by it. The polecat protocol mandates a poll of
// `pogo refinery show`, and that loop finds the failure AT FAILURE TIME, which
// beats mail latency. Four for four recovered without the mail: c6d7b in
// 12m43s unprompted, c3a96 in 3m unprompted, c32e3 and cdb58 in ~18m (both
// mid-outage, so their polls were failing too — c32e3 self-reported that it
// learned by polling, not by the mayor's nudge).
//
// The exposure that remains is the one c32e3 named: *an author who polls finds
// out at failure time; an author who has finished polling (or was stopped)
// finds out never.* That population — a polecat past its polling phase, or
// already stopped — has no channel at all today. That is why the address is
// still worth fixing, and why fixing it does not warrant more than this.
//
// # Why the BRANCH is the primary route and the registry is the fallback
//
// The registry answers only for a LIVE agent, and the exposed population is
// precisely the one the registry has forgotten. The branch name survives the
// agent: `polecat-c32e3` carries the agent name whether or not the process is
// still running, and mailboxes outlive their agents. So the branch is asked
// first and the registry second — the reverse ordering would resolve nothing in
// exactly the case this exists for.
//
// # Why the work-item box is KEPT rather than replaced
//
// It is a durable audit trail keyed to the ticket, and polecats are told to
// read both boxes. Dropping it would trade one silent gap for another; the
// defect was never that the work-item box is wrong, only that it was alone.

// failMailRoute is the recipient list for one MERGE FAILED notice, plus the
// evidence for how the agent recipient was found — or the reason none was.
type failMailRoute struct {
	// Recipients is the send list, in order, already deduplicated by the box
	// each name resolves to. Never contains an empty string.
	Recipients []string
	// Agent is the agent-owned mailbox added by this routing, or "" when none
	// could be resolved. When it equals a name already implied by Author it is
	// absent from Recipients — the same box, not a second delivery.
	Agent string
	// AgentSource is "branch", "registry", "author" (the author IS the agent
	// name — a crew agent or human authoring an MR), or "" when unresolved.
	AgentSource string
	// Unrouted is set when NO mailbox on this notice belongs to an agent: the
	// author names a work item, and neither the branch nor the registry yields
	// an agent. Reason carries the human-readable why.
	Unrouted bool
	Reason   string
}

// mailboxKey is the box a recipient name resolves to, for dedup only.
//
// mg canonicalizes the "mg-" prefix itself — `mg mail send mg-7dc1` and
// `mg mail send 7dc1` reach the same Maildir — so the two spellings must not
// count as two recipients. Nothing else is canonicalized: `c32e3` and `32e3`
// are genuinely different boxes, which is the whole defect.
func mailboxKey(name string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "mg-"))
}

// agentNameFromBranch extracts the agent name a polecat branch is named after,
// or "" if the branch is not a polecat branch.
func agentNameFromBranch(branch string) string {
	b := strings.TrimSpace(branch)
	if !strings.HasPrefix(b, strandedwork.BranchPrefix) {
		return ""
	}
	return strings.TrimPrefix(b, strandedwork.BranchPrefix)
}

// routeRefineryFailMail decides who is told about mr's failure.
//
// registryName resolves an author (work-item id or agent name) to the name of a
// LIVE agent in pogod's registry, returning "" when nobody answers. It is
// injected so this stays a pure function; production passes a closure over the
// registry.
func routeRefineryFailMail(mr *refinery.MergeRequest, registryName func(author string) string) failMailRoute {
	var route failMailRoute
	if mr == nil {
		return route
	}
	author := strings.TrimSpace(mr.Author)
	if author != "" {
		route.Recipients = append(route.Recipients, author)
	}

	// An author that is not a work-item id is an agent's own name — a crew
	// agent or a human resubmitting somebody's branch. The notice already
	// landed in a box with a reader, so there is nothing to add and nothing to
	// report.
	if author != "" && !client.LooksLikeWorkItemID(author) {
		route.Agent, route.AgentSource = author, "author"
		return route
	}

	agent, source := agentNameFromBranch(mr.Branch), "branch"
	if agent == "" && registryName != nil {
		agent, source = strings.TrimSpace(registryName(author)), "registry"
	}
	if agent == "" {
		route.Unrouted = true
		route.Reason = fmt.Sprintf("no agent mailbox could be resolved for branch %q: it carries no %q prefix and pogod's registry has no live agent for author %q",
			mr.Branch, strandedwork.BranchPrefix, author)
		return route
	}

	route.Agent, route.AgentSource = agent, source
	// Same box as the author's, which is the ordinary case: an agent named
	// after the bare suffix of its item ("mg-9a19" → "9a19") already reads the
	// box the author name resolves to. Deliver once.
	for _, existing := range route.Recipients {
		if mailboxKey(existing) == mailboxKey(agent) {
			return route
		}
	}
	route.Recipients = append(route.Recipients, agent)
	return route
}

// refineryFailRouteEvent is the record for a MERGE FAILED notice that did not
// reach an agent-owned mailbox, and the second half of mg-1fcc: *"a notice
// whose only recipient is a mailbox with no reader should be detectable. If the
// refinery cannot resolve a live agent for a branch, that is worth an event
// rather than a silent successful-looking delivery."*
//
// It fires on either shape of that, because both end the same way:
//
//   - UNROUTED — no agent name could be derived at all, so the notice went only
//     to the work-item box.
//   - UNDELIVERED — a name was resolved and `mg mail send` refused it
//     (no_such_mailbox is exit 3 since mg-d639), so the delivery that was
//     supposed to close this gap did not happen.
//
// The second case is this fix applied to itself. The defect being repaired is a
// notice that looks delivered and has no reader; a repair whose own extra send
// can fail while the surrounding code logs and moves on would reproduce it one
// layer up. So a refused send is an event, not just a log line — pogod logs to
// inherited stderr, and events.log is the file that is still there tomorrow.
//
// Returns ok=false when every recipient took delivery and at least one of them
// is an agent's own box, which is the healthy path and is deliberately silent.
//
// KNOWN LIMIT, stated rather than left to be discovered: nothing keys an alarm
// on `refinery_fail_notice_unrouted` today. It is queryable
// (`pogo events list --type=refinery_fail_notice_unrouted`) and the callsite
// also logs it, but there is no doctor row and no watcher — so this makes the
// gap RECORDABLE, not monitored, which is one step short of what mg-8011 argues
// for. That step was left out on purpose: the ticket asks for an event, the
// condition has never been observed in production, and a detector built for a
// population of zero is a detector nobody can calibrate.
func refineryFailRouteEvent(mr *refinery.MergeRequest, route failMailRoute, undelivered []string) (events.Event, bool) {
	if mr == nil {
		return events.Event{}, false
	}
	agentDelivered := route.Agent != ""
	for _, to := range undelivered {
		if route.Agent != "" && mailboxKey(to) == mailboxKey(route.Agent) {
			agentDelivered = false
		}
	}
	if agentDelivered && len(undelivered) == 0 {
		return events.Event{}, false
	}
	details := map[string]any{
		"mr":         mr.ID,
		"branch":     mr.Branch,
		"author":     mr.Author,
		"recipients": strings.Join(route.Recipients, ","),
	}
	switch {
	case route.Unrouted:
		details["reason"] = route.Reason
	case !agentDelivered:
		details["reason"] = fmt.Sprintf("the agent mailbox %q (resolved from %s) refused delivery, so this notice reached no agent",
			route.Agent, route.AgentSource)
	default:
		details["reason"] = "the notice reached its agent, but another recipient refused delivery"
	}
	if route.Agent != "" {
		details["agent"] = route.Agent
		details["agent_source"] = route.AgentSource
	}
	if len(undelivered) > 0 {
		details["undelivered"] = strings.Join(undelivered, ",")
	}
	details["agent_notified"] = agentDelivered
	return events.Event{
		EventType:  "refinery_fail_notice_unrouted",
		Agent:      "pogod",
		WorkItemID: mr.Author,
		Details:    details,
	}, true
}
