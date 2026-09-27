package verdictwatch

import (
	"slices"
	"testing"
)

// TestIDPatternsAcceptFourAndFiveHex is the positive control for mg-2f62:
// macguffin widened ids from 4 to 5 hex characters (drellem2/macguffin#33) and
// both widths coexist. idSuffix is anchored with ^$, so before this a 5-char id
// yielded NO shape names at all — the silent failure, not a loud one.
func TestIDPatternsAcceptFourAndFiveHex(t *testing.T) {
	for _, id := range []string{"mg-2f62", "mg-2f62a"} {
		if got := probeItemID.FindString("Created " + id + ": title"); got != id {
			t.Errorf("probeItemID on %q = %q, want %q", id, got, id)
		}
		bare := id[len("mg-"):]
		names := shapeNames(id)
		for _, want := range []string{bare, "cat-" + bare, "p" + bare} {
			if !slices.Contains(names, want) {
				t.Errorf("shapeNames(%q) lacks %q", id, want)
			}
		}
	}
	if got := probeItemID.FindString("mg-" + idShapeSHA); got != "" {
		t.Errorf("probeItemID matched %q inside a sha", got)
	}
	if got := shapeNames("mg-" + idShapeSHA); got != nil {
		t.Errorf("shapeNames(mg-<sha>) = %v, want none", got)
	}
}

// A 40-char sha written after `mg-` is not an id: the patterns are bounded at 5,
// never `{4,}`, and word-bounded so a longer hex run is not read as a prefix.
const idShapeSHA = "0123456789abcdef0123456789abcdef01234567"
