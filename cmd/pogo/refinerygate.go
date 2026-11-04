package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/refinery"
)

// newRefineryGateCmd builds `pogo refinery gate`: print the merge gate a repo
// declares, read exactly as the refinery reads it (mg-c07a5). The enth
// self_land template hands its output to enth-land-gate, which stays generic
// and never reads pogo config itself.
func newRefineryGateCmd(jsonOutput *bool) *cobra.Command {
	var repo, worktree string
	cmd := &cobra.Command{
		Use:   "gate --repo=<repo> [--worktree=<dir>]",
		Short: "Print the quality gate command a repo declares in .pogo/refinery.toml",
		Long: `Print, on one line, the merge gate the repo declares — the command the
refinery runs for it. It reads <worktree>/.pogo/refinery.toml first and
<repo>/.pogo/refinery.toml when the worktree declares nothing, as the refinery
does. Several declared gates are printed joined with && so they run in order
and stop at the first failure.

When neither file declares a gate it prints ` + refinery.FallbackGateCommand + `, and says on stderr
that it fell back. It reads local files only and needs no running pogod.

The enth self_land template runs this to choose the --gate it passes to
enth-land-gate, so a self-land is gated by the same command a merge would be.

Example:
  pogo refinery gate --repo=/path/to/repo --worktree="$PWD"`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if repo == "" && worktree == "" {
				cli.ExitWithError(*jsonOutput, "pogo refinery gate needs --repo or --worktree", cli.ExitError)
			}
			for _, p := range []*string{&repo, &worktree} {
				if *p != "" {
					if abs, err := filepath.Abs(*p); err == nil {
						*p = abs
					}
				}
			}
			command, declared := refinery.DeclaredGate(worktree, repo)
			if *jsonOutput {
				cli.PrintJSON(map[string]any{"command": command, "declared": declared})
				return
			}
			if !declared {
				var where []string
				for _, d := range []string{worktree, repo} {
					if d != "" {
						where = append(where, filepath.Join(d, ".pogo", "refinery.toml"))
					}
				}
				fmt.Fprintf(os.Stderr, "pogo refinery gate: no quality gate declared in %v — fell back to %s\n", where, command)
			}
			fmt.Println(command)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "the repo's path (its .pogo/refinery.toml is read second)")
	cmd.Flags().StringVar(&worktree, "worktree", "", "a checkout of the repo (its .pogo/refinery.toml wins)")
	return cmd
}
