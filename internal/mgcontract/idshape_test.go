package mgcontract

import "testing"

// TestMgIDAcceptsFourAndFiveHex is the positive control for mg-2f62: the probe
// reads the id `mg new` printed, and macguffin widened ids from 4 to 5 hex
// characters (drellem2/macguffin#33).
func TestMgIDAcceptsFourAndFiveHex(t *testing.T) {
	for _, id := range []string{"mg-2f62", "mg-2f62a"} {
		if got := mgID.FindString("Created " + id + ": title"); got != id {
			t.Errorf("mgID on %q = %q, want %q", id, got, id)
		}
	}
	if got := mgID.FindString("mg-" + idShapeSHA); got != "" {
		t.Errorf("mgID matched %q inside a sha", got)
	}
}

// A 40-char sha written after `mg-` is not an id: the patterns are bounded at 5,
// never `{4,}`, and word-bounded so a longer hex run is not read as a prefix.
const idShapeSHA = "0123456789abcdef0123456789abcdef01234567"
