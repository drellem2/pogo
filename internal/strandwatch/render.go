package strandwatch

import (
	"fmt"
	"strings"
)

// Render writes the human form.
//
// The COVERAGE BLOCK COMES FIRST and prints even when there are no findings,
// because "nothing to report" and "nothing looked at" are the two readings this
// report exists to keep apart — and the second one is what the four stranded
// branches of 2026-08-09 looked like from every board in the fleet.
//
// all=true additionally lists the exclusions by name. They are counted either
// way: a suppression a reader cannot see is indistinguishable from a miss.
func Render(rep Report, all bool) string {
	var b strings.Builder
	b.WriteString("stranded and landed-not-closed work — open items joined to their branches\n")
	fmt.Fprintf(&b, "  %d open work item(s) scanned across %d repo(s)\n", rep.ItemsScanned, len(rep.Repos))
	for _, c := range rep.Repos {
		fetched := "refs as this clone last saw them"
		if c.Fetched {
			fetched = "refs refreshed from origin"
		}
		if c.Error != "" {
			fmt.Fprintf(&b, "    %s — COULD NOT LIST BRANCHES: %s\n", c.Repo, c.Error)
			continue
		}
		fmt.Fprintf(&b, "    %s — %d item(s), %d polecat branch(es), %s\n", c.Repo, c.Items, c.Branches, fetched)
	}
	if rep.ItemsWithoutRepo > 0 {
		fmt.Fprintf(&b, "  %d open item(s) name no repo and were NOT checked — that is a gap, not a clean verdict\n",
			rep.ItemsWithoutRepo)
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

	if len(rep.Rows) == 0 {
		b.WriteString("\nNo open work item has work already sitting on a branch.\n")
		if rep.ItemsScanned == 0 {
			b.WriteString("Note: ZERO items were scanned, so this is a blind run, not a clean one.\n")
		}
		return b.String()
	}

	fmt.Fprintf(&b, "\n%d FINDING(S) — %d stranded, %d landed-but-not-closed, %d conflict suspect, %d UNJUDGED:\n",
		len(rep.Rows), rep.Count(KindStranded), rep.Count(KindLandedNotClosed),
		rep.Count(KindConflictSuspect), rep.Count(KindUnjudged))

	for _, r := range rep.Rows {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  %-17s %-10s %s\n", r.Kind, r.Item.Status, r.Item.ID)
		fmt.Fprintf(&b, "    %s\n", truncate(r.Item.Title, 96))
		if r.Kind == KindUnjudged {
			fmt.Fprintf(&b, "    branch %s COULD NOT BE READ: %s\n", r.Branch, r.Error)
			b.WriteString("    This is NOT a clean row. The branch was neither judged stranded nor judged\n" +
				"    landed, and an unjudged branch on an open item may be either.\n")
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
		fmt.Fprintf(&b, "    -> %s\n", r.Remedy())
	}

	b.WriteString("\nThis command submitted nothing and closed nothing. Both remedies are one command\n" +
		"and both are destructive in the wrong direction, so they stay with a reader.\n")
	return b.String()
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
