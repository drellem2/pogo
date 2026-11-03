package gitgc

import (
	"time"

	"github.com/drellem2/pogo/internal/vcs/gitvcs"
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
// commandBudget, as opposed to one that ran and failed. It is
// gitvcs.ErrCommandBudget: the runner moved to internal/vcs/gitvcs
// (slice 3, mg-d72cb) and this name is kept for its callers.
//
// The distinction is the whole point of the error existing. "git status failed"
// and "git status was still running after 90 seconds" send a reader to
// different places — a damaged repository versus a wedged filesystem or a lock
// — and an operator who reads the first when the second happened will go and
// fsck a repository that is fine. Every renderer that prints a status failure
// prints this text verbatim, so the distinction reaches the listing.
var ErrCommandBudget = gitvcs.ErrCommandBudget

// runSplit runs an external command under commandBudget and returns its stdout
// and stderr separately. See gitvcs.RunSplit, which it calls with the budget
// read at call time, so a test that shrinks commandBudget still bounds it.
//
// A command killed by the budget comes back wrapped in ErrCommandBudget; every
// other failure comes back as the RAW error from os/exec, untouched, because
// callers here read exit codes off it (`symbolic-ref` exits 1 for a detached
// head, `merge-base --is-ancestor` exits 1 for "not an ancestor").
func runSplit(name string, args ...string) (stdout, stderr []byte, err error) {
	return gitvcs.RunSplit(commandBudget, name, args...)
}

// runOutput is runSplit for callers that read stdout only — the shape
// exec.Cmd.Output() had at these call sites before the bound existed.
func runOutput(name string, args ...string) ([]byte, error) {
	return gitvcs.RunOutput(commandBudget, name, args...)
}

// runCombined is runSplit for callers that want stdout and stderr together the
// way exec.Cmd.CombinedOutput() gives them. See gitvcs.RunCombined for why the
// streams are concatenated rather than shared.
func runCombined(name string, args ...string) ([]byte, error) {
	return gitvcs.RunCombined(commandBudget, name, args...)
}

// work is this package's git Work backend. Its runner is runOutput, the one
// the cherry calls used before they moved onto it, so they stay under
// commandBudget and keep its raw-error shape.
var work = gitvcs.Work{Run: func(dir, stdin string, args ...string) (string, error) {
	out, err := runOutput("git", append([]string{"-C", dir}, args...)...)
	return string(out), err
}}

// workspaces is this package's git Workspaces backend. Its RunSplit is runSplit,
// so the `git status` behind WorktreeDirty stays under commandBudget, read at
// call time, exactly as it was before it moved (mg-afad2).
var workspaces = gitvcs.Workspaces{RunSplit: runSplit}
