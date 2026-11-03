package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/vcs"
)

// newRefCreateCmd builds `pogo refinery ref-create`: create a branch (a
// release branch) through the repo's backend (docs/design/enth-vcs.md §8,
// slice 12, mg-ae4ac).
func newRefCreateCmd(jsonOutput *bool) *cobra.Command {
	var repo, at string
	cmd := &cobra.Command{
		Use:   "ref-create <ref> --repo=<repo> [--at=<rev>]",
		Short: "Create a branch (a release branch) through the repo's VCS backend",
		Long: `Create the branch <ref> at --at, through the repo's VCS backend: the
release-process step "create the release branch AT the candidate".

  pogo refinery ref-create release/v1.2.0 --repo=/path/to/repo --at=<CAND>

On an enth repo this is 'enth ref create refs/heads/<ref> <Change>', a
compare-and-swap create on enthd. --at is a Change id (or a unique hex prefix),
a branch name, or a full ref. Re-running it when the branch already names that
Change is success and writes nothing; a branch that names anything else is
refused and never re-carved. Landing onto the branch afterwards is the worker's
ordinary self_land with --branch <ref> (enth land --ref refs/heads/<ref>).

On a git repo it is the same step 'pogo refinery submit --auto-create-target'
runs: --at is a branch on origin, and a ref that already exists is refused.
(To cut a git release branch at a commit sha, push it by hand as
docs/release-process.md shows.)

With no --at the branch is made from the repo's default branch, which is what
--auto-create-target does. On enth that is the only form --auto-create-target
has: an enth repo has no submit, so the branch a worker lands on must exist
before the worker is dispatched.

A followed enth repo (one enth imports from git) refuses every ref move until
its handover.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if repo == "" {
				cli.ExitWithError(*jsonOutput, "--repo is required", cli.ExitError)
			}
			res, err := client.CreateRef(refinery.RefCreateRequest{RepoPath: repo, Ref: args[0], At: at})
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			if *jsonOutput {
				cli.PrintJSON(res)
				return
			}
			switch {
			case res.VCS == vcs.Enth && res.Existed:
				fmt.Printf("%s already at enth %s (from %s): nothing to do\n", res.Ref, res.Change, res.At)
			case res.VCS == vcs.Enth:
				fmt.Printf("Created %s at enth %s (from %s)\n", res.Ref, res.Change, res.At)
			default:
				fmt.Printf("Created %s from %s\n", res.Ref, res.At)
			}
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "Repository path, as the project record names it (required)")
	cmd.Flags().StringVar(&at, "at", "", "Where the branch starts: an enth Change id, or a branch (default: the repo's default branch)")
	return cmd
}
