package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/ineffect"
)

// `pogo in-effect <commit>` (mg-3d0e) — is a merged commit actually executing?
//
// WHY IT IS NOT A `check-*`. The check-* family are standing sweeps over a
// population, run on a schedule or during a doctor pass. This is a QUESTION
// ABOUT ONE COMMIT, asked at the moment somebody is about to act on it: before
// archiving a done ticket, before dispatching a worker onto a fix that builds
// on another, before concluding that a symptom's absence means the fix landed.
// All three of those went wrong on 2026-08-19 and all three were one command
// away from being right.
//
// WHY IT DOES NOT PRINT ONE WORD. "Is this in effect" has no single answer on
// this box, and that is the finding, not a limitation of the report: the same
// commit is live in one artifact and inert in another. `half-live` is a real
// verdict here and the per-carrier rows are what make it actionable.
func newInEffectCmd(jsonOutput *bool) *cobra.Command {
	var (
		repo      string
		ref       string
		pogoHome  string
		deploySrc string
	)
	cmd := &cobra.Command{
		Use:   "in-effect <commit>",
		Short: "Is this commit EXECUTING? — per artifact class, per carrier, on this box",
		Long: `Answer "is commit X in effect?" by asking every runtime carrier of every
artifact class the commit touched.

WHY THIS IS NOT THE SAME QUESTION AS "IS IT MERGED". A merge puts bytes on a
branch. Whether those bytes are running depends on WHAT THE COMMIT TOUCHED, and
on this box the carriers move independently:

  compiled Go      needs a rebuild AND a restart of each binary that imports it
  agent prompts    embedded in a binary, installed under ~/.pogo/agents, and
                   read once at spawn by each running agent — three carriers
  scripts, plists  an installed copy in ~/.pogo/bin, or a checkout that runs
                   them in place (this one, and ~/.pogo/deploy-src)
  docs and tests   no runtime carrier at all

Measured on 2026-08-19: a single evening's merges were inert in pogod (five days
behind) while a merged change to the nightly runner was live, because its
installed copy had been refreshed. Both statements were true about the same day.
Deriving that per-fix by hand is what this command replaces.

VERDICTS ARE PER CARRIER, AND THERE ARE THREE OF THEM:

  live     the carrier was read and it carries the commit
  inert    the carrier was read and it does not
  unknown  the carrier could not be read, or holds something not comparable

` + "`unknown`" + ` is never folded into ` + "`inert`" + `. They owe different actions — redeploy
versus investigate — and a report that goes green because it measured nothing is
the failure this command exists to remove.

The overall verdict can be ` + "`half-live`" + `, which is the common case here and has
no name in any other surface. Exit status: 0 in effect (or nothing with a
runtime carrier), 1 inert or half-live, 3 not established.

EVERY ROW NAMES WHERE IT WAS MEASURED so it can be re-run by hand: a revision
carrier is ` + "`git merge-base --is-ancestor <commit> <observed>`" + `, and an installed
copy is dated by finding the newest revision of that path whose bytes it holds.

WHAT THIS CANNOT SEE, said out loud because it is the residual: the text a
RUNNING agent is holding. An agent reads its prompt at spawn and keeps no copy
anything can compare, so a current installed corpus is still not in effect for
an agent started before it (mg-385f). Prompt findings carry that caveat on every
run rather than rendering as a plain pass.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if repo == "" {
				wd, err := os.Getwd()
				if err != nil {
					cli.ExitWithError(*jsonOutput, "cannot resolve a repo: "+err.Error(), cli.ExitError)
				}
				repo = wd
			}
			rep := ineffect.Assess(args[0], ineffect.HostDeps(ineffect.HostOpts{
				Repo: repo, Ref: ref, PogoHome: pogoHome, DeploySrc: deploySrc,
			}))
			if *jsonOutput {
				cli.PrintJSON(rep)
			} else {
				os.Stdout.WriteString(rep.Text())
			}
			os.Exit(rep.ExitCode())
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "pogo checkout whose history is surveyed (default: the working directory)")
	cmd.Flags().StringVar(&ref, "ref", ineffect.DefaultRef, "branch installed copies are dated against")
	cmd.Flags().StringVar(&pogoHome, "pogo-home", "", "state root whose installed copies are inspected (default: the resolved POGO_HOME)")
	cmd.Flags().StringVar(&deploySrc, "deploy-src", "", "deploy checkout (default: <pogo-home>/deploy-src)")
	return cmd
}
