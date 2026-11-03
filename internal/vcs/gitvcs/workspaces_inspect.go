package gitvcs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/hookarm"
	"github.com/drellem2/pogo/internal/vcs"
)

// Remove, Dirty, Describe and Root (slice 3b, mg-afad2). Each was
// moved here from the call site that ran its git, without changing the argv,
// the environment or the text of what it reports:
//
//   - Remove from internal/gitgc's RemoveWorktreeForce,
//   - Dirty from internal/gitgc's WorktreeDirty (and its porcelain helpers),
//   - Describe from cmd/pogo/checkstranded.go's polecatWorktrees,
//   - Root from cmd/pogo/worktreedirty.go's worktreeRoot.
//
// Like the rest of Workspaces, none of them honours ctx: none of these commands
// was cancellable before. Dirty is bounded only when the caller injects a
// bounded runner (Workspaces.RunSplit), which is how gitgc keeps it under its
// commandBudget.

// SplitFunc runs an external command and returns its stdout and stderr
// separately, with the raw os/exec error. gitgc passes its budgeted runSplit.
type SplitFunc func(name string, args ...string) (stdout, stderr []byte, err error)

func (w Workspaces) runSplit(name string, args ...string) ([]byte, []byte, error) {
	if w.RunSplit != nil {
		return w.RunSplit(name, args...)
	}
	cmd := exec.Command(name, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.Bytes(), se.Bytes(), err
}

// Remove runs `git -C <repo> worktree remove --force <dir>`. A failure is a
// *vcs.CommandError carrying git's output. It does NOT delete what git leaves
// behind: gitgc treats this step as best-effort and removes the directory
// itself as the backstop, because a legacy worktree whose .git pointer is gone
// is one git refuses but gc must still reclaim.
func (Workspaces) Remove(_ context.Context, repo vcs.Repo, dir string) error {
	args := []string{"-C", repo.Path, "worktree", "remove", "--force", dir}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return &vcs.CommandError{Args: args, Output: out, Err: err}
	}
	return nil
}

// Dirty lists dir's uncommitted entries as `git status --porcelain` lines:
// modified, staged and untracked files, but not ignored ones, and not the
// untracked files pogod itself writes into every agent's tree (IsPogodArtefact).
// A collapsed `?? .claude/` is opened with --untracked-files=all so its
// contents can be judged one by one; any other collapsed directory stays one
// entry, which is the safe direction for a guard that destroys.
//
// It reads STDOUT only: `git status` writes warnings to stderr and exits 0, and
// a warning read as a porcelain line made a clean tree look dirty (mg-f4c0).
// A non-nil error means dirtiness could not be determined, and the caller must
// not read it as clean.
func (w Workspaces) Dirty(_ context.Context, dir string) ([]string, error) {
	if dir == "" {
		return nil, fmt.Errorf("empty worktree path")
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("stat worktree %s: %w", dir, err)
	}
	lines, err := w.PorcelainStatus(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range lines {
		if d, ok := UntrackedPath(line); ok && strings.HasSuffix(d, "/") &&
			path.Base(d) == ".claude" {
			inner, err := w.PorcelainStatus(dir, "--untracked-files=all", "--", d)
			if err != nil {
				return nil, err
			}
			for _, l := range inner {
				if !IsPogodArtefact(l) {
					files = append(files, l)
				}
			}
			continue
		}
		if !IsPogodArtefact(line) {
			files = append(files, line)
		}
	}
	return files, nil
}

// PorcelainStatus runs `git -C dir status --porcelain` with extra args and
// returns its non-empty stdout lines, trimmed. A failure carries git's trimmed
// stderr.
func (w Workspaces) PorcelainStatus(dir string, extra ...string) ([]string, error) {
	args := append([]string{"-C", dir, "status", "--porcelain"}, extra...)
	stdout, stderr, err := w.runSplit("git", args...)
	if err != nil {
		return nil, fmt.Errorf("status %s: %w: %s",
			dir, err, strings.TrimSpace(string(stderr)))
	}
	var lines []string
	for _, line := range strings.Split(string(stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// UntrackedPath returns the path of a `??` porcelain entry.
func UntrackedPath(line string) (string, bool) {
	return strings.CutPrefix(line, "?? ")
}

// IsPogodArtefact reports whether a porcelain entry is a file pogod itself
// writes into every agent's worktree, which is therefore not work
// (gh #162, #155, #240): the hook registration at hookarm.SettingsRelPath (at
// the root only — pogod writes it at the spawn directory and nowhere else) and
// the liveness stamp hookarm.StampName inside a .claude/ directory at ANY depth,
// because the stamp follows the session's cwd.
//
// UNTRACKED entries only. A tracked file under .claude/ that has been modified
// or deleted is a change git can see against HEAD and stays dirty, whatever its
// name — and pogo itself tracks .claude/commands/, so excluding the directory
// wholesale would let gc discard a real edit. Matching is by exact identity for
// the same reason. A quoted porcelain path (special characters) never matches,
// which keeps it dirty.
func IsPogodArtefact(line string) bool {
	p, ok := UntrackedPath(line)
	if !ok {
		return false
	}
	if p == filepath.ToSlash(hookarm.SettingsRelPath) {
		return true
	}
	return path.Base(p) == hookarm.StampName && path.Base(path.Dir(p)) == ".claude"
}

// Describe names the repository a worktree belongs to and the branch it has
// checked out, from the tree itself:
//
//   - `git -C dir rev-parse --path-format=absolute --git-common-dir`. A failure
//     is returned as os/exec's raw error, because the caller quotes it.
//   - a common dir whose base is not ".git" is a *vcs.UnnamedRepoError: the
//     tree answers, but the repository cannot be named from the layout.
//   - `git -C dir symbolic-ref --short HEAD`. Any failure leaves Branch empty
//     and is not an error: a detached HEAD has no branch to name.
//
// Dir is dir as given, not resolved to the worktree root (see Root). Base is
// not known to git and is left empty.
func (Workspaces) Describe(_ context.Context, dir string) (vcs.Workspace, error) {
	common, err := exec.Command("git", "-C", dir,
		"rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return vcs.Workspace{}, err
	}
	gitDir := strings.TrimSpace(string(common))
	if filepath.Base(gitDir) != ".git" {
		return vcs.Workspace{}, &vcs.UnnamedRepoError{
			Dir: dir,
			Reason: fmt.Sprintf("git-common-dir %q is not a conventional worktree layout, "+
				"so the repository it belongs to could not be named", gitDir),
		}
	}
	ws := vcs.Workspace{Dir: dir, Repo: vcs.Repo{Path: filepath.Dir(gitDir)}}
	if br, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output(); err == nil {
		ws.Branch = strings.TrimSpace(string(br))
	}
	return ws, nil
}

// Root runs `git -C dir rev-parse --show-toplevel`. The error says dir is not
// inside a git worktree, quoting git's stderr when it wrote any.
func (Workspaces) Root(_ context.Context, dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("%s is not inside a git worktree: %s", dir, msg)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("%s: git rev-parse --show-toplevel printed nothing", dir)
	}
	return root, nil
}
