// Package strandedwork answers one question about a work item: does it have
// pushed work that nobody is going to merge?
//
// WHY IT EXISTS (mg-b468). Stopping a polecat releases its macguffin claim and
// returns the item to available/ (internal/agent/claimrelease.go). That path
// consults nothing about the polecat's BRANCH, so an item whose worker pushed
// finished commits before it wedged goes back into the pool describing itself as
// unstarted. Dispatch then hands it to a fresh worker, which starts from the
// target branch and re-derives work that is already sitting on a remote ref.
//
// On 2026-08-05 that happened twice in one day. mg-9a19's audit was complete and
// its refinery gate had PASSED — the merge failed only at stage=fetch on a
// transient ssh error — and the re-dispatch spent its life duplicating 1026
// lines. Five more items were one dispatch away from something worse; see
// DispositionPreRegistration.
//
// TWO DISPOSITIONS, BECAUSE THEY NEED OPPOSITE HANDLING. A stranded branch is
// not one situation:
//
//	RESUBMIT           the commits are ordinary finished work. Submit the branch
//	                   to the refinery. Do NOT dispatch a worker at it.
//	PRE-REGISTRATION   the branch carries commits that recorded predictions
//	                   BEFORE the analysis existed. A re-dispatch must branch
//	                   FROM that commit and must never amend it.
//
// Collapsing the two would be worse than not checking at all, because the second
// one fails silently: a worker that starts from the target writes its predictions
// after seeing the results, and the artifact it produces is INDISTINGUISHABLE
// from a valid one. The control the pre-registration exists to establish is gone
// and nothing downstream can tell.
//
// AND A THIRD THAT LOOKS EXACTLY LIKE THE FIRST (mg-1af2):
//
//	CARRIED            the branch has commits the target lacks, but another
//	                   branch already carries them AND owns them. Nothing is
//	                   stranded; this branch is a pointer.
//
// A reviewer reviews by checking the branch under review out, so its own
// worktree branch ends up pointing at the builder's head. Every commit on it is
// then work the target does not have — true, and not stranding. Left
// unclassified, that read as RESUBMIT on every review polecat that ever ran, and
// the remedy it printed (`refinery submit <reviewer branch> --author=<reviewer
// item>`) would have submitted the builder's work twice under the wrong
// authorship. See DispositionCarried and ownerAmong; the discriminator is
// OWNERSHIP, not mere containment, because containment is symmetric and would
// silence the builder as readily as the reviewer.
//
// WHAT THIS PACKAGE DELIBERATELY DOES NOT KNOW. It has no notion of whether a
// polecat is running. That is not an omission — it is the finding doctor wrote
// into the ticket after missing three of the six affected items:
//
//	"a polecat is running" is NOT evidence that an item has no stranded pushed
//	work — it is the PRECONDITION for it, because the re-dispatch IS the
//	running polecat.
//
// A check that asks "why is the merge signal absent" accepts "still in flight"
// and stops looking. This package asks the other question — "does this item have
// pushed work its current dispatch is ignoring" — and there is no argument to it
// that a live worker could satisfy. Callers that know about running polecats must
// use that knowledge to ESCALATE a finding, never to suppress one.
package strandedwork

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// PreRegistrationPrefix is the commit-subject prefix that marks a
// pre-registration commit: predictions written down before the work that would
// confirm or refute them existed.
//
// It is a subject PREFIX rather than a trailer or a tag because the commit is
// made by a worker under time pressure, often as its first act, and the subject
// line is the one field it cannot forget to write. Matching is case-insensitive
// on the prefix and tolerant of leading whitespace; everything else about the
// subject is free text.
//
// ONE SPELLING, AND THIS CONSTANT IS WHERE THAT DECISION LIVES. All five
// pre-registration branches from the 2026-08-05 incident used exactly this
// lowercase-with-colon form — but that is five branches from one week and one
// author population, which is weak evidence for a convention and should not be
// cited as though it established one. Alternative spellings ("pre-registration:",
// "prereg:") are deliberately NOT accepted, and the reason is an asymmetry rather
// than confidence in the sample: a missed marker costs a resubmit verdict on a
// branch that needed the stronger one, while a false pre-registration verdict
// REFUSES a dispatch that should have proceeded — and false refusals are how a
// gate gets disarmed rather than obeyed. Widening buys a rarer catch at the price
// of a commoner false positive. A sixth branch spelling it differently is what
// should change that, and this line is where it would.
const PreRegistrationPrefix = "predictions:"

// Disposition is what a caller should DO about a branch. It is deliberately
// phrased as an action rather than a state ("resubmit", not "unmerged"), because
// the entire defect this package addresses was a correct state — the item is
// available — read as an action it does not license.
type Disposition string

const (
	// DispositionClean means every commit on the branch is already in the
	// target, by patch identity. Nothing is stranded. A re-dispatch is safe.
	DispositionClean Disposition = "clean"

	// DispositionResubmit means the branch carries finished work the target does
	// not have. Submit the branch to the refinery. Dispatching a worker at the
	// item duplicates work that already exists.
	DispositionResubmit Disposition = "resubmit"

	// DispositionPreRegistration means the branch carries an UNMERGED
	// pre-registration commit. A re-dispatch is not merely wasteful, it is
	// corrupting unless it branches from that commit and leaves it unamended.
	//
	// It outranks DispositionResubmit whenever both apply, and the precedence is
	// not a stylistic choice: resubmit advice followed against a
	// pre-registration branch loses nothing (the branch merges intact), while
	// pre-registration advice skipped in favour of resubmit advice loses the
	// control silently.
	DispositionPreRegistration Disposition = "pre_registration"

	// DispositionCarried means the branch has commits the target does not, but
	// they are ALREADY CARRIED by another branch that owns them. Nothing is
	// stranded: this branch is a pointer at somebody else's work.
	//
	// WHY IT EXISTS (mg-1af2). A reviewer polecat reviews by checking the PR
	// branch out, so its own worktree branch ends up pointing at the builder's
	// head. `git cherry` then reports the builder's commits as work the target
	// lacks — which is TRUE and not stranding, because the builder's branch is
	// carrying them and the builder is submitting them. On the gh-issue track
	// this is not a rare race; a reviewer's branch is a pointer at the builder's
	// head every single time, so the resubmit verdict fired on every review
	// polecat that ever ran.
	//
	// The false positive was not merely noisy. DispositionResubmit's remedy is
	// `pogo refinery submit <branch> --author=<this item>`, and following it on
	// a reviewer's branch submits the BUILDER's work a second time, under the
	// reviewer's authorship, racing the builder's own submission. The one thing
	// that caught it on 2026-08-12 was a human noticing that every "stranded"
	// commit subject named a different work item.
	DispositionCarried Disposition = "carried"
)

// Commit is one commit on a branch, as `git cherry -v` reports it.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// IsPreRegistration reports whether c records predictions made in advance.
func (c Commit) IsPreRegistration() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.Subject)), PreRegistrationPrefix)
}

// RescuePrefix is how a RESCUE commit announces itself, at the start of its
// subject: `RESCUE(mg-516e): <what> recovered from preserved worktree p516e —
// UNREVIEWED, not this committer's work (mg-51bf)`.
//
// WHAT A RESCUE COMMIT IS, AND WHY IT IS NOT ORDINARY WORK. When a polecat dies
// holding uncommitted work, the recovery commits that work out of the preserved
// worktree — deliberately with `--no-verify`, because a rescue of
// POSSIBLY-PARTIAL work must not be gated on whether that work compiles. Gating
// it is how the half-implementation, which is exactly the case the pre-commit
// hook refuses, stays uncommitted and unbacked-up. So the bypass is correct and
// the branch is still, by construction, work that HAS NEVER BEEN BUILT and that
// nobody has reviewed.
//
// THIS CONSTANT IS THE FIRST PLACE THE CONVENTION IS WRITTEN DOWN IN CODE, and
// that is worth stating rather than hiding: `grep -rn RESCUE` over this
// repository's Go, shell and markdown finds no emitter. Every one of these
// subjects was typed by an agent following a rescue ticket.
//
// THE POPULATION IS MEASURED AND IT IS NOT THE FIVE THE TICKET NAMED. Counting
// `git log --all --grep='^RESCUE'` across the three repositories this fleet works
// on 2026-08-19: 32 commits, from TWO rescue events, in TWO spellings of what
// goes in the parentheses.
//
//	mg-51bf   5 commits, pogo only    RESCUE(mg-516e): <what> recovered from preserved
//	                                  worktree p516e — UNREVIEWED, … (mg-51bf)
//	mg-11fa   27 commits, all three   RESCUE(p6b2d): 2 uncommitted path(s) from a
//	                                  retained worktree (mg-11fa)
//
// The first form parenthesises the WORK ITEM whose work was recovered; the
// second parenthesises the AGENT whose worktree it came out of. Only the prefix
// is common to both, which is the reason the predicate is the prefix and nothing
// else — a rule keyed on either payload would have silently covered one event
// and missed the other, and the missed one is 27 of the 32.
//
// Two rescue events is still a small sample and this is still a convention no
// code enforces. It should not be cited as though it were a format.
//
// THE ASYMMETRY RUNS THE OPPOSITE WAY FROM PreRegistrationPrefix, so the
// matching rule is deliberately wider rather than narrower. There, a false
// positive REFUSES a dispatch that should have proceeded, so the spelling is
// exact. Here, a false positive tells a reader to build and read a branch before
// submitting it — a cost of one build — while a MISS prints a paste-ready
// `refinery submit` for unbuilt, unreviewed, possibly half-implemented work, and
// the expensive direction is not the wasted gate run: it is the gate PASSING and
// that work merging to the target on the authority of a command a detector
// printed (mg-aed4). So the marker is matched case-insensitively, anchored at
// the start of the subject, and accepts either the parenthesised form or a bare
// colon.
//
// The value is the bare WORD, lowercased, with no delimiter — unlike
// PreRegistrationPrefix, which carries its colon. The delimiter is checked
// separately by IsRescue because the two live spellings differ in it, and a
// constant that baked in one of them would name a rule that only half the
// population follows.
const RescuePrefix = "rescue"

// rescueHeaderRe matches the parenthesised id in the marker itself:
// `RESCUE(mg-1d05):`. That id names the item whose work was RECOVERED.
var rescueHeaderRe = regexp.MustCompile(`^\s*[Rr][Ee][Ss][Cc][Uu][Ee]\(((?:mg|gh)-[A-Za-z0-9]{3,})\)`)

// IsRescue reports whether c is a rescue commit: work recovered out of a dead
// polecat's worktree and committed with the pre-commit hook bypassed.
//
// See RescuePrefix for the convention, its measured population, and why this
// match is wider than the pre-registration one.
func (c Commit) IsRescue() bool {
	s := strings.ToLower(strings.TrimSpace(c.Subject))
	if !strings.HasPrefix(s, RescuePrefix) {
		return false
	}
	rest := s[len(RescuePrefix):]
	return strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, ":")
}

// RescuedItem returns the work item whose work this commit recovered — the id
// inside the marker's own parentheses, `RESCUE(mg-1d05):` — or "" when the
// subject names none.
func (c Commit) RescuedItem() string {
	if m := rescueHeaderRe.FindStringSubmatch(c.Subject); m != nil {
		return m[1]
	}
	return ""
}

// RescueTracker returns the work item the RESCUE ITSELF was tracked under: the
// trailing `(mg-51bf)`, by the repository's ordinary commit convention.
//
// THE TWO IDS IN A RESCUE SUBJECT ARE DIFFERENT ITEMS AND THIS IS THE ONE WORTH
// PRINTING. `RESCUE(mg-1d05): … (mg-51bf)` names the recovered item first and
// the rescue last. A report joining branches to items already knows the first —
// it is the row's own subject — and knows nothing about the second, which is
// where a reader finds out why work was committed without a build at all. The
// first draft of this function returned the wrong one of the two and the wrong
// one is the redundant one, which is exactly how it survived a passing test.
//
// Best-effort, as WorkItemID is: "" when the subject names no trailing id.
func (c Commit) RescueTracker() string {
	if !c.IsRescue() {
		return ""
	}
	rest := c.Subject
	if loc := rescueHeaderRe.FindStringIndex(rest); loc != nil {
		rest = rest[loc[1]:]
	}
	if m := workItemRe.FindStringSubmatch(rest); m != nil {
		return m[1]
	}
	return ""
}

// Finding is the verdict on one branch.
type Finding struct {
	// Repo is the git repository the branch was read from.
	Repo string `json:"repo"`
	// Branch is the short branch name (e.g. "polecat-9a19").
	Branch string `json:"branch"`
	// Ref is the ref the commits were actually read from — the remote-tracking
	// ref when one exists, otherwise the local head. Reported because "the work
	// is on origin" and "the work is only in a local worktree git-gc is about to
	// reap" are different emergencies.
	Ref string `json:"ref"`
	// Pushed is true when Ref was a remote-tracking ref.
	Pushed bool `json:"pushed"`
	// Target is the ref the branch was compared against.
	Target string `json:"target"`
	// Found is false when neither a remote-tracking ref nor a local head exists
	// for Branch. Disposition is DispositionClean in that case, and it means
	// "there is no branch", not "the branch is merged".
	Found bool `json:"found"`

	// TipTime is the COMMITTER date of Ref's tip: when this branch was last
	// written to. Zero when it could not be read, and TipTimeError says why.
	//
	// IT EXISTS TO DATE A BRANCH AGAINST AN EXTERNAL RECORD (mg-441f). The
	// refinery keeps a history of merge requests, and "this branch was already
	// refused" is only a claim about the branch AS IT NOW STANDS if the refusal
	// came AFTER the last commit on it. A branch that was refused, fixed and
	// pushed again is an ordinary resubmit, and a check that read the refusal
	// without reading this would suppress the one remedy that works — the same
	// shape of error, in the opposite direction, as the one that check repairs.
	//
	// COMMITTER date and not author date: a rebase or an amend rewrites the
	// former and preserves the latter, and the question here is when the ref
	// last changed, not when the work was first written.
	//
	// A failure to read it is NOT fatal and NOT zero-by-default-is-fine: a zero
	// TipTime must be read as "unknown", never as "the epoch", because every
	// external record postdates the epoch and an unknown tip that compared as
	// very old would make every stale record look current.
	TipTime time.Time `json:"tip_time,omitempty"`
	// TipTimeError is why TipTime could not be read. Empty when it could.
	TipTimeError string `json:"tip_time_error,omitempty"`

	// Disposition is what to do. See the constants.
	Disposition Disposition `json:"disposition"`

	// Unmerged lists the commits the target does not have, oldest first.
	Unmerged []Commit `json:"unmerged,omitempty"`
	// Equivalent counts commits already present in the target under a different
	// SHA. The refinery merges by rebase, so a branch that landed cleanly has
	// every commit rewritten; counting those as stranded would fire this check
	// on every healthy branch in the repo. See Inspect for why `git cherry`.
	Equivalent int `json:"equivalent"`

	// PreRegistration is the OLDEST unmerged pre-registration commit, and it is
	// the ref a re-dispatch must branch from. Nil unless Disposition is
	// DispositionPreRegistration.
	PreRegistration *Commit `json:"pre_registration,omitempty"`

	// Rescue is the OLDEST unmerged RESCUE commit on the branch, when there is
	// one. It is the evidence that this branch's work was committed with the
	// pre-commit hook bypassed and has therefore NEVER BEEN BUILT — see
	// RescuePrefix.
	//
	// IT IS A FIELD AND NOT A DISPOSITION, deliberately. A rescue branch IS
	// stranded — its commits really are absent from the target — so the
	// disposition every caller switches on stays DispositionResubmit and the
	// dispatch refusal keeps refusing exactly as before. What changes is what a
	// REMEDY may say about it, and that decision belongs to each reporting
	// instrument rather than to a state machine every caller shares. The
	// check-stranded sweep is the one that consumes it today (mg-aed4); the
	// spawn-time guard's rescue cell is mg-ba32's, and it can read this field
	// without this package changing again.
	Rescue *Commit `json:"rescue,omitempty"`

	// CarriedBy names every OTHER polecat branch whose tip contains this
	// branch's head — i.e. every branch that already carries all of these
	// commits. Empty for the ordinary case where a branch's work exists nowhere
	// else. See Carrier for what it is used for.
	CarriedBy []string `json:"carried_by,omitempty"`
	// Carrier is the branch in CarriedBy that OWNS these commits, when one can
	// be identified. Set only for DispositionCarried.
	Carrier string `json:"carrier,omitempty"`
	// CarrierProbeError records that the CarriedBy probe itself failed. The
	// disposition then stays DispositionResubmit, because a probe that can only
	// ever SUPPRESS a report must not suppress one it did not actually run.
	CarrierProbeError string `json:"carrier_probe_error,omitempty"`

	// WorkItemID is the id recovered from the unmerged commit subjects, when one
	// is present (commit convention: a trailing "(mg-xxxx)"). Best-effort: it is
	// how a scan attributes an orphaned branch to an item, and it is empty for a
	// branch whose commits never named one.
	WorkItemID string `json:"work_item_id,omitempty"`
}

// Stranded reports whether the branch holds work the target does not.
func (f Finding) Stranded() bool {
	return f.Disposition == DispositionResubmit || f.Disposition == DispositionPreRegistration
}

// Provenance names where a branch's commits actually live, in the words the
// reader has to act on rather than as a boolean.
//
// IT IS NOT DECORATION (mg-bfe0). "PUSHED" and "LOCAL-ONLY" license different
// next moves and carry opposite urgency: pushed work is durable, discoverable by
// anyone reading `git ls-remote`, and recoverable at leisure; local-only work
// exists in one worktree on one host and git-gc reaps it. Every instrument in
// this package's blast radius used to render both as "PUSHED, UNMERGED work",
// which tells a reader the one thing about the urgent case that is false.
func Provenance(pushed bool) string {
	if pushed {
		return "PUSHED"
	}
	return "LOCAL-ONLY"
}

// LocalOnlyWarning is the sentence that has to accompany a LOCAL-ONLY verdict.
//
// It is a constant so every renderer of a LOCAL-ONLY verdict says the same
// thing, and it states the URGENCY rather than only the fact, because "not on origin" is a detail a
// reader skims past while "git-gc destroys it" is one they act on. The ordering
// against the ordinary stranded case is deliberate and is the ticket's own
// finding: a pushed branch is durable and discoverable by anyone reading `git
// ls-remote`; a local-only one is neither, so it is the more urgent case and not
// the lesser one.
const LocalOnlyWarning = "THE WORK IS NOT ON ORIGIN: it exists only in a worktree on this host, " +
	"git-gc reaps that worktree, and no other stranded-work instrument on any other host can see it. " +
	"Push it before anything else — stranded-on-origin is recoverable at leisure, this is not"

// localOnlyNote returns LocalOnlyWarning as a trailing clause, or "" when the
// branch is pushed and there is nothing extra to say.
//
// No trailing full stop, matching every other string Summary builds: callers
// embed these mid-sentence, and one that punctuated itself produced "this is
// not.. A polecat spawned now would..." in the dispatch refusal.
func localOnlyNote(pushed bool) string {
	if pushed {
		return ""
	}
	return " — " + LocalOnlyWarning
}

// SubmitRemedy renders the command that gets a stranded branch merged.
//
// IT IS ONE FUNCTION BECAUSE THE COMMAND IS NOT THE SAME FOR BOTH CASES, and
// getting that wrong is not a cosmetic error: `pogo refinery submit` REFUSES a
// branch that is not on origin (mg-586d). The merge worker checks the branch out
// as origin/<branch>, so an unpushed branch cannot merge, and submit rejects it
// at the door rather than accepting an MR id and failing later. Four separate
// instruments printed the bare submit line for both cases — the dispatch
// refusal, Finding.Summary, the stranded-work mail, and the check-stranded
// sweep — so the ONE population whose work is not durable was handed a command
// that cannot run (mg-bfe0).
//
// The push is CHAINED with && rather than described in prose, because the reader
// of a stranded-work remedy is deciding what to paste, and a prose caveat next to
// a runnable command loses to the command.
//
// It lives here rather than in each caller for the reason given on
// BranchMatchesItem: several callers depend on exactly this rule, and a second
// copy is a second rule the day one of them changes.
func SubmitRemedy(repo, branch, author string, pushed bool) string {
	submit := fmt.Sprintf("pogo refinery submit %s --repo=%s", branch, repo)
	if author != "" {
		submit += " --author=" + author
	}
	if pushed {
		return submit
	}
	return fmt.Sprintf("git -C %s push origin %s && %s", repo, branch, submit)
}

// Summary renders the finding as one line an agent or a person can act on
// without reading this package. It always names the remedy, because a report
// that only names the problem is what the pre-registration case cannot survive.
func (f Finding) Summary() string {
	switch f.Disposition {
	case DispositionPreRegistration:
		return fmt.Sprintf(
			"%s has %d unmerged commit(s) on %s, they are %s, and %s is a PRE-REGISTRATION commit (%q). "+
				"Do NOT dispatch a worker that branches from %s: it would write its predictions after "+
				"seeing the results, and the artifact would be indistinguishable from a valid one. "+
				"Either get %s merged (`%s`), or dispatch FROM %s and leave that commit unamended%s",
			f.Branch, len(f.Unmerged), f.Target, Provenance(f.Pushed),
			shortSHA(f.PreRegistration.SHA), f.PreRegistration.Subject,
			f.Target, f.Branch, SubmitRemedy(f.Repo, f.Branch, "", f.Pushed),
			shortSHA(f.PreRegistration.SHA), localOnlyNote(f.Pushed))
	case DispositionCarried:
		return fmt.Sprintf(
			"%s has %d commit(s) %s does not have, but ALL of them are already carried by %s, "+
				"which owns them (they name %s). Nothing is stranded: this branch is a pointer at "+
				"another item's work, and %s is what merges it. Do NOT submit %s — that would submit "+
				"%s's work a second time, under the wrong authorship",
			f.Branch, len(f.Unmerged), f.Target, f.Carrier, f.WorkItemID, f.Carrier, f.Branch, f.WorkItemID)
	case DispositionResubmit:
		return fmt.Sprintf(
			"%s has %d unmerged commit(s) on %s (%s), and they are %s. Get the branch merged (`%s`); "+
				"do NOT dispatch a worker at this item, it would re-derive work that already exists%s",
			f.Branch, len(f.Unmerged), f.Target, shortSHA(f.Unmerged[0].SHA), Provenance(f.Pushed),
			SubmitRemedy(f.Repo, f.Branch, "", f.Pushed), localOnlyNote(f.Pushed))
	default:
		if !f.Found {
			return fmt.Sprintf("no branch %s exists in %s", f.Branch, f.Repo)
		}
		return fmt.Sprintf("%s has nothing the target %s does not already have", f.Branch, f.Target)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// workItemRe matches the trailing work-item id in a commit subject, per the
// repo's convention: "fix(x): thing (mg-b468)".
var workItemRe = regexp.MustCompile(`\(((?:mg|gh)-[A-Za-z0-9]{3,})\)`)

// Inspect classifies one branch in repo against target.
//
// target may be empty, in which case the repository's default branch is
// resolved (see ResolveTarget). A target that cannot be resolved is an ERROR and
// never a clean verdict: "the comparison could not be made" and "there is
// nothing stranded" are different facts, and reporting the first as the second
// is the whole shape of the defect this package exists to close.
//
// WHY `git cherry` AND NOT `rev-list target..branch`. The refinery merges by
// rebasing the branch onto the target and fast-forwarding, so a branch that
// merged successfully has every one of its commits present upstream under a
// DIFFERENT sha. `rev-list target..branch` counts all of them as absent, which
// would report every healthy merged branch in the repo as stranded work. A
// detector that fires on healthy input is worse than no detector: it teaches its
// readers to skip the line, and this is the line the real stranding surfaces on.
// `git cherry` compares by patch id, so a rebase-rewritten commit reports as
// present.
func Inspect(repo, branch, target string) (Finding, error) {
	f := Finding{Repo: repo, Branch: branch, Disposition: DispositionClean}

	targetRef, err := ResolveTarget(repo, target)
	if err != nil {
		return f, err
	}
	f.Target = targetRef

	ref, pushed, found, err := resolveBranchRef(repo, branch)
	if err != nil {
		return f, err
	}
	if !found {
		return f, nil
	}
	f.Ref, f.Pushed, f.Found = ref, pushed, true
	f.TipTime, f.TipTimeError = tipTime(repo, ref)

	unmerged, equivalent, err := cherry(repo, targetRef, ref)
	if err != nil {
		return f, err
	}
	f.Unmerged, f.Equivalent = unmerged, equivalent
	if len(unmerged) == 0 {
		return f, nil
	}

	f.Disposition = DispositionResubmit
	f.WorkItemID = workItemID(unmerged)

	// Who else already has these commits? Head containment is the strict form of
	// the question: every unmerged commit is an ancestor of the head, so a branch
	// that contains the head contains all of them. A probe failure is recorded
	// and never fatal — see CarrierProbeError.
	if carriers, cerr := carriedBy(repo, ref, branch); cerr != nil {
		f.CarrierProbeError = cerr.Error()
	} else {
		f.CarriedBy = carriers
	}

	// Only an UNMERGED pre-registration commit forces the pre-registration
	// disposition. One that already landed on the target is safe: a worker
	// branching from the target inherits it, cannot amend it, and the control
	// stands. The distinction matters — a branch that merged its predictions and
	// then pushed follow-up work is an ordinary resubmit.
	for i := range unmerged {
		if unmerged[i].IsPreRegistration() {
			c := unmerged[i]
			f.PreRegistration = &c
			f.Disposition = DispositionPreRegistration
			break
		}
	}

	// The rescue marker, on the same population and for the same reason: only an
	// UNMERGED rescue commit is evidence of unbuilt work sitting outside the
	// target. One that already landed was built by whatever gate merged it.
	for i := range unmerged {
		if unmerged[i].IsRescue() {
			c := unmerged[i]
			f.Rescue = &c
			break
		}
	}

	// A branch that is only POINTING at work another branch owns is not stranded.
	// Checked last, and never against DispositionPreRegistration: that verdict is
	// the one the package's stated asymmetry says must not be crowded out, and a
	// suppression rule is exactly the kind of thing that crowds it out.
	if f.Disposition == DispositionResubmit {
		if owner := ownerAmong(f.CarriedBy, f.Branch, f.WorkItemID); owner != "" {
			f.Carrier, f.Disposition = owner, DispositionCarried
		}
	}
	return f, nil
}

// ownerAmong picks the branch in carriers that OWNS the commits attributed to
// workItemID, or "" when this branch owns them itself or no owner is identifiable.
//
// THE TEST IS ASYMMETRIC, AND THAT ASYMMETRY IS THE WHOLE FIX. "Some other
// branch contains these commits" — the obvious rule, and the one mg-1af2's
// ticket suggested — is not safe, because it is symmetric: when a reviewer's
// branch points at a builder's head, the builder's branch is ALSO contained by
// the reviewer's, so the rule would silence the builder's genuine stranding as
// readily as the reviewer's false one. mg-9a19 is the reason this detector
// exists and it must survive a reviewer having glanced at its branch.
//
// So the rule is ownership, decided by the repo's two naming conventions:
//
//   - the commits say whose work they are, via the trailing "(mg-xxxx)";
//   - a polecat branch says whose item it serves, via polecat-<agent name>.
//
// This branch is a POINTER when its own name does not claim the work its commits
// name, and some branch carrying those same commits does. The builder's branch
// claims them (polecat-paaf6 vs mg-aaf6), so it stays stranded; the reviewer's
// does not (polecat-p1c60 vs mg-aaf6), so it is carried.
//
// It answers "" — meaning "report as stranded" — whenever it cannot tell: no
// carriers, no work item id in the subjects, or no carrier whose name claims the
// id. A duplicate report costs a reader one comparison; a suppressed one costs
// what mg-9a19 cost.
func ownerAmong(carriers []string, branch, workItemID string) string {
	if workItemID == "" || len(carriers) == 0 {
		return ""
	}
	if BranchMatchesItem(branch, workItemID) {
		return ""
	}
	for _, c := range carriers {
		if BranchMatchesItem(c, workItemID) {
			return c
		}
	}
	return ""
}

// carriedBy lists the OTHER polecat branches whose tip contains head.
//
// The polecat namespace only, and both local heads and origin's copies, matching
// polecatBranches: the mechanism that produces a pointer-at-someone-else's-work
// branch is a polecat worktree checking a branch out, and widening the search to
// every ref in the repo would let an unrelated integration branch stand in as an
// "owner".
//
// branch itself is excluded by NAME, which also drops its own remote-tracking
// twin — refs/remotes/origin/<branch> trivially contains refs/heads/<branch>'s
// head, and counting that as a carrier would silence every pushed branch in the
// repo.
func carriedBy(repo, head, branch string) ([]string, error) {
	out, err := git(repo, "for-each-ref", "--contains", head, "--format=%(refname)",
		"refs/heads/"+BranchPrefix+"*", "refs/remotes/origin/"+BranchPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("list branches containing %s in %s: %w", head, repo, err)
	}
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "refs/heads/"):
			line = strings.TrimPrefix(line, "refs/heads/")
		case strings.HasPrefix(line, "refs/remotes/origin/"):
			line = strings.TrimPrefix(line, "refs/remotes/origin/")
		default:
			continue
		}
		if line == "HEAD" || line == branch || seen[line] {
			continue
		}
		seen[line] = true
		names = append(names, line)
	}
	return names, nil
}

// FetchTimeout bounds the best-effort refresh in Fetch. It is short because
// Fetch sits in front of a dispatch decision: a check that can hang for minutes
// is a check somebody removes.
const FetchTimeout = 20 * time.Second

// Fetch refreshes repo's remote-tracking refs, best-effort, and reports whether
// the refs that follow are fresh.
//
// WHY BEST-EFFORT AND NEVER FATAL. Stale remote-tracking refs make this package
// wrong in both directions — a target behind origin reports merged work as
// stranded, and a branch pushed from elsewhere is invisible — so the refresh is
// worth doing. But the incident that produced this check was a NETWORK OUTAGE,
// and a check that refuses to answer when the network is down is a check that is
// off during precisely the window it exists for. So a failed fetch degrades to
// "answer from what is on disk, and say the answer may be stale" rather than to
// no answer at all.
//
// The bool is returned rather than swallowed because the caller has to be able
// to say which of the two it got. "Checked against fresh refs" and "checked
// against whatever this clone last saw" are different claims.
func Fetch(repo string) (fresh bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), FetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--quiet", "origin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("fetch origin in %s: %s: %w", repo, strings.TrimSpace(string(out)), err)
	}
	return true, nil
}

// Scan classifies every polecat branch in repo — local heads and
// remote-tracking refs alike, deduplicated by branch name.
//
// It returns findings for stranded branches only. Clean branches are the
// overwhelming majority in any repo with history, and a report that listed them
// would bury the ones that matter.
//
// An individual branch whose inspection fails does not abort the scan: its error
// is collected and returned alongside the findings, so one unreadable ref cannot
// turn a scan of forty branches into a single error the caller renders as
// "nothing stranded".
func Scan(repo, target string) ([]Finding, []error) {
	branches, err := polecatBranches(repo)
	if err != nil {
		return nil, []error{err}
	}
	var findings []Finding
	var errs []error
	for _, b := range branches {
		f, err := Inspect(repo, b, target)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect %s: %w", b, err))
			continue
		}
		if f.Stranded() {
			findings = append(findings, f)
		}
	}
	return findings, errs
}

// BranchPrefix is the prefix every polecat branch carries. It duplicates
// gitgc.BranchPrefix by value rather than by import: this package is a leaf that
// internal/agent depends on, and internal/agent already imports internal/gitgc,
// so importing it back would close a cycle. The frozen-identifier test keeps the
// two literals equal.
const BranchPrefix = "polecat-"

// polecatBranches lists every polecat branch name in repo, from both local heads
// and origin's remote-tracking refs, sorted and deduplicated.
//
// BOTH, and not just origin. A polecat that committed but never pushed has work
// that exists only in its worktree's branch — and the worktree is exactly what
// git-gc reaps once the agent is gone, so local-only work is the more urgent of
// the two, not the lesser one.
func polecatBranches(repo string) ([]string, error) {
	out, err := git(repo, "for-each-ref", "--format=%(refname)",
		"refs/heads/"+BranchPrefix+"*", "refs/remotes/origin/"+BranchPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("list polecat branches in %s: %w", repo, err)
	}
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "refs/heads/"):
			line = strings.TrimPrefix(line, "refs/heads/")
		case strings.HasPrefix(line, "refs/remotes/origin/"):
			line = strings.TrimPrefix(line, "refs/remotes/origin/")
		default:
			continue
		}
		// origin/HEAD resolves into this namespace on some clones; it is a
		// symref, not a branch.
		if line == "HEAD" || seen[line] {
			continue
		}
		seen[line] = true
		names = append(names, line)
	}
	return names, nil
}

// BranchMatchesItem reports whether a polecat branch name plausibly belongs to
// workItemID, by the BRANCH-NAME route.
//
// A polecat branch is polecat-<agent name>, and an agent's name is derived from
// its work item's id — usually the bare suffix ("mg-9a19" → "9a19"), sometimes
// with a letter in front ("mg-b468" → "wb468", "mg-56ac" → "q56ac"), sometimes
// with the whole id ("polecat-mg-0fa6"). So the test is CONTAINMENT of the
// suffix, not equality.
//
// The suffix must be at least three characters. A shorter or malformed id would
// match every branch in the repo, and a detector that names all 634 of them is
// disarmed the first time somebody reads it.
//
// It lives here, next to the branch-listing code, because two callers depend on
// exactly this rule — the dispatch gate matching a branch to the item it is
// about to re-derive, and the sweep matching an item to the branch it may
// already have — and a second copy is a second rule the day one of them is
// widened.
func BranchMatchesItem(branch, workItemID string) bool {
	if workItemID == "" || branch == "" {
		return false
	}
	suffix := workItemID
	if _, after, ok := strings.Cut(workItemID, "-"); ok {
		suffix = after
	}
	if len(suffix) < 3 {
		return false
	}
	name := strings.TrimPrefix(branch, BranchPrefix)
	return strings.Contains(strings.ToLower(name), strings.ToLower(suffix))
}

// PolecatBranches lists every polecat branch name in repo, from local heads and
// origin's remote-tracking refs alike, sorted and deduplicated. Exported for the
// item-driven sweep, which needs the branch NAMES up front so it can join them
// against work items before paying for an Inspect on any of them.
func PolecatBranches(repo string) ([]string, error) { return polecatBranches(repo) }

// resolveBranchRef picks the ref to read a branch's commits from, preferring the
// pushed copy.
//
// The remote-tracking ref wins when both exist because it is the copy that
// survives the worktree being reaped, and it is what a resubmit would actually
// merge. The local head is the fallback rather than the primary for the same
// reason — but it is a fallback that must exist, since a polecat that never got
// as far as pushing still did work worth not throwing away.
func resolveBranchRef(repo, branch string) (ref string, pushed, found bool, err error) {
	for _, candidate := range []struct {
		ref    string
		pushed bool
	}{
		{"refs/remotes/origin/" + branch, true},
		{"refs/heads/" + branch, false},
	} {
		ok, verr := refExists(repo, candidate.ref)
		if verr != nil {
			return "", false, false, verr
		}
		if ok {
			return candidate.ref, candidate.pushed, true, nil
		}
	}
	return "", false, false, nil
}

// ResolveTarget resolves the ref a branch should be compared against.
//
// A named target is looked for on origin first and locally second, matching
// resolveBranchRef: the question is whether the work reached the SHARED history,
// and a local main that is behind origin would report merged work as stranded.
//
// An empty target asks the repository: origin/HEAD if it is set, then the
// conventional names. Failure to resolve is an error — see Inspect.
func ResolveTarget(repo, target string) (string, error) {
	if target != "" {
		for _, ref := range []string{"refs/remotes/origin/" + target, "refs/heads/" + target} {
			ok, err := refExists(repo, ref)
			if err != nil {
				return "", err
			}
			if ok {
				return ref, nil
			}
		}
		return "", fmt.Errorf("target %q resolves to no ref in %s (tried refs/remotes/origin/%s and refs/heads/%s)", target, repo, target, target)
	}

	if head, err := git(repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if head = strings.TrimSpace(head); head != "" {
			return head, nil
		}
	}
	for _, name := range []string{"main", "master"} {
		for _, ref := range []string{"refs/remotes/origin/" + name, "refs/heads/" + name} {
			ok, err := refExists(repo, ref)
			if err != nil {
				return "", err
			}
			if ok {
				return ref, nil
			}
		}
	}
	return "", fmt.Errorf("no default branch in %s: origin/HEAD is unset and neither main nor master exists", repo)
}

// tipTime reads the committer date of a ref's tip.
//
// It is best-effort by design: an unreadable date must not turn a branch that
// `git cherry` answered for into an unjudged row, because the primary question
// — is there unmerged work — has already been answered by then. What it must
// not do is return a zero time that a caller reads as a real one, so the error
// is returned alongside rather than logged and dropped.
func tipTime(repo, ref string) (time.Time, string) {
	out, err := git(repo, "log", "-1", "--format=%cI", ref)
	if err != nil {
		return time.Time{}, fmt.Sprintf("git log -1 --format=%%cI %s in %s: %v", ref, repo, err)
	}
	raw := strings.TrimSpace(out)
	if raw == "" {
		return time.Time{}, fmt.Sprintf("git log -1 --format=%%cI %s in %s printed nothing", ref, repo)
	}
	t, perr := time.Parse(time.RFC3339, raw)
	if perr != nil {
		return time.Time{}, fmt.Sprintf("parsing committer date %q of %s in %s: %v", raw, ref, repo, perr)
	}
	return t, ""
}

// cherry returns the commits on branchRef that targetRef does not have (oldest
// first) and the count of those it already has under a different sha.
//
// `git cherry -v <upstream> <head>` prints one line per commit on head:
//
//   - <sha> <subject>   no patch-equivalent commit upstream
//   - <sha> <subject>   an equivalent commit is already upstream
func cherry(repo, targetRef, branchRef string) ([]Commit, int, error) {
	out, err := git(repo, "cherry", "-v", targetRef, branchRef)
	if err != nil {
		return nil, 0, fmt.Errorf("git cherry %s %s in %s: %w", targetRef, branchRef, repo, err)
	}
	var unmerged []Commit
	equivalent := 0
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		mark, rest := line[0], strings.TrimSpace(line[1:])
		sha, subject, _ := strings.Cut(rest, " ")
		switch mark {
		case '+':
			unmerged = append(unmerged, Commit{SHA: sha, Subject: strings.TrimSpace(subject)})
		case '-':
			equivalent++
		}
	}
	return unmerged, equivalent, nil
}

// workItemID recovers the work item a branch's unmerged commits name, if any.
// The NEWEST mention wins: a branch that was rebased onto another item's work
// carries older ids that are not its own.
func workItemID(commits []Commit) string {
	for i := len(commits) - 1; i >= 0; i-- {
		if m := workItemRe.FindStringSubmatch(commits[i].Subject); m != nil {
			return m[1]
		}
	}
	return ""
}

// refExists reports whether ref names a commit in repo.
//
// `git rev-parse --verify --quiet` exits 1 for an absent ref, which exec reports
// as an error indistinguishable from "git is broken". show-ref is used instead
// precisely because its contract is narrower: an exit status of 1 means "no such
// ref" and nothing else, so a genuinely broken repository still surfaces as an
// error rather than as a clean absence.
func refExists(repo, ref string) (bool, error) {
	cmd := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", ref)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("show-ref %s in %s: %w", ref, repo, err)
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(exitErr.Stderr)), err)
		}
		return "", err
	}
	return string(out), nil
}

// LocalOnlyCommits lists the commits on repo's LOCAL branch head that no remote
// ref contains — `git rev-list refs/heads/<branch> --not --remotes`, newest
// first. An absent local head is (nil, nil): there is no local copy to lose.
//
// IT IS A DIFFERENT QUESTION FROM Inspect'S, AND THAT IS THE POINT (mg-ded2).
// Inspect asks "does the TARGET have these commits", which is the right question
// for a branch somebody still intends to merge. This asks "does any remote have
// this commit object at all", which is the right question for a branch nobody is
// going to merge, because it is the only one whose answer tracks what git-gc can
// destroy. The two populations differ by two orders of magnitude on this box:
// `git cherry` calls 435 polecat branches unmerged in one repo, and exactly one
// of the 46 polecat worktrees present held a commit on no remote ref
// (polecat-pc-rev-c5d5a10, measured 2026-08-19).
//
// `--not --remotes` and not `--not origin/<branch>` because the loss this
// answers about is loss of the OBJECT. A commit pushed under any other ref —
// carried by a sibling branch, cherry-picked onto a release branch — is on a
// server and recoverable at leisure; only a commit no remote ref reaches dies
// with the worktree.
func LocalOnlyCommits(repo, branch string) ([]Commit, error) {
	head := "refs/heads/" + branch
	ok, err := refExists(repo, head)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	out, err := git(repo, "rev-list", "--format=%H %s", "--no-commit-header", head, "--not", "--remotes")
	if err != nil {
		return nil, fmt.Errorf("git rev-list %s --not --remotes in %s: %w", head, repo, err)
	}
	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha, subject, _ := strings.Cut(line, " ")
		commits = append(commits, Commit{SHA: sha, Subject: strings.TrimSpace(subject)})
	}
	return commits, nil
}
