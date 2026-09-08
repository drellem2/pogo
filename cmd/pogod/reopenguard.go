package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refinery"
)

// The decision behind the auto-reopen that fires when a merge request FAILS
// (mg-4d21).
//
// # What the reopen is and what it did
//
// pogod's OnFailed callback has run `mg reopen <author>` on every failure since
// mg-06f2, unconditionally. `mg reopen` is the ONLY writer in macguffin that
// moves a work item OUT of done/ — measured against the live binary on
// 2026-09-08:
//
//	reopen on a done item      -> "Reopened mg-ed67", exit 0, lands in
//	                              work/claimed/mg-ed67.md — NO pid suffix
//	reopen on an archived item -> "is archived, not done.", exit 4
//	reopen on a claimed item   -> "not done — it is already claimed", exit 4
//
// So a failed submit CAN flip an already-`done` item back to `claimed`, which is
// what Daniel reported on drellem2/pogo#164 and had to repair by hand. The
// archived row is the discriminator for the negative instance the mayor found:
// mg-3ba8 was archived four minutes after its merge and its later failed MR did
// NOT flip it, because reopen refuses archived. Archiving promptly was masking
// this, not avoiding it — and the items nobody archives are the exposed ones.
//
// # Why the reopen was right once and is wrong now
//
// mg-06f2 built it for the case where a polecat calls `mg done` and exits and
// the merge THEN fails: the work never landed, so the item is closed over
// nothing. That case has since become the rare one. pogod closes the item AT
// MERGE (mg-2b71, gh #35), so an item sitting in done/ when a merge fails is
// usually done because an EARLIER merge by the same author landed — and the
// failure in hand is a follow-up branch, not the completion attempt.
//
// Reopening there destroys a true record and produces something worse than the
// open item mg-06f2 was chasing: claimed/ with no pid and no process. Every
// stall check in this repo scans available/ (internal/stallwatch), so nothing
// looks at it. That is the same shape as mg-8c4a — a state a sweep could not
// read — and it is why the one reported occurrence was found by a human.
//
// # The guard
//
// Ask the refinery whether ANY merge request authored by this item has landed.
// If one has, the item's done state rests on merged work and this failure is not
// evidence that it is unfinished: leave it alone and say so. Otherwise reopen as
// before.
//
// PR-flow merges COUNT here, unlike in the dispatch gate (mergedWorkFor, which
// excludes them because a PR-flow merge leaves real work to do). The question
// there is "would a worker re-derive landed code"; the question here is "did
// this item's done state come from work that landed", and a merge onto an
// integration branch answers that yes — the polecat closed the item itself
// afterwards, deliberately.
//
// THE FAILURE DIRECTION IS OPEN, matching the dispatch gate beside it: no
// author, no refinery, a history that has pruned the merge (100 entries /
// 7 days) all reopen. What that costs is stated rather than left to be found: a
// merge that has aged out of history is invisible here, so this guard is not a
// proof that no flip can happen — it is a refusal on a record the refinery
// wrote. The mail line below is the backstop for everything it cannot see.

// reopenOutcome is what happened to the failed MR author's work item, in a form
// both the mail line and the event are rendered from. One value, so the two can
// never disagree about it.
type reopenOutcome string

const (
	// reopenSkippedNoAuthor: the MR carried no --author, so there is no item.
	reopenSkippedNoAuthor reopenOutcome = "no_author"
	// reopenDeclinedLanded: a merge authored by this item already landed.
	reopenDeclinedLanded reopenOutcome = "declined_landed_merge"
	// reopenPerformed: the item moved done/ -> claimed/.
	reopenPerformed reopenOutcome = "reopened"
	// reopenNotDone: mg refused because the item is still claimed — the state
	// the reopen wanted, and the common case (mg-5d3f).
	reopenNotDone reopenOutcome = "already_claimed"
	// reopenArchived: mg refused because the item is archived (mg-3ba8).
	reopenArchived reopenOutcome = "archived"
	// reopenFailed: mg refused for any other reason, carried verbatim.
	reopenFailed reopenOutcome = "error"
)

// reopenResult is one disposition of the author's work item.
type reopenResult struct {
	Outcome reopenOutcome
	// Landed names the merge that blocked the reopen; empty otherwise.
	Landed refinery.MergeRequest
	// Err is mg's own refusal, for the outcomes that carry one.
	Err error
}

// landedMergeFor returns the most recent MERGED merge request authored by
// author, PR-flow included. History() is oldest-first, so the scan runs
// backwards and returns the newest match; which one it returns matters for the
// message and not for the verdict.
//
// StatusMerged only. A queued, processing, failed or cancelled MR is not landed
// work — and the MR that has just failed is in this history by the time OnFailed
// runs, so excluding non-merged statuses is what stops it matching itself.
func landedMergeFor(history []refinery.MergeRequest, author string) (refinery.MergeRequest, bool) {
	want := strings.ToLower(strings.TrimSpace(author))
	if want == "" {
		return refinery.MergeRequest{}, false
	}
	for i := len(history) - 1; i >= 0; i-- {
		mr := history[i]
		if mr.Status != refinery.StatusMerged {
			continue
		}
		if strings.ToLower(strings.TrimSpace(mr.Author)) != want {
			continue
		}
		return mr, true
	}
	return refinery.MergeRequest{}, false
}

// reopenAfterFailure decides the disposition of a failed MR's work item and
// carries it out. reopen is injected so the decision is testable without a
// macguffin store; production passes client.ReopenMGWorkItem.
func reopenAfterFailure(failed *refinery.MergeRequest, history []refinery.MergeRequest, reopen func(string) error) reopenResult {
	if failed == nil || strings.TrimSpace(failed.Author) == "" {
		return reopenResult{Outcome: reopenSkippedNoAuthor}
	}
	if landed, ok := landedMergeFor(history, failed.Author); ok {
		return reopenResult{Outcome: reopenDeclinedLanded, Landed: landed}
	}
	err := reopen(failed.Author)
	switch {
	case err == nil:
		return reopenResult{Outcome: reopenPerformed}
	case errors.Is(err, client.ErrMGWorkItemNotDone):
		return reopenResult{Outcome: reopenNotDone, Err: err}
	case errors.Is(err, client.ErrMGWorkItemArchived):
		return reopenResult{Outcome: reopenArchived, Err: err}
	default:
		return reopenResult{Outcome: reopenFailed, Err: err}
	}
}

// reopenMailLine is the "Work item:" line appended to the MERGE FAILED mail.
//
// IT IS PRINTED FOR EVERY OUTCOME, including the ones that changed nothing. The
// reported flip was repaired by hand because nothing said it had happened: the
// mail described the merge and never the side effect on the work item, and the
// only other record was a log line in a file pogod does not always write to
// (mg-a19a). A reader who has to infer whether their item moved will infer wrong
// in whichever direction is quieter.
func reopenMailLine(failed *refinery.MergeRequest, res reopenResult) string {
	if failed == nil || res.Outcome == reopenSkippedNoAuthor {
		return ""
	}
	id := failed.Author
	switch res.Outcome {
	case reopenDeclinedLanded:
		return fmt.Sprintf("Work item: %s was NOT reopened. %s (branch %s) already MERGED onto %s%s, "+
			"so this item's status rests on work that landed and a later failed branch is not evidence that it is "+
			"unfinished (mg-4d21). If this failure really does mean more work on this item, reopen it deliberately: "+
			"`mg reopen %s`.",
			id, res.Landed.ID, res.Landed.Branch, res.Landed.TargetRef, shortSHASuffix(res.Landed.MergedSHA), id)
	case reopenPerformed:
		return fmt.Sprintf("Work item: %s was REOPENED (done -> claimed) so its author can retry. NOTHING OWNS IT — "+
			"`mg reopen` writes claimed/%s.md with no pid, no polecat is running it, and every stall check scans "+
			"available/ only, so it will sit there unreported until somebody acts. Re-dispatch it, or close it again.",
			id, id)
	case reopenNotDone:
		return fmt.Sprintf("Work item: %s is still claimed and in progress — no reopen needed, nothing moved.", id)
	case reopenArchived:
		return fmt.Sprintf("Work item: %s is ARCHIVED, so it was not reopened and nothing moved. `mg reopen` refuses "+
			"an archived item; this is why an archived item never flips on a failed follow-up and a merely `done` one "+
			"does (mg-4d21).", id)
	default:
		return fmt.Sprintf("Work item: %s could NOT be reopened and its status is UNKNOWN to this notice: %v", id, res.Err)
	}
}

// shortSHASuffix renders " as 45b4421" for a non-empty sha, so the claim in the
// declined line is checkable with one `git log` and no daemon.
func shortSHASuffix(sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return ""
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return " as " + sha
}

// reopenEvent records the dispositions that are worth counting as they are made,
// and returns false for the ones that are not.
//
// EMITTED: the flip, the refusal that prevents a flip, and an unclassified mg
// error. Those are the outcomes a later reader would otherwise have to
// reconstruct from a status that has already changed.
//
// NOT EMITTED: no author, "still claimed" and "archived". Those changed nothing
// and are the ordinary case — 22 "already claimed" refusals appear in one
// 50,603-line pogod log (mg-5d3f), and an event per failure would bury the rare
// one under them. They are still in the mail line, where a reader who is looking
// at this failure sees them.
func reopenEvent(failed *refinery.MergeRequest, res reopenResult) (events.Event, bool) {
	if failed == nil {
		return events.Event{}, false
	}
	switch res.Outcome {
	case reopenPerformed, reopenDeclinedLanded, reopenFailed:
	default:
		return events.Event{}, false
	}
	details := map[string]any{
		"outcome": string(res.Outcome),
		"mr":      failed.ID,
		"branch":  failed.Branch,
		"target":  failed.TargetRef,
	}
	if res.Outcome == reopenDeclinedLanded {
		details["landed_mr"] = res.Landed.ID
		details["landed_branch"] = res.Landed.Branch
		details["landed_target"] = res.Landed.TargetRef
		if res.Landed.MergedSHA != "" {
			details["landed_sha"] = res.Landed.MergedSHA
		}
	}
	if res.Err != nil {
		details["error"] = res.Err.Error()
	}
	return events.Event{
		EventType:  "work_item_reopen_after_merge_failure",
		Agent:      "pogod",
		WorkItemID: failed.Author,
		Details:    details,
	}, true
}

// refineryHistoryOrNil reads the merge history off a refinery that may not
// exist. A nil queue answers an empty history, which the guard reads as "no
// landed merge found" and so reopens exactly as it did before this ticket —
// the open direction, deliberately, and the same one refineryMergedWork takes
// for the same reason.
func refineryHistoryOrNil(q *refinery.Refinery) []refinery.MergeRequest {
	if q == nil {
		return nil
	}
	return q.History()
}
