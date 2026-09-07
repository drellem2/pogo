package carrierdrift

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
)

// triagePromptPath is the shipped template whose step 3 tells a triage worker
// the exact acknowledgement to post.
const triagePromptPath = "templates/polecat-triage.md"

// TestAckMarkersStillMatchTheShippedPrompt is THIS PACKAGE'S OWN DEFECT AIMED AT
// ITSELF, and it is the reason this file exists as a test rather than a comment.
//
// AckMarkers is a COPY of text that lives somewhere else — the acknowledgement
// the shipped triage prompt hands a worker to post. That copy was correct when it
// was written. Nothing re-reads the prompt afterwards, so if the template is
// reworded and this list is not, the detector silently stops recognising
// acknowledgements and starts reporting every acknowledged issue as silence.
//
// A fact captured once, read forever after as current: that is the defect this
// package was built to detect, rebuilt inside the detector. A comment asking the
// next editor to remember would be the instruction that mg-039b's founding
// failure already proved insufficient — what was missing there was a DETECTOR,
// not a reminder. This is the detector.
//
// It asserts the two directions that matter:
//
//   - the prompt still contains at least one marker (the list has not gone stale
//     against a reworded template), and
//   - the marker it contains is at the START of the line the worker posts, since
//     IsAcknowledgement is prefix-anchored and a marker buried mid-sentence would
//     match here and never match a real comment.
func TestAckMarkersStillMatchTheShippedPrompt(t *testing.T) {
	raw, err := fs.ReadFile(agent.DefaultPromptsFS(), triagePromptPath)
	if err != nil {
		t.Fatalf("reading the shipped triage prompt %s: %v", triagePromptPath, err)
	}

	var matched []string
	for _, line := range strings.Split(string(raw), "\n") {
		if IsAcknowledgement(line) {
			matched = append(matched, strings.TrimSpace(line))
		}
	}
	if len(matched) == 0 {
		t.Fatalf("NO line in %s is recognised by IsAcknowledgement.\n\n"+
			"Either the prompt's acknowledgement wording changed and AckMarkers %q was not\n"+
			"updated with it, or the acknowledgement step was removed. Until one of those is\n"+
			"resolved, `pogo check-carriers` will report every acknowledged issue as silence —\n"+
			"which is this package's own defect, rebuilt inside the detector.",
			triagePromptPath, AckMarkers)
	}

	// Positive control on the instrument itself: the prompt is a large document
	// and the assertion above would also pass if IsAcknowledgement matched
	// everything. It must not.
	nonAck := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if !IsAcknowledgement(line) {
			nonAck++
		}
	}
	if nonAck == 0 {
		t.Fatal("IsAcknowledgement matched EVERY line of the prompt — the check above " +
			"proves nothing about the marker list")
	}
	t.Logf("matched %d acknowledgement line(s) out of %d in %s: %q",
		len(matched), len(matched)+nonAck, triagePromptPath, matched)
}

// TestEveryAckMarkerIsRecognisedByItsOwnPredicate guards the other end: a marker
// added to the list with stray whitespace, punctuation or casing that
// IsAcknowledgement cannot match would be a rule that exists and never fires.
func TestEveryAckMarkerIsRecognisedByItsOwnPredicate(t *testing.T) {
	if len(AckMarkers) == 0 {
		t.Fatal("AckMarkers is empty — every issue would read as unacknowledged")
	}
	for _, m := range AckMarkers {
		if !IsAcknowledgement(m) {
			t.Errorf("AckMarkers entry %q is not recognised by IsAcknowledgement", m)
		}
		if strings.TrimSpace(m) != m {
			t.Errorf("AckMarkers entry %q has surrounding whitespace; the predicate "+
				"trims the COMMENT, not the marker", m)
		}
		if strings.ToLower(m) != m {
			t.Errorf("AckMarkers entry %q is not lowercase; matching is done in lowercase "+
				"and an uppercase entry can only ever match by accident", m)
		}
	}
}
