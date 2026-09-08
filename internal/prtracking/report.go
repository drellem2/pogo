package prtracking

import (
	"fmt"
	"strings"
)

// Row is one classified PR.
type Row struct {
	PR     PR
	Result Result
}

// Report is a sweep's worth of rows plus the repos it could not read.
//
// Unreadable repos are carried, not dropped. A repo behind a SAML wall is the
// same shape of absence this whole package exists to name — "we could not see
// it" is not "there was nothing there" — and a report that lists zero findings
// over a repo it never reached is the defect wearing the detector's clothes.
type Report struct {
	Rows       []Row
	Unreadable map[string]string // slug -> why
	Repos      []string          // every repo asked about, readable or not
}

// Detect classifies each PR. A nil resolves yields StateUnmeasured rows.
func Detect(prs []PR, resolves func(string) bool) []Row {
	rows := make([]Row, 0, len(prs))
	for _, pr := range prs {
		rows = append(rows, Row{PR: pr, Result: Classify(pr, resolves)})
	}
	return rows
}

// Actionable reports whether anything in the sweep needs a human — an
// externally tracked PR counts, by design.
func (r Report) Actionable() bool {
	for _, row := range r.Rows {
		if row.Result.Actionable() {
			return true
		}
	}
	return len(r.Unreadable) > 0
}

// Counts returns the row count per state, keyed by the state's word.
func (r Report) Counts() map[string]int {
	c := map[string]int{
		StateTrackedHere.String():      0,
		StateTrackedElsewhere.String(): 0,
		StateNoTrackerFound.String():   0,
		StateUnmeasured.String():       0,
	}
	for _, row := range r.Rows {
		c[row.Result.State.String()]++
	}
	return c
}

// Render writes the human report. The denominators come first, so a zero is
// readable: "0 externally tracked out of 14 open PRs across 3 repos" and "0
// out of nothing measured" are different answers and must not print the same.
func (r Report) Render() string {
	var b strings.Builder
	c := r.Counts()
	fmt.Fprintf(&b, "Open-PR tracking over %d repo(s), %d open PR(s): %d tracked here, %d tracked elsewhere, %d no tracker found, %d unmeasured.\n",
		len(r.Repos), len(r.Rows),
		c[StateTrackedHere.String()], c[StateTrackedElsewhere.String()],
		c[StateNoTrackerFound.String()], c[StateUnmeasured.String()])

	emit := func(header string, want State) {
		var lines []string
		for _, row := range r.Rows {
			if row.Result.State == want {
				lines = append(lines, "  "+row.Result.Report(row.PR))
			}
		}
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s\n%s\n", header, strings.Join(lines, "\n"))
	}
	emit("TRACKED ELSEWHERE — actionable. Not a downgrade: an external-fleet PR is a thing to confirm, not to file away.", StateTrackedElsewhere)
	emit("NO TRACKER FOUND — actionable. File a carrier or establish supersession; never merge or close on a guess.", StateNoTrackerFound)
	emit("UNMEASURED — no resolver ran. Record under gaps; take no disposition.", StateUnmeasured)

	if len(r.Unreadable) > 0 {
		b.WriteString("\nREPOS NOT MEASURED — their PRs are absent from every count above:\n")
		for _, slug := range r.Repos {
			if why, bad := r.Unreadable[slug]; bad {
				fmt.Fprintf(&b, "  %s — %s\n", slug, why)
			}
		}
	}

	b.WriteString("\nThis answers only the pass's TRACKING question. Landed-ness is a separate\n" +
		"predicate (docs/pm-open-pr-pass.md), and a disposition needs both.\n")
	return b.String()
}
