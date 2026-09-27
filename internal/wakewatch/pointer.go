package wakewatch

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxPointerLen is the hard cap on every pointer nudge wakewatch sends, in
// BYTES (so a multi-byte subject can never smuggle a longer terminal write past
// a rune count). It is enforced by Fit, the only function that produces pointer
// text, and pinned by a test with a 400-char subject.
//
// Why a cap in code rather than a style rule: a long nudge loses its head
// (mg-8a70) — the terminal shows the tail and the reader acts on a fragment. A
// pointer carries no content of its own; it says WHERE the work is, and the
// reader goes and reads it there. That is only true if it is short enough to
// arrive whole, every time, whatever the subject line or title a sender typed.
const MaxPointerLen = 100

// maxQuoted is the design's soft budget for a subject or title inside a
// pointer (mg-5496: `"<subject≤40>"`). Fit shrinks it further when the rest of
// the line needs the room.
const maxQuoted = 40

// Trigger is one piece of unconsumed work a pointer points at.
type Trigger struct {
	// Kind is "mail" or "assign".
	Kind string
	// Event is the macguffin event type that produced it: mail.sent,
	// work.edited or work.created.
	Event string
	// MsgID and Box are set for mail; ItemID for an assignment.
	MsgID  string
	Box    string
	From   string
	ItemID string
	// Quoted is the mail subject or item title — possibly empty when it could
	// not be read, in which case the pointer simply carries no quote.
	Quoted string
}

// action is the actionable tail of a pointer: the one command the reader runs.
func (t Trigger) action() string {
	if t.Kind == "assign" {
		return "mg show " + t.ItemID
	}
	return "mg mail list " + t.Box
}

// describe is the head: what arrived.
func (t Trigger) describe() string {
	if t.Kind == "assign" {
		return "assigned: " + t.ItemID
	}
	head := "mail from " + sanitize(t.From)
	if sanitize(t.Quoted) != "" {
		head += ":"
	}
	return head
}

// Key is the recovery bookkeeping key for this trigger.
func (t Trigger) Key() string {
	if t.Kind == "assign" {
		return "item:" + t.ItemID
	}
	return "mail:" + t.MsgID
}

// BuildPointer renders one pointer for a batch of triggers to one recipient.
// The LATEST trigger is described; the ACTIONS of every distinct box or item
// in the batch come last, because the end of the line is the part that
// survives (mg-8a70). prefix is prepended to the head ("still unread: " for a
// re-nudge) and is the first thing to go when room runs out.
func BuildPointer(prefix string, batch []Trigger) string {
	if len(batch) == 0 {
		return ""
	}
	latest := batch[len(batch)-1]
	head := prefix
	if len(batch) > 1 {
		head += fmt.Sprintf("%d new, latest ", len(batch))
	}
	head += latest.describe()

	// Distinct actions, latest first so it is the one kept when they do not
	// all fit.
	var actions []string
	seen := map[string]bool{}
	for i := len(batch) - 1; i >= 0; i-- {
		a := batch[i].action()
		if !seen[a] {
			seen[a] = true
			actions = append(actions, a)
		}
	}
	return Fit(head, sanitize(latest.Quoted), actions)
}

// Fit assembles `<head> "<quoted>" — <actions joined by "; ">` and guarantees
// the result is at most MaxPointerLen bytes and valid UTF-8. Room is given up in
// this order, so the actionable words are the last thing lost:
//
//  1. trailing actions beyond the first
//  2. the quoted subject/title (shortened with "…", then dropped)
//  3. the head, cut from its right
//  4. finally — only if the first action alone exceeds the cap — the action's
//     own head, keeping its tail.
func Fit(head, quoted string, actions []string) string {
	head = sanitize(head)
	quoted = sanitize(quoted)
	if len(actions) == 0 {
		actions = []string{""}
	}
	for i := range actions {
		actions[i] = sanitize(actions[i])
	}
	if r := []rune(quoted); len(r) > maxQuoted {
		quoted = string(r[:maxQuoted-1]) + "…"
	}

	assemble := func(h, q string, acts []string) string {
		s := h
		if q != "" {
			s += ` "` + q + `"`
		}
		tail := strings.Join(acts, "; ")
		if tail != "" {
			if s != "" {
				s += " — "
			}
			s += tail
		}
		return s
	}

	for n := len(actions); n >= 1; n-- {
		if s := assemble(head, quoted, actions[:n]); len(s) <= MaxPointerLen {
			return s
		}
	}
	acts := actions[:1]
	for q := []rune(quoted); len(q) > 0; {
		q = q[:len(q)-1]
		cand := ""
		if len(q) > 0 {
			cand = string(q) + "…"
		}
		if s := assemble(head, cand, acts); len(s) <= MaxPointerLen {
			return s
		}
	}
	for h := []rune(head); len(h) > 0; {
		h = h[:len(h)-1]
		cand := ""
		if len(h) > 0 {
			cand = string(h) + "…"
		}
		if s := assemble(cand, "", acts); len(s) <= MaxPointerLen {
			return s
		}
	}
	return tailBytes(acts[0], MaxPointerLen)
}

// tailBytes returns the last at most n bytes of s, starting on a rune boundary.
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// sanitize makes a field safe to type into a terminal as part of ONE line: every
// control character (a newline would submit the prompt early; an escape would
// be interpreted) becomes a space, and runs of space collapse.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
