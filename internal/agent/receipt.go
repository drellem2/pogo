package agent

// Submission receipts.
//
// Writing to a PTY master succeeds whether or not anything is listening, so a
// nudge that returns nil says only "the bytes left pogod". The receipt is the
// other half: one record appended by the harness itself every time it SUBMITS
// a prompt. For Claude Code that is a UserPromptSubmit hook (see
// internal/claude/receipthook.go); for the fakes in the tests it is the input
// loop appending the line it just read. Either way the contract is the same —
// the file grows by one record per prompt the agent actually received, and
// nobody but the harness can make it grow.
//
// The count is the only thing read, so the record format is deliberately
// uninteresting: one RFC3339Nano timestamp per line. Records are appended with
// O_APPEND and are far below PIPE_BUF, so concurrent hook processes interleave
// whole lines rather than shredding each other.

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ReceiptDir returns the directory holding per-agent submission receipts:
// $POGO_HOME/agents/receipts. Derived from PromptDir for the same reason it
// is — an isolated daemon must not read or write the real user's state
// (mg-3dc3).
func ReceiptDir() string {
	return filepath.Join(PromptDir(), "receipts")
}

// SubmitReceiptPath returns the receipt file for the named agent.
func SubmitReceiptPath(name string) string {
	return filepath.Join(ReceiptDir(), name+".submits")
}

// RecordSubmit appends one submission record to path. Called from the harness
// hook process (`pogo hook prompt-submit`), never from pogod.
func RecordSubmit(path string) error {
	if path == "" {
		return errors.New("receipt path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create receipt dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open receipt file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(time.Now().Format(time.RFC3339Nano) + "\n"); err != nil {
		return fmt.Errorf("append receipt: %w", err)
	}
	return nil
}

// CountSubmits returns how many prompts the agent has submitted since its
// receipt file was reset at spawn.
//
// A missing file is (0, nil), NOT an error: ResetReceipt removes the file at
// spawn precisely so a fresh agent starts from zero without pogod having to
// write a file the harness owns. "Is there a signal at all" is a separate
// question, answered by Agent.receiptFile being non-empty — pogod sets that
// only when it has installed a hook it could resolve. Conflating the two would
// read a broken hook as a dropped message and escalate against an agent that
// received the nudge perfectly well.
func CountSubmits(path string) (int, error) {
	if path == "" {
		return 0, errors.New("receipt path is empty")
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) > 0 {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// ResetReceipt clears any receipt left by a previous run of an agent with this
// name, so the count a nudge compares against belongs to the live process. A
// stale file would otherwise make the first nudge of a new agent compare
// against a number that can never move again.
func ResetReceipt(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
