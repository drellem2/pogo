package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/strandedwork"
	"github.com/drellem2/pogo/internal/strandwatch"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// Stranded work on an enth repo, for pogod's two agent-driven detectors: the
// release reporter (reportStrandedWorkOnRelease) and the restart sweep
// (ReportStrandedWorkAcrossRestart). enth-vcs slice 9b, mg-35ae4.
//
// WHY A BRANCH IN FRONT OF strandedwork.Inspect. Both detectors asked git about
// polecat-<name>, and an enth repo's source path is an enth read checkout with
// no .git. Measured on a sandboxed enthd before this file existed, with an
// item carrying one unlanded Change: the release reporter logged "could NOT
// check branch polecat-<name> … exit status 128" and sent no event and no mail;
// the sweep counted it Unjudged, log only. Neither reached a reader outside
// pogod.log. (The dispatch gate failed open the same way — see the note on
// GitStrandedWorkGate; it is outside this slice.)
//
// WHAT "STRANDED" MEANS ON ENTH. Unlanded, non-superseded Changes carrying the
// item's Pogo-Item trailer, on an item no live polecat is working. That is the
// listing `pogo check-stranded` reads (slice 6b, mg-28cbc: `enth changes
// --unlanded`, superseded Changes dropped by enthvcs.Backend.ListChanges), and
// the row is described and remedied by strandwatch's enth row, so the mail and
// the sweep report cannot disagree about one item.
//
// THERE IS NO "OPEN MR" CASE. Under self-landing (mg-5b904) the refinery
// refuses enth MRs, so nothing can be waiting in its queue; slice 9's
// CheckOpenMR suppression was removed with this file rather than left wired to
// a question whose answer is always no. The remedy points at the self-land
// path (`enth land … && pogo land --item`), never at `pogo refinery submit`.
//
// AN UNREADABLE LISTING ALERTS. "Could not ask enthd" is not "no work": the
// alert goes out saying so, as slice 9's unreadable-queue rule did — a check
// whose failure reads as clean is the defect these detectors exist to close.

// enthStrandedProbeTimeout bounds one item's unlanded listing. It sits on the
// claim-release path, so it must not hold a release open indefinitely.
const enthStrandedProbeTimeout = 30 * time.Second

// enthUnlandedForItem lists item's unlanded, non-superseded Changes on e: the
// trailer listing check-stranded joins on Pogo-Item, filtered to one item.
func enthUnlandedForItem(e vcs.EnthRepo, item string) ([]vcs.Change, error) {
	ctx, cancel := context.WithTimeout(context.Background(), enthStrandedProbeTimeout)
	defer cancel()
	return enthvcs.Backend{Repo: e}.ListChanges(ctx, item, "", true)
}

// enthJudgement is what one enth item's stranded check established.
type enthJudgement struct {
	// Changes are the item's unlanded, non-superseded Changes, oldest first.
	Changes []vcs.Change
	// ListErr is set when the listing could not be read. Nothing else is
	// meaningful then, and the caller alerts.
	ListErr error
	// InFlight names a live polecat on the item other than the one being
	// judged: its Changes are work in progress, not stranded work.
	InFlight string
}

// judgeEnthItem lists item's unlanded work on e and decides whether another
// live polecat owns it. self is the polecat being released or swept, which is
// never its own evidence of being in flight.
//
// A witness that cannot be read leaves InFlight to the registry alone: the
// error direction is toward alerting, never toward suppressing.
func (r *Registry) judgeEnthItem(e vcs.EnthRepo, item, self string) enthJudgement {
	cs, err := enthUnlandedForItem(e, item)
	if err != nil {
		return enthJudgement{ListErr: err}
	}
	j := enthJudgement{Changes: cs}
	if len(cs) == 0 {
		return j
	}
	for _, p := range r.Polecats() {
		if p.Name != self && strings.TrimSpace(p.WorkItemID) == item {
			j.InFlight = p.Name
			return j
		}
	}
	witnessed, werr := WitnessedPolecatWorkItems()
	if werr != nil {
		log.Printf("strandedwork: enth item %s: could not read the witness to ask whether another polecat "+
			"is on it (%v) — judging from the registry alone, which can only over-report", item, werr)
	}
	for name, id := range witnessed {
		if name != self && strings.TrimSpace(id) == item {
			j.InFlight = name
			return j
		}
	}
	return j
}

// EnthStranded is the enth half of a StrandedAlert: what the trailer listing
// returned, or why it could not be read.
type EnthStranded struct {
	Remote string
	Repo   string
	// Trunk is the ref the work lands on.
	Trunk string
	// Work is the item's unlanded work, as `pogo check-stranded` describes it.
	// Nil exactly when ListingError is set.
	Work *strandwatch.EnthWork
	// ListingError is why the listing could not be read.
	ListingError string
}

// newEnthStranded builds the alert's enth half from a judgement.
func newEnthStranded(e vcs.EnthRepo, j enthJudgement) *EnthStranded {
	out := &EnthStranded{Remote: e.Remote, Repo: e.Repo, Trunk: e.Trunk}
	if out.Trunk == "" {
		out.Trunk = vcs.DefaultEnthTrunk
	}
	if j.ListErr != nil {
		// enth's refusal arrives with its JSON error line attached; one line
		// keeps the mail's field and the event payload readable.
		out.ListingError = strings.Join(strings.Fields(j.ListErr.Error()), " ")
		return out
	}
	out.Work = strandwatch.NewEnthWork(strandwatch.EnthSource{Remote: e.Remote, Repo: e.Repo, Trunk: out.Trunk}, j.Changes)
	return out
}

// enthFinding is the strandedwork.Finding an enth alert carries, so every
// field the shared alert plumbing reads (the branch label in a delivery
// failure, the target) names the enth work instead of a branch that does not
// exist. Pushed is true because an enth Change is durable on enthd the moment
// it is made (P4); nothing about it is local-only.
func enthFinding(repo, item string, es *EnthStranded) strandedwork.Finding {
	f := strandedwork.Finding{
		Repo:        repo,
		Target:      es.Trunk,
		Pushed:      true,
		Found:       true,
		Disposition: strandedwork.DispositionResubmit,
		WorkItemID:  item,
	}
	if es.Work == nil {
		f.Branch = "enth work of " + item + " (listing unreadable)"
		return f
	}
	f.Branch = strandwatch.EnthLabel(es.Work)
	f.Ref = es.Work.Changes[len(es.Work.Changes)-1]
	if !es.Work.Newest.IsZero() {
		f.TipTime = es.Work.Newest
	}
	return f
}

// trunkName is the trunk as the remedy's --target names it.
func (es *EnthStranded) trunkName() string {
	return strings.TrimPrefix(es.Trunk, "refs/heads/")
}

// remedy is the one line to run: strandwatch's enth remedy (the self-land
// path), or the listing to reproduce when it could not be read.
func (es *EnthStranded) remedy(item, repo, status string) string {
	if es.Work == nil {
		return fmt.Sprintf("enth --remote %s --repo %s changes --unlanded --trailer %s=%s   # reproduce the failure; "+
			"until it answers, whether %s has unlanded work is UNKNOWN, not none",
			es.Remote, es.Repo, vcs.TrailerItem, item, item)
	}
	row := strandwatch.Row{
		Item:   strandwatch.Item{ID: item, Repo: repo, Status: status},
		Branch: strandwatch.EnthLabel(es.Work),
		Kind:   strandwatch.KindStranded,
		Target: es.trunkName(),
		Enth:   es.Work,
	}
	return row.Remedy()
}

// summary is the one-sentence description an enth alert's event carries.
func (es *EnthStranded) summary(item string) string {
	if es.Work == nil {
		return fmt.Sprintf("the unlanded enth listing for %s on %s could not be read (%s) — whether it has "+
			"stranded work is unknown, and this is NOT a report that it has none", item, es.Repo, es.ListingError)
	}
	return fmt.Sprintf("%s has %d unlanded enth Change(s) on %s (%s) that never landed on %s, and no live "+
		"polecat is on it. An enth repo is self-landed (mg-5b904): nothing lands this work unless someone "+
		"lands it with `enth land` and records it with `pogo land --item=%s`",
		item, len(es.Work.Changes), es.Repo, strings.Join(shortIDs(es.Work.Changes), ", "), es.Trunk, item)
}

func shortIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = shortSHA(id)
	}
	return out
}

// details is the work_item_stranded_push payload for an enth finding. It
// shares the git payload's keys where they mean the same thing (branch,
// target, unmerged, reason, route, summary) so a consumer counting strandings
// counts both, and adds "vcs": "enth" plus the Change ids.
func (es *EnthStranded) details(item, reason, route string) map[string]any {
	d := map[string]any{
		"vcs":       "enth",
		"enth":      es.Remote,
		"enth_repo": es.Repo,
		"target":    es.Trunk,
		"reason":    reason,
		"route":     route,
		"summary":   es.summary(item),
	}
	if es.Work == nil {
		d["disposition"] = "unjudged"
		d["listing_error"] = es.ListingError
		return d
	}
	d["disposition"] = string(strandedwork.DispositionResubmit)
	d["branch"] = strandwatch.EnthLabel(es.Work)
	d["ref"] = es.Work.Changes[len(es.Work.Changes)-1]
	d["changes"] = es.Work.Changes
	d["unmerged"] = len(es.Work.Changes)
	d["stacks"] = es.Work.Stacks
	if len(es.Work.Workers) > 0 {
		d["workers"] = es.Work.Workers
	}
	return d
}

// reportEnthStrandedOnRelease is reportStrandedWorkOnRelease for an enth repo.
// Like the git path it never blocks the release; it reports.
func (r *Registry) reportEnthStrandedOnRelease(e vcs.EnthRepo, a *Agent, reason string) {
	j := r.judgeEnthItem(e, a.WorkItemID, a.Name)
	switch {
	case j.ListErr != nil:
		log.Printf("agent %s: could NOT list %s's unlanded enth Changes on %s while releasing it: %v — "+
			"ALERTING: the item is back in available/ and this is NOT a report that it is unstarted",
			a.Name, a.WorkItemID, e.Repo, j.ListErr)
	case len(j.Changes) == 0:
		return
	case j.InFlight != "":
		log.Printf("agent %s: %s has %d unlanded enth Change(s), but polecat %s is live on the item — "+
			"work in progress, NOT stranded, no alert sent (mg-35ae4)",
			a.Name, a.WorkItemID, len(j.Changes), j.InFlight)
		return
	}
	es := newEnthStranded(e, j)
	if j.ListErr == nil {
		log.Printf("agent %s: work item %s went back to available/ WITH UNLANDED ENTH WORK BEHIND IT (%s). %s",
			a.Name, a.WorkItemID, reason, es.summary(a.WorkItemID))
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_stranded_push",
		Agent:      a.eventAgent(),
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Details:    es.details(a.WorkItemID, reason, RouteRelease),
	})
	sendStrandedAlert(StrandedAlert{
		Polecat:    a.Name,
		WorkItemID: a.WorkItemID,
		Repo:       a.SourceRepo,
		Reason:     reason,
		Route:      RouteRelease,
		Finding:    enthFinding(a.SourceRepo, a.WorkItemID, es),
		Enth:       es,
	})
}

// sweepEnthCandidate judges one unadoptable polecat on an enth repo for the
// restart sweep, updating rep. The listing failing is counted Unjudged AND
// alerted: on enth the sweep has no other place this work is visible.
func (r *Registry) sweepEnthCandidate(e vcs.EnthRepo, c UnadoptablePolecat, rep *StrandedSweepReport) {
	if strings.TrimSpace(c.WorkItemID) == "" {
		// An empty item would list every unlanded Change in the repo, and none
		// of it could be attributed to this polecat.
		rep.Skipped++
		log.Printf("strandedwork: startup sweep cannot check polecat %s on enth repo %s: its witness records "+
			"no work item, and enth work is found by its %s trailer — NOT a report that it is clean",
			c.Name, c.SourceRepo, vcs.TrailerItem)
		return
	}
	j := r.judgeEnthItem(e, c.WorkItemID, c.Name)
	if j.InFlight == "" && c.Alive && j.ListErr == nil && len(j.Changes) > 0 {
		// The swept polecat itself is still running: under self-landing it can
		// still land its own Changes, so they are work in progress.
		j.InFlight = c.Name
	}
	switch {
	case j.ListErr != nil:
		rep.Unjudged++
		log.Printf("strandedwork: startup sweep could NOT list %s's unlanded enth Changes on %s (polecat %s): %v — "+
			"UNJUDGED, and alerted, because on enth this listing is the only place the work is visible",
			c.WorkItemID, e.Repo, c.Name, j.ListErr)
	case len(j.Changes) == 0:
		rep.Clean++
		return
	case j.InFlight != "":
		rep.InFlight++
		log.Printf("strandedwork: startup sweep found %s's %d unlanded enth Change(s) with polecat %s live on "+
			"the item — work in progress, NOT stranded (mg-35ae4)", c.WorkItemID, len(j.Changes), j.InFlight)
		return
	default:
		rep.Stranded++
	}
	es := newEnthStranded(e, j)
	reason := "polecat outlived a pogod restart, so the release gate could never fire for it"
	if j.ListErr == nil {
		log.Printf("strandedwork: STARTUP SWEEP found unlanded enth work behind unadoptable polecat %s "+
			"(work item %s, %s). %s", c.Name, c.WorkItemID, reason, es.summary(c.WorkItemID))
	}
	details := es.details(c.WorkItemID, reason, RouteRestartSweep)
	details["polecat_pid"] = c.PID
	details["still_alive"] = c.Alive
	details["witnessed_at"] = c.StartTime.Format(time.RFC3339)
	events.Emit(context.Background(), events.Event{
		EventType:  "work_item_stranded_push",
		Agent:      "cat-" + c.Name,
		WorkItemID: c.WorkItemID,
		Repo:       c.SourceRepo,
		Details:    details,
	})
	sendStrandedAlert(StrandedAlert{
		Polecat:    c.Name,
		WorkItemID: c.WorkItemID,
		Repo:       c.SourceRepo,
		Reason:     reason,
		Route:      RouteRestartSweep,
		StillAlive: c.Alive,
		Finding:    enthFinding(c.SourceRepo, c.WorkItemID, es),
		Enth:       es,
	})
}

// enthMessage renders an enth alert. It keeps the subject's "[stranded-push]"
// prefix and "do NOT dispatch" verbatim, as the git rendering does, because
// docs, filters and stallwatch key on them.
func (a StrandedAlert) enthMessage() (subject, body string) {
	es := a.Enth
	item := a.WorkItemID
	var b strings.Builder
	if es.Work == nil {
		subject = fmt.Sprintf("[stranded-push] could not list %s's enth work after %s stopped — do NOT dispatch at %s until it is checked",
			item, a.Polecat, item)
		fmt.Fprintf(&b, "pogod tried to check whether polecat %s left unlanded enth work behind, and could\n"+
			"not ask enthd. That is NOT a report that there is none.\n\n", a.Polecat)
	} else {
		n := len(es.Work.Changes)
		subject = fmt.Sprintf("[stranded-push] %s left %d unlanded enth Change(s) behind — do NOT dispatch at %s",
			a.Polecat, n, item)
		if a.itemIsClosed() {
			subject = fmt.Sprintf("[stranded-push] %s is %s but its enth work never landed — %s left %d unlanded Change(s) behind",
				item, a.ItemStatus, a.Polecat, n)
			fmt.Fprintf(&b, "A polecat's enth Changes carry work the trunk does not have, and the item they\n"+
				"belong to was closed anyway.\n\n")
		} else {
			fmt.Fprintf(&b, "A polecat's enth Changes carry work the trunk does not have, and the item they\n"+
				"belong to is back in the pool describing itself as untouched.\n\n")
		}
	}
	fmt.Fprintf(&b, "Polecat:    %s\n", a.Polecat)
	if a.ItemStatus != "" {
		fmt.Fprintf(&b, "Work item:  %s (status %s)\n", item, a.ItemStatus)
	} else {
		fmt.Fprintf(&b, "Work item:  %s\n", item)
	}
	fmt.Fprintf(&b, "Repo:       %s (enth: %s on %s)\n", a.Repo, es.Repo, es.Remote)
	fmt.Fprintf(&b, "Trunk:      %s\n", es.Trunk)
	if es.Work == nil {
		fmt.Fprintf(&b, "Listing:    could not be read (%s)\n", es.ListingError)
	} else {
		fmt.Fprintf(&b, "Unlanded:   %d Change(s) carrying %s=%s: %s\n", len(es.Work.Changes), vcs.TrailerItem, item,
			strings.Join(shortIDs(es.Work.Changes), ", "))
		if len(es.Work.Workers) > 0 {
			fmt.Fprintf(&b, "Workers:    %s (%d stack(s))\n", strings.Join(es.Work.Workers, ", "), es.Work.Stacks)
		}
	}
	fmt.Fprintf(&b, "Reason:     %s\n\n", a.Reason)

	if es.Work == nil {
		fmt.Fprintf(&b, "WHAT TO DO — reproduce the listing, and do not dispatch until it answers:\n\n")
	} else {
		fmt.Fprintf(&b, "WHAT TO DO — land it (or retire it), do not dispatch from the trunk:\n\n")
	}
	fmt.Fprintf(&b, "    %s\n\n", es.remedy(item, a.Repo, a.ItemStatus))
	if es.Work == nil {
		// Nothing below may claim the work exists: it is unknown. The
		// prohibition stays, because a dispatch is the one move that cannot
		// be undone if it does.
		fmt.Fprintf(&b, "DO NOT DISPATCH A WORKER AT %s UNTIL THE LISTING ANSWERS. If the item has unlanded\n"+
			"Changes, a worker started from the trunk re-derives them, and nothing else on the\n"+
			"board says so.\n\n", item)
		b.WriteString("The same fact is on the event spine as work_item_stranded_push (disposition\n" +
			"unjudged); this mail is the half that is observable from outside the daemon (mg-be37).\n")
		return subject, b.String()
	}
	fmt.Fprintf(&b, "%s\n\n", wrapAt("An enth repo is self-landed (mg-5b904): the refinery refuses enth "+
		"MRs, so there is no queue holding this work and `pogo refinery submit` is not a remedy. Nothing "+
		"lands it unless someone runs the land above, or a worker adopts it with spawn-polecat "+
		"--stranded-adopt and lands it itself. Superseded Changes (Pogo-Supersedes) and landed ones are "+
		"not counted here.", 88))

	if a.itemIsClosed() {
		fmt.Fprintf(&b, "THE ITEM IS ALREADY %s, SO THIS IS NOT A RE-DISPATCH RISK. What IS wrong is that\n"+
			"%s was closed with Changes that never reached %s — either they still have to land\n"+
			"(by the remedy above), or they were superseded and should be retired with a\n"+
			"Pogo-Supersedes Change. Establish which.\n\n",
			strings.ToUpper(a.ItemStatus), item, es.Trunk)
	} else if item != "" {
		fmt.Fprintf(&b, "DO NOT DISPATCH A WORKER AT %s FROM THE TRUNK. The work already exists as Changes on\n"+
			"enthd; a worker started from the trunk re-derives it. The board shows the item as\n"+
			"available and priority-wake will advertise it — that advice is wrong for as long as\n"+
			"these Changes are unlanded.\n\n", item)
	}

	if a.Route == RouteRestartSweep {
		fmt.Fprintf(&b, "HOW THIS WAS FOUND. By the startup sweep, not the release gate: this polecat outlived\n"+
			"a pogod restart, so no release could ever be reported for it. The finding may be hours\n"+
			"or days old.\n\n")
	} else {
		fmt.Fprintf(&b, "HOW THIS WAS FOUND. pogod released this polecat's claim and listed the item's\n"+
			"unlanded enth Changes on the way out (internal/agent/enthstranded.go).\n\n")
	}
	b.WriteString("The same fact is on the event spine as work_item_stranded_push; this mail is the\n" +
		"half that is observable from outside the daemon that found it (mg-be37).\n")
	return subject, b.String()
}
