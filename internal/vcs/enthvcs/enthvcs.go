// Package enthvcs is the enth backend (docs/design/enth-vcs.md).
//
// Slice 4 (mg-160e8) made selecting it real with every operation answering
// vcs.ErrUnsupported. Slice 5 (mg-cc5cd) gave Workspaces a body
// (workspaces.go): polecat checkouts through the repo's enthd, their excludes,
// Dirty via `enth status`, Remove, Describe, Root and Refresh. Slice 6
// (mg-30da0) gave Work one (work.go): the trailer listing, stacks, land
// state from land records. Slice 7 (mg-1fe2b) gave Lander one (land.go): the
// dry run, the gate checkout of its result, and the land bound to both by
// --expect-tip/--expect-new under a replayable request id. Slice 12
// (mg-ae4ac) gave Refs one (refs.go): the trunk as the default branch,
// `enth tag` with its idempotent retry, and `enth ref create`.
//
// Inert is the point for what is not built. A repo whose project record says
// `"vcs": "enth"` must not fall through to the git backend: git would act on a
// checkout that is not the thing work lands on. Refusing loudly is the safe
// behaviour until the enth path exists end to end.
package enthvcs

import (
	"fmt"

	"github.com/drellem2/pogo/internal/vcs"
)

// Backend is the enth backend for one repo.
type Backend struct {
	// Repo is the record's enth settings: where Create checks out from, and
	// what the inert operations name in their refusals. The dir-only
	// Workspaces methods (Dirty, Root, Describe, Remove, Exclude, Refresh)
	// read what they need from the checkout itself and work with a zero Repo.
	Repo vcs.EnthRepo
	// Bin is the enth executable; empty is DefaultBin on PATH.
	Bin string
}

var _ vcs.Backend = Backend{}

func (b Backend) unsupported(op string) error {
	return fmt.Errorf("enth %s (%s): %s: %w", b.Repo.Repo, b.Repo.Remote, op, vcs.ErrUnsupported)
}

func (Backend) Kind() vcs.Kind { return vcs.Enth }

// Refs is refs.go (slice 12, mg-ae4ac).
