package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/prtracking"
)

// newCheckPRTrackingCmd builds `pogo check-pr-tracking` (mg-1f04): the open-PR
// pass's tracking question, executable instead of only written down.
//
// A sibling of check-acks, check-carriers, check-intake, check-mailloops,
// check-orphans, check-prompts, check-review-decl, check-staleness,
// check-stranded, check-strandedmail and check-verdicts. Same membership
// criterion — A READ-ONLY DETECTOR THAT REPORTS A CONDITION AND TAKES NO
// ACTION. It never opens, closes, comments on or merges a PR, and it never
// files a work item.
func newCheckPRTrackingCmd(jsonOutput *bool) *cobra.Command {
	var (
		repos   []string
		control string
		limit   int
	)
	cmd := &cobra.Command{
		Use:   "check-pr-tracking",
		Short: "Report each open PR's tracking state: tracked here, tracked elsewhere, or no tracker found",
		Long: `Answer the open-PR pass's question 2 — "does a work item track this PR?" — for
every OPEN pull request of the named repos, with THREE answers rather than two.

WHY A THIRD ANSWER. ` + "`not landed + untracked-in-this-store = STRANDED`" + ` has no cell
for a PR tracked in a macguffin store this box cannot read, and an unrepresented
state reports as the nearest available one — here, an alarm. drellem2/macguffin
#28 was reported STRANDED while carrying a passing review from ` + "`mg-cc4b`" + ` against
build ticket ` + "`mg-2880`" + `, both of which live in the payitgov fleet's store, behind
a SAML wall our token does not clear. The PR was tracked, reviewed and passing.

AND WHY THE THIRD ANSWER STILL ALARMS. Daniel's ruling on that same PR was to
CLOSE it — "no PRs from payitgov agents" — so an external-fleet PR is exactly
the thing he wants surfaced. Downgrading these rows to informational, which is
what the finding first proposed, would have hidden the case he ruled on. The
sweep's reasoning was wrong and its outcome was right, and a detector can be
both. So ` + "`tracked elsewhere`" + ` is actionable, and this command exits 1 on it.

THE THREE ANSWERS:

  tracked here       an id THIS FLEET's naming put on the PR — its head branch
                     or its title — resolves in the local store. Unchanged from
                     what the pass already did.
  tracked elsewhere  ids are named on the PR, none resolve here, and none came
                     from this fleet's naming. Positive evidence of a tracker we
                     cannot read. Reported as "confirm this PR should exist".
  no tracker found   nothing named an id, OR this fleet's own naming named one
                     that is not in the store — which is a strand, because our
                     naming only ever names our own items.

MECHANICAL EVIDENCE BEATS PROSE, and that ordering is the whole safety property.
drellem2/pogo#93 — the strand the open-PR pass was built after — has a comment
reading "Triaging as mg-c76a", and mg-c76a DOES resolve here. Read prose first
and #93 reads "tracked" and goes quiet. Read the branch and title first and it
stays a finding, which is the answer the pass got right the first time.

WHAT THIS DOES NOT ANSWER. Landed-ness. The pass's disposition is a function of
BOTH questions and this command answers one of them; see
docs/pm-open-pr-pass.md for the predicate and the disposition table.

A REPO gh CANNOT READ IS NOT A REPO WITH NO FINDINGS. Unreadable repos are
listed separately and make the run actionable, rather than contributing a
reassuring zero.

Exit status: 0 nothing actionable, 1 something is (including an unreadable
repo), 2 usage, 3 the tracking resolver's own positive control failed — or
none could be derived — so no negative it produced would mean anything.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if len(repos) == 0 {
				cli.ExitWithError(*jsonOutput, "no repos given: pass --repo owner/name at least once", cli.ExitNotFound)
			}

			// A positive control ALWAYS runs, before any repo is read. Without
			// one, an `mg` that cannot answer turns every PR in the fleet into
			// a finding at once, all of them looking measured — so the control
			// is derived from the store rather than left to a flag somebody
			// remembers to pass.
			if control == "" {
				derived, err := prtracking.AutoControlID()
				if err != nil {
					cli.ExitWithError(*jsonOutput, err.Error()+" — pass --control <a-live-item-id>", cli.ExitUnknown)
				}
				control = derived
			}
			resolves, err := prtracking.LocalResolver(control)
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitUnknown)
			}

			rep := prtracking.Report{Unreadable: map[string]string{}, Repos: repos}
			for _, slug := range repos {
				prs, err := prtracking.OpenPRs(slug, limit)
				if err != nil {
					rep.Unreadable[slug] = err.Error()
					continue
				}
				rep.Rows = append(rep.Rows, prtracking.Detect(prs, resolves)...)
			}

			if *jsonOutput {
				type outRow struct {
					Repo       string   `json:"repo"`
					Number     int      `json:"number"`
					Branch     string   `json:"branch"`
					Title      string   `json:"title"`
					State      string   `json:"state"`
					Actionable bool     `json:"actionable"`
					Mechanical []string `json:"mechanical_ids,omitempty"`
					Prose      []string `json:"prose_ids,omitempty"`
					Resolved   []string `json:"resolved_ids,omitempty"`
					Report     string   `json:"report"`
				}
				rows := make([]outRow, 0, len(rep.Rows))
				for _, r := range rep.Rows {
					rows = append(rows, outRow{
						Repo: r.PR.Repo, Number: r.PR.Number, Branch: r.PR.HeadRefName,
						Title: r.PR.Title, State: r.Result.State.String(),
						Actionable: r.Result.Actionable(),
						Mechanical: r.Result.Mechanical, Prose: r.Result.Prose,
						Resolved: r.Result.Resolved, Report: r.Result.Report(r.PR),
					})
				}
				cli.PrintJSON(map[string]interface{}{
					"repos":      rep.Repos,
					"open_prs":   len(rep.Rows),
					"counts":     rep.Counts(),
					"unreadable": rep.Unreadable,
					"rows":       rows,
					"actionable": rep.Actionable(),
				})
			} else {
				fmt.Print(rep.Render())
			}

			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringArrayVar(&repos, "repo", nil,
		"GitHub repo as owner/name; repeatable")
	cmd.Flags().StringVar(&control, "control", "",
		"Work-item id known to exist, used as the tracking resolver's positive control; default: one derived from the local store, because a control that can be forgotten defaults to the unsafe answer")
	cmd.Flags().IntVar(&limit, "limit", 100,
		"Maximum open PRs to read per repo")
	return cmd
}
