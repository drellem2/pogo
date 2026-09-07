package refusalstreak

import "strings"

// This file is the enumerated half of the detector: the known refusal/failure
// strings mg-6616 classified, and the reasons they map to.
//
// # The list is the cheap part, and it is NOT the predicate
//
// mg-6f3d proposed alarming on "N consecutive assistant turns matching a known
// refusal/failure string". Measured against the corpus that proposal was drawn
// from — 114 crew transcript files, 76,541 assistant turns, read 2026-09-07 —
// the string is the wrong predicate on its own, in both directions:
//
//   - 21 turns MATCH a failure string and are real, token-spending model turns:
//     an agent WRITING ABOUT the outage (this file's author included). A
//     string-only detector calls those failures. See VerdictAmbiguous.
//   - 0 structurally-synthetic turns matched NO string, so on that corpus the
//     list below is complete — which is exactly the property that cannot be
//     relied on, because it was complete about the failures that had already
//     happened.
//
// So the PREDICATE is structural (see turn.synthetic) and the STRING only names
// which mode it was. A mode nobody has enumerated yet is still a failure, and
// lands on ReasonUnrecognised rather than on health. That is mg-6616's own
// finding applied here: its classifier had an implicit `work` default and scored
// 386 failures as healthy turns, and the fix was not a better string list, it was
// removing the default bucket when the default is the healthy state.

// Reason names which failure mode a turn is. It is descriptive: nothing about
// the detection depends on it, so an unlisted mode degrades the NAME and never
// the verdict.
type Reason string

const (
	// ReasonUnrecognised is a structurally-synthetic failure turn whose text
	// matches nothing below. It is a FAILURE, loudly — the whole point of
	// separating the predicate from the list is that this bucket exists and is
	// not called healthy.
	ReasonUnrecognised Reason = "unrecognised"
	ReasonTimeout      Reason = "timeout"
	ReasonEntitlement  Reason = "entitlement"
	ReasonSpendLimit   Reason = "spend_limit"
	ReasonLoginPrompt  Reason = "login"
	ReasonAPIError     Reason = "api_error"
)

// Human returns a one-line description for the alarm a person reads.
func (r Reason) Human() string {
	switch r {
	case ReasonTimeout:
		return "every turn is timing out before the model answers"
	case ReasonEntitlement:
		return "the account's Claude Code subscription access is disabled — an ADMIN setting, which /login does not fix"
	case ReasonSpendLimit:
		return "the account's monthly spend limit is reached — a human must raise it"
	case ReasonLoginPrompt:
		return "the harness credential is rejected and it is asking for an interactive /login"
	case ReasonAPIError:
		return "the harness cannot reach the API (DNS, TLS, or a dropped connection)"
	default:
		return "the harness is failing every turn for a reason nothing here has a name for — READ THE DETAIL"
	}
}

// Marker is one enumerated failure string.
type Marker struct {
	// Reason is the mode this string names.
	Reason Reason
	// Text is the substring, verbatim from the transcripts.
	Text string
	// Seen is how many assistant turns carried it in the corpus this table was
	// read out of (crew transcripts, ~/.claude/projects/-Users-daniel--pogo-agents-*,
	// measured 2026-09-07). It is recorded so that a later reader can tell an
	// entry that was earning its place from one added on a hunch.
	Seen int
}

// DefaultMarkers is the table, ordered most specific first: `API Error:` is a
// PREFIX several of the others also carry, so it must be tried last or it will
// claim the login-403 line ("Please run /login · API Error: 403 …") and rename
// an auth outage as a network one.
//
// Counts are from the 12,230 structurally-synthetic assistant turns in that
// corpus. Adding a mode is one struct literal.
var DefaultMarkers = []Marker{
	{Reason: ReasonEntitlement, Text: "Your organization has disabled Claude subscription access", Seen: 2755},
	{Reason: ReasonSpendLimit, Text: "hit your monthly spend limit", Seen: 713},
	{Reason: ReasonLoginPrompt, Text: "Please run /login", Seen: 374},
	{Reason: ReasonTimeout, Text: "Request timed out", Seen: 7678},
	{Reason: ReasonAPIError, Text: "API Error:", Seen: 710},
}

// match returns the reason for the first marker present in text, and whether
// any matched at all. The bool is separate from the Reason because "no marker
// matched" and "matched the unrecognised marker" are different facts and a
// caller that could not tell them apart would be back to a default bucket.
func match(text string, markers []Marker) (Reason, bool) {
	if markers == nil {
		markers = DefaultMarkers
	}
	for _, m := range markers {
		if m.Text != "" && strings.Contains(text, m.Text) {
			return m.Reason, true
		}
	}
	return ReasonUnrecognised, false
}
