package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/reviewdecl"
)

// newCheckReviewDeclCmd builds `pogo check-review-decl` (mg-253e): the on-demand
// half of pogod's review-declaration detector. Same detector, same inputs, same
// rendered text as the mail the standing runner sends — so a coordinator that
// receives a notice can reproduce it exactly rather than take its word.
//
// A sibling of check-acks, check-commit-body, check-intake, check-mailloops,
// check-prompts, check-staleness, check-strandedmail, check-teardown and
// check-verdicts. The family's membership criterion is a READ-ONLY DETECTOR THAT
// REPORTS A CONDITION AND TAKES NO ACTION, and this one is at the strict end of
// it: it has no seam through which a work item could be written at all.
func newCheckReviewDeclCmd(jsonOutput *bool) *cobra.Command {
	var (
		root  string
		since string
	)
	cmd := &cobra.Command{
		Use:   "check-review-decl",
		Short: "Report review tickets whose `reviews:` line is missing or unusable (never writes one)",
		Long: `Report every REVIEW TICKET whose carrier block declares no usable ` + "`reviews:`" + `
line, so pogod's done-reaper cannot exempt the builder it reviews.

WHY THIS EXISTS. mg-aaf6 (drellem2/pogo#131 part 3) made an open review survive a
self-closing builder: the review ticket declares ` + "`reviews: <build-item-id>`" + ` at
creation, and the done-reaper refuses to reap a builder some live reviewer's
ticket names. The declaration is written by a coordinator following an
instruction — and IF IT IS NOT WRITTEN, NOTHING REPORTS THAT. The guard simply
never fires, the builder is reaped mid-review exactly as before, and no artifact
anywhere says a declaration was expected and missing. That residual is mg-253e,
and this is the sweep it asked for.

FINDINGS, all four of which mean the same thing — the exemption cannot fire:

  missing         no ` + "`reviews:`" + ` line at all. The residual this exists for.
  self-reference  the line names the review ticket's own id. donereap refuses a
                  self-reference by name, so the line protects nothing.
  unusable value  the value is not a bare work-item id. The exemption matches by
                  EXACT STRING EQUALITY, so ` + "`mg-aaf6 (the builder)`" + ` can never
                  match anything.
  undatable       no line AND no readable ` + "`created:`" + ` stamp, so the convention
                  boundary below cannot be applied. NOT resolved to either side.

AND TWO POPULATIONS THAT ARE COUNTED BUT NEVER ALERTED ON:

  pre-convention  filed before ` + "`reviews:`" + ` landed on main (commit c045a9a,
                  ` + reviewdecl.ConventionLandedAt.Format(time.RFC3339) + `). They had no convention to follow.
                  Listed rather than dropped: an exclusion nobody can see is
                  indistinguishable from a scan that missed them.
  not classifiable  the carrier block is somewhere the parser cannot reach
                  (mg-27d4), so whether the item is even a review ticket is
                  unknown. The dispatch gate already refuses these outright, so
                  no review polecat is spawned and the exemption is never needed.

IT PARSES WITH THE ENFORCER'S PARSER, not a grep. A ` + "`reviews:`" + ` line outside the
LEADING carrier block renders perfectly under ` + "`mg show`" + ` and is invisible to the
done-reaper. A grep would call that declared; this reports what the guard sees.

THIS COMMAND REPORTS ONLY — it never writes a ` + "`reviews:`" + ` line. A coordinator
that skipped one may have had a reason, and a detector that repairs the thing it
measures cannot be trusted to measure it.

Scans available/, claimed/, done/ and pending/. Archived and shelved items are
NOT scanned, and the report states the statuses it covered rather than implying
it saw the whole store.

Exit status is 0 when nothing is actionable and 1 when anything is found.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			boundary := reviewdecl.ConventionLandedAt
			if since != "" {
				t, err := time.Parse(time.RFC3339, since)
				if err != nil {
					// Refused rather than defaulted. A --since nobody can parse,
					// silently falling back to the built-in boundary, would answer
					// a question the operator did not ask and look like it had.
					fmt.Fprintf(os.Stderr, "--since %q: want an RFC3339 stamp such as 2026-08-12T00:00:00Z (%v)\n", since, err)
					os.Exit(cli.ExitError)
				}
				boundary = t.UTC()
			}

			src := reviewdecl.Source{Root: root}
			items, err := src.Items()
			if err != nil {
				// A store we could not read is not "no findings". Fail loudly:
				// silence here would be this detector reproducing, inside itself,
				// the silent absence it was built to catch.
				fmt.Fprintf(os.Stderr, "cannot read work-item store: %v\n", err)
				os.Exit(cli.ExitError)
			}

			rep := reviewdecl.Detect(items, boundary)
			rep.Statuses = src.Statuses()

			if *jsonOutput {
				type outFinding struct {
					ID      string `json:"id"`
					Title   string `json:"title,omitempty"`
					Status  string `json:"status,omitempty"`
					Kind    string `json:"kind"`
					Reviews string `json:"reviews,omitempty"`
					Created string `json:"created,omitempty"`
				}
				conv := func(fs []reviewdecl.Finding) []outFinding {
					out := make([]outFinding, 0, len(fs))
					for _, f := range fs {
						row := outFinding{
							ID: f.Item.ID, Title: f.Item.Title, Status: f.Item.Status,
							Kind: string(f.Kind), Reviews: f.Item.Reviews,
						}
						if !f.Item.Created.IsZero() {
							row.Created = f.Item.Created.UTC().Format(time.RFC3339)
						}
						out = append(out, row)
					}
					return out
				}
				cli.PrintJSON(map[string]interface{}{
					// The denominators come FIRST because they are what makes a
					// zero readable. mg-253e asked for the population to be
					// stated and not only the count missing.
					"scanned":    rep.Scanned,
					"population": rep.Population,
					"statuses":   rep.Statuses,
					"boundary":   boundary.Format(time.RFC3339),

					"missing":        conv(rep.Missing),
					"self_reference": conv(rep.SelfReference),
					"malformed":      conv(rep.Malformed),
					"undatable":      conv(rep.Undatable),
					"pre_convention": conv(rep.PreConvention),
					"declared":       conv(rep.Declared),
					"opaque":         conv(rep.Opaque),

					"unprotected": rep.Unprotected(),
					"actionable":  rep.Actionable(),
				})
			} else {
				fmt.Print(rep.Render())
			}

			if rep.Actionable() {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"macguffin work directory to scan (default ~/.macguffin/work)")
	cmd.Flags().StringVar(&since, "since", "",
		"Override the convention boundary as an RFC3339 stamp; review tickets filed before it are excluded as pre-convention")
	return cmd
}
