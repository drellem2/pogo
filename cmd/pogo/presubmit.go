package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// newPresubmitCmd builds `pogo presubmit`, the polecat protocol's pre-submit
// check as a verb (docs/design/enth-vcs.md §12, P2; slice 6, mg-30da0).
//
// The check used to exist only as fifteen lines of shell in the polecat
// templates (presubmit_check, mg-c184d). Duplicating it per VCS would put two
// copies of it in each of two templates, so it becomes a verb that asks
// through the seam. On git it is that shell, check for check and line for
// line (TestPresubmitGitEqualsTheTemplateShell runs both on the same trees;
// the shell is kept in cmd/pogo/testdata/presubmit_check.sh). Both arms of the
// templates now run this verb in its place (mg-71014).
func newPresubmitCmd() *cobra.Command {
	var item, target, worker string
	var printStack bool
	cmd := &cobra.Command{
		Use:   "presubmit",
		Short: "Refuse to submit work that would deliver nothing (the polecat pre-submit check)",
		Long: `Check, from inside a polecat's workspace (the current directory), that
submitting it to the refinery would deliver something. Prints one
"REFUSING TO SUBMIT: ..." explanation and exits 1 when it would not; prints
"pre-submit OK: ..." and exits 0 when it would.

In a git worktree it runs exactly the presubmit_check shell the polecat
templates carried before it, in its order: git fetch origin; pogo worktree-dirty must be empty; the branch must
carry commits ahead of origin/<target>; and HEAD must equal origin/<branch>
(the branch is pushed). --item is accepted and not needed.

In an enth checkout (a directory holding .enth/checkout) --item is required:
  - the checkout must be clean (enth status, pogod's own files excluded);
  - its base must not be an unlanded Change without a Pogo-Item trailer — that
    work is invisible to every trailer query, and it is refused with that
    remedy rather than missed;
  - the unlanded Changes carrying Pogo-Item=<item> (and Pogo-Worker=<worker>,
    default $POGO_AGENT_NAME) must form exactly ONE stack: none delivers
    nothing, and several are refused as ambiguous, naming each stack's worker;
  - that stack's top must be the checkout's base, so what is submitted is
    the work in this checkout.
  - no Change of that stack may carry a GitHub closing keyword (Fixes #N,
    Closes owner/repo#N, …): its message reaches the GitHub mirror verbatim
    and would close the issue there. This is the closing-ref check the
    refinery ran on enth work before workers landed their own (mg-5b904).
Changes are durable on enthd when made, so there is nothing to push.

--print-stack (enth only) prints the approved stack's Change ids, bottom first,
on stdout as ONE line separated by single spaces, and every message on stderr.
It prints nothing on stdout when it refuses. That one-line format is a
contract with two consumers (mg-88ece): the worker's own land, which splits it
on any whitespace into one argument per id (enth land <ids> --request-id …),
and a re-made Change's Pogo-Supersedes trailer, whose value it is verbatim and
which must stay on one line. A consumer must not read it one id per line.

A check that cannot run (git, enth or worktree-dirty failing) refuses: it
never reads as clean.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if worker == "" {
				worker = os.Getenv("POGO_AGENT_NAME")
			}
			if printStack {
				ok := printPresubmitStack(context.Background(), ".", item, worker, os.Stdout, os.Stderr)
				if !ok {
					os.Exit(1)
				}
				return
			}
			ok := checkPresubmit(context.Background(), ".", item, target, worker, os.Stdout, os.Stderr)
			if !ok {
				os.Exit(1)
			}
		},
	}
	cmd.Flags().StringVar(&item, "item", "", "Work item the submission is for (required on an enth repo)")
	cmd.Flags().StringVar(&target, "target", "main", "Target ref the work will be merged into")
	cmd.Flags().StringVar(&worker, "worker", "", "Worker whose Changes to list on enth (default $POGO_AGENT_NAME)")
	cmd.Flags().BoolVar(&printStack, "print-stack", false, "enth: print the approved stack's Change ids (bottom first) on stdout as ONE line, single-space-separated, messages on stderr — the arguments of the worker's own enth land, and the value of a re-make's Pogo-Supersedes trailer")
	return cmd
}

// checkPresubmit picks the check by the tree's marker, as worktree-dirty does.
func checkPresubmit(ctx context.Context, dir, item, target, worker string, stdout, stderr io.Writer) bool {
	kind, root, err := enthvcs.Detect(dir)
	if err != nil {
		fmt.Fprintf(stdout, "REFUSING TO SUBMIT: %v\n", err)
		return false
	}
	if kind == vcs.Enth {
		return presubmitEnth(ctx, root, item, worker, stdout)
	}
	return presubmitGit(dir, target, stdout, stderr)
}

// presubmitGit is the former template shell's presubmit_check. Each step's refusal text is
// the shell's, byte for byte, and git's own output goes where the shell's
// did: fetch's to the terminal, a failed command's stderr to stderr.
func presubmitGit(dir, target string, stdout, stderr io.Writer) bool {
	refuse := func(msg string) bool { fmt.Fprintln(stdout, msg); return false }
	git := func(passStdout bool, args ...string) (string, error) {
		c := exec.Command("git", args...)
		c.Dir = dir
		var out bytes.Buffer
		if passStdout {
			c.Stdout = stdout
		} else {
			c.Stdout = &out
		}
		c.Stderr = stderr
		err := c.Run()
		return strings.TrimRight(out.String(), "\n"), err
	}
	// $BRANCH, as the protocol computes it.
	branch, _ := git(false, "rev-parse", "--abbrev-ref", "HEAD")

	if _, err := git(true, "fetch", "origin"); err != nil {
		return refuse("REFUSING TO SUBMIT: git fetch origin failed — cannot tell what this branch carries")
	}
	root, err := worktreeRoot(dir)
	var files []string
	if err == nil {
		_, files, err = gitgc.WorktreeDirty(root)
	}
	if err != nil {
		fmt.Fprintln(stderr, "Error: "+err.Error())
		return refuse("REFUSING TO SUBMIT: pogo worktree-dirty failed — cannot tell whether this worktree holds uncommitted work")
	}
	if len(files) > 0 {
		fmt.Fprintln(stdout, "REFUSING TO SUBMIT: uncommitted work in this worktree — commit and push it, or discard it deliberately:")
		return refuse(strings.Join(files, "\n"))
	}
	aheadOut, err := git(false, "rev-list", "--count", "origin/"+target+"..HEAD")
	ahead, perr := strconv.Atoi(strings.TrimSpace(aheadOut))
	if err != nil || perr != nil {
		return refuse("REFUSING TO SUBMIT: git rev-list origin/" + target + "..HEAD failed")
	}
	if ahead == 0 {
		fmt.Fprintln(stdout, "REFUSING TO SUBMIT: "+branch+" carries no commits ahead of origin/"+target+" — nothing would be delivered.")
		return refuse("Commit and push your work. (If an earlier MR of yours already merged this branch, do not resubmit: check it with pogo refinery show <id>.)")
	}
	head, _ := git(false, "rev-parse", "HEAD")
	pushed, _ := git(false, "rev-parse", "-q", "--verify", "origin/"+branch)
	if head != pushed {
		return refuse("REFUSING TO SUBMIT: origin/" + branch + " is not your HEAD — git push origin \"" + branch + "\" first")
	}
	fmt.Fprintf(stdout, "pre-submit OK: %d commit(s) ahead of origin/%s, worktree clean, branch pushed\n", ahead, target)
	return true
}

// presubmitEnth is the same question on enth (§12), asked by
// enthvcs.Presubmit; this only prints its answer.
func presubmitEnth(ctx context.Context, root, item, worker string, stdout io.Writer) bool {
	_, ok := presubmitEnthStack(ctx, root, item, worker, stdout)
	return ok
}

// presubmitEnthStack is presubmitEnth returning the approved stack. Beyond
// enthvcs.PresubmitStack it runs the closing-ref check on the stack's
// messages: the worker lands its own work, so this is the last check before
// the land (mg-5b904).
func presubmitEnthStack(ctx context.Context, root, item, worker string, msgs io.Writer) ([]string, bool) {
	msg, s, b, err := enthvcs.PresubmitStack(ctx, root, item, worker, "")
	if err == nil {
		err = refinery.EnthClosingRefs(ctx, b, s.Revs)
	}
	if err != nil {
		fmt.Fprintf(msgs, "REFUSING TO SUBMIT: %v\n", err)
		return nil, false
	}
	fmt.Fprintln(msgs, msg)
	return s.Revs, true
}

// printPresubmitStack is --print-stack: the check's messages on stderr, the
// stack's ids on stdout as one space-separated line (enthvcs.PrintStackLine,
// the contract self_land and Pogo-Supersedes rely on), and nothing on stdout
// when it refuses.
func printPresubmitStack(ctx context.Context, dir, item, worker string, stdout, stderr io.Writer) bool {
	kind, root, err := enthvcs.Detect(dir)
	if err != nil {
		fmt.Fprintf(stderr, "REFUSING TO SUBMIT: %v\n", err)
		return false
	}
	if kind != vcs.Enth {
		fmt.Fprintln(stderr, "REFUSING TO SUBMIT: --print-stack is for an enth checkout; this is a git worktree, whose work is its branch")
		return false
	}
	ids, ok := presubmitEnthStack(ctx, root, item, worker, stderr)
	if !ok {
		return false
	}
	fmt.Fprintln(stdout, enthvcs.PrintStackLine(ids))
	return true
}
