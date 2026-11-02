package agent

import (
	"errors"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/gitvcs"
)

// workspaces is the VCS backend for every workspace pogod creates or touches:
// the polecat worktree, its rollback and leftover-branch reclaim, the
// adoption check, the injected-persona exclude, and the crew workspace
// refresh (enth-vcs slice 2, mg-cc7ae; docs/design/enth-vcs.md §3).
//
// It is git for every repo. Selecting a backend per repo is slice 4; until
// then there is nothing to select between, and this one value is the seam.
var workspaces vcs.Workspaces = gitvcs.Workspaces{FetchTimeout: polecatFetchTimeout}

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
