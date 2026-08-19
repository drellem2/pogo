package strandwatch

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/strandedwork"
)

// Render writes the human form.
//
// The COVERAGE BLOCK COMES FIRST and prints even when there are no findings,
// because "nothing to report" and "nothing looked at" are the two readings this
// report exists to keep apart — and the second one is what the four stranded
// branches of 2026-08-09 looked like from every board in the fleet.
//
// THE HEADER STATES THE BOUND, NOT THE DENOMINATOR (mg-8baa). It used to read
// "112 open work item(s) scanned across 9 repo(s)", which is the population this
// sweep enumerated and not the population it looked at: three of those 112 were
// in repos whose branch listing failed and were never checked. "Scanned" is the
// word a reader takes as coverage, so the count beside it has to be the checked
// one, and any shortfall has to be on the same line rather than inferable by
// subtracting two numbers printed six lines apart.
//
// all=true additionally lists the exclusions by name. They are counted either
// way: a suppression a reader cannot see is indistinguishable from a miss.
func Render(rep Report, all bool) string {
	var b strings.Builder
	b.WriteString("stranded and landed-not-closed work — open items joined to their branches\n")
	fmt.Fprintf(&b, "  %d of %d open work item(s) CHECKED across %d repo(s)%s\n",
		rep.ItemsChecked, rep.ItemsScanned, len(rep.Repos), coverageShortfall(rep))
	for _, c := range rep.Repos {
		fetched := "refs as this clone last saw them"
		if c.Fetched {
			fetched = "refs refreshed from origin"
		}
		if c.Error != "" {
			fmt.Fprintf(&b, "    %s — COULD NOT LIST BRANCHES: %s\n", c.Repo, c.Error)
			continue
		}
		discovered := ""
		if !c.NamedByOpenItem {
			// The line that could not exist before mg-ded2. Without it a repository
			// holding polecat worktrees and named by no open item produced no output
			// of any kind, and the report was indistinguishable from a full sweep.
			discovered = " — NO OPEN ITEM NAMES THIS REPO; it is here because a polecat " +
				"worktree on this host points at it"
		}
		fmt.Fprintf(&b, "    %s — %d item(s), %d polecat branch(es), %d worktree(s), %s%s\n",
			c.Repo, c.Items, c.Branches, c.Worktrees, fetched, discovered)
	}
	for _, u := range rep.ReposUnmatched {
		// LOUD, and above the summary sentence rather than below it. The measured
		// defect was not that the information was missing — `0 repo(s)` was
		// printed — but that it sat one line above a summary sentence that
		// contradicted it and lost to the reassuring one.
		fmt.Fprintf(&b, "  --repo %q MATCHED NOTHING: %s\n", u.Repo, u.Error)
		fmt.Fprintf(&b, "    No open work item names it, no polecat worktree on this host points at it,\n"+
			"    and it could not be listed as a git repository. THIS RUN SAYS NOTHING ABOUT IT.\n")
	}
	if rep.ItemsWithoutRepo > 0 {
		fmt.Fprintf(&b, "  %d open item(s) name no repo and were NOT checked — that is a gap, not a clean verdict\n",
			rep.ItemsWithoutRepo)
	}
	if rep.ItemsOutOfScope > 0 {
		fmt.Fprintf(&b, "  %d open item(s) are outside the --repo restriction and were not looked at\n",
			rep.ItemsOutOfScope)
	}
	if rep.QueueUnreadable != "" {
		fmt.Fprintf(&b, "  refinery queue UNREADABLE (%s) — a branch already awaiting merge could not be\n"+
			"  excluded, so a row below may be one somebody already submitted\n", rep.QueueUnreadable)
	}
	fmt.Fprintf(&b, "  %d exclusion(s): branches of running polecats and branches already queued\n", len(rep.Excluded))
	if all {
		for _, e := range rep.Excluded {
			fmt.Fprintf(&b, "    %-14s %-24s %s\n", e.ItemID, e.Branch, e.Reason)
		}
	}
	for _, e := range rep.InspectErrors {
		fmt.Fprintf(&b, "  COULD NOT READ: %s\n", e)
	}
	for _, w := range rep.WorktreesUnreadableList {
		fmt.Fprintf(&b, "  WORKTREE NOT READ: %s — %s\n", w.Path, w.Error)
	}
	if len(rep.Frame) > 0 {
		b.WriteString("\nWHAT THIS REPORT CANNOT SEE:\n")
		for _, line := range rep.Frame {
			for _, wrapped := range wrap(line, 84) {
				fmt.Fprintf(&b, "  %s\n", wrapped)
			}
		}
	}
	if why := rep.Blind(); why != "" {
		// Ahead of the verdict, because the verdict is the sentence that travels.
		fmt.Fprintf(&b, "\nTHIS RUN MEASURED NOTHING: %s\n", why)
	}

	if len(rep.Rows) == 0 && rep.Blind() != "" {
		// NOT an all-clear. The summary sentence is what gets skimmed, quoted and
		// pasted into mail, so on a run that resolved nothing it must not be the
		// sentence a reader takes away — `--repo one_third_width_three` and
		// `--repo this-repo-does-not-exist-anywhere` printed byte-identical
		// all-clears and exited 0 (mg-ded2, gap 3).
		b.WriteString("\nNO VERDICT. Nothing was scanned, so \"nothing found\" would be a statement\n" +
			"about this command's arguments and not about the fleet's work.\n")
		return b.String()
	}

	if len(rep.Rows) == 0 {
		// The scope of this sentence is the CHECKED items, and it says so. Every
		// item that was not checked is a row above by construction (see
		// KindRepoUnreadable) with the single exception of a --repo restriction the
		// caller asked for, which is named here rather than left as a silent
		// narrowing of "no open work item".
		if rep.ItemsOutOfScope > 0 {
			fmt.Fprintf(&b, "\nNo open work item IN THE %d CHECKED has work already sitting on a branch.\n"+
				"%d further open item(s) were outside --repo and this run says nothing about them.\n",
				rep.ItemsChecked, rep.ItemsOutOfScope)
		} else {
			b.WriteString("\nNo open work item has work already sitting on a branch.\n")
		}
		if rep.ItemsScanned == 0 {
			b.WriteString("Note: ZERO items were scanned, so this is a blind run, not a clean one.\n")
		}
		return b.String()
	}

	// RESCUE-UNBUILT LEADS THE COUNT because it leads the rows (Kind.Rank), and
	// because the number a reader needs on the header line is "how many of these
	// must NOT be submitted" — the count that used to be folded silently into
	// `stranded` (mg-aed4).
	fmt.Fprintf(&b, "\n%d FINDING(S) — %d RESCUE-UNBUILT, %d stranded, %d landed-but-not-closed, "+
		"%d conflict suspect, %d UNJUDGED, %d REPO UNREADABLE, %d ORPHAN BRANCH:\n",
		len(rep.Rows), rep.Count(KindRescueUnbuilt), rep.Count(KindStranded),
		rep.Count(KindLandedNotClosed), rep.Count(KindConflictSuspect), rep.Count(KindUnjudged),
		rep.Count(KindRepoUnreadable), rep.Count(KindOrphanBranch))

	for _, r := range rep.Rows {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  %-17s %-10s %s\n", r.Kind, r.StatusLabel(), r.Subject())
		if r.Kind == KindOrphanBranch {
			fmt.Fprintf(&b, "    in %s — %s\n", r.Item.Repo, strandedwork.LocalOnlyWarning)
			fmt.Fprintf(&b, "    %d commit(s) on NO remote ref, and no OPEN work item names this branch:\n", r.Unmerged)
			for _, s := range r.Subjects {
				fmt.Fprintf(&b, "      %s\n", truncate(s, 92))
			}
			b.WriteString("    This is the row the open-item join cannot produce. There is no owner to ask\n" +
				"    and no item to submit under, so the first move is to make the object durable.\n")
			fmt.Fprintf(&b, "    -> %s\n", r.Remedy())
			continue
		}
		fmt.Fprintf(&b, "    %s\n", truncate(r.Item.Title, 96))
		if r.Kind == KindUnjudged {
			fmt.Fprintf(&b, "    branch %s COULD NOT BE READ: %s\n", r.Branch, r.Error)
			b.WriteString("    This is NOT a clean row. The branch was neither judged stranded nor judged\n" +
				"    landed, and an unjudged branch on an open item may be either.\n")
			fmt.Fprintf(&b, "    -> %s\n", r.Remedy())
			continue
		}
		if r.Kind == KindRepoUnreadable {
			fmt.Fprintf(&b, "    repo %s COULD NOT BE LISTED: %s\n", quoteRepo(r.Item.Repo), r.Error)
			b.WriteString("    NO BRANCH WAS LOOKED FOR on this item. It is not stranded, not landed and\n" +
				"    not clean — it is unchecked, and an unchecked open item may be carrying work\n" +
				"    that a dispatch would re-derive.\n")
			fmt.Fprintf(&b, "    -> %s\n", r.Remedy())
			continue
		}
		pushed := "LOCAL ONLY — not on origin, and git-gc reaps the worktree"
		if r.Pushed {
			pushed = "pushed"
		}
		fmt.Fprintf(&b, "    branch %s (%s) vs %s\n", r.Branch, pushed, r.Target)
		switch r.Kind {
		case KindLandedNotClosed:
			fmt.Fprintf(&b, "    %d commit(s) already in the target under a rewritten sha; 0 unmerged.\n", r.Equivalent)
			b.WriteString("    The work is ON the target and the item is still asking for it.\n")
		case KindConflictSuspect:
			fmt.Fprintf(&b, "    %d unmerged commit(s) by patch id, BUT %s.\n", r.Unmerged, r.Presence.Describe())
			b.WriteString("    That is the signature of a rebase that resolved a conflict: the work landed\n" +
				"    and the patch id did not survive. VERIFY BEFORE ACTING — submitting is noise,\n" +
				"    closing throws the branch away, and the two instruments disagree.\n")
		default:
			fmt.Fprintf(&b, "    %d unmerged commit(s); %s.\n", r.Unmerged, r.Presence.Describe())
			for _, s := range r.Subjects {
				fmt.Fprintf(&b, "      %s\n", truncate(s, 92))
			}
		}
		if r.PreRegistration != nil {
			fmt.Fprintf(&b, "    PRE-REGISTRATION commit %s is unmerged — a worker branching from %s would\n"+
				"    write its predictions AFTER seeing the results. Branch FROM it or resubmit.\n",
				short(r.PreRegistration.SHA), r.Target)
		}
		if r.Rescue != nil {
			// PRINTED ON WHATEVER KIND THE ROW ENDED UP AS. Only the stranded cell
			// becomes KindRescueUnbuilt (see classify), but a conflict_suspect row
			// carrying a rescue commit is still unreviewed work and a reader deciding
			// what to do about it needs to know that. (landed_not_closed can never
			// reach here: the field is populated from UNMERGED commits and that row
			// has none.)
			tracker := ""
			if id := r.Rescue.RescueTracker(); id != "" {
				tracker = fmt.Sprintf(", rescue tracked at %s", id)
			}
			fmt.Fprintf(&b, "    RESCUE COMMIT %s%s — committed with `--no-verify` and NEVER BUILT.\n",
				short(r.Rescue.SHA), tracker)
			b.WriteString("    This is possibly-partial work recovered from a dead polecat's worktree: the hook\n" +
				"    bypass was deliberate (a rescue must not be gated on whether the work compiles) and\n" +
				"    it means nobody has compiled this and nobody has reviewed it. A PASSING gate is the\n" +
				"    worse outcome here, not the good one — it merges half-implemented code.\n")
		}
		fmt.Fprintf(&b, "    -> %s\n", r.Remedy())
	}

	b.WriteString("\nThis command submitted nothing and closed nothing. Both remedies are one command\n" +
		"and both are destructive in the wrong direction, so they stay with a reader.\n")
	return b.String()
}

// coverageShortfall names the gap between the population and what was looked at,
// on the header line itself.
//
// It is a suffix on that line and not a paragraph below it because the header is
// the part that gets skimmed, quoted and pasted into mail. The mayor's ticket
// quotes it having travelled exactly that way, as "112 open work item(s) scanned
// across 7 repo(s)" — that repo count is theirs, not re-derived here; the re-run
// that reproduced the defect on 2026-08-14 read 9, the board having gained repos
// in between. Same 112, same three items nobody looked at.
func coverageShortfall(rep Report) string {
	var parts []string
	if n := rep.ItemsScanned - rep.ItemsChecked - rep.ItemsOutOfScope; n > 0 {
		parts = append(parts, fmt.Sprintf("%d NOT CHECKED", n))
	}
	if rep.ItemsOutOfScope > 0 {
		parts = append(parts, fmt.Sprintf("%d outside --repo", rep.ItemsOutOfScope))
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, ", ")
}

// quoteRepo renders an item's repo field so a bare relative name is visibly a
// bare relative name. `onethird_program` and `/Users/daniel/research/onethird_program`
// are one character of prefix apart in a report and two different repositories in
// fact; the store holds both forms today.
func quoteRepo(repo string) string {
	if repo == "" {
		return "(none)"
	}
	return fmt.Sprintf("%q", repo)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// wrap breaks a frame line at word boundaries so a boundary statement stays
// readable in a terminal. The frame is the part of this report meant to be read
// rather than grepped, and an 800-column paragraph is not read.
func wrap(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var (
		lines []string
		cur   = words[0]
	)
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	return append(lines, cur)
}
