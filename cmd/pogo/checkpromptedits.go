package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/promptedit"
)

// newCheckPromptEditsCmd builds `pogo check-prompt-edits` (mg-0c96): the
// on-demand half of pogod's hand-edit detector. Same detector, same inputs, same
// classification as the standing runner — so an agent that receives a notice can
// reproduce it exactly rather than take its word.
//
// A sibling of check-acks, check-commit-body, check-intake, check-mailloops,
// check-prompts, check-review-decl, check-staleness, check-strandedmail,
// check-teardown and check-verdicts. The family's membership criterion is a
// READ-ONLY DETECTOR THAT REPORTS A CONDITION AND TAKES NO ACTION, and this one
// is at the strict end of it: internal/promptedit has no repair seam at all.
//
// IT IS NOT THE DELIVERY MECHANISM. mg-10e3 is the siting hazard this ticket was
// told to read first: `pogo doctor --check` is a working detector reporting into
// a surface nobody runs on a cadence, and shipping this as a command someone is
// instructed to type would reproduce that exactly. The cadence is pogod's
// heartbeat (internal/promptedit.Watcher). This command is how you check the
// runner's work by hand.
func newCheckPromptEditsCmd(jsonOutput *bool) *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "check-prompt-edits",
		Short: "Report installed prompts edited in place since pogo wrote them (never repairs one)",
		Long: `Report every installed prompt whose body no longer matches the hash its own
stamp records — the signature of an edit made in place after the installer
wrote the file.

THE INSTRUMENT ALREADY EXISTED AND NOTHING RAN IT. Each deployed prompt with an
upstream carries a self-describing stamp on its first line:

    <!-- pogo-prompt: embed=sha256:... body=sha256:... -->

A disagreement between that body hash and sha256(body) IS the hand-edit
signature, and it needs no .dist sidecar and no reference checkout — two
commands reproduce any finding this prints:

    head -1 <prompt>
    tail -n +2 <prompt> | shasum -a 256

It was armed and unscheduled. On 2026-08-20 a hand-edited mayor.md was noticed
only because a shipped update happened to collide with the edited region and
pogod declined the sync; had the edit sat where no shipped change touched,
nothing would have reported it — indefinitely.

THE DOMAIN, WHICH IS MOST OF WHAT THIS COMMAND PRINTS. An UNSTAMPED file is
ambiguous between "by design, no upstream" and "should have a stamp and lost
it", and nothing in the file tells them apart. ~/.pogo/agents holds a lot of
legitimately local material — crew/architect.md, crew/pa.md, crew/pm-*.md,
pm/anti-drift-protocol.md — with no upstream in dev/pogo, so the deployed file
IS the source and "hand-edited since it shipped" is not a meaningful question
for it.

So every enumerated file lands in exactly one of four buckets, and the three
that are not readings each carry their OWN reason rather than being pooled into
one "unknown" count:

  JUDGED               the corpus ships this path and the file is stamped. A
                       mismatch is a finding; a match is clean.
  stamp-missing        the corpus ships it and the stamp is GONE. Unjudgeable,
                       and the one unstamped case worth a reader's attention.
  upstream-withdrawn   stamped by an older install, and the corpus no longer
                       ships the path. What the stamp says is printed, but
                       there is nothing to reconcile against.
  no-upstream          not shipped, not stamped. The deployed file is the
                       source. Expected, and normally the largest bucket.

Pooling those three would break the report in one direction or the other:
reported as findings they are false positives and the report gets ignored;
waved through, a genuinely lost stamp hides among them.

IT REPORTS ONLY, and here that is a hard constraint. A repair that carries a
local line forward changes the body, which stales the stamp, and the stamp
cannot be recomputed without the installer's exact canonicalisation. A tool
that recomputed it anyway would silently certify a body it never validated —
converting an honest "unknown" into a false "verified". Each finding names the
agent that can act on it, because that agent is the only party who can judge
whether the edit is still load-bearing.

EXPECT A BACKLOG ON A FIRST RUN RATHER THAN A CLEAN BASELINE. Mismatches
predate any recently named commit. That is information about the corpus, not
the instrument being broken.

The domain is defined by THIS BINARY's embedded corpus, deliberately and not by
a git ref: the embed is the same artifact that writes the stamps being read, so
the domain and the stamp writer move together. A prompt added to the repo after
this binary was built reads as unshipped until a redeploy — which lands it in
the no-upstream census, never in the findings.

Exit status is 0 when nothing is edited and 1 when anything is found.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if root == "" {
				root = agent.PromptDir()
			}
			shipped, err := promptedit.LoadShipped(agent.DefaultPromptsFS())
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			rep, err := promptedit.Scan(root, shipped, agent.CoordinatorName())
			if err != nil {
				// A tree we could not walk is not "no findings". Fail loudly:
				// silence here would be this detector reproducing, inside
				// itself, the silent absence it was built to catch.
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}

			if *jsonOutput {
				cli.PrintJSON(map[string]interface{}{
					// The denominators come FIRST because they are what makes a
					// zero readable: "0 findings" over 0 judged files is not the
					// same answer as "0 findings" over 9.
					"root":          rep.Root,
					"enumerated":    rep.Total(),
					"shipped_paths": rep.ShippedPaths,
					"judged":        len(rep.Clean) + len(rep.Findings),

					"findings":   rep.Findings,
					"clean":      rep.Clean,
					"unreadable": rep.Unreadable,
					// The census travels in the JSON for the same reason it is
					// rendered: an exclusion a consumer cannot see is
					// indistinguishable from a scan that missed it. Each row
					// carries the reason it was not judged.
					"out_of_domain": rep.OutOfDomain,
				})
			} else {
				fmt.Print(rep.Render())
			}

			if len(rep.Findings) > 0 {
				os.Exit(cli.ExitError)
			}
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"installed prompt tree to sweep (default: the resolved ~/.pogo/agents)")
	return cmd
}
