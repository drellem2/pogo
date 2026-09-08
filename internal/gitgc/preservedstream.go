package gitgc

import (
	"fmt"
	"strings"
)

// The streamed rendering of `pogo gc --list-preserved` (mg-1530,
// drellem2/pogo#158).
//
// # The defect, stated once, in the terms the reporter saw it
//
// The command wrote its FIRST BYTE only after the entire scan finished. Every
// external call it made was unbounded — 114 git/mg subprocesses in one measured
// run over 60 directories, none with a timeout — and each retained tree costs a
// full working-tree walk on top. So one slow tree produced exactly what #158
// reports: zero output, no return, and no indication of which tree. The
// operator's only move was to kill it, and their next command after that is
// `pogo gc --repo=... --apply --force`, which is repo-scoped, forced, and
// DISCARDS uncommitted work. A listing that cannot be read is not merely
// unhelpful here; it pushes the reader toward the destructive alternative.
//
// # What streaming changes, and what it deliberately does not
//
// It does not make the scan faster. A scan that took four minutes still takes
// four minutes. What changes is that those four minutes are now legible: the
// denominator is known before the first tree is touched, each tree is NAMED
// BEFORE it is read (so a stall names its own cause), and each retained tree's
// full block lands the moment it resolves. A killed scan therefore leaves a
// PARTIAL INVENTORY behind rather than nothing — which is the difference
// between an operator who knows about four of their retained trees and one who
// knows about none.
//
// # A partial listing must SAY it is partial (pm-pogo's acceptance condition)
//
// This is the requirement that shapes the header, and it is not decoration. The
// reader of this output is deciding what to delete. A listing that stopped early
// but reads as complete is WORSE than the hang it replaced: the hang at least
// announced itself. So the header states the sentinel ("scan complete") that a
// finished listing ends with, and StreamedTail is the only thing that prints it.
// Absence of that line is the signal, and it is a signal the reader was told
// about before they had any rows to misread.

// StreamedHeader opens a streamed listing: what is being scanned, how much of
// it there is, and how to tell a finished listing from a truncated one.
func StreamedHeader(polecatsDir, repoFilter string, total int) string {
	var b strings.Builder
	writeHeadline(&b, PreservedReport{PolecatsDir: polecatsDir})
	if repoFilter != "" {
		// Named without a count, because the count does not exist yet. A
		// filtered listing that says "0 tree(s) in other repositories not
		// shown" before it has looked is the same failure as a truncated file
		// list that does not say it truncated — the real figure is in the
		// counts at the end, where it has been measured.
		fmt.Fprintf(&b, "  (filtered to repo %s — trees in other repositories are NOT shown, and\n"+
			"   how many were excluded is counted at the end. A tree whose .git pointer could\n"+
			"   not be read is shown anyway, since it may be this one)\n", repoFilter)
	}
	fmt.Fprintf(&b, "  %d director(ies) to read. Each retained tree is printed BELOW as it "+
		"resolves;\n", total)
	fmt.Fprintf(&b, "  the counts can only be known at the end and are printed there.\n")
	fmt.Fprintf(&b, "  Progress goes to STDERR, one line per directory, naming each tree BEFORE it\n")
	fmt.Fprintf(&b, "  is read — so if this stalls, the last line names the tree it stalled on.\n")
	fmt.Fprintf(&b, "\n  IF THIS OUTPUT ENDS WITHOUT THE %q LINE, THE SCAN DID NOT\n",
		streamCompleteSentinel)
	fmt.Fprintf(&b, "  FINISH AND WHAT YOU HAVE READ IS PARTIAL — there are retained trees below\n")
	fmt.Fprintf(&b, "  the last one shown.\n")
	return b.String()
}

// streamCompleteSentinel is the phrase StreamedTail opens with and StreamedHeader
// promises. Nothing else prints it, which is what makes its ABSENCE mean
// something.
const streamCompleteSentinel = "scan complete"

// PreservedPreamble is the report's refusal to do the reader's job for them,
// exported so the streamed listing can print it BEFORE its first tree.
//
// Position is the whole reason this is exported. In the grouped report the
// preamble sits above the trees because the trees come after it; in a streamed
// one the first tree may land minutes before the last, so a preamble held to
// the end would reach a reader who has already made up their mind. It is
// printed once, before the first row, or not at all when there are no rows.
func PreservedPreamble() string { return preservedPreamble }

// StreamedTree renders one retained tree the moment it resolves.
//
// It is writeTree's block with the repository named on the row — see
// writeTreeRow for why that field has to travel with a streamed row and not
// with a grouped one.
//
// Only RETAINED trees stream. A tree in use by a live polecat is not the
// population this command exists to enumerate, it is explicitly do-not-touch,
// and StreamedTail lists every one of them; streaming it too would put the same
// tree in the output twice, which in a listing this long is how a reader ends
// up counting one tree as two.
func StreamedTree(t PreservedTree) string {
	var b strings.Builder
	writeTreeRow(&b, t, true)
	return b.String()
}

// StreamedTail renders everything that only a FINISHED scan can know: the
// counts, the per-repository reclaim blast radius, the trees in use, and the
// errors.
//
// The per-repository grouping moves here from the middle of the report, and it
// keeps its job. `pogo gc --repo=<repo> --apply --force` is repo-scoped and
// forced — it takes every eligible retained tree in that repository at once,
// not the one the operator just read — and that fact has to appear beside the
// count of trees it would take. It cannot appear ABOVE a streamed row, because
// no row knows how many siblings it has until the scan ends; so it appears
// here, naming them.
func (r PreservedReport) StreamedTail() string {
	var b strings.Builder
	r.applyCounts()

	fmt.Fprintf(&b, "\n%s — %d retained tree(s) printed above.\n",
		streamCompleteSentinel, r.RetainedCount)
	writeCounts(&b, r)

	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\n  note: %s\n", n)
	}

	if len(r.Retained) == 0 {
		fmt.Fprintf(&b, "\nNothing is retained. Every polecat worktree here is clean or in use.\n")
	} else {
		fmt.Fprintf(&b, "\nwhat `--force` would reclaim, per repository:\n")
		for _, group := range groupByRepo(r.Retained) {
			writeRepoHeader(&b, group)
		}
		b.WriteString(preservedFooter)
	}

	writeInUse(&b, r)
	writeErrors(&b, r)
	return b.String()
}
