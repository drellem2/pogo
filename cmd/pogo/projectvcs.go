package main

import (
	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/vcs"
)

// The --vcs flags shared by `pogo project add` and `pogo project set-vcs`
// (enth-vcs §2, slice 4, mg-160e8).

func addVCSFlags(cmd *cobra.Command, kind, remote, repo, trunk *string) {
	cmd.Flags().StringVar(kind, "vcs", "", "version-control backend: git or enth")
	cmd.Flags().StringVar(remote, "enth-remote", "", "enth: the enthd endpoint, e.g. https://enth.app")
	cmd.Flags().StringVar(repo, "enth-repo", "", "enth: the repo name on enthd, e.g. owner/name")
	cmd.Flags().StringVar(trunk, "enth-trunk", "", "enth: the ref work lands on (default "+vcs.DefaultEnthTrunk+")")
}

func enthFlagsChanged(cmd *cobra.Command) bool {
	for _, f := range []string{"enth-remote", "enth-repo", "enth-trunk"} {
		if cmd.Flags().Changed(f) {
			return true
		}
	}
	return false
}

// enthFromFlags is the enth settings the flags give, or nil when none was
// passed — so `--vcs=git` sends no enth object, and an enth flag given with
// --vcs=git reaches pogod and is refused there rather than silently dropped.
func enthFromFlags(cmd *cobra.Command, remote, repo, trunk string) *vcs.EnthRepo {
	if !enthFlagsChanged(cmd) {
		return nil
	}
	return &vcs.EnthRepo{Remote: remote, Repo: repo, Trunk: trunk}
}
