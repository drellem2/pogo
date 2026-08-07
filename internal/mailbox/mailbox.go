// Package mailbox holds the one answer to "which mg mailbox is this, and does
// this schedule message read it?".
//
// It is a leaf package with no pogo dependencies for a structural reason. The
// registration guard lives in internal/scheduler, the stranded-mail sweep in
// internal/strandedmail, and the text of the nudge that tells a polecat where
// to look in internal/agent — and internal/scheduler already imports
// internal/agent, so internal/agent cannot import it back. Without somewhere
// neutral to put these three functions, the agent side would have to keep its
// own copy of "same mailbox?".
//
// That copy is the bug. This whole lineage (mg-aa96, mg-4f8c) is two components
// answering "where does this agent's mail live?" differently and nothing
// noticing: the template said one box, the correspondents used another, and
// `mg mail list` reported both states with an exit code of 0. A second
// canonicalizer that drifts by one `mg-` prefix reproduces it exactly.
package mailbox

import "strings"

// Canonical reduces a mailbox name to the identity mg itself resolves it to, so
// callers compare what mg compares. mg strips a leading `mg-`: `mg mail list
// mg-aa96` and `mg mail list aa96` both read the mailbox `aa96` (verified
// against the live binary, 2026-08-05). Comparison is case-insensitive because
// a name that differs only in case is a typo, not a second mailbox.
func Canonical(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "mg-")
}

// ListInvocations extracts EVERY mailbox a schedule message tells its agent to
// read, in the order the message names them, by finding each
// `mg mail list <mailbox>` invocation in the body. An empty result means the
// message prescribes no such invocation — a message that says "check your mail"
// without naming a mailbox cannot disagree with anything.
//
// It returns a SET rather than a single name because the correct instruction is
// to read more than one box: mg mailboxes have no registration, so an agent's
// mail is in whichever box its senders typed — its agent name or its work-item
// id — and nothing settles which (mg-4f8c). A parser that returned only the
// first would make every caller blind to the second, which is the same
// one-answer-per-question assumption that caused the bug.
//
// Parsing is token-based rather than a strict regexp because these messages are
// prose: the invocation shows up bare, in backticks, quoted, or trailed by a
// comma. Flags between `list` and the mailbox (`--json`) are skipped.
func ListInvocations(message string) []string {
	fields := strings.Fields(strings.Map(func(r rune) rune {
		switch r {
		case '`', '"', '\'':
			return ' '
		}
		return r
	}, message))

	var boxes []string
	for i := 0; i+2 < len(fields); i++ {
		if fields[i] != "mg" || fields[i+1] != "mail" || fields[i+2] != "list" {
			continue
		}
		for _, tok := range fields[i+3:] {
			tok = strings.Trim(tok, ".,;:!?()[]")
			if tok == "" {
				continue
			}
			if strings.HasPrefix(tok, "-") {
				continue // a flag, e.g. --json; the mailbox is still ahead
			}
			boxes = append(boxes, tok)
			break
		}
	}
	return boxes
}

// Reads reports whether a mail-check message sends its agent to the named
// mailbox, comparing canonically.
func Reads(message, name string) bool {
	want := Canonical(name)
	for _, b := range ListInvocations(message) {
		if Canonical(b) == want {
			return true
		}
	}
	return false
}
