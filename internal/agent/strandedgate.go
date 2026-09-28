package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/strandedwork"
)

// StrandedWorkGate answers, at the moment of dispatch, whether a work item
// already has pushed work that the worker about to be spawned would ignore.
//
// WHY IT IS AT DISPATCH AND NOT ONLY AT STOP (mg-b468). Stopping a wedged
// polecat releases its claim and returns the item to available/ without
// consulting its branch, so an item whose worker finished and pushed re-enters
// the pool describing itself as unstarted. reportStrandedWorkOnRelease (below)
// makes that visible in the log and the event stream at the moment it happens —
// but a report is only as good as whoever reads it, and on 2026-08-05 the report
// did not exist and the re-dispatch went out three minutes after the stop.
// Dispatch is the harm moment: it is where duplicated work starts, and for a
// pre-registration branch it is where the corruption becomes silent and
// permanent. A guard has to be able to refuse there.
//
// WHY A RUNNING POLECAT IS NOT AN EXEMPTION. Nothing in this gate consults the
// registry, the witness store, or any other notion of liveness, and that is the
// finding doctor wrote into the ticket after missing three of six affected
// items: "a polecat is running" is not evidence that an item has no stranded
// pushed work, it is the PRECONDITION for it, because the re-dispatch IS the
// running polecat. See TestStrandedWorkRefusalIgnoresRunningPolecat.
//
// An interface so the handler is testable without a git repository, mirroring
// DispatchGate and DispatchPairingGate.
type StrandedWorkGate interface {
	// StrandedFindings returns the stranded branches attributable to workItemID
	// in repo, or an error if the question could not be answered.
	StrandedFindings(workItemID, repo, target string) ([]strandedwork.Finding, error)
}

// GitStrandedWorkGate is the production StrandedWorkGate: it inspects the
// polecat branches that could be attributed to the work item (see
// strandedwork.ItemCandidates) and keeps the stranded ones that are.
type GitStrandedWorkGate struct{}

// StrandedFindings implements StrandedWorkGate.
//
// ATTRIBUTION IS A UNION OF TWO IMPERFECT ROUTES, and neither is complete alone:
//
//   - the COMMIT SUBJECT, which by this repo's convention ends in "(mg-xxxx)".
//     Reliable for finished work; useless for a pre-registration commit, whose
//     subject is a prediction and often names no item.
//   - the BRANCH NAME. A polecat branch is polecat-<agent name>, and an agent's
//     name is derived from its work item's id — usually the bare suffix
//     ("mg-9a19" → "9a19"), sometimes with a letter in front ("mg-b468" →
//     "wb468"). Reliable for a pre-registration branch; wrong for a branch whose
//     agent was named after something else.
//
// The union covers both incident shapes. What it cannot cover is a branch that
// neither names its item in a commit nor carries the id in its name, and that
// limit is stated here rather than left to be discovered: this gate reduces the
// odds of a silent re-derivation, it does not eliminate them.
//
// The FAILURE DIRECTION IS OPEN, matching MGDispatchGate: no id, no repo, an
// unreadable repository, a target that will not resolve — all dispatch. A gate
// that refused every spawn whose repo it could not scan would halt the fleet
// over a git error, and `--id` is optional by design. The refusal only fires on
// a branch positively read from disk with commits positively absent from the
// target.
func (GitStrandedWorkGate) StrandedFindings(workItemID, repo, target string) ([]strandedwork.Finding, error) {
	if workItemID == "" || repo == "" {
		return nil, nil
	}
	// Refresh first, because stale remote-tracking refs make the answer wrong in
	// both directions: a target behind origin reports merged work as stranded, and
	// a branch pushed from another clone is invisible. A failed fetch does NOT
	// stop the check — the incident this gate exists for was a network outage, and
	// a guard that stands down when the network is down is off in exactly the
	// window it was built for.
	if _, err := strandedwork.Fetch(repo); err != nil {
		log.Printf("stranded-work gate: could not refresh %s (%v) — checking %s against refs "+
			"this clone last saw, which may be stale in both directions", repo, err, workItemID)
	}
	// Inspect only the branches that could be attributed to this item, not every
	// polecat branch in the repo (mg-110b, drellem2/pogo#175). The prefilter is
	// exact — see strandedwork.ItemCandidates for the argument — so the refusal
	// is the one Scan-then-filter would have given, at 0.1-2.4s instead of
	// minutes on a repo with ~970 polecat branches. AttributableTo below still
	// makes the final call; the candidates only decide what is worth a
	// `git cherry`.
	start := time.Now()
	targetRef, err := strandedwork.ResolveTarget(repo, target)
	if err != nil {
		return nil, fmt.Errorf("scanning %s for stranded polecat branches: %v", repo, err)
	}
	candidates, total, err := strandedwork.ItemCandidates(repo, targetRef, workItemID)
	if err != nil {
		return nil, fmt.Errorf("scanning %s for stranded polecat branches: %v", repo, err)
	}
	// targetRef is a full ref, and ResolveTarget only accepts a branch name, so
	// the scan is handed the caller's target and resolves it once itself.
	findings, errs := strandedwork.ScanBranches(repo, target, candidates)
	if len(errs) > 0 && len(findings) == 0 {
		return nil, fmt.Errorf("scanning %s for stranded polecat branches: %v", repo, errs[0])
	}
	for _, err := range errs {
		log.Printf("stranded-work gate: %v (the scan continued; other branches were still checked)", err)
	}
	var mine []strandedwork.Finding
	for _, f := range findings {
		if AttributableTo(f, workItemID) {
			mine = append(mine, f)
		}
	}
	// One line per dispatch, so the gate's cost is visible in pogod.log rather
	// than inferred from a slow spawn (drellem2/pogo#175 was diagnosed from the
	// outside because this line did not exist).
	log.Printf("stranded-work gate: %s in %s: inspected %d candidate(s) of %d polecat branch(es) "+
		"against %s, %d attributable stranded finding(s), took %s",
		workItemID, repo, len(candidates), total, targetRef, len(mine), time.Since(start).Round(time.Millisecond))
	return mine, nil
}

// AttributableTo reports whether a stranded branch belongs to workItemID, by
// either route described on StrandedFindings.
//
// The branch-name route matches on the item's SUFFIX (the id with its "mg-"
// stripped) appearing in the agent name, not on equality, because pogod hands
// out prefixed names when the bare suffix is taken. It requires the suffix to be
// at least three characters so a short or malformed id cannot match every branch
// in the repo — a gate that refuses everything is disarmed within the day.
func AttributableTo(f strandedwork.Finding, workItemID string) bool {
	if workItemID == "" {
		return false
	}
	if strings.EqualFold(f.WorkItemID, workItemID) {
		return true
	}
	return strandedwork.BranchMatchesItem(f.Branch, workItemID)
}

// SetStrandedWorkGate installs the gate consulted before a polecat is
// dispatched. Passing nil restores the default, which is GitStrandedWorkGate{}
// — functional, not a no-op, for the same reason SetDispatchGate's default is.
func (r *Registry) SetStrandedWorkGate(g StrandedWorkGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.strandedWorkGate = g
}

func (r *Registry) getStrandedWorkGate() StrandedWorkGate {
	r.mu.RLock()
	g := r.strandedWorkGate
	r.mu.RUnlock()
	if g == nil {
		return GitStrandedWorkGate{}
	}
	return g
}

// strandedWorkRefusal returns the refusal message for a work item that already
// has unmerged work, or "" when dispatch is allowed.
//
// The message is disposition-specific because the two cases need OPPOSITE
// handling, and a refusal that flattened them would be actively harmful: told
// only "there is a branch", a reader re-dispatches from the target, which is the
// one thing a pre-registration branch cannot survive.
//
// IT DOES NOT KEY ON AN OPEN WORK ITEM (mg-5ec6, answering a question raised for
// mg-be37 and never delivered). `pogo check-stranded` iterates OPEN items —
// strandwatch.OpenStatuses is {available, claimed, pending} — so a branch whose
// item was closed by a SIBLING's merge falls outside its domain by construction.
// That property is documented there and is NOT shared here: this gate reads
// nothing but the spawn request's id, the repo and the target, and refuses on a
// branch positively read from disk. A `done` item gets no refusal only because
// nothing dispatches at a `done` item — and dispatch is the only harm this gate
// exists to stop, so there is nothing left for it to be blind to. The residual
// (a closed item whose branch never landed) belongs to the release-time reporter
// below, which is equally status-blind. See
// TestSpawnStillRefusedWhenTheWorkItemIsAlreadyDone.
//
// THE SIBLING SHAPE IS COVERED, and it is worth naming because it was reported as
// an open hole. A polecat's submit failed terminally on a DNS error, its claim was
// released, and a second polecat was dispatched onto the same item four seconds
// later while the first one's branch sat pushed and unmerged; it re-derived the
// ticket for 43 minutes. Today that second dispatch is refused: the first
// polecat's branch is attributable to the item by BranchMatchesItem (polecat-x8af0
// against mg-8af0 matches on the suffix), so the gate fires whatever the sibling's
// agent letter is. See TestSpawnRefusedForASiblingsPushedBranchOnTheSameItem.
//
// IT NAMES THE PROVENANCE RATHER THAN ASSERTING "PUSHED" (mg-bfe0). The gate has
// covered local-only branches since it shipped — Scan reads refs/heads as well
// as refs/remotes/origin, and a polecat worktree's branch lives in the source
// repo's ref namespace, so a preserved worktree holding an unpushed
// pre-registration commit DOES refuse (TestSpawnRefusedForLocalOnlyPreRegistration
// is that case end to end). What it could not do was SAY so: every refusal read
// "already has PUSHED, UNMERGED work" and prescribed `pogo refinery submit`,
// which the refinery REFUSES for a branch that is not on origin (mg-586d).
//
// So for the one population whose work is not durable, the gate fired correctly
// and then told the reader two false things — that the work was safe on origin,
// and that a command which cannot run was the remedy. mg-bfe0 was filed believing
// the guard was blind here; it is not blind, it was misreporting, and a refusal
// nobody can act on is the failure mode a blind guard would have had anyway.
func (r *Registry) strandedWorkRefusal(workItemID, repo, target string) string {
	_, refusal := r.strandedWorkCheck(workItemID, repo, target)
	return refusal
}

// strandedWorkCheck is strandedWorkRefusal with the FINDINGS kept.
//
// The findings are returned because the refusal is no longer the only thing a
// caller does with them: --stranded-adopt bases the new worktree on the branch
// the gate found, so the dispatch path needs the branch itself and not only the
// sentence describing it (mg-ba32). Re-scanning to recover it would ask git the
// same question twice and — worse — could get a different answer than the
// refusal the operator is reading.
//
// findings is non-empty exactly when refusal is non-empty; both are empty when
// the gate allows the dispatch or could not answer.
func (r *Registry) strandedWorkCheck(workItemID, repo, target string) ([]strandedwork.Finding, string) {
	findings, err := r.getStrandedWorkGate().StrandedFindings(workItemID, repo, target)
	if err != nil {
		// Loud but not fatal — see the fail-open rationale on StrandedFindings.
		log.Printf("stranded-work gate: could not check work item %s in %s: %v — "+
			"dispatching WITHOUT the stranded-work check; if this item has pushed work on a "+
			"polecat branch, this spawn is about to re-derive it", workItemID, repo, err)
		return nil, ""
	}
	if len(findings) == 0 {
		return nil, ""
	}

	// Pre-registration first, whatever the scan order: it is the disposition
	// whose advice must not be crowded out.
	//
	// THE REMEDY CLAUSE COMES FROM THE SHARED TABLE (mg-8cda). Both messages
	// below used to end in an unconditional "Get the branch merged instead
	// (`pogo refinery submit ...`)" whatever the second opinion appended after
	// it — so from #174 on, this refusal printed "PARTLY PRESENT — check by hand"
	// and an imperative submit in one breath, the contradiction the mail had just
	// been cured of. The cell is measured first and chooses the clause.
	for _, f := range findings {
		if f.Disposition == strandedwork.DispositionPreRegistration {
			presence, note := strandedwork.Corroborate(f.Repo, f)
			cell := f.Cell(presence)
			// The remedy names only mechanisms that exist. That used to rule OUT a
			// re-dispatch here — a spawn always based its worktree on
			// origin/<target> (resolvePolecatBaseRef) and no flag based it on a
			// sha, so "continue from the pre-registration commit" was a hand-typed
			// `git worktree add` outside the dispatch path entirely. Since mg-ba32
			// it IS a spawn option: --stranded-adopt bases the worktree on this
			// branch's ref, so the commit arrives as an ancestor of the worker's own
			// branch, which is what makes it unamendable. The hand-typed form is no
			// longer spelled out here — Summary() above already names the
			// dispatch-from-the-sha route for a reader working outside pogod — and
			// the flag is what this refusal is in a position to offer.
			msg := fmt.Sprintf("work item %s already has %s, UNMERGED work, and it includes a "+
				"PRE-REGISTRATION commit: %s. A polecat spawned now would base its worktree on %s and write "+
				"its predictions AFTER seeing the results — the artifact would look identical to a valid "+
				"one, so nothing downstream could catch it. Instead, %s. "+
				"%s Either way %s already carries %s, and never amend that commit",
				workItemID, strandedwork.Provenance(f.Pushed), f.SummaryIn(cell), f.Target,
				strandedwork.RemedyPhrase(cell, f, workItemID),
				strandedExits(f)+".",
				f.Branch, f.PreRegistration.SHA[:min(12, len(f.PreRegistration.SHA))])
			if note != "" {
				msg += ". " + note
			}
			return findings, msg
		}
	}
	f := findings[0]
	presence, note := strandedwork.Corroborate(f.Repo, f)
	cell := f.Cell(presence)
	msg := fmt.Sprintf("work item %s already has %s, UNMERGED work: %s. A worker dispatched FROM THE "+
		"TARGET at it re-derives work that already exists — mg-9a19 lost 1026 lines that way. Instead, "+
		"%s. %s",
		workItemID, strandedwork.Provenance(f.Pushed), f.SummaryIn(cell),
		strandedwork.RemedyPhrase(cell, f, workItemID), strandedExits(f))
	// The second opinion travels WITH the refusal and never instead of it
	// (mg-5ec6). `git cherry` over-reports on a branch that landed through an
	// ordinary clean rebase, and this refusal is where that costs the most: told
	// only "there is unmerged work", an operator who can see the same subject on
	// main learns that the gate is wrong and reaches for --stranded-override on
	// reflex. Told the ratio, they can tell THIS refusal from the next one.
	// Deliberately not a suppression — see strandedwork.Corroborate.
	if note != "" {
		msg += ". " + note
	}
	return findings, msg
}

// strandedExits is the sentence that names BOTH ways past this gate, and it is
// the half mg-ba32 was filed for.
//
// The refusal used to offer one exit and assert a reason for it — "dispatch
// anyway with --stranded-override if this branch is genuinely spent". That
// sentence is a claim about the branch, and the gate has no way to know whether
// it is true: it fires just as hard on a branch that is finished and only needs
// a rebase, which is the case where the dispatch is not a re-derivation at all
// but the only route the work has left. An operator holding that case had to
// type a flag that said the opposite of what they meant, and after that nothing
// in the log could tell the two apart.
//
// So the exits are stated as the two dispositions they are, with what each one
// DOES to the branch rather than a reason the operator may not hold. Neither is
// recommended here: which one is right is a fact about the branch, and the
// reader is the one who can go and read it.
func strandedExits(f strandedwork.Finding) string {
	return fmt.Sprintf("If a worker really has to go here, say WHICH of the two you mean: "+
		"--stranded-adopt=\"<why>\" bases its worktree ON %s so the existing work is continued and "+
		"landed, and --stranded-override=\"<why>\" bases it on %s and leaves %s behind. Passing both "+
		"is refused",
		f.Ref, f.Target, f.Branch)
}

// reportStrandedWorkOnRelease records that a polecat being stopped left pushed
// work behind, at the moment its work item goes back to available/.
//
// IT NEVER BLOCKS THE RELEASE, and the asymmetry is deliberate. Refusing to
// release the claim would trade this ticket's failure (an item that reads as
// unstarted) for mg-fb13's (an item stranded in claimed/ under a dead pid,
// invisible to dispatch AND to stall-watch, recoverable only by hand). The item
// must return to the pool; what must stop is the pool describing it as
// untouched. So this reports, and the refusal lives at dispatch — see
// strandedWorkRefusal.
//
// Attribution here is EXACT rather than heuristic: pogod knows this agent's name
// and therefore its branch, which is the one moment in the item's life when the
// mapping is not a guess.
//
// IT NOW MAILS, and that was the defect this whole detector had (mg-be37). For
// three months its only outputs were the log line and the event below — both
// written from inside pogod after the agent process is gone, so neither reached
// an operator terminal or an agent inbox, and `work_item_stranded_push` had no
// consumer anywhere in the tree. It fired correctly on all five stranded
// branches of 2026-08-09 and the measured gap to somebody NOTICING was ~1h, 2.5h
// and ~3h. The addressee closes that gap; see strandedmail.go.
//
// The mail is sent LAST and its failure is swallowed by the sink, so the event
// is already durable whatever happens to mg. That ordering is the point: the
// improvement is a second output, not a new dependency of the first.
//
// IT IS AGENT-DRIVEN, NOT ITEM-DRIVEN, AND NEVER CONSULTS THE ITEM'S STATUS
// (mg-5ec6). It is called from releasePolecatClaim BEFORE that function so much
// as probes claimed/, so a polecat stopped while its item is already `done` —
// closed by a SIBLING's merge, the shape reported for mg-be37 and never answered
// — is reported exactly like any other. The status IS read, once, but only by
// sendStrandedAlert and only to WORD the alert: mg-1af2 made a closed item drop
// the do-not-dispatch paragraph, which is a different sentence and not a
// different decision. Nothing here can be silenced by a status, which is the
// property that makes the sibling shape visible; see
// TestReleaseReportsStrandedWorkWhenASiblingAlreadyClosedTheItem.
func (r *Registry) reportStrandedWorkOnRelease(a *Agent, reason string) {
	if a == nil || a.Type != TypePolecat || a.WorkItemID == "" || a.SourceRepo == "" {
		return
	}
	branch := gitgc.BranchPrefix + a.Name
	// Same best-effort refresh as the dispatch gate, and the same reason it must
	// not be fatal: a polecat wedged by an outage is stopped while the outage is
	// still on. A push made from this worktree already updated the local
	// remote-tracking ref, so the common case answers correctly with no network at
	// all — the fetch is for the pushes this clone did not make.
	if _, ferr := strandedwork.Fetch(a.SourceRepo); ferr != nil {
		log.Printf("agent %s: could not refresh %s before checking %s for stranded work (%v) — "+
			"checking against refs this clone last saw", a.Name, a.SourceRepo, branch, ferr)
	}
	f, err := strandedwork.Inspect(a.SourceRepo, branch, "")
	if err != nil {
		// "Could not check" must never be logged as "nothing was stranded".
		log.Printf("agent %s: could NOT check branch %s for stranded work while releasing %s: %v — "+
			"the item is back in available/ and this is NOT a report that it is unstarted",
			a.Name, branch, a.WorkItemID, err)
		return
	}
	if f.Disposition == strandedwork.DispositionCarried {
		// The suppression is LOGGED AND EMITTED rather than silent (mg-1af2). A
		// check that can only ever remove an alert has to be observable, or
		// "this branch was correctly identified as a pointer" and "the detector
		// stopped working" are the same silence — which is the shape of defect
		// this whole detector has already been fixed for twice.
		log.Printf("agent %s: branch %s has %d commit(s) %s lacks, but %s already carries and owns them "+
			"— NOT stranded, no alert sent (mg-1af2). %s",
			a.Name, f.Branch, len(f.Unmerged), f.Target, f.Carrier, f.Summary())
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_push_carried",
			Agent:      a.eventAgent(),
			WorkItemID: a.WorkItemID,
			Repo:       a.SourceRepo,
			Details: map[string]any{
				"branch":     f.Branch,
				"ref":        f.Ref,
				"target":     f.Target,
				"carrier":    f.Carrier,
				"carried_by": f.CarriedBy,
				"owner_item": f.WorkItemID,
				"unmerged":   len(f.Unmerged),
				"reason":     reason,
				"route":      RouteRelease,
			},
		})
		return
	}
	// Is the work a PR awaiting review (drellem2/pogo#147)? A probe failure
	// leaves f as it was and the alert below goes out — see
	// strandedwork.Finding.CheckOpenPR for why that direction is not negotiable.
	f.CheckOpenPR(a.SourceRepo, strandedPRProbe)
	if f.Disposition == strandedwork.DispositionAwaitingReview {
		// Logged and emitted, never silent — the mg-1af2 contract above, for the
		// same reason: a suppression nobody can count is indistinguishable from
		// a detector that stopped working.
		log.Printf("agent %s: branch %s has %d commit(s) %s lacks, but they are the head of open PR #%d "+
			"(%s) — awaiting review, NOT stranded, no alert sent (mg-dbb75). %s",
			a.Name, f.Branch, len(f.Unmerged), f.Target, f.PR, f.PRBranch, f.Summary())
		events.Emit(context.Background(), events.Event{
			EventType:  "work_item_push_awaiting_review",
			Agent:      a.eventAgent(),
			WorkItemID: a.WorkItemID,
			Repo:       a.SourceRepo,
			Details:    awaitingReviewDetails(f, reason, RouteRelease),
		})
		return
	}
	if !f.Stranded() {
		return
	}
	presence, note := strandedwork.Corroborate(a.SourceRepo, f)
	// The shared table's cell words the summary (mg-8cda): Summary() alone
	// is an unconditional submit, printed beside a second opinion that may
	// say "check by hand first".
	summary := f.SummaryIn(f.Cell(presence))
	log.Printf("agent %s: work item %s went back to available/ WITH PUSHED WORK BEHIND IT (%s). %s. %s",
		a.Name, a.WorkItemID, reason, summary, note)
	details := map[string]any{
		"branch":      f.Branch,
		"ref":         f.Ref,
		"pushed":      f.Pushed,
		"target":      f.Target,
		"disposition": string(f.Disposition),
		"unmerged":    len(f.Unmerged),
		"reason":      reason,
		"summary":     summary,
		"route":       RouteRelease,
	}
	addOriginDetails(details, f)
	addPresenceDetails(details, presence, note)
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_stranded_push",
		Agent:      a.eventAgent(),
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Details:    details,
	})
	sendStrandedAlert(StrandedAlert{
		Polecat:       a.Name,
		WorkItemID:    a.WorkItemID,
		Repo:          a.SourceRepo,
		Reason:        reason,
		Route:         RouteRelease,
		Finding:       f,
		Presence:      presence,
		SecondOpinion: note,
	})
}

// strandedPRProbe is the open-PR probe both agent-driven emitters apply before
// alerting (mg-dbb75). A variable so tests can answer for GitHub.
var strandedPRProbe strandedwork.PRProbe = strandedwork.GitHubOpenPR

// awaitingReviewDetails is the payload of work_item_push_awaiting_review, the
// event that makes the open-PR suppression countable.
func awaitingReviewDetails(f strandedwork.Finding, reason, route string) map[string]any {
	return map[string]any{
		"branch":     f.Branch,
		"ref":        f.Ref,
		"origin_ref": f.OriginRef,
		"target":     f.Target,
		"pr":         f.PR,
		"pr_branch":  f.PRBranch,
		"owner_item": f.WorkItemID,
		"unmerged":   len(f.Unmerged),
		"reason":     reason,
		"route":      route,
	}
}

// addOriginDetails puts the two probes that can only ever REMOVE part of an
// alert on its payload when they ran and are worth knowing: where on origin the
// work was found, and that the open-PR check could not answer. The second is
// the one that matters — an alert that went out because GitHub was unreachable
// has to be tellable from one that went out because there was no PR, or the
// fail-toward-alert direction (strandedwork.Finding.CheckOpenPR) is invisible.
func addOriginDetails(details map[string]any, f strandedwork.Finding) {
	if f.OriginRef != "" && f.OriginRef != f.Ref {
		details["origin_ref"] = f.OriginRef
	}
	if f.OriginProbeError != "" {
		details["origin_probe_error"] = f.OriginProbeError
	}
	if f.PRProbeError != "" {
		details["pr_probe_error"] = f.PRProbeError
	}
}

// addPresenceDetails puts the content second opinion on an event payload.
//
// The NOTE and the NUMBERS both go on, and neither substitutes for the other: a
// consumer counting how often `git cherry` over-reports needs the ratio, and a
// person reading one event needs the sentence that says what the ratio licenses
// (nothing, on its own — see strandedwork.Corroborate).
//
// `presence_measured` is emitted even when false, because "the branch was too
// small to measure" is a real answer and an absent key would read as the check
// not having run.
func addPresenceDetails(details map[string]any, p strandedwork.Presence, note string) {
	if note == "" {
		return
	}
	details["second_opinion"] = note
	details["presence_measured"] = p.Measured
	details["presence_added"] = p.Added
	details["presence_present"] = p.Present
	details["presence_suggests_landed"] = p.SuggestsLanded()
}
