package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/ghpr"
)

// newOriginRepoCmd builds `pogo origin-repo`, which the polecat templates use
// to pin their gh pr calls to origin with -R (mg-42fe8). It prints exactly the
// OWNER/REPO pogod's own PR lookups pin to (ghpr.PinToOrigin, drellem2/pogo#259),
// so a PR a polecat opens is in the repository pogod later asks about — a
// shell re-spelling of the URL parse in each template could not promise that.
//
// It reads git only and never contacts pogod or GitHub.
func newOriginRepoCmd(jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "origin-repo [dir]",
		Short: "Print the OWNER/REPO that dir's origin remote names, for gh -R",
		Long: `Print the GitHub OWNER/REPO that the origin remote of the repository
containing dir (default: the current directory) points at — the value to pass
to gh's -R so that a gh pr call asks origin, not whichever repository gh would
pick itself (a gh repo set-default, an upstream remote).

This is the same derivation pogod uses for its own PR lookups, the refinery's
PR close and its PR-body closing-keyword check.

Exits non-zero, with nothing on stdout, when origin cannot be pinned: there is
no origin, or it is not a github.com URL (an ssh alias such as github-work, an
Enterprise host). pogod's lookups then run unpinned and let gh choose, so a
caller that wants to agree with them runs its gh call without -R too.`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			repo, err := ghpr.OriginRepo(dir, 10*time.Second)
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			if *jsonOutput {
				cli.PrintJSON(map[string]any{"repo": repo})
				return
			}
			fmt.Println(repo)
		},
	}
}
