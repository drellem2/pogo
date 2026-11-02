package gitvcs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/drellem2/pogo/internal/ghtoken"
	"github.com/drellem2/pogo/internal/vcs"
)

// Refs answers the ref questions: the default branch (R3), creating a branch
// (R4, --auto-create-target), and publishing a release tag (R18,
// --post-merge-tag).
//
// Default and Create run git as `git -C <repo>` with exactly the environments
// the refinery gave them before this backend existed; Tag runs through Runner.
type Refs struct {
	Runner Runner
}

var _ vcs.Refs = Refs{}

// Default returns the default branch name for repo. It tries origin/HEAD first
// (working clones with a populated remote), then asks origin directly, then
// falls back to local HEAD (bare repos used in tests, and clones with no
// remote at all). Returns an error only when no lookup works.
//
// The remote probe matters because local HEAD is a poor proxy for "the repo
// default": in a working clone it is simply whatever branch is checked out, so
// a clone parked on a feature branch would report that branch as the default
// and every merge to main would look like a PR-flow integration step (mg-7746).
func (Refs) Default(ctx context.Context, repo vcs.Repo) (string, error) {
	repoPath := repo.Path
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").CombinedOutput()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if rest, ok := strings.CutPrefix(s, "origin/"); ok && rest != "" {
			return rest, nil
		}
	}
	if ref, ok := RemoteDefaultBranch(repoPath); ok {
		return ref, nil
	}
	out, err = exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "HEAD").CombinedOutput()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("could not determine default branch for %s", repoPath)
}

// RemoteDefaultBranch asks origin which branch its HEAD points at. Used when
// refs/remotes/origin/HEAD is absent locally — it is only written by `git
// clone` and is easily lost. GIT_TERMINAL_PROMPT=0 so an auth-required HTTPS
// remote fails fast rather than hanging on stdin under launchd.
func RemoteDefaultBranch(repoPath string) (string, bool) {
	cmd := exec.Command("git", "-C", repoPath, "ls-remote", "--symref", "origin", "HEAD")
	cmd.Env = ghtoken.ChildEnv(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ref:")
		if !ok {
			continue
		}
		// Format: "ref: refs/heads/main\tHEAD"
		fields := strings.Fields(rest)
		if len(fields) < 2 || fields[1] != "HEAD" {
			continue
		}
		if name, ok := strings.CutPrefix(fields[0], "refs/heads/"); ok && name != "" {
			return name, true
		}
	}
	return "", false
}

// CreateRef creates branch ref pointing at branch at in repo. In a working clone
// it pushes origin/<at> to a new branch on origin; in a bare repo (the layout
// used by tests) it creates the branch directly via update-ref.
func (Refs) CreateRef(ctx context.Context, repo vcs.Repo, ref, at string) error {
	repoPath, targetRef, sourceRef := repo.Path, ref, at
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--is-bare-repository").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect %s: %v: %s", repoPath, err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "true" {
		shaOut, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/heads/"+sourceRef).CombinedOutput()
		if err != nil {
			return fmt.Errorf("source_ref %q not found in %s: %s", sourceRef, repoPath, strings.TrimSpace(string(shaOut)))
		}
		sha := strings.TrimSpace(string(shaOut))
		if upOut, err := exec.Command("git", "-C", repoPath, "update-ref", "refs/heads/"+targetRef, sha).CombinedOutput(); err != nil {
			return fmt.Errorf("update-ref %s in %s: %v: %s", targetRef, repoPath, err, strings.TrimSpace(string(upOut)))
		}
		return nil
	}

	// Working clone path: make sure we have origin/<source> locally, then
	// push it to a new branch on origin. GIT_TERMINAL_PROMPT=0 so an
	// auth-required HTTPS remote fails fast instead of hanging.
	fetchCmd := exec.Command("git", "-C", repoPath, "fetch", "origin", sourceRef)
	fetchCmd.Env = ghtoken.ChildEnv(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	if fOut, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fetch source_ref %q from origin: %v: %s", sourceRef, err, strings.TrimSpace(string(fOut)))
	}
	pushCmd := exec.Command("git", "-C", repoPath, "push", "origin",
		"refs/remotes/origin/"+sourceRef+":refs/heads/"+targetRef)
	pushCmd.Env = ghtoken.ChildEnv(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	if pOut, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("push %s:%s to origin: %v: %s", sourceRef, targetRef, err, strings.TrimSpace(string(pOut)))
	}
	return nil
}

// Tag creates the annotated tag name on commit at in repo and pushes it to
// origin.
//
// It is idempotent in the only direction that is safe to be: a tag that
// already exists on origin pointing at `at` is success (TagAlreadyPresent), so
// a resubmitted MR (gh #34) or a retried merge converges instead of failing on
// its own prior work. A tag that exists pointing SOMEWHERE ELSE is a hard
// error and is never moved — a published release tag that silently relocates
// is worse than a missing one, and the disagreement means a human needs to
// look. The local clone's copy of the tag is fair game either way; it is
// private to the caller and gets realigned from origin.
func (r Refs) Tag(ctx context.Context, repo vcs.Repo, name, at, msg string) (vcs.TagResult, error) {
	wtDir, tag, sha := repo.Path, name, at
	ref := "refs/tags/" + tag

	// Ask origin, not the local clone: the clone can hold a stale tag from an
	// earlier attempt, and origin is what "the tag exists" has to mean.
	existing, err := r.remoteTagSHA(wtDir, tag)
	if err != nil {
		return 0, fmt.Errorf("look up tag %s on origin: %w", tag, err)
	}
	if existing != "" {
		if existing == sha {
			return vcs.TagAlreadyPresent, nil
		}
		return 0, fmt.Errorf("tag %s already exists on origin at %s but this merge landed as %s — refusing to move a published tag; "+
			"resolve by hand (inspect both commits, then delete and re-push the tag if the existing one is wrong)",
			tag, existing, sha)
	}

	// Drop any local copy so the create below cannot fail on debris from a
	// previous attempt. Errors are ignored: the common case is no such tag.
	r.Runner.Run(wtDir, "tag", "-d", tag)

	// Annotated, not lightweight: `git describe` prefers annotated tags, which
	// is how a release tag is normally read back, and it records who made it
	// and when.
	if out, gerr := r.Runner.Run(wtDir, "tag", "-a", tag, "-m", msg, sha); gerr != nil {
		return 0, fmt.Errorf("create tag %s at %s: %s: %w", tag, sha, out, gerr)
	}

	if out, gerr := r.Runner.Run(wtDir, "push", "origin", ref); gerr != nil {
		// Someone may have pushed the same tag between the lookup above and
		// this push. Re-read origin: if it now agrees with us the race was
		// benign, and reporting a failure would block a correct release.
		if raced, rerr := r.remoteTagSHA(wtDir, tag); rerr == nil && raced == sha {
			return vcs.TagRacedPresent, nil
		}
		return 0, fmt.Errorf("push tag %s to origin: %s: %w", tag, out, gerr)
	}
	return vcs.TagCreated, nil
}

// remoteTagSHA returns the commit origin's copy of tag points at, or "" when
// origin has no such tag. An annotated tag is resolved through to its commit
// via the ^{} peeled ref, so the answer is comparable with a merge SHA.
//
// ls-remote is used rather than a fetch-then-rev-parse so the check cannot be
// answered from a stale local ref, and so it stays read-only.
func (r Refs) remoteTagSHA(wtDir, tag string) (string, error) {
	ref := "refs/tags/" + tag
	out, err := r.Runner.Run(wtDir, "ls-remote", "origin", ref, ref+"^{}")
	if err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	var direct, peeled string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		switch fields[1] {
		case ref:
			direct = fields[0]
		case ref + "^{}":
			peeled = fields[0]
		}
	}
	// The peeled value is the commit an annotated tag points at; prefer it.
	// For a lightweight tag there is no peeled line and the direct value
	// already is the commit.
	if peeled != "" {
		return peeled, nil
	}
	return direct, nil
}
