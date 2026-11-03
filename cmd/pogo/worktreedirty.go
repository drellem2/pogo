package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/cli"
	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
	"github.com/drellem2/pogo/internal/vcs/gitvcs"
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
		Short: "Print uncommitted work in a git worktree or enth checkout, ignoring pogod's own .claude files",
		Long: `Print the uncommitted entries in the git worktree containing dir (default:
the current directory), one git status --porcelain line each. No output means
the tree holds no uncommitted work.

This is the check gc uses before it removes a worktree: modified, staged and
untracked files all count, EXCEPT the untracked files pogod writes into every
agent's tree itself — the hook settings .claude/settings.local.json at the
worktree root, and the mail-hook stamp .claude/pogo-mailhook.stamp at any
depth. Any other untracked file counts, including other files under .claude/,
and so does any change to a tracked file whatever its name.

In an enth checkout (a directory holding .enth/checkout) the same question is
asked of ` + "`enth status`" + `, and its edits are printed in the same shape: " M path"
for a changed file, "?? path" for a new one (including one outside a narrowed
checkout's selection), " D path" for a deletion. pogod's own files are kept out
of enth status by the checkout's .enth/info/exclude.

Exits non-zero, with nothing on stdout, when dirtiness cannot be determined
(not a git worktree or enth checkout, git or enth failed, or a directory
carrying both markers). A caller must treat that as a refusal, not as clean.`,
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
//
// An enth checkout (enth-vcs slice 5, mg-cc5cd) resolves to its own root, the
// nearest directory holding .enth/checkout; gitgc.WorktreeDirty then asks
// `enth status` there. A directory carrying both markers is refused.
func worktreeRoot(dir string) (string, error) {
	kind, root, err := enthvcs.Detect(dir)
	if err != nil {
		return "", err
	}
	if kind == vcs.Enth {
		return root, nil
	}
	return worktreeWorkspaces.Root(context.Background(), dir)
}

// worktreeWorkspaces is the backend worktree-dirty and check-stranded read
// workspaces through. git is the only one wired here (enth-vcs slice 3b,
// mg-afad2).
var worktreeWorkspaces vcs.Workspaces = gitvcs.Workspaces{}
