package prtracking

import (
	"reflect"
	"testing"
)

// TestIDsAcceptsFourAndFiveHex is the positive control for mg-2f62: macguffin
// widened ids from 4 to 5 hex characters (drellem2/macguffin#33) and both
// widths coexist, so a pattern that silently drops the 5-char width reports
// "no ids" about a PR that names one.
func TestIDsAcceptsFourAndFiveHex(t *testing.T) {
	if got := IDs("fixes mg-2f62 and mg-2f62a"); !reflect.DeepEqual(got, []string{"mg-2f62", "mg-2f62a"}) {
		t.Errorf("IDs = %v, want [mg-2f62 mg-2f62a]", got)
	}
	if got := IDs("mg-" + idShapeSHA); got != nil {
		t.Errorf("IDs(mg-<sha>) = %v, want none", got)
	}
}

// A 40-char sha written after `mg-` is not an id: the patterns are bounded at 5,
// never `{4,}`, and word-bounded so a longer hex run is not read as a prefix.
const idShapeSHA = "0123456789abcdef0123456789abcdef01234567"
