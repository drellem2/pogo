package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/strandedwork"
)

// The ADOPT exit from the stranded-work gate (mg-ba32).
//
// WHY IT EXISTS. Until this file the gate had exactly one way past it —
// --stranded-override — and exactly one story about what that meant: "dispatch
// anyway ... if this branch is genuinely spent". But the gate refuses TWO
// populations that want OPPOSITE handling:
//
//   - THE BRANCH IS SPENT. Start over from the target; the branch is left
//     behind and nothing on it is inherited. That is what --stranded-override
//     does, and what its help has always said.
//   - THE BRANCH IS GOOD. Somebody has to pick it up and land it — rebase it,
//     finish it, resubmit it. The gate has no cell for this at all, and yet it
//     is the only way the work can land: the refusal's own advice ("get the
//     branch merged") is not something the gate can do, and when a merge needs
//     a rebase it takes a worker.
//
// On 2026-08-14 at ~02:21Z that second case reached the gate at mg-5058 and was
// told "do NOT dispatch a worker at this item, it would re-derive work that
// already exists" — of a dispatch whose whole purpose was to NOT re-derive it.
// The honest action required --stranded-override, whose help asserted a reason
// the operator did not hold, so a paragraph of prose had to be written into the
// flag to say the opposite of what the flag claimed. Once both populations share
// one flag, neither the event log nor the flag's help can tell them apart
// afterwards.
//
// WHY IT IS NOT A RENAME. A second flag that did the same thing under a
// friendlier word would re-commit the defect it repairs: the name would assert
// an adoption that never happened. --stranded-adopt bases the new worktree ON
// the stranded ref, so the worker starts standing on the work instead of in
// front of it, and verifyAdoption below refuses the spawn if that did not
// actually take. The two exits are therefore distinguishable at the decision
// (two flags), in the mechanism (two base refs), and in the record (two events).
//
// WHY "ADOPT" AND NOT "RESCUE", which is what mg-ba32 calls it. RESCUE is
// already taken in this tree and means something narrower and unrelated: a
// commit whose subject starts `RESCUE(` , written with the pre-commit hook
// bypassed to recover work out of a preserved worktree (strandedwork.RescuePrefix,
// strandwatch.KindRescueUnbuilt). A `dispatch_stranded_work_rescue` event sitting
// next to those would be read as being about such a commit. "Adopt" names what
// the dispatch does to the branch and collides with nothing.

// strandedAdoption is the decision to continue a stranded branch rather than
// leave it behind: which finding was adopted, why, and what else the gate saw.
type strandedAdoption struct {
	// Finding is the branch the new worktree is based on.
	Finding strandedwork.Finding
	// Reason is the operator's written why, verbatim.
	Reason string
	// Refusal is the gate text this bypassed, kept for the event.
	Refusal string
	// Others are the other stranded branches the gate found for this item and
	// did NOT adopt. Recorded rather than dropped: a dispatch that adopts one of
	// three branches has left two behind, and a record that named only the
	// adopted one would read as if there had been nothing to choose.
	Others []string
}

// BaseRef is the ref a worktree adopting this branch must be created from.
func (a strandedAdoption) BaseRef() string { return a.Finding.Ref }

// adoptableFinding picks which of the gate's findings an adopt dispatch
// continues, and reports whether one can be continued at all.
//
// The ORDER MATCHES THE REFUSAL's: pre-registration first whatever the scan
// order, because that is the disposition whose handling must not be crowded out
// — and because it is the one whose only correct remedy IS continuing the
// branch. Ties below it go to the first finding, which is the branch the refusal
// text named.
//
// It refuses a finding it cannot base a worktree on (Found false, or no ref)
// rather than silently falling back to the target: falling back would produce a
// worktree that adopted nothing under a flag whose name says it did, which is
// the exact failure mode this whole file exists to end.
func adoptableFinding(findings []strandedwork.Finding) (strandedwork.Finding, bool) {
	var pick strandedwork.Finding
	var found bool
	for _, f := range findings {
		if f.Ref == "" || !f.Found {
			continue
		}
		if f.Disposition == strandedwork.DispositionPreRegistration {
			return f, true
		}
		if !found {
			pick, found = f, true
		}
	}
	return pick, found
}

// otherBranches lists every finding except the adopted one, by branch name.
func otherBranches(findings []strandedwork.Finding, adopted strandedwork.Finding) []string {
	var others []string
	for _, f := range findings {
		if f.Branch != adopted.Branch {
			others = append(others, f.Branch)
		}
	}
	return others
}

// conflictingStrandedExits is the refusal for a spawn that passed BOTH exits.
//
// It is a refusal and not a precedence rule on purpose. The two flags are the
// two halves of a decision that has to have been made — "this branch is spent"
// and "this branch is good" cannot both be what the operator meant — so picking
// one for them would record a decision nobody took, in an event whose entire
// value is that it says which decision was taken.
const conflictingStrandedExits = "both --stranded-adopt and --stranded-override were given, " +
	"and they mean OPPOSITE things: adopt bases the new worker's worktree ON the stranded branch so " +
	"its work is continued, override bases it on the target and leaves that branch behind. Pass exactly " +
	"one. If you do not know which, read the branch first — the refusal below names it"

// unadoptableStranded is the refusal for an adopt dispatch whose gate findings
// carry no ref a worktree can be based on. Rare — the gate refuses on a branch
// positively read from disk — but the alternative to saying so is basing the
// worktree on the target and calling it an adoption.
func unadoptableStranded(workItemID string, findings []strandedwork.Finding) string {
	var names []string
	for _, f := range findings {
		names = append(names, f.Branch)
	}
	return fmt.Sprintf("--stranded-adopt was given for %s, but none of the stranded branches the gate "+
		"found (%s) resolves to a ref this worktree can be created from, so there is nothing to adopt. "+
		"Nothing was dispatched: basing the worktree on the target instead would be an ordinary "+
		"re-derivation wearing the adopt flag's name. Inspect the branches by hand, and use "+
		"--stranded-override=\"<why>\" if the work really does have to be redone from the target",
		workItemID, strings.Join(names, ", "))
}

// verifyAdoption checks that the branch just created really does carry the ref
// it was supposed to adopt.
//
// THE REMEDY IS SUBJECT TO THE DEFECT IT REMEDIES. This whole file is about a
// flag that asserted something the mechanism did not do; the way --stranded-adopt
// fails that way is a worktree that got created from the target anyway — a
// mistyped ref, a ref deleted between the gate and the add, a fallback taken
// silently. So the assertion is MEASURED after the fact rather than assumed from
// having passed the argument, and a spawn that cannot prove it adopted is failed
// rather than logged.
func verifyAdoption(repo, ref, branch string) error {
	if repo == "" || ref == "" || branch == "" {
		return fmt.Errorf("cannot verify the adoption: repo=%q ref=%q branch=%q", repo, ref, branch)
	}
	err := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", ref, branch).Run()
	if err != nil {
		return fmt.Errorf("branch %s was created but does NOT contain %s: the worktree did not adopt "+
			"the stranded work (%v). Refusing the spawn rather than handing a worker a tree that only "+
			"looks adopted", branch, ref, err)
	}
	return nil
}

// strandedAdoptPrelude is the block prepended to an adopting worker's prompt.
//
// IT IS NOT OPTIONAL DECORATION. An adopting worker wakes up on a branch that
// already carries somebody else's commits, in a worktree whose base is not the
// target — and every polecat prompt in this tree tells it that its branch was
// created for it. Without this block the worker's most likely reading of `git
// log` is that main already contains this work, which is the one conclusion that
// makes it abandon the branch it was dispatched to land.
//
// It goes through TemplateVars.Prelude — prepended to the RENDERED prompt, never
// through the template — for that field's own reason: a `{{if}}` block would put
// the control back in an on-disk file whose absence is silent.
func strandedAdoptPrelude(workItemID string, a strandedAdoption) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ⚠ THIS IS AN ADOPTION — your worktree is based on %s, NOT on %s\n\n",
		a.Finding.Ref, a.Finding.Target)
	fmt.Fprintf(&b, "Work item %s already had %s, unmerged work on `%s`, and the dispatcher chose to "+
		"CONTINUE it rather than start over. The stated reason was:\n\n    %s\n\n",
		workItemID, strandedwork.Provenance(a.Finding.Pushed), a.Finding.Branch, a.Reason)
	fmt.Fprintf(&b, "So the commits already in your `git log` are **not** on %s and are **not** yours: "+
		"they are the work you were sent to land. %s\n\n", a.Finding.Target, a.Finding.Summary())
	b.WriteString("**Do not re-derive them, and do not reset to the target.** Your job is to get this " +
		"work merged — rebase it, finish what is missing, fix what the merge needs — and submit the " +
		"branch you are standing on. `git log " + a.Finding.Target + "..HEAD` is the inherited work; " +
		"read it before you write anything.\n\n")
	if a.Finding.PreRegistration != nil {
		fmt.Fprintf(&b, "**One of the inherited commits is a PRE-REGISTRATION commit (%s).** It records "+
			"predictions made BEFORE the results were known, and that is its entire value. Never amend "+
			"it, never reword it, and never rebase in a way that lets its content be edited after you "+
			"have seen how things turned out.\n\n",
			shortSHA(a.Finding.PreRegistration.SHA))
	}
	if !a.Finding.Pushed {
		fmt.Fprintf(&b, "**%s** — push early.\n\n", strandedwork.LocalOnlyWarning)
	}
	if len(a.Others) > 0 {
		fmt.Fprintf(&b, "Other stranded branches for this item were found and NOT adopted: %s. "+
			"Nothing here inherits them; say so in your verdict if they matter.\n\n",
			strings.Join(a.Others, ", "))
	}
	return b.String()
}

// emitPolecatStrandedAdopted records an ADOPT dispatch.
//
// It is a DIFFERENT event type from dispatch_stranded_work_overridden and that
// is the durable half of mg-ba32: a reader asking later what happened to a
// stranded branch gets "a worker was sent to continue it" or "a worker was sent
// past it", never the flattened "somebody overrode the gate" that both readings
// used to collapse into.
func emitPolecatStrandedAdopted(spawnReq SpawnPolecatAPIRequest, a strandedAdoption) {
	actor := "pogod"
	if spawnReq.Name != "" {
		actor = "cat-" + spawnReq.Name
	}
	details := map[string]any{
		"agent_type":       string(TypePolecat),
		"agent_name":       spawnReq.Name,
		"reason":           a.Reason,
		"refusal":          a.Refusal,
		"adopted_branch":   a.Finding.Branch,
		"adopted_ref":      a.Finding.Ref,
		"base_ref":         a.BaseRef(),
		"pushed":           a.Finding.Pushed,
		"target":           a.Finding.Target,
		"disposition":      string(a.Finding.Disposition),
		"unmerged":         len(a.Finding.Unmerged),
		"pre_registration": a.Finding.PreRegistration != nil,
	}
	if len(a.Others) > 0 {
		details["not_adopted"] = a.Others
	}
	events.Emit(context.Background(), events.Event{
		EventType:  "dispatch_stranded_work_adopted",
		Agent:      actor,
		WorkItemID: spawnReq.Id,
		Repo:       spawnReq.Repo,
		Details:    details,
	})
}
