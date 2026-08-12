package main

// `pogo investigations [terms...]` (mg-22c7): search docs/investigations/ by
// file CONTENTS and print the matching reports with their paths.
//
// This is phase 1 of a two-phase decision, and phase 1 is deliberately small:
// no schema change, no wiring into `mg new` or the polecat dispatch template,
// nothing that fires on your behalf. See internal/investigations for why the
// corpus is the files rather than README.md.
//
// PHASE 1 IS ALSO THE EXPERIMENT, WHICH IS WHY IT EMITS AN EVENT.
//
// The objection to this command is the same objection that killed the
// alternatives: a tool you must REMEMBER to run is still recall-dependent, and
// on the night that produced this ticket three agents each held relevant notes
// and none fired. So the question of whether the gap was friction (a tool fixes
// it) or recall (only automatic triggers fix it) is open, and phase 2 —
// suggesting matches at `mg new`, or carrying the search in the dispatch
// template — is gated on the answer.
//
// A gate needs both branches to be observable. Nothing on this box records a
// CLI invocation: all 72 event types in ~/.pogo/events.log are daemon-side, so
// "built and never used" — the branch that JUSTIFIES phase 2 — would have
// produced no artifact at all, and silence would be indistinguishable from
// "nobody has needed it yet". Every invocation of this command therefore emits
// one investigation_search event, including the zero-match and error paths,
// because a search that was attempted and found nothing is the most informative
// record this command can leave.
//
// Scope note: this is one event for one subcommand. It is not a general
// cli_invoked, which is a separate decision with its own volume and privacy
// questions that nobody has made.
//
// READING THE COUNT AT THE GATE. An invocation count alone cannot settle it: a
// zero could mean nobody remembered (recall failure, build phase 2) or that no
// question arose the corpus could answer (no problem, do nothing) — opposite
// conclusions from the same number. The deciding measurement is how many
// incidents in the window had an answer in the corpus that went unfound; the
// event count is the cheap half.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/investigations"
)

// EventTypeInvestigationSearch is the event every invocation emits. Documented
// in docs/event-log.md; see the file comment above for why it exists.
const EventTypeInvestigationSearch = "investigation_search"

// exitInvestigationsUsage is returned for a malformed invocation or a corpus
// that cannot be located. A search that RAN exits 0 whether or not it matched:
// this is a measurement, and a measurement that sets an error status on an
// empty result invites being read as a verdict about the corpus.
const exitInvestigationsUsage = 2

// defaultMatchLines is how many matching lines are shown per document. Enough
// to judge relevance, few enough that a broad query stays readable. Doc.Hits
// always reports the untruncated count.
const defaultMatchLines = 3

func newInvestigationsCmd(jsonOutput *bool) *cobra.Command {
	var (
		dir        string
		limit      int
		matchLines int
		filesOnly  bool
	)
	cmd := &cobra.Command{
		Use:   "investigations [terms...]",
		Short: "Search docs/investigations/ by file contents (not the README index)",
		Long: `Search the point-in-time reports in docs/investigations/ and print the ones that
match, with their paths. With no terms, list the whole corpus.

Terms are matched case-insensitively as literal substrings against each file's
CONTENTS and its filename. Multiple terms are ANDed: every term must appear
somewhere in the file. Results are ordered by number of matching lines.

  pogo investigations drain                 # every report mentioning a drain
  pogo investigations registry pty          # both terms, same file
  pogo investigations --files launchd       # paths only
  pogo investigations                       # list all of them

IT SEARCHES THE FILES, NOT README.md. The index in that directory is
hand-maintained and lags: when this command was written it omitted 10 of 45
files, and the omissions skewed toward the NEWEST reports — the ones most likely
to bear on current work. A search over the index would have been worst exactly
where it is most needed, and, worse, would have answered "nothing exists" from
an instrument that could not see the candidate space. Index coverage is printed
as a diagnostic so that gap stays visible; it is never used as a filter.

Every invocation is recorded as one ` + EventTypeInvestigationSearch + ` event in
~/.pogo/events.log — including searches that match nothing. Read them with:

  pogo events list --since=720h --type=` + EventTypeInvestigationSearch,
		Args: cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			runInvestigations(args, dir, limit, matchLines, filesOnly, *jsonOutput)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "corpus directory (default: nearest "+investigations.DefaultDir+" at or above the working directory)")
	cmd.Flags().IntVar(&limit, "limit", 0, "show at most N matching documents (0 = all)")
	cmd.Flags().IntVar(&matchLines, "matches", defaultMatchLines, "matching lines to show per document (0 = all)")
	cmd.Flags().BoolVar(&filesOnly, "files", false, "print matching paths only")
	return cmd
}

func runInvestigations(terms []string, dirFlag string, limit, matchLines int, filesOnly, asJSON bool) {
	dir := dirFlag
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			failInvestigations(dir, terms, asJSON, fmt.Sprintf("cannot determine working directory: %v", err))
		}
		found, err := investigations.FindCorpus(wd)
		if err != nil {
			failInvestigations(dir, terms, asJSON, fmt.Sprintf("%v (pass --dir to search elsewhere)", err))
		}
		dir = found
	}

	res, err := investigations.Search(dir, terms, matchLines)
	if err != nil {
		failInvestigations(dir, terms, asJSON, fmt.Sprintf("cannot search %s: %v", dir, err))
	}

	outcome := "matched"
	if len(res.Docs) == 0 {
		outcome = "no_match"
	}
	emitSearchEvent(res.Dir, terms, res, outcome, "")

	shown := res.Docs
	truncated := 0
	if limit > 0 && len(shown) > limit {
		truncated = len(shown) - limit
		shown = shown[:limit]
	}

	if asJSON {
		cli.PrintJSON(struct {
			*investigations.Result
			Shown     int `json:"shown"`
			Truncated int `json:"truncated"`
		}{Result: res, Shown: len(shown), Truncated: truncated})
		return
	}

	if filesOnly {
		for _, d := range shown {
			fmt.Println(displayPath(d.Path))
		}
		printCorpusFooter(res, truncated, len(terms) == 0)
		return
	}

	query := strings.Join(terms, " ")
	if query == "" {
		fmt.Printf("%d investigations in %s\n\n", len(res.Docs), displayPath(res.Dir))
	} else {
		fmt.Printf("%d of %d investigations match %q\n\n", len(res.Docs), res.Searched, query)
	}

	for _, d := range shown {
		fmt.Printf("%s\n", displayPath(d.Path))
		fmt.Printf("  %s\n", d.Title)
		if d.Meta != "" {
			fmt.Printf("  %s\n", d.Meta)
		}
		for _, m := range d.Matches {
			fmt.Printf("  %5d: %s\n", m.Line, truncateLine(m.Text, 140))
		}
		if d.Hits > len(d.Matches) {
			fmt.Printf("        … %d more matching lines\n", d.Hits-len(d.Matches))
		}
		fmt.Println()
	}

	if len(res.Docs) == 0 {
		// The point of this paragraph is that a zero here is a fact about the
		// FILES and nothing more. An empty result read as "no investigation
		// covers this" is the exact move that cost four re-derivations.
		fmt.Printf("No file contains all of those terms.\n" +
			"That is a statement about the text of the files, not about whether an\n" +
			"investigation covers the topic — try fewer or broader terms.\n\n")
	}
	printCorpusFooter(res, truncated, len(terms) == 0)
}

// printCorpusFooter states the denominator on every run, matched or not. "3
// results" without "of 46 files searched" is the shape of claim this whole
// ticket exists to stop.
//
// nameUnindexed lists the index's omissions by name. That is worth printing in
// the listing mode, where the corpus itself is the subject, and it is noise on
// every routine search — so the COUNT is unconditional and the names are behind
// a listing or --json. A caveat repeated until it is skipped has stopped being
// a caveat.
func printCorpusFooter(res *investigations.Result, truncated int, nameUnindexed bool) {
	if truncated > 0 {
		fmt.Printf("%d further matching documents not shown (--limit).\n", truncated)
	}
	fmt.Printf("Searched %d files in %s — file CONTENTS, not README.md.\n",
		res.Searched, displayPath(res.Dir))
	switch {
	case res.IndexReadErr != "":
		fmt.Printf("README.md could not be read (%s), so index coverage is unknown — not zero.\n", res.IndexReadErr)
	case len(res.Unindexed) > 0:
		fmt.Printf("README.md mentions %d of those %d; %d are absent from the index and would be\n"+
			"invisible to a search over it.\n", res.Indexed, res.Searched, len(res.Unindexed))
		if nameUnindexed {
			for _, u := range res.Unindexed {
				fmt.Printf("  not in README.md: %s\n", u)
			}
		}
	default:
		fmt.Printf("README.md mentions all %d.\n", res.Searched)
	}
	for _, s := range res.Skipped {
		fmt.Printf("NOT searched: %s (%s)\n", s.File, s.Reason)
	}
}

// failInvestigations is the ONLY way this command exits non-zero, and it emits
// before it exits. Someone who reached for the command and could not use it has
// used it, and that is the branch a silent failure would erase — a command that
// records its successes and drops its failures reports a friction problem as
// solved. TestInvestigations_NoExitPathSkipsTheEvent pins the single funnel, so
// a later edit cannot reintroduce a quiet exit by adding one call.
func failInvestigations(dir string, terms []string, asJSON bool, msg string) {
	emitSearchEvent(dir, terms, nil, "error", msg)
	cli.ExitWithError(asJSON, msg, exitInvestigationsUsage)
}

// emitSearchEvent records one invocation. Best-effort by construction —
// events.Emit never fails a caller — but it is called on every path out of
// runInvestigations, including the ones that exit non-zero.
func emitSearchEvent(dir string, terms []string, res *investigations.Result, outcome, errMsg string) {
	details := map[string]any{
		"query":      strings.Join(terms, " "),
		"terms":      len(terms),
		"outcome":    outcome,
		"corpus_dir": dir,
	}
	if res != nil {
		details["files_searched"] = res.Searched
		details["matches"] = len(res.Docs)
		details["unindexed"] = len(res.Unindexed)
		details["skipped"] = len(res.Skipped)
	}
	if errMsg != "" {
		details["error"] = errMsg
	}
	events.Emit(context.Background(), events.Event{
		EventType: EventTypeInvestigationSearch,
		Agent:     events.ResolveAgent(agent.CoordinatorName()),
		Details:   details,
	})
}

// displayPath prefers a path relative to the working directory when that does
// not climb out of it, so output stays copy-pasteable without being a wall of
// worktree prefix.
func displayPath(p string) string {
	wd, err := os.Getwd()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(wd, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}

func truncateLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
