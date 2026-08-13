package main

// The "agent-state repo publication" line in `pogo doctor --check` (mg-015c).
//
// WHAT IT DETECTS. A repository this host pushes agent state to that is PUBLIC.
// Two live members on this box, both private as of 2026-08-13:
// `drellem2/pogo-config` (work tree `~/.pogo`, tracks the crew prompts including
// `agents/crew/pa.md` — Daniel's email address, his calendar ids, his
// personal-assistant setup) and `drellem2/pogo-agent-memory` (work tree the
// shared memory corpus, which held his real calendar events in its history).
//
// WHY IT IS THE ONE ROW THAT FAILS ON A REPO'S MERE STATE. Daniel ruled on
// 2026-08-13 that pogo-config "absolutely should not be public" — a standing
// constraint on the state, not a correction to a document. The sibling row
// (`$POGO_HOME version control`) deliberately never sets doctor's exit code,
// because untracking install output is a human ops decision with a debatable
// remedy. This is not that: the content is already sensitive, the remedy is one
// command, and a constraint reported at the same volume as a tidiness note is a
// constraint nobody can script against.
//
// WHY IT IS A SEPARATE ROW RATHER THAN A CLAUSE ON THE SIBLING. The sibling is
// named after $POGO_HOME and is structurally about one directory. This question
// has two subjects today and gained the second one *while it was being written*.
// Folding it in would have meant either duplicating the finding across rows or
// building a check that could not see the repo it was told about on day one.
//
// WHY IT RENDERS EVEN WHEN EVERYTHING IS PRIVATE. Both subjects are private, so
// this row will spend its life green — which is exactly why the green must name
// what it checked. A row that speaks only on exposure is indistinguishable from
// a row that stopped running.

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/homevcs"
)

// agentStatePublicationCheckName is the doctor checklist row this renders on.
const agentStatePublicationCheckName = "agent-state repo publication"

// agentStateMaxHolds caps how many subject labels one repo lists. Every
// per-agent memory dir under $POGO_HOME resolves to the same work tree, so this
// list is routinely long and is evidence rather than the finding.
const agentStateMaxHolds = 4

// agentStateMaxUnversionedNamed caps the named directories under no repo at all.
// These are the clean entries and they outnumber the findings on this host; the
// count is the fact, the names are evidence.
const agentStateMaxUnversionedNamed = 3

// agentStatePublicationLine renders one doctor check row from a publication
// audit. The most exposed repository leads: the head of a checklist detail is
// the part that gets skimmed and forwarded.
func agentStatePublicationLine(rep homevcs.PublicationReport) (status, detail string) {
	if len(rep.Subjects) == 0 {
		// Nothing to check is not the same as nothing found. A caller that
		// enumerated no subjects has a bug, and the row must not read as an
		// all-clear on its behalf.
		return "warn", "no agent-state directories were enumerated, so nothing checked whether any repository this host pushes to is public"
	}

	status = "pass"
	var parts []string
	for _, r := range rep.Repos {
		s, d := agentStateRepoClause(r)
		status = worseCheckStatus(status, s)
		parts = append(parts, d)
	}
	for _, u := range rep.Undecided {
		status = worseCheckStatus(status, "warn")
		parts = append(parts, "NOT ESTABLISHED for "+u+" — read as unchecked, not as private")
	}
	// Collapsed to a count with a few names. These are the CLEAN entries — a
	// directory under no repo pushes nothing — and on this host they outnumber
	// the findings ten to one. Printing them all buried the two verdicts that
	// matter under fifteen lines of good news.
	if n := len(rep.Unversioned); n > 0 {
		named := rep.Unversioned
		suffix := ""
		if n > agentStateMaxUnversionedNamed {
			named = named[:agentStateMaxUnversionedNamed]
			suffix = fmt.Sprintf(", +%d more", n-agentStateMaxUnversionedNamed)
		}
		parts = append(parts, fmt.Sprintf("%d other agent-state %s under no git work tree, so nothing there is pushed anywhere (%s%s)",
			n, pick(n, "directory is", "directories are"), strings.Join(named, "; "), suffix))
	}

	// Spelled out rather than run through plural(): that helper appends a bare
	// "s", which turns "directory" into "directorys".
	lead := fmt.Sprintf("checked %d agent-state %s across %d %s",
		len(rep.Subjects), pick(len(rep.Subjects), "directory", "directories"),
		len(rep.Repos), pick(len(rep.Repos), "repository", "repositories"))
	return status, lead + ". " + strings.Join(parts, ". ")
}

// agentStateRepoClause renders one repository's verdict.
func agentStateRepoClause(r homevcs.RepoPublication) (status, detail string) {
	named := r.Name
	if named == "" {
		named = r.Remote
	}
	holds := holdsPhrase(r.Holds)

	if r.Remote == "" {
		return "pass", fmt.Sprintf("%s (%s) has no origin remote, so nothing it versions leaves this host", r.Toplevel, holds)
	}
	if r.Unknown != "" {
		return "warn", fmt.Sprintf("PUBLICATION STATE NOT ESTABLISHED for origin %s (%s, holding %s): %s — read this as unchecked, NOT as private; ask by hand with `gh repo view %s --json visibility`",
			named, r.Toplevel, holds, r.Unknown, named)
	}
	switch r.Visibility {
	case homevcs.VisibilityPrivate:
		return "pass", fmt.Sprintf("origin %s (holding %s) is PRIVATE", named, holds)
	case homevcs.VisibilityPublic:
		return "fail", fmt.Sprintf("SECURITY: origin %s is PUBLIC and it versions %s — everything that repository tracks, and everything ever committed to it, is world-readable. Make it private now (`gh repo edit %s --visibility private`) and treat the history as already disclosed",
			named, holds, named)
	case "":
		// A repo with a remote, no verdict and no reason is a defect in this
		// check, not a private repo. Falling through to the clean branch here
		// is how a detector stops detecting with no test going red.
		return "warn", fmt.Sprintf("origin %s (holding %s) carries NO publication verdict and no reason for the absence — that is a defect in this check, not evidence the repo is private; ask by hand with `gh repo view %s --json visibility`",
			named, holds, named)
	default:
		return "warn", fmt.Sprintf("origin %s (holding %s) reports visibility %s, which is not PRIVATE: what it versions is readable beyond this host. If that is not intended, `gh repo edit %s --visibility private`",
			named, holds, r.Visibility, named)
	}
}

// holdsPhrase names the subjects a repo versions, capped.
func holdsPhrase(holds []string) string {
	if len(holds) == 0 {
		return "no enumerated subject"
	}
	if len(holds) <= agentStateMaxHolds {
		return strings.Join(holds, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(holds[:agentStateMaxHolds], ", "), len(holds)-agentStateMaxHolds)
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// worseCheckStatus picks the status a reader must act on when one row carries
// several findings. Ordered fail > warn > pass, so a clean finding can never
// mask a dirty one — the row's status is a floor, not an average.
func worseCheckStatus(a, b string) string {
	rank := func(s string) int {
		switch s {
		case "fail":
			return 2
		case "warn":
			return 1
		}
		return 0
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}
