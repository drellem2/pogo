package strandedwork

import "fmt"

// Cell is the ONE decision table every surface that judges a stranded branch
// reads to choose its remedy (mg-8cda).
//
// THERE WERE TWO TABLES AND THEY DRIFTED, which is drellem2/pogo#174's whole
// shape. `pogo check-stranded` (internal/strandwatch) and pogod's stranded mail
// (internal/agent/strandedmail.go) — and the dispatch refusal beside it — each
// decided "submit / check by hand / do not submit" from the same two facts, with
// their own switch statements. mg-aed4 taught the rescue cell to check-stranded
// only, and the mail re-grew the contradiction; #174 then taught the partly-present
// band to the mail only, and check-stranded disagreed in the other direction. A
// fix applied to one table is a drift introduced into the other, so the cells
// live here and every surface renders the cell it is handed.
//
// WHAT IT DECIDES FROM is exactly what every surface can assemble at the moment
// it speaks: the content second opinion (Presence) and whether an unmerged
// commit is a RESCUE commit. Both are properties of the branch alone.
//
// WHAT IT DOES NOT DECIDE, and why that is not cheap to change. check-stranded
// has two further cells that consult facts outside the branch: `refused_before`
// (the refinery's merge HISTORY says an unchanged resubmit repeats a failure)
// and `in_flight` (the refinery QUEUE already holds the branch). Those are
// OVERLAYS on a Cell rather than rows of this table — each only ever withholds
// the submit that CellResubmit or CellCheckByHand would print — and the pogod
// surfaces do not have them. pogod hosts the refinery in-process, so the data
// exists; what it would take is (1) moving cmd/pogo's history/queue conversion
// (refineryMergeHistory, queuedRefineryBranches) out of the CLI into a package
// both binaries import, (2) exporting strandwatch's historyView.forBranch — the
// stale-refusal and aged-out-window logic is the part that must not be
// re-implemented — (3) injecting a history source into agent.Registry the way
// SetStrandedWorkGate injects the branch scan, and (4) giving the mail and the
// refusal a refused and an in-flight rendering. See docs/operations.md,
// "One decision table".
type Cell string

const (
	// CellResubmit: the second opinion was measured and is consistent with
	// absent, and no unmerged commit is a rescue. The one cell whose remedy is an
	// unconditional, paste-ready submit.
	CellResubmit Cell = "resubmit"

	// CellCheckByHand: the second opinion is unmeasured, unavailable, or PARTLY
	// PRESENT (ContentAbsentRatio..ContentLandedRatio) — the row rests on
	// `git cherry` alone, which over-reports (mg-5ec6). Check whether the work
	// already landed first; the submit is printed only as conditional on that.
	CellCheckByHand Cell = "check_by_hand"

	// CellSuggestsLanded: the target already holds essentially every line the
	// branch adds (>= ContentLandedRatio). NO submit is printed, even
	// conditionally: the work most likely landed under another sha, submitting it
	// is noise and closing the item throws the branch away if it did not, so the
	// remedy names neither action (strandwatch's conflict_suspect).
	//
	// IT OUTRANKS THE RESCUE CELL, and that is safe precisely because it prints
	// no submit either: the rescue fact is still stated on the row, and what this
	// cell adds — that the target may ALREADY hold the work — is the one fact the
	// rescue cell would lose (mg-aed4).
	CellSuggestsLanded Cell = "suggests_landed"

	// CellRescueUnbuilt: an unmerged commit is a RESCUE commit — recovered from a
	// dead polecat's worktree with the pre-commit hook bypassed, never built and
	// never reviewed. NO submit is printed: its failure mode is a gate PASSING
	// and merging unreviewed work (mg-aed4).
	CellRescueUnbuilt Cell = "rescue_unbuilt"
)

// Decide is the table. rescue is the unmerged rescue commit, or nil.
func Decide(p Presence, rescue *Commit) Cell {
	switch {
	case p.SuggestsLanded():
		return CellSuggestsLanded
	case rescue != nil:
		return CellRescueUnbuilt
	case p.ConsistentWithAbsent():
		return CellResubmit
	default:
		return CellCheckByHand
	}
}

// PrintsSubmit reports whether a surface may print a submit command in this
// cell at all, conditional or not.
func (c Cell) PrintsSubmit() bool {
	return c == CellResubmit || c == CellCheckByHand
}

// RescueCommit returns the unmerged rescue commit this finding carries: Rescue
// when Inspect set it, otherwise the first unmerged commit that is one. The
// fallback is for findings assembled by hand, which set Unmerged and not Rescue;
// a table that read only the field would let such a finding skip the rescue cell.
func (f Finding) RescueCommit() *Commit {
	if f.Rescue != nil {
		return f.Rescue
	}
	for i := range f.Unmerged {
		if f.Unmerged[i].IsRescue() {
			return &f.Unmerged[i]
		}
	}
	return nil
}

// HandCheckCommand is the one command that answers "did this item's work already
// land on target under another sha?": the refinery's merges keep the commit
// subject, and the subject carries the work-item id. "" when there is no item id
// to search for.
func HandCheckCommand(repo, workItemID, target string) string {
	if workItemID == "" {
		return ""
	}
	return fmt.Sprintf("git -C %s log --oneline --fixed-strings --grep=%s %s", repo, workItemID, target)
}

// ReadRescueCommand is the command a rescue cell prints in place of a submit: the
// branch's unmerged work, in full, so that it is read before anything else.
func ReadRescueCommand(repo, target, ref string) string {
	return fmt.Sprintf("git -C %s log -p %s..%s", repo, target, ref)
}

// RemedyPhrase is the remedy as one clause, for surfaces that speak in a single
// sentence — the dispatch refusal and Finding.Summary. The mail and the
// check-stranded row render the same cell in their own layout; this is the
// wording where there is no layout.
//
// It starts lower-case so that callers can lead into it ("Instead, ..." /
// "Either ..., or ...").
func RemedyPhrase(c Cell, f Finding, author string) string {
	submit := SubmitRemedy(f.Repo, f.Branch, author, f.Pushed)
	id := author
	if id == "" {
		id = f.WorkItemID
	}
	check := fmt.Sprintf("look for the same commit subject on %s under another sha", f.Target)
	if cmd := HandCheckCommand(f.Repo, id, f.Target); cmd != "" {
		check = fmt.Sprintf("`%s`", cmd)
	}
	ref := f.Ref
	if ref == "" {
		ref = f.Branch
	}
	switch c {
	case CellResubmit:
		return fmt.Sprintf("get the branch merged (`%s`)", submit)
	case CellSuggestsLanded:
		return fmt.Sprintf("check by hand first whether it already landed on %s (%s) — the target "+
			"already holds essentially every line it adds, so submitting is likely noise and closing "+
			"throws the branch away if it did not; do neither blind", f.Target, check)
	case CellRescueUnbuilt:
		return fmt.Sprintf("read it and build it first (`%s`) — it carries a RESCUE commit that "+
			"bypassed the pre-commit hook and has never been built or reviewed, so no submit "+
			"command is given here on purpose", ReadRescueCommand(f.Repo, f.Target, ref))
	default:
		return fmt.Sprintf("check by hand first whether it already landed on %s (%s), and only if it "+
			"did NOT, get it merged (`%s`)", f.Target, check, submit)
	}
}

// Cell is this finding's row in the table, given its measured presence.
func (f Finding) Cell(p Presence) Cell {
	return Decide(p, f.RescueCommit())
}
