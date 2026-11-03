package agent

import (
	"errors"
	"sync/atomic"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/gitvcs"
)

// workspaces is the git backend for every workspace pogod creates or touches:
// the polecat worktree, its rollback and leftover-branch reclaim, the
// adoption check, the injected-persona exclude, and the crew workspace
// refresh (enth-vcs slice 2, mg-cc7ae; docs/design/enth-vcs.md §3).
//
// Since slice 5 (mg-cc5cd) it is git's only. A repo whose project record says
// enth gets enthvcs instead, chosen two ways:
//
//   - by REPO at spawn (repoVCS, installed by pogod from the project record),
//     because the checkout does not exist yet;
//   - by DIRECTORY afterwards (workspacesAt), because a tree is what it is:
//     the persona exclude and the crew refresh act on a directory, and reading
//     its marker cannot disagree with the tree the way a lookup could.
var workspaces vcs.Workspaces = gitvcs.Workspaces{FetchTimeout: polecatFetchTimeout}

// RepoVCS is the backend lookup by repo: the kind and enth settings recorded
// for the repo at repoPath. pogod passes project.VCSOf.
type RepoVCS func(repoPath string) (vcs.Kind, *vcs.EnthRepo)

var repoVCS atomic.Pointer[RepoVCS]

// SetRepoVCS installs the lookup the spawn path consults. nil uninstalls it,
// and every repo is then git — which is what every repo was before selection.
func SetRepoVCS(f RepoVCS) {
	if f == nil {
		repoVCS.Store(nil)
		return
	}
	repoVCS.Store(&f)
}

// enthRepoFor returns the enth settings when the repo at repoPath is recorded
// as enth, else nil.
func enthRepoFor(repoPath string) *vcs.EnthRepo {
	f := repoVCS.Load()
	if f == nil || repoPath == "" {
		return nil
	}
	kind, e := (*f)(repoPath)
	if kind != vcs.Enth {
		return nil
	}
	if e == nil {
		e = &vcs.EnthRepo{}
	}
	return e
}

// workspacesAt is the backend for the existing tree at dir, by its repo
// marker (enthvcs.Detect): enth for an enth checkout, git otherwise. A tree
// holding both markers is refused, never guessed.
func workspacesAt(dir string) (vcs.Workspaces, error) {
	kind, _, err := enthvcs.Detect(dir)
	if err != nil {
		return nil, err
	}
	if kind == vcs.Enth {
		return enthvcs.Backend{}, nil
	}
	return workspaces, nil
}

// commandErrorParts splits a backend error into the cause and the verbatim
// command output, the two halves the messages here have always printed as
// "%v\n%s". A non-command error yields itself and no output.
func commandErrorParts(err error) (error, []byte) {
	var ce *vcs.CommandError
	if errors.As(err, &ce) {
		return ce.Err, ce.Output
	}
	return err, nil
}
