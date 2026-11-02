package gitvcs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/freshen"
	"github.com/drellem2/pogo/internal/ghtoken"
	"github.com/drellem2/pogo/internal/vcs"
)

// Workspaces is the git backend of vcs.Workspaces: one linked worktree per
// polecat, on its own polecat-<name> branch (enth-vcs slice 2, mg-cc7ae).
//
// Every method was moved out of internal/agent (api.go, strandedadopt.go,
// command.go) and internal/freshen's call site without changing the git it
// runs, the environment it runs it in, or what it does with the output. The
// policy around those calls — which leftover branch may be reclaimed, whose
// directory a rollback may delete, what a refusal says — stayed in
// internal/agent, because it is not a fact about git.
//
// Only ResolveBase is bounded, by FetchTimeout, exactly as before. The other
// methods take ctx for the interface and do not honour it: before the seam
// they ran unbounded, local-only git commands, and slice 2 changes no
// behaviour.
type Workspaces struct {
	// FetchTimeout bounds ResolveBase's fetch, wall clock, across the narrow
	// attempt and its wide retry. Zero means no fetch can succeed in time.
	FetchTimeout time.Duration
}

var _ vcs.Workspaces = Workspaces{}

// ResolveBase returns the git ref a new polecat worktree should be based on.
// It fetches origin so the polecat sees the latest merged commits, then
// prefers origin/<target> (the branch the polecat will target via the
// refinery) and falls back to origin/HEAD's default branch, then origin/main.
// Returns "" when no usable origin is available, in which case the caller
// bases the worktree on the source repo's local HEAD.
//
// This exists because creating the worktree from local HEAD made polecats
// invisible to commits that had been merged to origin/main but not yet
// pulled into the source checkout, producing false reports of unmerged work.
func (w Workspaces) ResolveBase(_ context.Context, repo vcs.Repo, target string) string {
	sourceRepo := repo.Path
	if sourceRepo == "" {
		return ""
	}
	if err := exec.Command("git", "-C", sourceRepo, "remote", "get-url", "origin").Run(); err != nil {
		return ""
	}
	if !FetchBaseRefs(sourceRepo, target, w.FetchTimeout) {
		return ""
	}
	if target != "" {
		ref := "origin/" + target
		if exec.Command("git", "-C", sourceRepo, "rev-parse", "--verify", ref).Run() == nil {
			return ref
		}
	}
	if out, err := exec.Command("git", "-C", sourceRepo, "symbolic-ref", "refs/remotes/origin/HEAD").Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		if r := strings.TrimPrefix(ref, "refs/remotes/"); r != ref {
			return r
		}
	}
	if exec.Command("git", "-C", sourceRepo, "rev-parse", "--verify", "origin/main").Run() == nil {
		return "origin/main"
	}
	return ""
}

// FetchBaseRefs updates the remote-tracking refs ResolveBase is about to
// read, bounded by fetchTimeout. It reports whether origin is usable; false
// means the caller should fall back to local HEAD.
//
// A timeout needs no new failure semantics: ResolveBase ALREADY returns ""
// when the fetch fails, and "" already means "base the worktree on local
// HEAD". A bounded failure reuses the path an unbounded one would eventually
// have taken anyway, minus the wait (mg-538e).
//
// # Narrowing, and why it falls back
//
// The old call was a bare `git fetch origin`: all 707 refs of this repo, of
// which 635 are `polecat-*` branches, with `remote.origin.prune` and
// `fetch.prune` both unset — a cost that grows with every polecat ever spawned.
// Narrowing it to the refs ResolveBase actually reads contributed only ~0.4s
// of the measured 1.9s, so it is not the win; it is a cap on a slowly
// degrading one.
//
// It is narrow-THEN-WIDE rather than narrow-only because git fails a fetch
// whole when any refspec matches nothing on the remote. A target branch that
// does not exist on origin yet is an ordinary case here (ResolveBase's
// fallback chain exists for it), and a narrow-only fetch would turn it from
// "fall back to origin/HEAD" into "fall back to local HEAD" — a silent
// regression in the thing ResolveBase exists to prevent. So a narrow fetch
// that fails is retried once, wide, inside the SAME deadline: the bound is on
// the wall clock, not on the number of attempts.
func FetchBaseRefs(sourceRepo, branch string, fetchTimeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	if specs := FetchRefspecs(ctx, sourceRepo, branch); len(specs) > 0 {
		args := append([]string{"-C", sourceRepo, "fetch", "--no-tags", "origin"}, specs...)
		narrow := exec.CommandContext(ctx, "git", args...)
		narrow.Env = ghtoken.ChildEnvContext(ctx, os.Environ())
		if out, err := narrow.CombinedOutput(); err == nil {
			return true
		} else if ctx.Err() == nil {
			log.Printf("polecat: narrow fetch of %v failed in %s, retrying wide: %v\n%s",
				specs, sourceRepo, err, out)
		}
	}

	wide := exec.CommandContext(ctx, "git", "-C", sourceRepo, "fetch", "--no-tags", "origin")
	wide.Env = ghtoken.ChildEnvContext(ctx, os.Environ())
	out, err := wide.CombinedOutput()
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		log.Printf("polecat: fetch origin in %s exceeded %s and was killed — basing the worktree on local HEAD. "+
			"The polecat may not see commits merged to origin since this checkout last pulled",
			sourceRepo, fetchTimeout)
		return false
	}
	log.Printf("polecat: fetch origin failed in %s, falling back to local HEAD: %v\n%s", sourceRepo, err, out)
	return false
}

// FetchRefspecs returns explicit refspecs for the refs ResolveBase reads: the
// polecat's target branch and the repo's default branch. Both are resolved
// without touching the network — the default comes from the local
// refs/remotes/origin/HEAD symref. Returns nil when nothing narrow can be
// named, which makes the caller fetch wide.
//
// It takes the fetch's ctx even though it never goes to the network. The
// defect mg-538e repaired is an unbounded subprocess on the pre-claim spawn
// path; adding a NEW unbounded subprocess there while fixing it would be the
// repair exhibiting the defect it repairs. Sharing the deadline costs nothing
// and leaves nothing on that path outside it.
func FetchRefspecs(ctx context.Context, sourceRepo, branch string) []string {
	seen := map[string]bool{}
	var specs []string
	add := func(b string) {
		if b == "" || seen[b] {
			return
		}
		seen[b] = true
		specs = append(specs, fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", b, b))
	}
	add(branch)
	if out, err := exec.CommandContext(ctx, "git", "-C", sourceRepo, "symbolic-ref", "refs/remotes/origin/HEAD").Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		// Only when the prefix was actually there. TrimPrefix returns the input
		// unchanged when it is not, which would name a refspec out of a full
		// ref path and fail the narrow fetch for no reason.
		if b := strings.TrimPrefix(ref, "refs/remotes/origin/"); b != ref {
			add(b)
		}
	}
	return specs
}

// BranchExists reports whether refs/heads/<branch> exists in repo.
func (Workspaces) BranchExists(_ context.Context, repo vcs.Repo, branch string) bool {
	if repo.Path == "" || branch == "" {
		return false
	}
	return exec.Command("git", "-C", repo.Path, "rev-parse", "--verify", "--quiet",
		"refs/heads/"+branch).Run() == nil
}

// BranchSpent reports whether every commit on branch is already reachable
// from base — i.e. the branch holds nothing that would be lost by deleting
// it. It answers "no" on any error: an unreadable branch is treated as
// carrying work, because the cost of a wrong "yes" is destroyed commits.
func (Workspaces) BranchSpent(_ context.Context, repo vcs.Repo, branch, base string) bool {
	out, err := exec.Command("git", "-C", repo.Path, "rev-list", "--count",
		base+".."+branch).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "0"
}

// DeleteBranch runs `git branch -D`. git refuses a branch checked out in any
// worktree, so a live polecat's checked-out branch is never at risk here.
func (Workspaces) DeleteBranch(_ context.Context, repo vcs.Repo, branch string) error {
	args := []string{"-C", repo.Path, "branch", "-D", branch}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return &vcs.CommandError{Args: args, Output: out, Err: err}
	}
	return nil
}

// Create runs `git worktree add <dir> -b <branch> [<base>]`.
//
// `git worktree add -b` creates the branch and *then* checks it out, so a
// failure can leave the branch behind with no worktree. Create does not clean
// that up: whether the branch and the directory are this caller's to delete
// depends on what existed before the call, which only the caller measured
// (mg-d22a, drellem2/pogo#167).
func (Workspaces) Create(_ context.Context, s vcs.WorkspaceSpec) (vcs.Workspace, error) {
	args := []string{"-C", s.Repo.Path, "worktree", "add", s.Dir, "-b", s.Branch}
	if s.Base != "" {
		args = append(args, s.Base)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return vcs.Workspace{}, &vcs.CommandError{Args: args, Output: out, Err: err}
	}
	return vcs.Workspace{Dir: s.Dir, Repo: s.Repo, Branch: s.Branch, Base: s.Base}, nil
}

// Rollback runs `git worktree remove <dir> --force`. The --force is
// deliberate and only correct for a tree this process just created and no
// agent ran in — see internal/agent's cleanupFailedPolecatSpawn, the only
// caller, which also removes the directory itself as a backstop.
func (Workspaces) Rollback(_ context.Context, repo vcs.Repo, dir string) error {
	args := []string{"-C", repo.Path, "worktree", "remove", dir, "--force"}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return &vcs.CommandError{Args: args, Output: out, Err: err}
	}
	return nil
}

// Contains runs `git merge-base --is-ancestor <base> <branch>`. The returned
// error is git's own (usually "exit status 1"), unwrapped, because the caller
// quotes it.
func (Workspaces) Contains(_ context.Context, repo vcs.Repo, base, branch string) error {
	return exec.Command("git", "-C", repo.Path, "merge-base", "--is-ancestor", base, branch).Run()
}

// Exclude appends relPath to the worktree's .git/info/exclude so `git add -A`
// never stages it. relPath is relative to the worktree root (dir). The pattern
// is anchored to the root with a leading slash so a same-named file deeper in
// the tree is unaffected. Idempotent: an already-excluded path is left
// untouched. group is a header line written once, before the first pattern
// this exclude file receives under it.
//
// info/exclude is shared across a repo's linked worktrees; the caller's
// patterns are pogo's own, never-tracked files, so excluding them repo-wide is
// invisible and harmless. Returns an error the caller may log.
func (Workspaces) Exclude(_ context.Context, dir, group, relPath string) error {
	if dir == "" || relPath == "" {
		return nil
	}
	pattern := "/" + filepath.ToSlash(filepath.Clean(relPath))

	excludePath, err := excludePath(dir)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil // already excluded
		}
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	var b strings.Builder
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteString("\n")
	}
	// Group all of this caller's patterns under a single header: only emit it
	// the first time this exclude file is touched for that group.
	if !strings.Contains(string(data), group) {
		b.WriteString(group + "\n")
	}
	b.WriteString(pattern + "\n")
	_, err = f.WriteString(b.String())
	return err
}

// excludePath resolves the info/exclude file for the worktree rooted at dir.
// `git rev-parse --git-path` handles every layout — plain repo, linked
// worktree (whose info/exclude lives in the shared common dir), submodule — so
// we don't hand-parse gitdir indirection. The toplevel is cross-checked
// against dir because git skips an invalid .git entry and resolves to an
// enclosing repo, whose exclude file we must not touch.
func excludePath(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel", "--git-path", "info/exclude")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git info/exclude: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("unexpected git rev-parse output: %q", string(out))
	}
	toplevel, exclude := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if toplevel == "" || exclude == "" {
		return "", fmt.Errorf("empty git rev-parse output")
	}
	if !samePath(toplevel, dir) {
		return "", fmt.Errorf("dir %s is not a repo root (toplevel %s); refusing to edit enclosing repo", dir, toplevel)
	}
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(dir, exclude)
	}
	return exclude, nil
}

// samePath reports whether two paths name the same directory, tolerating
// trailing separators and symlinks (macOS /tmp vs /private/tmp).
func samePath(a, b string) bool {
	return resolvePath(a) == resolvePath(b)
}

func resolvePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// Refresh fast-forwards a long-lived checkout to its upstream, or says why it
// would not. The git work is internal/freshen's Checkout, unchanged; see that
// package for the design (fetch first, OID-derived verdict, never touch a
// dirty tree).
func (Workspaces) Refresh(_ context.Context, dir string) vcs.RefreshResult {
	return freshen.Checkout(dir)
}
