package gitgc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// commandBudget is how long ONE external command (a git subcommand, `mg list`)
// may take before this package kills it and reports the kill.
//
// # Why a bound exists at all (mg-1530, drellem2/pogo#158)
//
// `pogo gc --list-preserved` had ZERO bounded subprocesses. One measured run on
// this host spawned 114 of them for 60 directories — 46 `git status`, 39
// `symbolic-ref`, 21 `rev-parse`, 7 `for-each-ref`, 1 `mg list` — and every one
// of them could block forever: a git index lock nobody will release, a
// filesystem that stopped answering, an `mg` waiting on something. The command
// then produced no output and never returned, which is what #158 reports.
//
// The bound does not make the scan fast and is not meant to. It converts an
// unbounded wait into a NAMED ROW: the tree is still listed, with the failure
// text saying the budget was exceeded, and the scan moves on to the next tree.
//
// # The value, and why it is this generous
//
// 90 seconds is far above any legitimate call measured here (a full 60-directory
// scan takes 1.67-2.31s end to end, all 114 subprocesses included) and far below
// "forever", which is the only number it is competing with. It is deliberately
// NOT tuned to be tight: a bound that fires on a slow-but-working git turns a
// correct row into a "could not read" row, and this listing is read by a human
// deciding what to delete. Erring long costs a wait; erring short costs a fact.
//
// It is a var so a test can shrink it. Nothing in production writes it.
var commandBudget = 90 * time.Second

// ErrCommandBudget marks a command this package KILLED for exceeding
// commandBudget, as opposed to one that ran and failed.
//
// The distinction is the whole point of the error existing. "git status failed"
// and "git status was still running after 90 seconds" send a reader to
// different places — a damaged repository versus a wedged filesystem or a lock
// — and an operator who reads the first when the second happened will go and
// fsck a repository that is fine. Every renderer that prints a status failure
// prints this text verbatim, so the distinction reaches the listing.
var ErrCommandBudget = errors.New("still running after the per-command budget")

// runSplit runs an external command under commandBudget and returns its stdout
// and stderr separately.
//
// A command killed by the budget comes back wrapped in ErrCommandBudget; every
// other failure comes back as the RAW error from os/exec, untouched, because
// callers here read exit codes off it (`symbolic-ref` exits 1 for a detached
// head, `merge-base --is-ancestor` exits 1 for "not an ancestor") and a wrapped
// *exec.ExitError would defeat the type assertions that do that. The budget
// error is safe to wrap precisely because a killed process never carries a
// meaningful exit code — it carries "signal: killed", which no caller reads.
func runSplit(name string, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	rerr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return so.Bytes(), se.Bytes(), budgetError(name, args)
	}
	return so.Bytes(), se.Bytes(), rerr
}

// runOutput is runSplit for callers that read stdout only — the shape
// exec.Cmd.Output() had at these call sites before the bound existed.
func runOutput(name string, args ...string) ([]byte, error) {
	out, _, err := runSplit(name, args...)
	if err != nil {
		return out, err
	}
	return out, nil
}

// runCombined is runSplit for callers that want stdout and stderr interleaved
// the way exec.Cmd.CombinedOutput() gives them.
//
// The two buffers are concatenated rather than shared, so the ORDER within each
// stream is git's and the order between them is not. Every caller of this uses
// the result as an error message or reads a single line of stdout, and none
// depends on the interleaving; the alternative — one shared buffer — would
// reintroduce the defect WorktreeDirty was fixed for (a warning on stderr read
// as a porcelain line), one function over.
func runCombined(name string, args ...string) ([]byte, error) {
	so, se, err := runSplit(name, args...)
	out := so
	if len(se) > 0 {
		out = append(append([]byte{}, so...), se...)
	}
	return out, err
}

func budgetError(name string, args []string) error {
	invocation := name
	if len(args) > 0 {
		invocation += " " + strings.Join(args, " ")
	}
	return fmt.Errorf("%s: %w of %s (killed)", invocation, ErrCommandBudget, commandBudget)
}
