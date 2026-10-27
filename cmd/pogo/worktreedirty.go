package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/gitgc"
)

// newWorktreeDirtyCmd builds `pogo worktree-dirty`, the polecat templates'
// pre-submit dirty check (mg-54263).
//
// The templates used to run a bare `git status --porcelain`, which counts the
// files pogod itself writes into every agent's tree — the hook settings and the
// mail-hook stamp under .claude/. In a repo whose .gitignore lacks .claude/ that
// made every polecat refuse to submit (gh #240). This command answers with the
// predicate gc already uses (gitgc.WorktreeDirty), so the template and the
// gc guard cannot disagree about what counts as uncommitted work, and no
// artefact name is spelled a second time. Git's ignore rules are not touched.
//
// It reads git only and never contacts pogod.
func newWorktreeDirtyCmd(jsonOutput *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "worktree-dirty [dir]",
		Short: "Print uncommitted work in a git worktree, ignoring pogod's own .claude files",
		Long: `Print the uncommitted entries in the git worktree containing dir (default:
the current directory), one git status --porcelain line each. No output means
the tree holds no uncommitted work.

This is the check gc uses before it removes a worktree: modified, staged and
untracked files all count, EXCEPT the untracked files pogod writes into every
agent's tree itself — the hook settings .claude/settings.local.json at the
worktree root, and the mail-hook stamp .claude/pogo-mailhook.stamp at any
depth. Any other untracked file counts, including other files under .claude/,
and so does any change to a tracked file whatever its name.

Exits non-zero, with nothing on stdout, when dirtiness cannot be determined
(not a git worktree, git failed). A caller must treat that as a refusal, not
as clean.`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			root, err := worktreeRoot(dir)
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			dirty, files, err := gitgc.WorktreeDirty(root)
			if err != nil {
				cli.ExitWithError(*jsonOutput, err.Error(), cli.ExitError)
			}
			if *jsonOutput {
				if files == nil {
					files = []string{}
				}
				cli.PrintJSON(map[string]any{"worktree": root, "dirty": dirty, "files": files})
				return
			}
			for _, f := range files {
				fmt.Println(f)
			}
		},
	}
}

// worktreeRoot resolves dir to the top of its worktree. WorktreeDirty must be
// handed the root: the root-only settings exclusion and the pathspec it uses to
// open a collapsed `?? .claude/` are both root-relative, and a polecat's shell
// is often cd'd into a subdirectory by the time it submits.
func worktreeRoot(dir string) (string, error) {
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
