package main

import (
	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/vcs"
)

// The --vcs flags shared by `pogo project add` and `pogo project set-vcs`
// (enth-vcs §2, slice 4, mg-160e8; --enth-workspace-path is slice 5,
// mg-cc5cd).

type vcsFlags struct {
	kind, remote, repo, trunk string
	workspacePaths            []string
}

func addVCSFlags(cmd *cobra.Command, f *vcsFlags) {
	cmd.Flags().StringVar(&f.kind, "vcs", "", "version-control backend: git or enth")
	cmd.Flags().StringVar(&f.remote, "enth-remote", "", "enth: the enthd endpoint, e.g. https://enth.app")
	cmd.Flags().StringVar(&f.repo, "enth-repo", "", "enth: the repo name on enthd, e.g. owner/name")
	cmd.Flags().StringVar(&f.trunk, "enth-trunk", "", "enth: the ref work lands on (default "+vcs.DefaultEnthTrunk+")")
	cmd.Flags().StringSliceVar(&f.workspacePaths, "enth-workspace-path", nil,
		"enth: narrow every polecat checkout to this repo-relative path (repeatable; default: the whole repo). "+
			"A worker must `enth checkout --add PATH DIR` before editing outside it")
}

func enthFlagsChanged(cmd *cobra.Command) bool {
	for _, f := range []string{"enth-remote", "enth-repo", "enth-trunk", "enth-workspace-path"} {
		if cmd.Flags().Changed(f) {
			return true
		}
	}
	return false
}

// enthFromFlags is the enth settings the flags give, or nil when none was
// passed — so `--vcs=git` sends no enth object, and an enth flag given with
// --vcs=git reaches pogod and is refused there rather than silently dropped.
func enthFromFlags(cmd *cobra.Command, f vcsFlags) *vcs.EnthRepo {
	if !enthFlagsChanged(cmd) {
		return nil
	}
	return &vcs.EnthRepo{Remote: f.remote, Repo: f.repo, Trunk: f.trunk, WorkspacePaths: f.workspacePaths}
}
