package refinery

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/vcs"
)

// Release branches through the seam (enth-vcs §8, slice 12, mg-ae4ac).
//
// `pogo refinery ref-create <ref> [--at <rev>]` is release-process step 1
// ("create the release branch AT the candidate") as a pogo verb, so that it
// goes through the repo's backend rather than a hand `git push`:
//
//   - enth: `enth ref create refs/heads/<ref> <Change>`, a CAS create. A
//     branch that already names the Change is success (the retry); one that
//     names anything else is refused and never re-carved.
//   - git: the same Refs.CreateRef that `pogo refinery submit
//     --auto-create-target` runs (git's --at is a branch on origin, as it is
//     there). A ref that already exists is refused: the submit path likewise
//     never re-carves an existing target.
//
// No --at is the repo's default branch: --auto-create-target's source. On
// enth that is the only form --auto-create-target has, because an enth repo
// has no submit for the flag to ride on (§6): the worker lands onto a branch
// that must already exist.

// RefCreateRequest is the JSON body for POST /refinery/ref-create.
type RefCreateRequest struct {
	RepoPath string `json:"repo_path"`
	// Ref is the branch to create: a bare name or refs/heads/<name>.
	Ref string `json:"ref"`
	// At is where: a Change id (enth) or branch name; "" is the default
	// branch.
	At string `json:"at,omitempty"`
}

// RefCreateResult is what was created.
type RefCreateResult struct {
	VCS vcs.Kind `json:"vcs"`
	Ref string   `json:"ref"`
	// At is the source as resolved: the default branch's name when the
	// request had none.
	At string `json:"at"`
	// Change is the enth Change the branch names (enth only).
	Change string `json:"change,omitempty"`
	// Existed: the branch already named Change; nothing was written (enth).
	Existed bool `json:"existed,omitempty"`
}

const refCreateTimeout = 2 * time.Minute

// CreateRef creates a branch in repo through its backend. See the file
// comment.
func (r *Refinery) CreateRef(req RefCreateRequest) (RefCreateResult, error) {
	req.Ref, req.At = strings.TrimSpace(req.Ref), strings.TrimSpace(req.At)
	if req.RepoPath == "" || req.Ref == "" {
		return RefCreateResult{}, fmt.Errorf("repo_path and ref are required")
	}
	if err := RefusePogoHomeRepo(req.RepoPath); err != nil {
		return RefCreateResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), refCreateTimeout)
	defer cancel()

	if e, ok := enthRepoFor(req.RepoPath); ok {
		b := enthWork(e)
		m, err := b.CreateBranch(ctx, req.Ref, req.At)
		if err != nil {
			return RefCreateResult{}, err
		}
		at := req.At
		if at == "" {
			at, _ = b.Default(ctx, vcs.Repo{})
		}
		if m.Existed {
			log.Printf("refinery: ref-create %s in %s: already at enth %s — nothing to do", m.Ref, req.RepoPath, shortSHA(m.Change))
		} else {
			log.Printf("refinery: ref-create %s in %s at enth %s (from %s, entry %d)", m.Ref, req.RepoPath, shortSHA(m.Change), at, m.Entry)
		}
		return RefCreateResult{VCS: vcs.Enth, Ref: m.Ref, At: at, Change: m.Change, Existed: m.Existed}, nil
	}

	// git: exactly Submit's --auto-create-target steps.
	ref := strings.TrimPrefix(req.Ref, "refs/heads/")
	at := strings.TrimPrefix(req.At, "refs/heads/")
	if at == "" {
		def, err := detectDefaultBranch(req.RepoPath)
		if err != nil {
			return RefCreateResult{}, fmt.Errorf("ref-create %q: detect default branch: %w", ref, err)
		}
		at = def
	}
	if at == ref {
		return RefCreateResult{}, fmt.Errorf("ref-create %q: it is its own source", ref)
	}
	if err := validateTargetRef(req.RepoPath, ref); err == nil {
		return RefCreateResult{}, fmt.Errorf("ref-create %q: it already exists on origin — refusing to re-carve it (a release branch is cut once)", ref)
	}
	if err := createTargetRef(req.RepoPath, ref, at); err != nil {
		return RefCreateResult{}, fmt.Errorf("ref-create %q from %q: %w", ref, at, err)
	}
	if err := validateTargetRef(req.RepoPath, ref); err != nil {
		return RefCreateResult{}, fmt.Errorf("created %q but post-validation failed: %w", ref, err)
	}
	log.Printf("refinery: ref-create %q from %q in %s", ref, at, req.RepoPath)
	return RefCreateResult{VCS: vcs.Git, Ref: ref, At: at}, nil
}
