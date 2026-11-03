// Package enthvcs is the enth backend (docs/design/enth-vcs.md).
//
// Slice 4 (mg-160e8) made selecting it real with every operation answering
// vcs.ErrUnsupported. Slice 5 (mg-cc5cd) gave Workspaces a body
// (workspaces.go): polecat checkouts through the repo's enthd, their excludes,
// Dirty via `enth status`, Remove, Describe, Root and Refresh. Work, Lander and
// Refs are still inert until slices 6-7.
//
// Inert is the point for what is not built. A repo whose project record says
// `"vcs": "enth"` must not fall through to the git backend: git would act on a
// checkout that is not the thing work lands on. Refusing loudly is the safe
// behaviour until the enth path exists end to end.
package enthvcs

import (
	"context"
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

// Work

func (b Backend) Find(ctx context.Context, repo vcs.Repo, q vcs.WorkQuery) ([]vcs.Submission, error) {
	return nil, b.unsupported("find work")
}
func (b Backend) State(ctx context.Context, repo vcs.Repo, s vcs.Submission, target string) (vcs.WorkState, error) {
	return vcs.WorkState{}, b.unsupported("work state")
}
func (b Backend) Messages(ctx context.Context, repo vcs.Repo, s vcs.Submission) ([]vcs.Message, error) {
	return nil, b.unsupported("messages")
}
func (b Backend) Diff(ctx context.Context, repo vcs.Repo, s vcs.Submission) (vcs.Diff, error) {
	return vcs.Diff{}, b.unsupported("diff")
}
func (b Backend) Cat(ctx context.Context, repo vcs.Repo, rev, path string) ([]byte, error) {
	return nil, b.unsupported("cat")
}
func (b Backend) Fetch(ctx context.Context, repo vcs.Repo) error {
	return b.unsupported("fetch")
}

// Lander

func (b Backend) Prepare(ctx context.Context, mr vcs.MR) (vcs.Prepared, error) {
	return vcs.Prepared{}, b.unsupported("prepare")
}
func (b Backend) Land(ctx context.Context, p vcs.Prepared) (vcs.Landed, error) {
	return vcs.Landed{}, b.unsupported("land")
}
func (b Backend) Cleanup(ctx context.Context, mr vcs.MR) error {
	return b.unsupported("cleanup")
}

// Refs

func (b Backend) Default(ctx context.Context, repo vcs.Repo) (string, error) {
	return "", b.unsupported("default branch")
}
func (b Backend) CreateRef(ctx context.Context, repo vcs.Repo, ref, at string) error {
	return b.unsupported("create ref")
}
func (b Backend) Tag(ctx context.Context, repo vcs.Repo, name, at, msg string) (vcs.TagResult, error) {
	return 0, b.unsupported("tag")
}
