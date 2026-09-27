package gitgc

import (
	"slices"
	"testing"
)

// TestCandidateIDsAcceptFourAndFiveHex is the positive control for mg-2f62:
// macguffin widened ids from 4 to 5 hex characters (drellem2/macguffin#33) and
// both widths coexist, so the glued-name recovery must spell both.
//
// The hex-letter generation prefix is the case that matters: the old
// unbounded `[0-9a-f]{4}` read `a2198` — letter `a` + mg-2198, a name
// pogod really hands out — as mg-a219, a different item.
func TestCandidateIDsAcceptFourAndFiveHex(t *testing.T) {
	cases := []struct {
		name string
		want []string // must all be candidates
		not  []string // must not be
	}{
		{"p2f62", []string{"mg-2f62"}, nil},
		{"p2f62a", []string{"mg-2f62a"}, []string{"mg-2f62"}},             // 5-char id, non-hex prefix
		{"a2198", []string{"mg-a2198", "mg-2198"}, []string{"mg-a219"}},   // bare 5-char OR hex letter + 4-char
		{"ab2198", []string{"mg-b2198"}, []string{"mg-ab21", "mg-ab219"}}, // hex letter + 5-char id
		{"polecat-2f62a-r", []string{"mg-2f62a"}, nil},
	}
	for _, c := range cases {
		got := candidateIDs(c.name)
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("candidateIDs(%q) = %v, lacks %q", c.name, got, w)
			}
		}
		for _, n := range c.not {
			if slices.Contains(got, n) {
				t.Errorf("candidateIDs(%q) = %v, contains %q", c.name, got, n)
			}
		}
	}
	for _, c := range []struct{ owner, item string }{{"a2198", "mg-2198"}, {"p2f62a", "mg-2f62a"}} {
		if !OwnerMatchesItem(c.owner, c.item) {
			t.Errorf("OwnerMatchesItem(%q, %q) = false, want true", c.owner, c.item)
		}
	}
	if OwnerMatchesItem("p2f62a", "mg-2f62") {
		t.Error("OwnerMatchesItem(p2f62a, mg-2f62) = true — a 5-char name was read as its 4-char prefix")
	}
	// A sha is a hex run of 40: no candidate may be sliced out of it.
	for _, c := range candidateIDs("p" + idShapeSHA) {
		if c != "p"+idShapeSHA && c != "mg-p"+idShapeSHA {
			t.Errorf("candidateIDs(p<sha>) produced %q", c)
		}
	}
}

// A 40-char sha written after `mg-` is not an id: the patterns are bounded at 5,
// never `{4,}`, and word-bounded so a longer hex run is not read as a prefix.
const idShapeSHA = "0123456789abcdef0123456789abcdef01234567"
