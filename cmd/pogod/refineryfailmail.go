package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/refinery"
)

// The failure mail's classification block (mg-e5c2).
//
// The mail is the surface that reaches a coordinator who is not sitting at a
// terminal, so it carries the same three things `pogo refinery show` does: which
// class the failure is, how many attempts were made, and — for every failing
// attempt — the transport and git's RAW output.
//
// The raw output is not an optional extra. On 2026-08-05, 20 of 31 failures were
// ssh reporting `Undefined error: 0` and 11 were HTTPS reporting `Could not
// resolve host: github.com`. Readers who saw only the ssh wording reasoned from
// errno semantics and produced two confident wrong mechanisms over several
// hours; the HTTPS half named the cause outright. A failure record that shows
// one transport's summary can reproduce that, so this one shows neither summary
// nor a single transport.

// refineryFailureClassLabel is the class as it appears in the mail subject,
// falling back to "unclassified" so the subject shape is stable.
func refineryFailureClassLabel(mr *refinery.MergeRequest) string {
	if mr.FailureClass == "" {
		return string(refinery.ClassUnclassified)
	}
	return string(mr.FailureClass)
}

// refineryAttemptSummary states, in one line, how hard the refinery tried and —
// when it stopped — why it stopped. Before mg-e5c2 the mail said only
// "Consecutive failures: 1", which is the AUTHOR's streak and was routinely
// read as the attempt count.
func refineryAttemptSummary(mr *refinery.MergeRequest) string {
	n := mr.AttemptCount
	if n == 0 {
		n = len(mr.Attempts)
	}
	var s string
	switch n {
	case 0:
		s = "no attempt was recorded"
	case 1:
		s = "1 — it failed ONCE and was not retried"
	default:
		s = fmt.Sprintf("%d — it failed after %d attempts", n, n)
	}
	s += "\n" + refineryWaitSummary(mr)
	if mr.NotRetriedReason != "" {
		s += "\nNo further retry: " + mr.NotRetriedReason
	}
	return s
}

// refineryWaitSummary states how long the refinery ACTUALLY slept in retry
// backoff before giving up (mg-c3b7).
//
// The mail previously carried the attempt count and the budget's own wording,
// and those two cannot separate "the network was down for longer than anyone
// could wait" from "we did not really wait". On 2026-08-10 a clean 8m58s gate
// was discarded by a fetch failure, and the mail's reader had to go to the
// refinery log to learn that the whole retry campaign had lasted 52 seconds
// against an outage measured at 15m26s. The waited time is the number that
// makes the budget auditable against the event, so it goes in the mail.
//
// Derived by summing the per-attempt records rather than read from a new
// field, so it is equally correct for merge requests already on disk.
func refineryWaitSummary(mr *refinery.MergeRequest) string {
	var secs float64
	var retried int
	for _, a := range mr.Attempts {
		secs += a.BackoffSeconds
		if a.Retried {
			retried++
		}
	}
	if secs <= 0 {
		return "Waited: 0s in retry backoff — nothing here was retried, so this says nothing about how long the condition lasted."
	}
	return fmt.Sprintf("Waited: %s in retry backoff across %d retried attempt(s) before giving up. Compare that against how long the condition actually lasted: if it outlasted the wait, this is a budget that ran out, not a branch that is broken.",
		time.Duration(secs*float64(time.Second)).Round(time.Second), retried)
}

// refineryAttemptDetail renders one block per failing attempt.
func refineryAttemptDetail(mr *refinery.MergeRequest) string {
	if len(mr.Attempts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n--- Per-attempt record (transport and RAW error, never a normalised summary) ---\n")
	for _, a := range mr.Attempts {
		fmt.Fprintf(&b, "\n#%d  stage=%s  class=%s  transport=%s\n",
			a.Attempt, blankAs(a.Stage, "unknown"), blankAs(string(a.Class), "unknown"), blankAs(a.Transport, "unknown"))
		if a.Command != "" {
			fmt.Fprintf(&b, "    command: %s\n", a.Command)
		}
		switch {
		case a.Retried && a.BackoffSeconds > 0:
			fmt.Fprintf(&b, "    retried: yes, after %.0fs of backoff\n", a.BackoffSeconds)
		case a.Retried:
			b.WriteString("    retried: yes, immediately\n")
		default:
			fmt.Fprintf(&b, "    retried: NO — %s\n", blankAs(a.NotRetriedReason, "no reason recorded"))
		}
		if a.RawError != "" {
			b.WriteString("    raw error:\n")
			for _, line := range strings.Split(strings.TrimRight(a.RawError, "\n"), "\n") {
				fmt.Fprintf(&b, "      %s\n", line)
			}
		}
	}
	return b.String()
}

func blankAs(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// Who a MERGE FAILED notice is ADDRESSED to (mg-1fcc).
//
// The refinery addressed the author as `mr.Author`, which for a polecat is its
// WORK-ITEM id (`mg-32e3`). The running agent's mailbox is named after the
// agent (`c32e3`). Those are two different Maildirs, so on 2026-08-10 two
// notices sat unread in `mg-32e3` and `mg-db58` while `c32e3` and `cdb58` — the
// only actors that can resubmit — had empty inboxes.
//
// # Scope: this is a REDUNDANCY repair, not a work-stranding bug
//
// The primary channel is the polecat's own polling loop (protocol step 6), and
// it works: four of four polecats that hit this recovered without the mail
// (c6d7b 12m43s and c3a96 3m unprompted; c32e3 and cdb58 in the same window,
// with c32e3 self-reporting that it learned from `pogo refinery show`, not from
// mail). The 12-vs-18-minute spread is the network outage those failures landed
// in, not the channel. So this fix does not rescue the common case — the common
// case was never lost.
//
// What it closes is the narrow case the polling loop cannot cover, in c32e3's
// own words: *an author who polls finds out at failure time; an author who has
// finished polling (or was stopped) finds out never.* That is the mg-be37
// population — pushed, unmerged, nobody watching.
//
// # Why not rely on the polecat reading both boxes
//
// It already does: the template tells every polecat to check `$POGO_AGENT_NAME`
// AND its work-item id on each mail-check fire, so the notice was reachable, not
// unreachable. But that makes a convention load-bearing for correctness — every
// polecat has to remember a second mailbox named after its ticket, forever, and
// a rescuer reading a stopped polecat's inbox has no such instruction at all.
// Addressing the notice correctly costs one registry lookup and removes the
// dependency.

// refineryFailureAddressees is the recipient list for one MERGE FAILED notice,
// together with the evidence for whether an agent that can ACT on it was found.
type refineryFailureAddressees struct {
	// Mailboxes is the ordered, deduplicated list of names to send to. The
	// work-item box stays on it — it may be a deliberate audit trail, and the
	// ticket explicitly sanctions sending to both — but it is no longer the
	// only entry.
	Mailboxes []string

	// Agent is the registry name of the live agent resolved for this merge
	// request, or "" when none could be resolved. "" is the whole point of this
	// struct: it is the state that used to look like a successful delivery.
	Agent string

	// BranchOwner is the agent name embedded in the branch (`polecat-c32e3` ->
	// `c32e3`), or "" if the branch does not carry one. It is mailed even when
	// no live agent is registered under it: the Maildir outlives the process,
	// so it is where a rescuer or a successor looks.
	BranchOwner string

	// Reason explains an empty Agent, in the words the event will carry.
	Reason string
}

// AgentResolved reports whether a live agent is on the recipient list.
func (a refineryFailureAddressees) AgentResolved() bool { return a.Agent != "" }

// refineryAgentLookup is the slice of agent.Registry that addressee resolution
// needs. Narrow so the resolution is testable without a real registry.
type refineryAgentLookup interface {
	Get(name string) *agent.Agent
	GetByWorkItemOrName(id string) *agent.Agent
}

// resolveRefineryFailureAddressees decides who hears about a failed merge.
//
// The BRANCH is asked first and the author second, because the branch is the
// fact about whose work this is: `polecat-c32e3` names c32e3 whatever string
// the submitter put in --author. The author lookup is the fallback that keeps
// crew- and human-authored merge requests working, where the branch names no
// agent or names one that is long gone.
func resolveRefineryFailureAddressees(reg refineryAgentLookup, mr *refinery.MergeRequest) refineryFailureAddressees {
	out := refineryFailureAddressees{}
	if mr == nil {
		out.Reason = "no merge request"
		return out
	}
	out.BranchOwner = gitgc.BranchSuffix(mr.Branch)

	var live *agent.Agent
	if reg != nil {
		if out.BranchOwner != "" {
			live = reg.Get(out.BranchOwner)
		}
		if live == nil {
			live = reg.GetByWorkItemOrName(mr.Author)
		}
	}
	if live != nil {
		out.Agent = live.Name
	}

	add := func(name string) {
		if strings.TrimSpace(name) == "" {
			return
		}
		for _, existing := range out.Mailboxes {
			if existing == name {
				return
			}
		}
		out.Mailboxes = append(out.Mailboxes, name)
	}
	// The agent first — it is the only recipient that can resubmit. Then the
	// branch owner, which is the same string in the common case and a distinct
	// one when a crew agent submitted somebody else's branch. The work item
	// last: it is the audit trail, and it was the bug.
	add(out.Agent)
	add(out.BranchOwner)
	add(mr.Author)

	switch {
	case out.Agent != "":
		// Resolved; no reason to record.
	case reg == nil:
		out.Reason = "no agent registry was available to resolve an owner"
	case out.BranchOwner == "":
		out.Reason = fmt.Sprintf("branch %q carries no %q prefix, so it names no agent, and nothing is registered under author %q",
			mr.Branch, gitgc.BranchPrefix, mr.Author)
	default:
		out.Reason = fmt.Sprintf("nothing is registered as %q (from branch %q) or as author %q — the owner has stopped, "+
			"or never ran in this daemon; if it was stopped mid-merge its branch is pushed and unmerged (mg-be37)",
			out.BranchOwner, mr.Branch, mr.Author)
	}
	return out
}

// emitRefineryFailureNoticeUnaddressed records a MERGE FAILED notice that
// reached no live agent — the case the polling loop cannot cover, because there
// is nobody left polling.
//
// It is an EVENT and not a mail on purpose, the same call worktreecleanup.go's
// A15 row makes: the thing that just failed is addressing, and answering a
// failed address by inventing another address is a retry dressed as an alarm.
// The coordinator still gets its own copy of every failure notice on the line
// below the send loop, so this event is the structured residue, not the only
// signal.
//
// Observable as: `pogo events list --type refinery_failure_notice_unaddressed`.
// A non-empty result means a branch failed to merge and no running process was
// told — check whether the branch is pushed and unmerged.
//
// delivery maps each recipient to "delivered" or the send error, so the event
// answers the second half of the question too: an unregistered mailbox refuses
// the send (`no_such_mailbox`, exit 3 since mg-d639), and without this the
// refusal would be a log line in a file nothing queries.
func emitRefineryFailureNoticeUnaddressed(mr *refinery.MergeRequest, addr refineryFailureAddressees, delivery map[string]string) {
	details := map[string]any{
		"merge_request_id": mr.ID,
		"branch":           mr.Branch,
		"target":           mr.TargetRef,
		"author":           mr.Author,
		"class":            refineryFailureClassLabel(mr),
		"branch_owner":     addr.BranchOwner,
		"mailboxes":        addr.Mailboxes,
		"reason":           addr.Reason,
	}
	if len(delivery) > 0 {
		// Sorted so the same set of outcomes renders the same way twice — a
		// reader diffing two of these should see the failures, not map order.
		keys := make([]string, 0, len(delivery))
		for k := range delivery {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		outcomes := make([]string, 0, len(keys))
		for _, k := range keys {
			outcomes = append(outcomes, k+"="+delivery[k])
		}
		details["delivery"] = outcomes
	}
	events.Emit(context.Background(), events.Event{
		EventType: "refinery_failure_notice_unaddressed",
		// "refinery" per the refinery section's convention in
		// docs/event-log.md: the actor is the refinery, and the agent that was
		// NOT reached is the thing this event says does not exist.
		Agent:      "refinery",
		WorkItemID: mr.Author,
		Repo:       mr.RepoPath,
		Details:    details,
	})
}
