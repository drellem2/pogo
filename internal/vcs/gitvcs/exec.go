package gitvcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// This file is internal/gitgc's bounded subprocess runner, moved here without
// change (slice 3, mg-d72cb). gitgc keeps its own commandBudget var and
// thin wrappers that pass it in, so the budget's rationale (mg-1530,
// drellem2/pogo#158) and the test that shrinks it stay where they were.

// ErrCommandBudget marks a command KILLED for exceeding its budget, as opposed
// to one that ran and failed. gitgc re-exports it as gitgc.ErrCommandBudget.
//
// The distinction is the whole point of the error existing. "git status failed"
// and "git status was still running after 90 seconds" send a reader to
// different places — a damaged repository versus a wedged filesystem or a lock
// — and an operator who reads the first when the second happened will go and
// fsck a repository that is fine.
var ErrCommandBudget = errors.New("still running after the per-command budget")

// RunSplit runs an external command under budget and returns its stdout and
// stderr separately.
//
// A command killed by the budget comes back wrapped in ErrCommandBudget; every
// other failure comes back as the RAW error from os/exec, untouched, because
// callers read exit codes off it (`symbolic-ref` exits 1 for a detached head,
// `merge-base --is-ancestor` exits 1 for "not an ancestor") and a wrapped
// *exec.ExitError would defeat the type assertions that do that. The budget
// error is safe to wrap precisely because a killed process never carries a
// meaningful exit code — it carries "signal: killed", which no caller reads.
func RunSplit(budget time.Duration, name string, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	rerr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return so.Bytes(), se.Bytes(), budgetError(budget, name, args)
	}
	return so.Bytes(), se.Bytes(), rerr
}

// RunOutput is RunSplit for callers that read stdout only — the shape
// exec.Cmd.Output() had at gitgc's call sites before the bound existed.
func RunOutput(budget time.Duration, name string, args ...string) ([]byte, error) {
	out, _, err := RunSplit(budget, name, args...)
	if err != nil {
		return out, err
	}
	return out, nil
}

// RunCombined is RunSplit for callers that want stdout and stderr together the
// way exec.Cmd.CombinedOutput() gives them.
//
// The two buffers are concatenated rather than shared, so the ORDER within each
// stream is git's and the order between them is not. Every caller uses the
// result as an error message or reads a single line of stdout, and none
// depends on the interleaving; the alternative — one shared buffer — would
// reintroduce the defect gitgc.WorktreeDirty was fixed for (a warning on stderr
// read as a porcelain line), one function over.
func RunCombined(budget time.Duration, name string, args ...string) ([]byte, error) {
	so, se, err := RunSplit(budget, name, args...)
	out := so
	if len(se) > 0 {
		out = append(append([]byte{}, so...), se...)
	}
	return out, err
}

func budgetError(budget time.Duration, name string, args []string) error {
	invocation := name
	if len(args) > 0 {
		invocation += " " + strings.Join(args, " ")
	}
	return fmt.Errorf("%s: %w of %s (killed)", invocation, ErrCommandBudget, budget)
}
