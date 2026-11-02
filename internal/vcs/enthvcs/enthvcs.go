// Package enthvcs is the enth backend (docs/design/enth-vcs.md). It is INERT:
// slice 4 (mg-160e8) makes selecting it real, and every operation answers
// vcs.ErrUnsupported until slices 5-7 give it a body. It uses no enth API.
//
// Inert is the point. A repo whose project record says `"vcs": "enth"` must
// not fall through to the git backend: git would act on a checkout that is
// not the thing work lands on. Refusing loudly is the safe behaviour until
// the enth path exists end to end.
package enthvcs

import (
	"context"
	"fmt"

	"github.com/drellem2/pogo/internal/vcs"
)

// Backend is the enth backend for one repo.
type Backend struct {
	// Repo is the record's enth settings. Unused while the backend is inert;
	// carried so the error names which repo was asked.
	Repo vcs.EnthRepo
}

var _ vcs.Backend = Backend{}

func (b Backend) unsupported(op string) error {
	return fmt.Errorf("enth %s (%s): %s: %w", b.Repo.Repo, b.Repo.Remote, op, vcs.ErrUnsupported)
}

func (Backend) Kind() vcs.Kind { return vcs.Enth }

// Workspaces
//
// Four of these are total by contract (no error result). Their inert answers
// are the ones that make the caller do nothing: no base, no branch, never
// "spent" (so nothing is deleted), and a Refresh that reports failure, which
// freshen reads as UNKNOWN rather than as current.

func (Backend) ResolveBase(ctx context.Context, repo vcs.Repo, target string) string { return "" }
func (Backend) BranchExists(ctx context.Context, repo vcs.Repo, branch string) bool {
	return false
}
func (Backend) BranchSpent(ctx context.Context, repo vcs.Repo, branch, base string) bool {
	return false
}
func (b Backend) DeleteBranch(ctx context.Context, repo vcs.Repo, branch string) error {
	return b.unsupported("delete branch")
}
func (b Backend) Create(ctx context.Context, s vcs.WorkspaceSpec) (vcs.Workspace, error) {
	return vcs.Workspace{}, b.unsupported("create workspace")
}
func (b Backend) Rollback(ctx context.Context, repo vcs.Repo, dir string) error {
	return b.unsupported("rollback workspace")
}
func (b Backend) Contains(ctx context.Context, repo vcs.Repo, base, branch string) error {
	return b.unsupported("contains")
}
func (b Backend) Exclude(ctx context.Context, dir, group, relPath string) error {
	return b.unsupported("exclude")
}
func (b Backend) Refresh(ctx context.Context, dir string) vcs.RefreshResult {
	return vcs.RefreshResult{Path: dir, Status: vcs.RefreshFailed, Behind: -1, Ahead: -1, Detail: b.unsupported("refresh").Error()}
}
func (b Backend) Remove(ctx context.Context, dir string) error {
	return b.unsupported("remove workspace")
}
func (b Backend) Dirty(ctx context.Context, dir string) ([]string, error) {
	return nil, b.unsupported("dirty")
}
func (b Backend) Describe(ctx context.Context, dir string) (vcs.Workspace, error) {
	return vcs.Workspace{}, b.unsupported("describe workspace")
}

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
