package main

// The "$POGO_HOME version control" line in `pogo doctor --check` (mg-3610).
//
// WHAT IT DETECTS. A git repository that versions the files pogo writes under
// $POGO_HOME. On this fleet's host that repository is `drellem2/pogo-config`,
// whose working tree IS `~/.pogo`, and which tracks `agents/mayor.md`,
// `agents/crew/doctor.md`, `agents/templates/*.md` and `projects.json` — all
// install or daemon output. The tree is therefore permanently dirty (measured
// 2026-08-05: 11 files, 431 insertions, 162 deletions, no hand edits), and a
// `git pull` there conflicts in the live prompt files the crew is reading.
//
// WHY IT REPORTS HERE. Nothing in the pogo repo mentions pogo-config — the word
// does not appear in it — so the condition is deducible from neither repo
// alone. Three generations of `agents/templates/polecat-build-pr.md` existed
// simultaneously (pogo-config HEAD / the working tree / pogo source) and agents
// spent hours reasoning about prompt staleness against the wrong baseline. A
// detector on the checklist people already read is the cheapest thing that
// makes the second repo discoverable at all.
//
// WHY IT WARNS AND NEVER FAILS. `fail` sets doctor's exit code. The remedy —
// `git rm --cached` plus a gitignore in a live directory holding the fleet's
// mail spool, schedules and events.log — is a machine-local ops action for a
// human, and whoever is scripting doctor's exit status did not ask to be
// blocked on somebody else's decision to run it. Same posture as the launchd
// and audit-successor rows.
//
// WHY THE ROW RENDERS EVEN WHEN CLEAN. "Not tracked" and "not checked" are
// different facts, and a row that appears only on drift is invisible in exactly
// the way its subject is. The four states — no repo / repo tracks nothing pogo
// writes / repo tracks install output / could not look — are each said out loud.

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/homevcs"
)

// homeVCSCheckName is the doctor checklist row this renders on.
const homeVCSCheckName = "$POGO_HOME version control"

// homeVCSMaxListed caps the named paths. The list is evidence, not the finding;
// a reader who needs all of them has `git ls-files` and the count to check it
// against.
const homeVCSMaxListed = 8

// homeVCSLine renders one doctor check row from an audit.
func homeVCSLine(rep homevcs.Report) (status, detail string) {
	if rep.Unknown != "" {
		return "warn", "NOT CHECKED: " + rep.Unknown
	}
	if !rep.Versioned {
		return "pass", fmt.Sprintf("%s is not inside a git work tree, so nothing versions the %d path(s) pogo writes there",
			rep.Home, rep.Examined)
	}

	where := rep.Toplevel
	if rep.Remote != "" {
		where += " (origin " + rep.Remote + ")"
	}

	if len(rep.Tracked) == 0 {
		return "pass", fmt.Sprintf("%s is inside git work tree %s, which tracks none of the %d path(s) pogo writes there — that repo versions only files no pogo process rewrites",
			rep.Home, where, rep.Examined)
	}

	named := make([]string, 0, len(rep.Tracked))
	for i, f := range rep.Tracked {
		if i == homeVCSMaxListed {
			named = append(named, fmt.Sprintf("(+%d more)", len(rep.Tracked)-homeVCSMaxListed))
			break
		}
		state := f.Writer
		if f.Dirty {
			state += ", MODIFIED"
		}
		named = append(named, fmt.Sprintf("%s [%s]", f.Path, state))
	}

	dirty := rep.DirtyCount()
	lead := fmt.Sprintf("git work tree %s TRACKS %d of the %d path(s) pogo writes under %s",
		where, len(rep.Tracked), rep.Examined, rep.Home)
	consequence := "every install rewrites them, so that tree can never be clean and a merge into it conflicts with an install"
	if dirty > 0 {
		// Precise about WHEN it bites: a pull conflicts only once an incoming
		// commit touches one of these paths, and on 2026-08-06 the pending
		// pogo-config commit touched none of them. Saying "a pull conflicts"
		// flat would be an urgency the measurement does not support, and the
		// first reader to pull cleanly would discount the row entirely.
		consequence = fmt.Sprintf("%d of them are modified RIGHT NOW by pogo rather than by hand, so a `git pull` there conflicts in files the fleet is reading the moment an incoming commit touches one of them", dirty)
	}
	return "warn", fmt.Sprintf("%s: %s. %s. Do NOT resolve this by committing them — that records one machine's output as shared truth and re-dirties on the next install; see docs/pogo-home-version-control.md",
		lead, strings.Join(named, "; "), consequence)
}
