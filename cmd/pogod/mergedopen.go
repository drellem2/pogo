package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refinery"
)

// The addressee for a merge whose work item did NOT close (mg-9d4e).
//
// WHAT ARRIVES HERE. reapMergedPolecat calls `mg done` on the author's behalf the
// instant a branch lands. Two different things make that call fail, and until
// this file they were one log line:
//
//	the POLECAT won the race        -> "already done", its verdict stands, nothing is wrong
//	the item REFUSED TO CLOSE       -> the merge landed, the item stays open, nobody is told
//
// The second is the state mg-9d4e is about. An item tagged `declares-remainder`
// names work that must outlive it, and `mg done` refuses such an item until a
// successor is named — correctly; the alternative failure is a remainder silently
// dropped, which also happened on 2026-08-12 (mg-69f1) and is worse. So the close
// is turned away, the polecat is stopped ~0.5s later, its claim is released, and
// the item is back in available/ describing itself as unstarted work. It happened
// to mg-0e8c at 23:42Z and mg-ac0c at 23:51Z that night, and priority-wake
// advertised both as "high priority, ready and unclaimed" within minutes.
//
// WHY THE DAEMON AND NOT THE POLECAT. The ticket's own suggestion was to close
// the loop at the worker — have it file the successor, or mail the coordinator,
// before exiting. It cannot: pogod stops it about half a second after the merge,
// whether or not the close applied (the stop below this call site is not
// conditional on it). The polecat is not slow to notice, it is gone. The daemon
// is the only party still running at the moment the fact is established, and it
// already holds everything the message needs.
//
// WHY THE COORDINATOR. Resolving this means filing a successor, or archiving an
// item somebody else's ticket has already superseded, and then closing it. That
// is the mayor's job, and the mayor escalates to a human when it is not. Same
// routing as the stranded-push alert next door, for the same reason.
//
// THE EVENT IS THE DURABLE HALF AND IT IS EMITTED FIRST. A mail is best-effort —
// no mg on PATH, a mailbox refusal — and this alert must degrade to a record
// rather than to nothing. That ordering is the lesson mg-be37 paid for: its
// detector was correct for three months and its only outputs were pogod's log and
// an event nothing consumed.
//
// # TELLING THIS ALERT FROM THE ONE THAT ALREADY ARRIVES (mg-f17c)
//
// A DIFFERENT merged-not-closed mail arrives today, and confusing the two would
// score this path as verified when nothing of it had run. They are separable by
// inspection, no timing argument needed:
//
//	THIS path      subject `[merged-not-closed] <id> is MERGED but still open …`
//	               to agent.CoordinatorName(), preceded by a
//	               `work_item_merged_not_closed` event on the spine.
//	THE OLD path   subject `MERGED BUT NOT CLOSED: <id> — <title>`
//	               to the item's CREATOR, from internal/filernotify (mg-f120,
//	               mg-2b71), body opens "The work item you filed had its branch
//	               MERGED". No event.
//
// # OBSERVED AT RUNTIME 2026-08-20 (mg-161a)
//
// This alert is no longer unexercised. scripts/mergedopen-runtime_test.sh builds
// a pogod that is NOT a test binary, gives it a private everything, and drives a
// real `declares-remainder` item through a real refinery merge; the sink below
// runs for real and the mail lands in a real maildir the coordinator reads back
// with `mg mail list`. That is the one observation no test in this package can
// make, because defaultMergedOpenAlertMail refuses to send under
// testing.Testing() — every unit test here asserts against a stub in its place.
//
// THE DELIVERY HAS A RED ARM AND IT BITES: with no coordinator mailbox
// registered the send fails and `work_item_merged_not_closed_undelivered` is
// emitted, with the event above already on the spine. Both arms are asserted, so
// the control has been shown to distinguish them rather than merely to pass.
//
// WHAT IS STILL UNOBSERVED, stated so the next reader does not over-read the
// above: this path has never fired on the LIVE fleet. Measured 2026-08-20 04:2xZ,
// after the redeploy that made it live: zero `work_item_merged_not_closed` events
// across ~/.pogo/events.log and events.log.1, and zero mails under
// ~/.macguffin/mail whose SUBJECT line is this path's, against 18 old-path filer
// mails. It has been shown to work; it has not yet had a live occasion.
//
// AND A TRAP IN THAT CENSUS, because mg-f17c's own wording invites it: a
// SUBSTRING search for `[merged-not-closed]` over the live mail store returns 1
// hit today, and it is not this path — it is mg-f17c's verdict, quoted verbatim
// inside a filernotify `COMPLETED:` mail, which contains the string while
// describing its absence. Anchor the census to `^Subject: `.

// mergedOpenAlert is one merged-but-unclosed work item with everything a reader
// needs to act, assembled at the moment the fact is established.
type mergedOpenAlert struct {
	// WorkItemID is the item that merged and did not close.
	WorkItemID string
	// Worker is the polecat that did the work, or "" when the merge had no live
	// polecat (a coordinator submitting a stranded branch by hand, mg-be37).
	Worker string
	// Repo, Branch, MR, Target and MergedSHA are the merge, so the claim is
	// checkable with one `git log` and one `pogo refinery show`.
	Repo      string
	Branch    string
	MR        string
	Target    string
	MergedSHA string
	// CloseError is what `mg done` actually said. It is quoted verbatim because
	// it names the reason — "declares a remainder" is the common one, but this
	// alert must not assume it and then be wrong in the one case that is
	// something else.
	CloseError string
	// StatusUnknown is true when the item's status could not be read after the
	// failed close. It changes the wording and nothing else: an unreadable store
	// is not evidence that the item closed, so the alert still goes out.
	StatusUnknown bool
}

// mergedOpenAlertMail is the mail sink, swappable for tests. Production sends via
// `mg mail send`; tests capture.
var mergedOpenAlertMail = defaultMergedOpenAlertMail

// mergedOpenItemDone probes whether the work item reached a terminal state,
// swappable for tests. It is the discriminator between the two failure kinds
// above, and it asks the STORE rather than parsing the error text: `mg done`
// answers "already done" and "declares a remainder" with the same exit code, so
// a text match would be a second, weaker copy of a fact the store states
// outright.
var mergedOpenItemDone = client.MGWorkItemDone

// reportMergedButOpen fires when the post-merge `mg done` did not apply. It is
// silent for the benign half and loud for the other.
//
// A FAILED PROBE ALERTS. "The item is closed" is the only reading that
// suppresses this, and an unreadable store is not that reading — treating it as
// one would let a transient mg failure silence the alert in exactly the window
// where mg is also what failed the close.
func reportMergedButOpen(mr *refinery.MergeRequest, worker string, closeErr error) {
	if mr == nil || mr.Author == "" || closeErr == nil {
		return
	}
	done, err := mergedOpenItemDone(mr.Author)
	if err == nil && done {
		// The polecat won the race and its own result stands — the better
		// outcome, not a degraded one. Logged at low volume so the race is still
		// visible in the record.
		log.Printf("refinery: mg done %s on the merged worker's behalf was refused because the item is "+
			"ALREADY DONE — the worker wrote its own result first and that result stands (%v)",
			mr.Author, closeErr)
		return
	}
	a := mergedOpenAlert{
		WorkItemID:    mr.Author,
		Worker:        worker,
		Repo:          mr.RepoPath,
		Branch:        mr.Branch,
		MR:            mr.ID,
		Target:        mr.TargetRef,
		MergedSHA:     mr.MergedSHA,
		CloseError:    closeErr.Error(),
		StatusUnknown: err != nil,
	}
	// The CALLER has already logged that the close did not apply — reapMergedPolecat's
	// "MERGED BUT NOT CLOSED" line (mg-2b71) says it, for gated items too, which do
	// not reach here. What this line adds is that the alert was RAISED, which is a
	// different fact and the one a reader chasing a missing mail needs.
	log.Printf("refinery: raising merged-not-closed alert for work item %s — branch %s landed on %s as %s "+
		"and the item is open with its work already on the target, which is the state priority-wake "+
		"advertises as ready and unclaimed (mg-9d4e)",
		a.WorkItemID, a.Branch, a.Target, a.MergedSHA)
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_merged_not_closed",
		Agent:      "pogod",
		WorkItemID: a.WorkItemID,
		Repo:       a.Repo,
		Details: map[string]any{
			"worker":         a.Worker,
			"branch":         a.Branch,
			"mr":             a.MR,
			"target":         a.Target,
			"merged_sha":     a.MergedSHA,
			"close_error":    a.CloseError,
			"status_unknown": a.StatusUnknown,
		},
	})
	mergedOpenAlertMail(a)
}

// defaultMergedOpenAlertMail delivers one alert to the coordinator.
//
// BEST-EFFORT AND DELIBERATELY SO. The work_item_merged_not_closed event is
// already on the spine by the time this runs, so a machine with no mg on PATH
// loses the improvement and not the record. What it must never do is take the
// daemon down with it or hold up the stop that follows.
func defaultMergedOpenAlertMail(a mergedOpenAlert) {
	// NEVER FROM A TEST BINARY. This package has tests that drive
	// reapMergedPolecat with a failing `complete` — that is how the already-done
	// race is covered — so without this guard a unit test manufactures a genuine
	// fleet alarm in the coordinator's real inbox. Same reasoning and same lever
	// as macguffinStoreRoot's: the isolation belongs in the sink, not in a
	// remembered stub at each call site. testmain_test.go stubs it as well; this
	// is the half that survives the stub being dropped.
	if testing.Testing() {
		log.Printf("merged-not-closed: NOT mailing under a test binary (%s would have gone to %s)",
			a.WorkItemID, agent.CoordinatorName())
		return
	}
	to := agent.CoordinatorName()
	subject, body := a.Message()
	if err := client.SendMGMail(to, "pogod", subject, body); err != nil {
		log.Printf("merged-not-closed: cannot mail %s about %s (%v)", to, a.WorkItemID, err)
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_merged_not_closed_undelivered",
			Agent:      "pogod",
			WorkItemID: a.WorkItemID,
			Repo:       a.Repo,
			Details: map[string]any{
				"recipient": to,
				"branch":    a.Branch,
				"mr":        a.MR,
				"error":     err.Error(),
			},
		})
	}
}

// Message renders the alert as (subject, body).
//
// THE SUBJECT CARRIES THE STATE, NOT THE ERROR. "mg done failed" invites the
// reader to look for a broken tool; what actually happened is that a work item
// is open with its work already merged, and that is the fact a skimmed subject
// line has to deliver. The remedy travels with it — a notice that names only the
// problem gets resolved with the move priority-wake is already recommending,
// which is the dispatch this exists to prevent.
func (a mergedOpenAlert) Message() (subject, body string) {
	subject = fmt.Sprintf("[merged-not-closed] %s is MERGED but still open — file its successor, do not dispatch",
		a.WorkItemID)

	var b strings.Builder
	fmt.Fprintf(&b, "A branch merged and its work item did not close. The work is on %s; the item is not\n"+
		"closed, so it returns to the pool describing itself as unstarted.\n\n", a.Target)
	fmt.Fprintf(&b, "Work item:  %s\n", a.WorkItemID)
	if a.Worker != "" {
		fmt.Fprintf(&b, "Worker:     %s (stopped at merge)\n", a.Worker)
	} else {
		fmt.Fprintf(&b, "Worker:     none was running at merge (hand-submitted branch)\n")
	}
	fmt.Fprintf(&b, "Repo:       %s\n", a.Repo)
	fmt.Fprintf(&b, "Branch:     %s\n", a.Branch)
	fmt.Fprintf(&b, "Merge:      %s -> %s as %s\n", a.MR, a.Target, a.MergedSHA)
	fmt.Fprintf(&b, "mg said:    %s\n\n", a.CloseError)

	if a.StatusUnknown {
		fmt.Fprintf(&b, "THE ITEM'S STATUS COULD NOT BE READ after the failed close, so this alert does not\n"+
			"claim to know it is open — it claims that nothing established it is CLOSED. Check it\n"+
			"first (`mg show %s`); if it is already done, this notice is spent.\n\n", a.WorkItemID)
	}

	fmt.Fprintf(&b, "WHAT TO DO — close the item, do not dispatch at it:\n\n")
	fmt.Fprintf(&b, "    mg done %s --successor=<the id that carries the remainder>\n\n", a.WorkItemID)
	fmt.Fprintf(&b, "The usual cause is the `declares-remainder` guard doing its job: the item names work\n"+
		"that must outlive it, `mg done` refuses it until a successor exists, and the successor was\n"+
		"never filed. File it, then close this item against it. If the remainder is already filed\n"+
		"under another id, name that id — it does not have to be new.\n\n")

	fmt.Fprintf(&b, "DO NOT WEAKEN THE GUARD. Its alternative failure is worse and has happened: an\n"+
		"untagged item closes cleanly and drops its remainder silently, with nothing to notice. A\n"+
		"completed item re-offered to the queue is loud and recoverable; a lost remainder is not.\n\n")

	fmt.Fprintf(&b, "DISPATCH IS ALREADY REFUSED AT %s. pogod's merged-work gate (mg-9d4e) refuses a\n"+
		"spawn onto an item whose branch has landed, so a coordinator acting on priority-wake's\n"+
		"advice gets a 409 rather than a worker that re-derives merged work. That refusal is a\n"+
		"backstop for this mail, not a substitute: it fires only if someone tries, it names the\n"+
		"same remedy, and it is bypassable with --merged-override.\n\n", a.WorkItemID)

	b.WriteString("The same fact is on the event spine as work_item_merged_not_closed; this mail is the\n" +
		"half that is observable from outside the daemon that found it.\n")
	return subject, b.String()
}
