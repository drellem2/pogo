package refinery

import (
	"fmt"
	"strings"
)

// gateOutputRecordCap bounds the gate output kept on a MergeRequest — the
// value that is persisted to refinery-state.json and read back by
// `pogo refinery show`.
//
// # Why there is a cap at all
//
// There wasn't one. `runLane` assigned the gate's complete stdout+stderr to
// mr.GateOutput and the record went into history unmodified. Measured on the
// live host on 2026-08-10: refinery-state.json was 6.3 MB, of which 5.83 MB
// (93%) was `history[].gate_output`, largest single entry 518 KB. Every save
// re-marshalled and re-fsynced all of it, and until mg-538e every one of those
// saves ran with Refinery.mu held.
//
// The arithmetic that sets the number: history retains MaxHistoryLen entries
// (default 100), so this cap is a per-file ceiling of 100 x 8 KB = 800 KB for
// the field that was measured at 5.83 MB. It is deliberately much larger than
// the event log's gateOutputCap (1 KB) — that bound exists to keep a single
// event line under the PIPE_BUF atomicity threshold, which is a constraint the
// state file does not have. This record is read by a human triaging one
// failure and should carry enough to triage it.
//
// # Why head AND tail, and why it says so
//
// Same argument as GateExcerpt (see gateexcerpt.go): the header is where "what
// did this gate resolve, and what was it about to do" lives, and the tail is
// where the failure lives. A tail-only cut loses the first, and a head-only
// cut loses the second.
//
// And the cut ANNOUNCES ITSELF, inline, with both byte counts. A bounded read
// whose bound is not stated manufactures an absence: it renders "the gate never
// printed that" and "the gate printed it outside the window" identically. That
// is the same defect the excerpt record exists to remove, and re-creating it
// here — in the repair for an oversized state file — would be the repair
// exhibiting the class of defect it repairs.
const gateOutputRecordCap = 8192

// gateOutputElisionMarker is the literal that appears in place of the removed
// middle. Tests and readers key off it; keep it greppable.
const gateOutputElisionMarker = "refinery: gate output elided"

// capGateOutput bounds s at gateOutputRecordCap bytes, keeping the head and the
// tail and replacing the middle with a self-describing marker. Input shorter
// than the cap is returned unchanged, byte for byte.
func capGateOutput(s string) string {
	return capGateOutputTo(s, gateOutputRecordCap)
}

// capGateOutputTo is capGateOutput with an explicit bound, so tests can drive
// it without generating megabytes.
func capGateOutputTo(s string, cap int) string {
	if cap <= 0 || len(s) <= cap {
		return s
	}
	marker := fmt.Sprintf("\n\n... [%s: %%d of %d bytes removed here; the head and tail below are verbatim, "+
		"the full output was in the gate's own log] ...\n\n", gateOutputElisionMarker, len(s))
	// The marker's own length depends on the elided count, which depends on the
	// marker's length. Two passes converge: render with the count computed from
	// a first-pass marker, then keep whichever budget that leaves.
	budget := cap - len(fmt.Sprintf(marker, len(s)))
	if budget < 2 {
		// The cap is too small to hold a marker plus any content. Say what
		// happened rather than returning a silently empty record.
		return fmt.Sprintf("[%s: all %d bytes removed; cap is %d bytes, too small to excerpt]\n",
			gateOutputElisionMarker, len(s), cap)
	}
	// Favour the tail: a failing gate's reason is usually the last thing it
	// said. The head still gets a third, which is where the resolved-config
	// header lives.
	head := budget / 3
	tail := budget - head
	head = utf8Floor(s, head)
	tail = len(s) - utf8Ceil(s, len(s)-tail)
	elided := len(s) - head - tail
	return s[:head] + fmt.Sprintf(marker, elided) + s[len(s)-tail:]
}

// utf8Floor steps n back to a UTF-8 boundary so a prefix cut never splits a
// rune. Mirrors the step-back in truncate.
func utf8Floor(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return n
}

// utf8Ceil steps n forward to a UTF-8 boundary, for a suffix cut.
func utf8Ceil(s string, n int) int {
	if n <= 0 {
		return 0
	}
	for n < len(s) && s[n]&0xC0 == 0x80 {
		n++
	}
	return n
}

// gateOutputWasCapped reports whether s carries the elision marker — i.e.
// whether what a reader is holding is the whole gate output or an excerpt of
// it. Exists so callers can ask instead of guessing from a length.
func gateOutputWasCapped(s string) bool {
	return strings.Contains(s, gateOutputElisionMarker)
}
