package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/vcs"
)

// The --vcs flags shared by `pogo project add` and `pogo project set-vcs`
// (enth-vcs §2, slice 4, mg-160e8; --enth-workspace-path is slice 5,
// mg-cc5cd; --enth-token-file is mg-3a813).

type vcsFlags struct {
	kind, remote, repo, trunk, tokenFile string
	workspacePaths                       []string
}

const enthTokenFileHelp = "enth: a file holding the enth token this project's polecats and pogo's enth calls for it act as " +
	"(a JSON object with a \"token\" field, or the bare token). The record stores the PATH, never the value; " +
	"default: pogod's $ENTH_TOKEN, else the shared `enth login`"

func addVCSFlags(cmd *cobra.Command, f *vcsFlags) {
	cmd.Flags().StringVar(&f.kind, "vcs", "", "version-control backend: git or enth")
	cmd.Flags().StringVar(&f.remote, "enth-remote", "", "enth: the enthd endpoint, e.g. https://enth.app")
	cmd.Flags().StringVar(&f.repo, "enth-repo", "", "enth: the repo name on enthd, e.g. owner/name")
	cmd.Flags().StringVar(&f.trunk, "enth-trunk", "", "enth: the ref work lands on (default "+vcs.DefaultEnthTrunk+")")
	cmd.Flags().StringSliceVar(&f.workspacePaths, "enth-workspace-path", nil,
		"enth: narrow every polecat checkout to this repo-relative path (repeatable; default: the whole repo). "+
			"A worker must `enth checkout --add PATH DIR` before editing outside it")
	cmd.Flags().StringVar(&f.tokenFile, "enth-token-file", "", enthTokenFileHelp)
}

func enthFlagsChanged(cmd *cobra.Command) bool {
	for _, f := range []string{"enth-remote", "enth-repo", "enth-trunk", "enth-workspace-path", "enth-token-file"} {
		if cmd.Flags().Changed(f) {
			return true
		}
	}
	return false
}

// enthFromFlags is the enth settings the flags give, or nil when none was
// passed — so `--vcs=git` sends no enth object, and an enth flag given with
// --vcs=git reaches pogod and is refused there rather than silently dropped.
func enthFromFlags(cmd *cobra.Command, f vcsFlags) (*vcs.EnthRepo, error) {
	if !enthFlagsChanged(cmd) {
		return nil, nil
	}
	e := &vcs.EnthRepo{Remote: f.remote, Repo: f.repo, Trunk: f.trunk, WorkspacePaths: f.workspacePaths}
	if f.tokenFile != "" {
		p, err := checkTokenFile(f.tokenFile)
		if err != nil {
			return nil, err
		}
		e.TokenFile = p
	}
	return e, nil
}

// checkTokenFile resolves a --enth-token-file argument to the absolute path
// the record stores, and refuses one that cannot be read as a token now:
// otherwise the first sign of a typo would be a refused spawn. The value is
// read only to validate it and is never printed.
func checkTokenFile(arg string) (string, error) {
	p := arg
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand %s: %v", arg, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("invalid token file path %s: %v", arg, err)
	}
	if _, err := vcs.ReadEnthTokenFile(p); err != nil {
		return "", err
	}
	return p, nil
}

// findProject is the registered project at path (compared cleaned, since the
// registry's path carries a trailing separator).
func findProject(projs []project.Project, path string) (project.Project, bool) {
	for _, p := range projs {
		if filepath.Clean(p.Path) == filepath.Clean(path) {
			return p, true
		}
	}
	return project.Project{}, false
}

// withTokenFile is p's enth settings with the token file replaced (""
// clears it), for `pogo project set-enth-token`. A project that is not enth
// is refused: a token has nothing to act on there.
func withTokenFile(p project.Project, tokenFile string) (*vcs.EnthRepo, error) {
	if p.Kind() != vcs.Enth || p.Enth == nil {
		return nil, fmt.Errorf("%s is a %s project; an enth token file applies only to an enth project (pogo project set-vcs --vcs=enth)", p.Path, p.Kind())
	}
	e := *p.Enth
	e.WorkspacePaths = append([]string(nil), p.Enth.WorkspacePaths...)
	e.TokenFile = tokenFile
	return &e, nil
}

// gcRepoVCS is the backend `pogo gc --repo` sweeps repo as: the kind pogod's
// project record names (mg-0ff9c). An enth project is not swept as git — its
// worktree and branch phases are skipped and the summary's coverage line says
// so. When pogod cannot be asked, or repo has no record, the answer is git,
// which is what gc assumed before vcs selection existed; the note says which,
// so a sweep that fell back does not read as one that checked.
func gcRepoVCS(repo string) (vcs.Kind, string) {
	projs, err := client.GetProjects()
	if err != nil {
		return vcs.Git, fmt.Sprintf("note: could not ask pogod for %s's vcs (%v); sweeping it as git", repo, err)
	}
	return gcKindOf(projs, repo), ""
}

// gcKindOf is gcRepoVCS's lookup over the records, split out so it is testable
// without a daemon. A repo with no record is git.
func gcKindOf(projs []project.Project, repo string) vcs.Kind {
	if p, ok := findProject(projs, repo); ok {
		return p.Kind()
	}
	return vcs.Git
}
