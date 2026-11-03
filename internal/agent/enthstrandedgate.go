package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/vcs"
)

// The spawn-time stranded-work gate on an enth repo (mg-e2ab1, the follow-up
// slice 9b named and left out).
//
// WHY A BRANCH IN FRONT OF THE GIT GATE. GitStrandedWorkGate asks git about
// polecat branches, and an enth project's path is an enth read checkout with
// no .git. Measured on a sandboxed enthd by mg-35ae4, before its change: Fetch
// and ResolveTarget failed, pogod logged "dispatching WITHOUT the stranded-work
// check", and an enth item with unlanded Changes was dispatched from the trunk
// with nothing refusing it. So when the project record says enth, the gate
// judges from the listing the release reporter and the restart sweep already
// use (judgeEnthItem: `enth changes --unlanded`, landed and superseded Changes
// dropped), and the git gate is not consulted at all. Every git repo goes on
// exactly as before.
//
// LIVENESS IS NOT AN EXEMPTION HERE EITHER. judgeEnthItem says whether another
// polecat is live on the item, because the release reporter must not alert
// about work in progress. The dispatch gate refuses anyway, for
// StrandedWorkGate's reason: a second worker on an item whose Changes a live
// polecat is still writing duplicates them just as surely. The refusal names
// the live polecat so the reader knows which case they are in.
//
// THE EXITS ARE THE GIT GATE'S. --stranded-override starts the worker from the
// trunk and leaves the Changes behind; --stranded-adopt makes its checkout at
// the stack's top Change. The adopt half is resolved later in the spawn, by
// resolveEnthAdoption, from the same trailer listing — never by
// adoptableFinding, whose findings name branches an enth repo does not have.
//
// AN UNREADABLE LISTING FAILS OPEN, AND SAYS SO. The direction matches the git
// gate's (a gate that refused every spawn whose repo it could not read would
// halt the fleet over one enthd hiccup — and a checkout from an unreachable
// enthd fails on its own a few lines later). What it must not do is fail open
// SILENTLY, which is what the git gate's log line amounted to: slice 9's rule,
// as the release reporter applies it, is that "could not ask enthd" is alerted
// and never rendered as "no work". So a dispatch that went out unchecked sends
// the [stranded-push] mail and a work_item_stranded_push event (route
// "dispatch", disposition "unjudged") naming the polecat it let through, and
// the spawn response carries the same sentence as its warning. The alert is
// sent only once the spawn has succeeded: before that, nothing was dispatched,
// and a mail saying something was would be false.

// RouteDispatch is the stranded alert sent by the enth dispatch gate when the
// item's listing could not be read and the dispatch went out unchecked.
const RouteDispatch = "dispatch"

// enthStrandedCheck is the dispatch gate's judgement on an enth repo.
//
// refusal is non-empty when the item has unlanded, non-superseded Changes.
// unchecked is non-nil when the listing could not be read: the dispatch is
// allowed, and the caller must alert once it has gone out
// (alertEnthDispatchUnchecked). Both are empty when there is no work item to
// list — an empty item would list every unlanded Change in the repo — or when
// the item has no unlanded work.
func (r *Registry) enthStrandedCheck(e vcs.EnthRepo, item, self, repo string) (refusal string, unchecked *EnthStranded) {
	item = strings.TrimSpace(item)
	if item == "" {
		return "", nil
	}
	j := r.judgeEnthItem(e, item, self)
	if j.ListErr != nil {
		es := newEnthStranded(e, j)
		log.Printf("stranded-work gate: could NOT list %s's unlanded enth Changes on %s: %v — "+
			"dispatching WITHOUT the stranded-work check, and ALERTING once the spawn is out "+
			"(this is NOT a report that the item has no unlanded work)", item, e.Repo, j.ListErr)
		return "", es
	}
	if len(j.Changes) == 0 {
		log.Printf("stranded-work gate: %s on enth repo %s: no unlanded, non-superseded Change carries %s=%s",
			item, e.Repo, vcs.TrailerItem, item)
		return "", nil
	}
	return enthStrandedRefusal(item, repo, newEnthStranded(e, j), j.InFlight), nil
}

// enthStrandedRefusal is the 409 text for an enth item with unlanded work. It
// names the Changes, the self-land remedy, and both exits with what each one
// does to the Changes.
func enthStrandedRefusal(item, repo string, es *EnthStranded, inFlight string) string {
	w := es.Work
	top := w.Changes[len(w.Changes)-1]
	var b strings.Builder
	fmt.Fprintf(&b, "work item %s already has %d UNLANDED enth Change(s) on %s carrying %s=%s: %s (top %s)",
		item, len(w.Changes), es.Repo, vcs.TrailerItem, item, strings.Join(shortIDs(w.Changes), ", "), top)
	if len(w.Workers) > 0 {
		fmt.Fprintf(&b, ", by %s", strings.Join(w.Workers, ", "))
	}
	fmt.Fprintf(&b, ". They never landed on %s, and landed or superseded (%s) Changes are not counted. ",
		es.Trunk, vcs.TrailerSupersedes)
	if inFlight != "" {
		fmt.Fprintf(&b, "Polecat %s is live on the item, so these may be work in progress — a second worker "+
			"duplicates them all the same. ", inFlight)
	}
	b.WriteString("A worker dispatched FROM THE TRUNK at it re-derives work that already exists. ")
	fmt.Fprintf(&b, "An enth repo is self-landed (mg-5b904), so nothing lands these unless someone does: "+
		"land them with `%s`. ", es.remedy(item, repo, ""))
	fmt.Fprintf(&b, "If a worker really has to go here, say WHICH of the two you mean: "+
		"--stranded-adopt=\"<why>\" makes its enth checkout AT %s so the existing work is continued and "+
		"landed, and --stranded-override=\"<why>\" starts it from %s and leaves those Changes behind. "+
		"Passing both is refused", top, es.Trunk)
	return b.String()
}

// enthDispatchUncheckedWarning is the sentence the spawn response carries
// when the enth listing could not be read and the dispatch went out anyway.
func enthDispatchUncheckedWarning(item, repo string, es *EnthStranded) string {
	return fmt.Sprintf("the stranded-work gate could NOT list %s's unlanded enth Changes on %s (%s) and let "+
		"this dispatch through UNCHECKED — this is NOT a report that the item has none. Reproduce the "+
		"listing: %s", item, es.Repo, es.ListingError, es.remedy(item, repo, ""))
}

// alertEnthDispatchUnchecked reports a dispatch the enth gate let through
// without an answer from enthd: the event, then the mail. Called only once
// the spawn has succeeded, so the polecat it names exists.
func alertEnthDispatchUnchecked(spawnReq SpawnPolecatAPIRequest, es *EnthStranded) {
	reason := "the dispatch gate could not list the item's unlanded enth Changes, and dispatched anyway"
	details := es.details(spawnReq.Id, reason, RouteDispatch)
	details["polecat"] = spawnReq.Name
	details["dispatched"] = true
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_stranded_push",
		Agent:      "cat-" + spawnReq.Name,
		WorkItemID: spawnReq.Id,
		Repo:       spawnReq.Repo,
		Details:    details,
	})
	sendStrandedAlert(StrandedAlert{
		Polecat:    spawnReq.Name,
		WorkItemID: spawnReq.Id,
		Repo:       spawnReq.Repo,
		Reason:     reason,
		Route:      RouteDispatch,
		Finding:    enthFinding(spawnReq.Repo, spawnReq.Id, es),
		Enth:       es,
	})
}

// enthDispatchUncheckedMessage renders the unchecked-dispatch alert. Unlike
// the release and sweep renderings of an unreadable listing, it cannot say
// "do not dispatch": the dispatch has already happened. It says what the
// gate did, and what to do if the listing, once it answers, shows work.
func (a StrandedAlert) enthDispatchUncheckedMessage() (subject, body string) {
	es, item := a.Enth, a.WorkItemID
	subject = fmt.Sprintf("[stranded-push] could not list %s's enth work at dispatch — %s was dispatched WITHOUT the stranded-work check",
		item, a.Polecat)
	var b strings.Builder
	fmt.Fprintf(&b, "pogod dispatched polecat %s at %s, and its stranded-work gate could not ask enthd\n"+
		"whether the item already has unlanded Changes. The gate FAILED OPEN: the dispatch went\n"+
		"out unchecked. That is NOT a report that there is no unlanded work.\n\n", a.Polecat, item)
	fmt.Fprintf(&b, "Polecat:    %s\n", a.Polecat)
	if a.ItemStatus != "" {
		fmt.Fprintf(&b, "Work item:  %s (status %s)\n", item, a.ItemStatus)
	} else {
		fmt.Fprintf(&b, "Work item:  %s\n", item)
	}
	fmt.Fprintf(&b, "Repo:       %s (enth: %s on %s)\n", a.Repo, es.Repo, es.Remote)
	fmt.Fprintf(&b, "Trunk:      %s\n", es.Trunk)
	fmt.Fprintf(&b, "Listing:    could not be read (%s)\n\n", es.ListingError)
	fmt.Fprintf(&b, "WHAT TO DO — reproduce the listing:\n\n    %s\n\n", es.remedy(item, a.Repo, a.ItemStatus))
	fmt.Fprintf(&b, "%s\n\n", wrapAt("If it shows unlanded Changes from an earlier worker, "+a.Polecat+
		" started from the trunk and is re-deriving them. Stop it (`pogo agent stop "+a.Polecat+"`) and "+
		"either land the Changes (`enth land … && pogo land --item="+item+"`) or re-dispatch with "+
		"spawn-polecat --stranded-adopt so the worker continues them. If it shows none, nothing is wrong.", 88))
	b.WriteString("HOW THIS WAS FOUND. By the dispatch gate (internal/agent/enthstrandedgate.go), which\n" +
		"fails open on an unreadable listing and alerts instead of refusing (mg-e2ab1).\n\n")
	b.WriteString("The same fact is on the event spine as work_item_stranded_push (route dispatch,\n" +
		"disposition unjudged); this mail is the half that is observable from outside the daemon (mg-be37).\n")
	return subject, b.String()
}
