package service

import (
	"reflect"
	"testing"
)

// TestMissingWorkItemIDsAcceptsFourAndFiveHex is the positive control for
// mg-2f62: macguffin widened ids from 4 to 5 hex characters
// (drellem2/macguffin#33). A 5-char id must be read WHOLE — read as its 4-char
// prefix it names a different item, and an installed copy carrying mg-2f62
// would wrongly make a missing mg-2f62a look present.
func TestMissingWorkItemIDsAcceptsFourAndFiveHex(t *testing.T) {
	source := []byte("# mg-2f62 fix\n# mg-2f62a fix\n# mg-" + idShapeSHA + "\n")
	installed := []byte("# mg-2f62 fix\n")
	if got := missingWorkItemIDs(source, installed); !reflect.DeepEqual(got, []string{"mg-2f62a"}) {
		t.Errorf("missingWorkItemIDs = %v, want [mg-2f62a]", got)
	}
	for _, id := range []string{"mg-2f62", "mg-2f62a"} {
		if got := workItemID.FindString("(" + id + ")"); got != id {
			t.Errorf("workItemID on %q = %q, want %q", id, got, id)
		}
	}
}

// A 40-char sha written after `mg-` is not an id: the patterns are bounded at 5,
// never `{4,}`, and word-bounded so a longer hex run is not read as a prefix.
const idShapeSHA = "0123456789abcdef0123456789abcdef01234567"
