package wedgewatch

import (
	"bytes"

	"github.com/drellem2/pogo/internal/agent"
)

// This file is mg-fc8d item (1): the PTY-content check for KNOWN dead-end
// states. It is the cheap, direct half of the detector and it is permanently
// incomplete — every entry below was added after an incident, and the next
// incident will be a prompt that is not here. That is not an argument against
// it; it is the argument for counter.go, which needs no enumeration.
//
// # Matching is whitespace-insensitive, and that is not cosmetic
//
// Claude Code renders modal footers as TUI columns placed with cursor-forward
// escapes (ESC[<n>C), NOT with literal spaces. agent.StripANSI deletes those
// escapes outright rather than substituting a space, so an on-screen
// "1:Bad 2:Fine 3:Good 0:Dismiss" reaches a scan buffer as the run-together
// "1:Bad2:Fine3:Good0:Dismiss". mg-f36b is the ticket for what that costs: the
// rating-dialog watcher logged ZERO dismissals between its 2026-05-19 merge and
// the 2026-07-13 wedge, because a literal compare against the spaced string
// never matched in production. It was not broken; it was invisible.
//
// So both the buffer and the marker have ASCII whitespace removed before
// comparison, identically to internal/claude's matchNormalize. The spaces in
// the constants below are for the reader.

// Marker is one enumerated dead-end state.
type Marker struct {
	// Sig is the signature this marker raises.
	Sig Signature
	// Text is the on-screen phrase, written with its natural spacing.
	Text string
	// Why records what this string means, for the report and for the next
	// person deciding whether to keep it.
	Why string
}

// The dead-end phrases, verbatim from the 2026-08-04 and 2026-08-05 terminals
// where they were read by hand.
const (
	// LoginPromptText is what every wedged terminal showed under the 401s. It
	// is the most specific auth marker there is: nothing but the harness's own
	// re-auth prompt prints it.
	LoginPromptText = "Please run /login"

	// API401Text is the error class, deliberately truncated before the
	// explanation. The two observed full lines were
	//
	//	API Error: 401 OAuth access token has been revoked.
	//	API Error: 401 OAuth access token has expired. Re-authenticate to continue.
	//
	// and matching the shared prefix catches both plus any third wording. Note
	// what the detector must NOT do with a hit: the first of those lines says
	// "revoked" and on 2026-08-04 NOTHING WAS REVOKED. The string is evidence
	// of a symptom; classify.go decides what it means.
	API401Text = "API Error: 401"

	// ConnUnableText is the connect-failure line as the harness prints it.
	ConnUnableText = "Unable to connect to API"

	// ENOTFOUNDText is Node's DNS-resolution failure code, which is what the
	// 2026-08-04 outage actually left in the logs (github.com:22 unreachable,
	// ~20:20-20:38Z). It is matched on its own as well as inside the phrase
	// above, because the surrounding wording varies with which client failed
	// and the code does not.
	ENOTFOUNDText = "ENOTFOUND"

	// EAIAGAINText is the transient-DNS sibling of ENOTFOUND. Same class, same
	// response; included because an outage that produces one commonly produces
	// the other and a detector split across two codes would report half of it.
	EAIAGAINText = "EAI_AGAIN"

	// RatingDialogText is Claude Code's mid-session session-rating prompt.
	// mg-4421's watcher dismisses this; a hit here means the dismissal did not
	// win, which is worth knowing precisely because mg-f36b is a ticket about
	// that watcher being silently unable to match for two months.
	//
	// Pinned byte-identical to claude.RatingDialogMarker by
	// TestModalMarkersMatchTheModalWatcher.
	RatingDialogText = "1:Bad 2:Fine 3:Good 0:Dismiss"

	// RateLimitText is the first menu option of the API rate-limit-options
	// modal. Pinned byte-identical to claude.RateLimitMarker by the same test.
	RateLimitText = "Stop and wait for limit to reset"
)

// DefaultMarkers is the enumerated table. Adding a dead-end state is one struct
// literal and nothing else.
var DefaultMarkers = []Marker{
	{
		Sig:  SigLoginPrompt,
		Text: LoginPromptText,
		Why:  "the harness is asking for an interactive re-auth; the session cannot proceed on its own",
	},
	{
		Sig:  SigAPI401,
		Text: API401Text,
		Why:  "an API call was rejected as unauthenticated — WHICH IS NOT THE SAME AS a revoked credential",
	},
	{
		Sig:  SigConnectivity,
		Text: ConnUnableText,
		Why:  "the harness could not reach the API at all",
	},
	{
		Sig:  SigConnectivity,
		Text: ENOTFOUNDText,
		Why:  "DNS resolution failed — the network, not the credential",
	},
	{
		Sig:  SigConnectivity,
		Text: EAIAGAINText,
		Why:  "transient DNS failure — the network, not the credential",
	},
	{
		Sig:  SigRatingDialog,
		Text: RatingDialogText,
		Why:  "the session-rating dialog is up and eating input (mg-4421's watcher should have dismissed it)",
	},
	{
		Sig:  SigRateLimitModal,
		Text: RateLimitText,
		Why:  "the rate-limit-options modal is up and eating input (mg-4421's watcher should have dismissed it)",
	},
}

// MarkerHit is one enumerated state found in a buffer.
type MarkerHit struct {
	Sig  Signature
	Text string
	Why  string
}

// ScanMarkers returns every enumerated dead-end state visible in raw PTY bytes.
//
// It is pure and stateless: a hit is an OBSERVATION, never a finding. The
// watcher will not report one until the agent has also been stalled for
// MarkerHoldDown, because the strings above appear in the PTY of any agent
// merely writing about them — this package's own author included.
func ScanMarkers(raw []byte, markers []Marker) []MarkerHit {
	if len(raw) == 0 {
		return nil
	}
	if markers == nil {
		markers = DefaultMarkers
	}
	clean := Normalize(agent.StripANSI(raw))
	var hits []MarkerHit
	for _, m := range markers {
		if bytes.Contains(clean, Normalize([]byte(m.Text))) {
			hits = append(hits, MarkerHit{Sig: m.Sig, Text: m.Text, Why: m.Why})
		}
	}
	return hits
}

// Signatures reduces hits to their distinct signatures, sorted.
func Signatures(hits []MarkerHit) []Signature {
	seen := map[Signature]bool{}
	var out []Signature
	for _, h := range hits {
		if seen[h.Sig] {
			continue
		}
		seen[h.Sig] = true
		out = append(out, h.Sig)
	}
	return sortSigs(out)
}

// Normalize strips ASCII whitespace. Applied to BOTH the ANSI-stripped buffer
// and the marker before comparison; see the file header for why collapsing to
// nothing rather than to a single space is the correct choice against a TUI
// whose column spacing is cursor moves.
//
// Exported because counter.go parses the same normalized form: the elapsed
// counter is rendered in the same status line as the modal footers and arrives
// with its spaces deleted the same way, so "Baked for 3m 2s" is read as
// "Bakedfor3m2s".
func Normalize(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			continue
		}
		out = append(out, c)
	}
	return out
}
