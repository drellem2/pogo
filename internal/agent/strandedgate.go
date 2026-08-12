package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

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

// GitStrandedWorkGate is the production StrandedWorkGate: it scans repo's
// polecat branches and keeps the ones attributable to the work item.
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
	findings, errs := strandedwork.Scan(repo, target)
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
// has pushed, unmerged work, or "" when dispatch is allowed.
//
// The message is disposition-specific because the two cases need OPPOSITE
// handling, and a refusal that flattened them would be actively harmful: told
// only "there is a branch", a reader re-dispatches from the target, which is the
// one thing a pre-registration branch cannot survive.
func (r *Registry) strandedWorkRefusal(workItemID, repo, target string) string {
	findings, err := r.getStrandedWorkGate().StrandedFindings(workItemID, repo, target)
	if err != nil {
		// Loud but not fatal — see the fail-open rationale on StrandedFindings.
		log.Printf("stranded-work gate: could not check work item %s in %s: %v — "+
			"dispatching WITHOUT the stranded-work check; if this item has pushed work on a "+
			"polecat branch, this spawn is about to re-derive it", workItemID, repo, err)
		return ""
	}
	if len(findings) == 0 {
		return ""
	}

	// Pre-registration first, whatever the scan order: it is the disposition
	// whose advice must not be crowded out.
	for _, f := range findings {
		if f.Disposition == strandedwork.DispositionPreRegistration {
			// The remedy names only mechanisms that exist. A polecat spawn always
			// bases its worktree on origin/<target> (resolvePolecatBaseRef) — there
			// is no flag that bases it on a sha — so "re-dispatch from the
			// pre-registration commit" is not a spawn option, it is a checkout of
			// the branch that already carries that commit.
			return fmt.Sprintf("work item %s already has PUSHED, UNMERGED work, and it includes a "+
				"PRE-REGISTRATION commit: %s. A polecat spawned now would base its worktree on %s and write "+
				"its predictions AFTER seeing the results — the artifact would look identical to a valid "+
				"one, so nothing downstream could catch it. Resubmit the branch instead "+
				"(`pogo refinery submit %s --repo=%s`); if the analysis genuinely has to be redone, CONTINUE "+
				"ON %s (`git -C %s worktree add <dir> %s`), which already carries %s, and never amend that "+
				"commit. Dispatch anyway with --stranded-override=\"<why>\" only if this branch is spent",
				workItemID, f.Summary(), f.Target, f.Branch, f.Repo,
				f.Branch, f.Repo, f.Branch, f.PreRegistration.SHA[:min(12, len(f.PreRegistration.SHA))])
		}
	}
	f := findings[0]
	return fmt.Sprintf("work item %s already has PUSHED, UNMERGED work: %s. Dispatching a worker at it "+
		"re-derives work that already exists — mg-9a19 lost 1026 lines that way. Resubmit the branch "+
		"instead. Dispatch anyway with --stranded-override=\"<why>\" if this branch is genuinely spent",
		workItemID, f.Summary())
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
	if !f.Stranded() {
		return
	}
	log.Printf("agent %s: work item %s went back to available/ WITH PUSHED WORK BEHIND IT (%s). %s",
		a.Name, a.WorkItemID, reason, f.Summary())
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_stranded_push",
		Agent:      a.eventAgent(),
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Details: map[string]any{
			"branch":      f.Branch,
			"ref":         f.Ref,
			"pushed":      f.Pushed,
			"target":      f.Target,
			"disposition": string(f.Disposition),
			"unmerged":    len(f.Unmerged),
			"reason":      reason,
			"summary":     f.Summary(),
			"route":       RouteRelease,
		},
	})
	sendStrandedAlert(StrandedAlert{
		Polecat:    a.Name,
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Reason:     reason,
		Route:      RouteRelease,
		Finding:    f,
	})
}
