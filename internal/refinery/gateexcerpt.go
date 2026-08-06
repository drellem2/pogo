package refinery

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// The bounds on how much of a running gate's text is retained and reported.
//
// They are constants here and are COPIED INTO EVERY SNAPSHOT, so a reader
// decides whether what they are looking at is truncated from the record in
// front of them rather than from this file. A bounded read whose bound is not
// stated manufactures an absence: it renders "the gate never said that" and
// "the gate said it outside my window" identically, which is the defect this
// whole record exists to remove, reintroduced one layer down.
//
// # Why a head AND a tail, and not just a tail
//
// "Show me the last N lines" is what everyone asks for and it would have left
// mg-9adc's motivating incident fully intact. The line that refuted a wrong
// hypothesis about a slow gate — `watchlist consistent: 17 paths` — is the
// FIRST line that gate emits. Seventy-seven minutes later it is outside any
// tail anyone would choose. Gates print what they resolved and what they are
// about to do in a header, so the header is where the answer to "why is this
// slow" usually lives; the tail is where the answer to "where is it now" lives.
// Neither substitutes for the other, so both are kept.
const (
	// excerptHeadLines is how many opening lines are kept verbatim, forever.
	// They are captured once and never evicted — that is the point of them.
	excerptHeadLines = 25
	// excerptTailLines is how many of the most recent lines are kept.
	excerptTailLines = 40
	// excerptLineBytes bounds a SINGLE line. Bounding the line count alone does
	// not bound memory: one `base64 < bigfile` in a gate is a single line of
	// arbitrary size. A line cut here is marked inline, in the line itself.
	excerptLineBytes = 500
)

// GateExcerpt is what a running gate has SAID — its own text, bounded and
// self-describing.
//
// It is deliberately a separate field from LastOutput and OutputLines rather
// than a re-typing of either. Those two answer "when did it last speak" and
// "how much has it spoken", and the second is the trap mg-9adc was filed
// against: a line COUNT is a measure of volume, and volume cannot tell a
// compute phase from a hang. A gate frozen at "140 lines, last 26m ago" and a
// gate that has printed 140 lines of a passing suite are the same reading.
//
// Every count here is a count of what the gate produced, not of what was kept.
// Lines is the true total; Elided says how many of them are missing from this
// record and why they are missing is the bound, stated alongside.
type GateExcerpt struct {
	// Head is the gate's opening lines, in order, capped at HeadLimit. Captured
	// once when the gate starts talking and never evicted.
	Head []string `json:"head,omitempty"`
	// Tail is the most recent lines, in order, capped at TailLimit and with any
	// line already present in Head removed — so a short gate's output reads as
	// one continuous transcript rather than as a doubled one.
	Tail []string `json:"tail,omitempty"`
	// HeadLimit, TailLimit and LineBytes are the bounds in force, carried so
	// staleness of THIS FILE cannot make the record lie about its own limits.
	HeadLimit int `json:"head_limit"`
	TailLimit int `json:"tail_limit"`
	LineBytes int `json:"line_bytes"`
	// Lines is how many complete lines the gate has produced in total — not how
	// many are in this record.
	Lines int `json:"lines"`
	// Elided is how many lines fell between Head and Tail and are therefore NOT
	// in this record. Zero means the record is the gate's complete output so
	// far, which is a different and much stronger statement.
	Elided int `json:"elided"`
	// TruncatedLines counts lines that were longer than LineBytes and were cut.
	// Each such line carries its own inline marker as well; this is the count
	// so a reader scanning the JSON does not have to look for them.
	TruncatedLines int `json:"truncated_lines,omitempty"`
	// Pending is the bytes the gate has written since its last newline — a line
	// in progress. It is reported separately from Tail rather than folded into
	// it because it is exactly the reading a mid-write gate produces, and
	// because the useful case is a gate that stopped there: a polecat once spent
	// three gate timeouts without ever learning its gate had halted at
	// "Building pogod into the sandbox..." with no newline after it.
	Pending string `json:"pending,omitempty"`
	// PendingDropped is bytes past LineBytes in that unterminated line.
	PendingDropped int `json:"pending_dropped_bytes,omitempty"`
}

// Complete reports whether this record holds every line the gate has produced.
func (x *GateExcerpt) Complete() bool {
	return x != nil && x.Elided == 0
}

// Spoke reports whether the gate has produced any text at all — including an
// unterminated line, which is text even though it is not yet a line.
func (x *GateExcerpt) Spoke() bool {
	return x != nil && (x.Lines > 0 || x.Pending != "")
}

// First returns the gate's opening line, which is where a gate typically states
// what it resolved and what it is about to run.
func (x *GateExcerpt) First() string {
	if x == nil {
		return ""
	}
	if len(x.Head) > 0 {
		return x.Head[0]
	}
	return x.Pending
}

// Latest returns the most recent text the gate produced, preferring an
// unterminated line because that is the newest thing it said.
func (x *GateExcerpt) Latest() string {
	if x == nil {
		return ""
	}
	if x.Pending != "" {
		return x.Pending
	}
	if n := len(x.Tail); n > 0 {
		return x.Tail[n-1]
	}
	if n := len(x.Head); n > 0 {
		return x.Head[n-1]
	}
	return ""
}

// Header is the one sentence that states what this record is and what it is
// missing. It is never omitted, in any of the three cases — complete, bounded,
// or empty — because an omitted header is what lets a bounded window read as a
// complete one.
func (x *GateExcerpt) Header() string {
	if x == nil {
		return "NOT RECORDED — this pogod did not capture the gate's text at all. " +
			"That is not the same as the gate being silent, and nothing here says which it is."
	}
	if !x.Spoke() {
		return "NOTHING YET — the gate has produced no output at all. " +
			"This is a measurement, not a bound hiding one: nothing was elided."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s so far", plural(x.Lines, "line"))
	if x.Elided > 0 {
		fmt.Fprintf(&b, ", of which %s BETWEEN the first %d and the last %d are NOT shown "+
			"(bounds: head %d, tail %d)", plural(x.Elided, "line"), len(x.Head), len(x.Tail),
			x.HeadLimit, x.TailLimit)
	} else {
		fmt.Fprintf(&b, ", all shown — nothing elided (bounds: head %d, tail %d, not reached)",
			x.HeadLimit, x.TailLimit)
	}
	if x.TruncatedLines > 0 {
		fmt.Fprintf(&b, "; %s cut at %d bytes, marked inline", plural(x.TruncatedLines, "long line"), x.LineBytes)
	}
	return b.String()
}

// Report renders the excerpt as operator-facing lines: a header stating the
// bound, then the kept text with its ORIGINAL line numbers, then the elision
// gap where it falls.
//
// The line numbers are the mechanism, not decoration. They are the gate's own
// numbering, so the jump across the gap is visible as a jump — a reader cannot
// mistake the concatenation of a head and a tail for a contiguous transcript.
func (x *GateExcerpt) Report() []string {
	out := []string{x.Header()}
	if x == nil || !x.Spoke() {
		return out
	}
	width := len(fmt.Sprintf("%d", x.Lines))
	if width < 3 {
		width = 3
	}
	for i, l := range x.Head {
		out = append(out, fmt.Sprintf("%*d | %s", width, i+1, l))
	}
	if x.Elided > 0 {
		out = append(out, fmt.Sprintf("%*s ~ %s not shown here ~", width, "", plural(x.Elided, "line")))
	}
	firstTail := x.Lines - len(x.Tail) + 1
	for i, l := range x.Tail {
		out = append(out, fmt.Sprintf("%*d | %s", width, firstTail+i, l))
	}
	if x.Pending != "" || x.PendingDropped > 0 {
		note := fmt.Sprintf("%*s > %s", width, "", x.Pending)
		if x.PendingDropped > 0 {
			note += fmt.Sprintf(" …[+%d bytes cut]", x.PendingDropped)
		}
		out = append(out, note+"   (still being written — no newline after it yet)")
	}
	return out
}

// excerptBuffer is the write side: it consumes the gate's bytes as they are
// produced and keeps a bounded window of them.
//
// It sits directly on the gate's output path, so it holds its own small mutex
// rather than the refinery's lock. Contending the refinery lock on every write
// from a chatty gate would make the whole pipeline pay for observability, and
// the observability would then be the thing slowing down what it is measuring.
type excerptBuffer struct {
	mu   sync.Mutex
	head []string

	// tail is a ring of the most recent lines. A ring rather than a re-sliced
	// slice so a gate emitting a million lines does not grow a backing array a
	// million entries long behind a window of forty.
	tail     []string
	tailNext int
	tailFull bool

	// partial is the bytes written since the last newline, capped at lineBytes;
	// partialDropped counts what was thrown away past that cap.
	partial        []byte
	partialDropped int

	total     int
	truncated int

	headLimit, tailLimit, lineBytes int
}

func newExcerptBuffer() *excerptBuffer {
	return &excerptBuffer{
		head:      make([]string, 0, excerptHeadLines),
		tail:      make([]string, excerptTailLines),
		headLimit: excerptHeadLines,
		tailLimit: excerptTailLines,
		lineBytes: excerptLineBytes,
	}
}

// write consumes one chunk of gate output. It is called from the exec output
// path on every write the gate makes, so it does no I/O and no allocation
// beyond the line it is keeping.
func (e *excerptBuffer) write(p []byte) {
	if e == nil || len(p) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			e.appendPartial(p)
			return
		}
		e.appendPartial(p[:i])
		e.commitLine()
		p = p[i+1:]
	}
}

// appendPartial accumulates bytes of the line currently being written, bounded.
// Past the bound the bytes are counted and discarded: keeping the HEAD of an
// over-long line rather than its tail is deliberate, because a line's meaning
// (a test name, a phase banner, a path) is at its start.
func (e *excerptBuffer) appendPartial(b []byte) {
	room := e.lineBytes - len(e.partial)
	if room <= 0 {
		e.partialDropped += len(b)
		return
	}
	if len(b) > room {
		e.partial = append(e.partial, b[:room]...)
		e.partialDropped += len(b) - room
		return
	}
	e.partial = append(e.partial, b...)
}

// commitLine turns the accumulated bytes into a kept line.
func (e *excerptBuffer) commitLine() {
	line := sanitizeExcerptLine(string(e.partial))
	if e.partialDropped > 0 {
		// Marked in the line itself, not only in a counter. The reader who needs
		// to know a line was cut is the reader looking AT that line.
		line += fmt.Sprintf(" …[+%d bytes cut at the %d-byte line bound]", e.partialDropped, e.lineBytes)
		e.truncated++
	}
	e.partial = e.partial[:0]
	e.partialDropped = 0

	e.total++
	if len(e.head) < e.headLimit {
		e.head = append(e.head, line)
	}
	e.tail[e.tailNext] = line
	e.tailNext++
	if e.tailNext == len(e.tail) {
		e.tailNext = 0
		e.tailFull = true
	}
}

// tailLines returns the ring in emission order.
func (e *excerptBuffer) tailLines() []string {
	if !e.tailFull {
		return e.tail[:e.tailNext]
	}
	out := make([]string, 0, len(e.tail))
	out = append(out, e.tail[e.tailNext:]...)
	return append(out, e.tail[:e.tailNext]...)
}

// snapshot returns a detached copy for the persisted record.
//
// It returns a non-nil record even for a gate that has said nothing. An absent
// record and a record saying "zero lines" are different claims — the first
// means this pogod does not capture gate text, the second means the gate has
// not spoken — and collapsing them is the shape of every defect in this file's
// history.
func (e *excerptBuffer) snapshot() *GateExcerpt {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	x := &GateExcerpt{
		HeadLimit:      e.headLimit,
		TailLimit:      e.tailLimit,
		LineBytes:      e.lineBytes,
		Lines:          e.total,
		TruncatedLines: e.truncated,
	}
	if len(e.head) > 0 {
		x.Head = append([]string(nil), e.head...)
	}
	// Drop from the tail anything the head already carries, so a gate whose
	// whole output fits inside both windows is not reported twice.
	tail := e.tailLines()
	if first := e.total - len(tail); first < e.headLimit {
		if drop := e.headLimit - first; drop >= len(tail) {
			tail = nil
		} else {
			tail = tail[drop:]
		}
	}
	if len(tail) > 0 {
		x.Tail = append([]string(nil), tail...)
	}
	// Derived from what was actually kept rather than from the limits, so a
	// change to either window cannot leave this number describing the old one.
	if x.Elided = e.total - len(x.Head) - len(x.Tail); x.Elided < 0 {
		x.Elided = 0
	}
	if len(e.partial) > 0 || e.partialDropped > 0 {
		x.Pending = sanitizeExcerptLine(string(e.partial))
		x.PendingDropped = e.partialDropped
	}
	return x
}

// ellipsizeLine shortens one captured line for a summary view that has room
// for a single line. The ellipsis is the whole point: a shortened line must be
// visibly shortened, or a reader will quote it as the gate's complete words.
func ellipsizeLine(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// sanitizeExcerptLine makes one captured line safe to print and to marshal.
//
// Carriage returns are removed: a gate drawing a progress bar writes them, and
// left in place they return the terminal to column zero and overwrite the
// excerpt with itself — a rendering that hides the very text this exists to
// show. Invalid UTF-8 is repaired because the line bound can cut a multi-byte
// rune in half, and because a gate that prints a binary blob should not be able
// to produce a record that renders as mojibake.
func sanitizeExcerptLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
