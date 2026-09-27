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
// Each record is one line: an RFC3339Nano timestamp, optionally followed by a
// tab and a JSON excerpt of the submitted prompt (see "Receipt content"
// below). A record is appended in a single write with O_APPEND, which places
// the whole write at end of file, so concurrent hook processes interleave
// whole lines rather than shredding each other.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	return appendReceiptLine(path, time.Now().Format(time.RFC3339Nano)+"\n")
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

// Receipt content (mg-8a70).
//
// A count proves that A prompt was submitted, not that it was THIS prompt, or
// all of it. On Claude Code 2.1.283 a nudge longer than the tty's input queue
// (1022 bytes on darwin) arrives in two reads; the harness turns the first into
// a paste placeholder and then drops it at submit, so the agent receives only
// the last ~120 characters — and the receipt still moved by one. pogod logged
// 128 such fires as nudge_sent mode=confirm. So the hook now also records an
// excerpt of what was actually submitted, and the confirm path compares it
// with what was sent (see judgeDelivery).
//
// The record stays one line: "<RFC3339Nano>\t<json>". CountSubmits still counts
// lines, and a line without the tab — a hook binary older than this, or a test
// fake — parses as a record whose content is unknown, which is never judged.

// receiptExcerptRunes bounds each excerpt. It must exceed the fingerprint
// (deliveryFingerprintRunes) by at least the length of any wrapper the harness
// puts around a prompt — Claude Code wraps a pasted prompt in a
// `<pasted_content id="…">` tag of ~30 characters — so the sent head can still be
// found inside the received head.
const receiptExcerptRunes = 100

// SubmitRecord is one parsed receipt line.
type SubmitRecord struct {
	// HasContent is false for a record that carries no excerpt: a legacy
	// timestamp-only line, or a hook that could not read the prompt.
	HasContent bool `json:"-"`
	// Len is the submitted prompt's length in runes.
	Len int `json:"len"`
	// Head and Tail are the first and last receiptExcerptRunes runes of it.
	Head string `json:"head"`
	Tail string `json:"tail"`
}

// newSubmitRecord builds the excerpt record for a submitted prompt.
func newSubmitRecord(prompt string) SubmitRecord {
	r := []rune(prompt)
	head, tail := r, r
	if len(r) > receiptExcerptRunes {
		head = r[:receiptExcerptRunes]
		tail = r[len(r)-receiptExcerptRunes:]
	}
	return SubmitRecord{HasContent: true, Len: len(r), Head: string(head), Tail: string(tail)}
}

// RecordSubmitPrompt appends one submission record carrying an excerpt of the
// prompt the harness submitted. Called from the harness hook process.
func RecordSubmitPrompt(path, prompt string) error {
	if path == "" {
		return errors.New("receipt path is empty")
	}
	data, err := json.Marshal(newSubmitRecord(prompt))
	if err != nil {
		return fmt.Errorf("encode receipt: %w", err)
	}
	return appendReceiptLine(path, time.Now().Format(time.RFC3339Nano)+"\t"+string(data)+"\n")
}

func appendReceiptLine(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create receipt dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open receipt file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("append receipt: %w", err)
	}
	return nil
}

// parseSubmitRecord parses one non-empty receipt line.
func parseSubmitRecord(line string) SubmitRecord {
	_, js, ok := strings.Cut(line, "\t")
	if !ok {
		return SubmitRecord{}
	}
	var rec SubmitRecord
	if err := json.Unmarshal([]byte(js), &rec); err != nil {
		return SubmitRecord{}
	}
	rec.HasContent = true
	return rec
}

// ReadSubmits returns every record in the receipt file, in submission order.
// Its length equals CountSubmits; a missing file is (nil, nil).
func ReadSubmits(path string) ([]SubmitRecord, error) {
	if path == "" {
		return nil, errors.New("receipt path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []SubmitRecord
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		out = append(out, parseSubmitRecord(line))
	}
	return out, nil
}
