package memcheck

import "testing"

// TestWorkItemIDAcceptsFourAndFiveHex is the positive control for mg-2f62:
// macguffin widened ids from 4 to 5 hex characters (drellem2/macguffin#33) and
// both widths coexist, so an index line citing a 5-char id must be checked, not
// silently skipped.
func TestWorkItemIDAcceptsFourAndFiveHex(t *testing.T) {
	for _, id := range []string{"mg-2f62", "mg-2f62a"} {
		if got := workItemID.FindString("OPEN: " + id + " is in flight"); got != id {
			t.Errorf("workItemID on %q = %q, want %q", id, got, id)
		}
	}
	if got := workItemID.FindString("mg-" + idShapeSHA); got != "" {
		t.Errorf("workItemID matched %q inside a sha", got)
	}
}

// A 40-char sha written after `mg-` is not an id: the patterns are bounded at 5,
// never `{4,}`, and word-bounded so a longer hex run is not read as a prefix.
const idShapeSHA = "0123456789abcdef0123456789abcdef01234567"
