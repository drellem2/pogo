package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/refinery"
)

// A MERGE FAILED notice reaches the AGENT that owns the branch, not only the
// work item id (mg-1fcc).

func noRegistry(string) string { return "" }

func recipientSet(r failMailRoute) map[string]bool {
	got := make(map[string]bool, len(r.Recipients))
	for _, to := range r.Recipients {
		got[to] = true
	}
	return got
}

// The incident itself: author "mg-32e3" resolves to box `32e3`, the live agent
// reads `c32e3`, and on 2026-08-10 two notices sat unread in the former.
func TestNoticeIsAddressedToTheAgentAndNotOnlyTheWorkItem(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-naa0", Author: "mg-32e3", Branch: "polecat-c32e3"}
	route := routeRefineryFailMail(mr, noRegistry)
	got := recipientSet(route)
	if !got["c32e3"] {
		t.Errorf("recipients %v omit the agent c32e3 — this is the mg-1fcc defect: the notice goes to a box its addressee does not poll", route.Recipients)
	}
	if !got["mg-32e3"] {
		t.Errorf("recipients %v dropped the work-item box — it is a deliberate audit trail and polecats are told to read it", route.Recipients)
	}
	if route.Unrouted {
		t.Error("a resolvable branch reported unrouted")
	}
}

// The branch is asked BEFORE the registry, because the exposed population — a
// polecat stopped or past its polling phase — is exactly the one the registry
// has forgotten.
func TestAgentIsResolvedFromTheBranchWhenTheRegistryHasForgottenIt(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-naag", Author: "mg-db58", Branch: "polecat-cdb58"}
	route := routeRefineryFailMail(mr, noRegistry)
	if route.Agent != "cdb58" || route.AgentSource != "branch" {
		t.Fatalf("agent=%q source=%q, want cdb58 from the branch — a registry-only route resolves nobody for a stopped polecat, which is the case this fix exists for",
			route.Agent, route.AgentSource)
	}
}

// A branch with no polecat- prefix still has a live agent to tell.
func TestRegistryAnswersWhenTheBranchNameCannot(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-1", Author: "mg-7f10", Branch: "feature/hand-named"}
	route := routeRefineryFailMail(mr, func(author string) string {
		if author == "mg-7f10" {
			return "w7f10"
		}
		return ""
	})
	if route.Agent != "w7f10" || route.AgentSource != "registry" {
		t.Fatalf("agent=%q source=%q, want w7f10 from the registry", route.Agent, route.AgentSource)
	}
	if !recipientSet(route)["w7f10"] {
		t.Errorf("recipients %v omit the registry-resolved agent", route.Recipients)
	}
}

// The ordinary case — an agent named after the bare suffix of its item — is ONE
// box under two spellings, because mg canonicalizes the "mg-" prefix. Sending
// twice would double every failure notice in the fleet, which is why this is
// deduped rather than appended unconditionally.
func TestTheCommonCaseIsOneBoxAndIsNotMailedTwice(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-2", Author: "mg-9a19", Branch: "polecat-9a19"}
	route := routeRefineryFailMail(mr, noRegistry)
	if len(route.Recipients) != 1 {
		t.Errorf("recipients %v — `mg-9a19` and `9a19` are the same Maildir, so this is one delivery", route.Recipients)
	}
	if route.Agent != "9a19" {
		t.Errorf("agent = %q, want 9a19", route.Agent)
	}
}

// A crew agent or human authoring an MR names ITSELF as the author, so the
// notice already has a reader. Nothing to add, and nothing to alarm about.
func TestACrewAuthorIsAlreadyItsOwnMailbox(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-3", Author: "mayor", Branch: "roadmap-sync"}
	route := routeRefineryFailMail(mr, noRegistry)
	if len(route.Recipients) != 1 || route.Recipients[0] != "mayor" {
		t.Errorf("recipients = %v, want just mayor", route.Recipients)
	}
	if route.Unrouted {
		t.Error("a crew author reported unrouted — its own name IS a mailbox with a reader")
	}
	if _, ok := refineryFailRouteEvent(mr, route, nil); ok {
		t.Error("a healthy crew-authored notice emitted an event; that is noise on the common path")
	}
}

// The second half of the ticket: a notice whose only recipient is a box with no
// reader must be DETECTABLE rather than a silent successful-looking delivery.
func TestAnUnroutableNoticeIsAnEventRatherThanSilence(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-4", Author: "mg-be37", Branch: "hand-made-branch"}
	route := routeRefineryFailMail(mr, noRegistry)
	if !route.Unrouted {
		t.Fatal("no agent was resolvable and the route did not say so")
	}
	if got := recipientSet(route); got["hand-made-branch"] || len(route.Recipients) != 1 {
		t.Errorf("recipients = %v, want only the work-item box", route.Recipients)
	}
	ev, ok := refineryFailRouteEvent(mr, route, nil)
	if !ok {
		t.Fatal("an unrouted notice emitted nothing — the silent successful-looking delivery mg-1fcc names")
	}
	if ev.EventType != "refinery_fail_notice_unrouted" {
		t.Errorf("event type = %q", ev.EventType)
	}
	if ev.Details["agent_notified"] != false {
		t.Errorf("agent_notified = %v, want false", ev.Details["agent_notified"])
	}
	if !strings.Contains(ev.Details["reason"].(string), "polecat-") {
		t.Errorf("reason does not say what it looked for: %q", ev.Details["reason"])
	}
}

// This fix applied to itself. The added send can be REFUSED (no_such_mailbox is
// exit 3 since mg-d639); if that only logged, the repair would reproduce the
// defect it repairs — a notice that looks delivered and has no reader.
func TestARefusedDeliveryToTheAgentIsAlsoAnEvent(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-5", Author: "mg-32e3", Branch: "polecat-c32e3"}
	route := routeRefineryFailMail(mr, noRegistry)
	ev, ok := refineryFailRouteEvent(mr, route, []string{"c32e3"})
	if !ok {
		t.Fatal("the agent's mailbox refused the notice and nothing recorded it")
	}
	if ev.Details["agent_notified"] != false {
		t.Errorf("agent_notified = %v, want false — the send failed", ev.Details["agent_notified"])
	}
	if ev.Details["undelivered"] != "c32e3" {
		t.Errorf("undelivered = %v, want c32e3", ev.Details["undelivered"])
	}
}

// A failure to reach the AUDIT box while the agent took delivery is still
// reported, but must not claim the agent was missed.
func TestAnAgentThatTookDeliveryIsNotReportedAsMissed(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-6", Author: "mg-32e3", Branch: "polecat-c32e3"}
	route := routeRefineryFailMail(mr, noRegistry)
	ev, ok := refineryFailRouteEvent(mr, route, []string{"mg-32e3"})
	if !ok {
		t.Fatal("a refused delivery went unrecorded")
	}
	if ev.Details["agent_notified"] != true {
		t.Errorf("agent_notified = %v, want true — c32e3 took the notice", ev.Details["agent_notified"])
	}
}

// The healthy path stays silent: every recipient took delivery and one of them
// is the agent's own box.
func TestAFullyDeliveredNoticeEmitsNothing(t *testing.T) {
	mr := &refinery.MergeRequest{ID: "mr-7", Author: "mg-32e3", Branch: "polecat-c32e3"}
	route := routeRefineryFailMail(mr, noRegistry)
	if _, ok := refineryFailRouteEvent(mr, route, nil); ok {
		t.Error("the healthy path emitted an event")
	}
}

func TestMailboxKeyCanonicalizesOnlyTheMGPrefix(t *testing.T) {
	if mailboxKey("mg-1fcc") != mailboxKey("1fcc") {
		t.Error("mg canonicalizes the mg- prefix; these are one Maildir")
	}
	if mailboxKey("p1fcc") == mailboxKey("1fcc") {
		t.Error("`p1fcc` and `1fcc` were treated as one box — they are two, and that conflation IS mg-1fcc")
	}
}

func TestRoutingSurvivesAnEmptyAuthor(t *testing.T) {
	route := routeRefineryFailMail(&refinery.MergeRequest{ID: "mr-8", Branch: "polecat-abc"}, noRegistry)
	if len(route.Recipients) != 1 || route.Recipients[0] != "abc" {
		t.Errorf("recipients = %v, want the branch's agent alone", route.Recipients)
	}
}

// A nil MR or a nil registry lookup must not panic the refinery's failure
// callback — it runs on the merge loop, and a panic there loses the notice
// entirely, which is a worse version of the defect being fixed.
func TestRoutingSurvivesNilInputs(t *testing.T) {
	if got := routeRefineryFailMail(nil, nil); len(got.Recipients) != 0 || got.Unrouted {
		t.Errorf("nil MR routed to %v (unrouted=%v)", got.Recipients, got.Unrouted)
	}
	route := routeRefineryFailMail(&refinery.MergeRequest{ID: "mr-9", Author: "mg-32e3", Branch: "polecat-c32e3"}, nil)
	if !recipientSet(route)["c32e3"] {
		t.Errorf("recipients %v — the branch route must not depend on a registry lookup being wired", route.Recipients)
	}
	if _, ok := refineryFailRouteEvent(nil, failMailRoute{}, nil); ok {
		t.Error("a nil MR produced an event")
	}
}
