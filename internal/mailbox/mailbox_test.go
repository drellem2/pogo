package mailbox

import "testing"

// TestCanonicalMatchesWhatMGResolves pins the one rule that makes two spellings
// the same inbox. Verified against the live `mg` binary: `mg mail list mg-aa96`
// and `mg mail list aa96` both read the box `aa96`, and `mg mail list p4f8c` on
// a never-used name reports "No mailbox for p4f8c yet" (2026-08-05, re-checked
// 2026-08-07).
//
// The stakes are asymmetric and that is why this is pinned rather than assumed.
// If Canonical ever became stricter — stopped stripping `mg-`, say — two names
// for one box would start comparing as different, the registration guard would
// refuse correct schedules, and the stranded-mail sweep would report healthy
// polecats. If it became looser, genuinely different boxes would collapse and
// the guard would wave through the mismatch it exists to catch.
func TestCanonicalMatchesWhatMGResolves(t *testing.T) {
	tests := []struct{ in, want string }{
		{"aa96", "aa96"},
		{"mg-aa96", "aa96"},    // mg strips the prefix
		{"MG-AA96", "aa96"},    // and a case-only difference is a typo, not a box
		{"  aa96  ", "aa96"},   // prose whitespace
		{"waa96", "waa96"},     // NOT the same box as aa96 — this is the 2026-08-05 defect
		{"mg-mg-x", "mg-x"},    // one prefix stripped, not all
		{"pm-pogo", "pm-pogo"}, // a crew name is not an mg- form
		{"", ""},
	}
	for _, tc := range tests {
		if got := Canonical(tc.in); got != tc.want {
			t.Errorf("Canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestReadsIsTheOnePredicate covers the shared question — "does this schedule
// open that box?" — that the registration guard (internal/scheduler) and the
// stranded-mail sweep (internal/strandedmail) both ask. They must answer it
// identically; two answers that could drift by one `mg-` prefix is how this
// whole class of bug starts, which is why the function lives here rather than
// being reimplemented on each side.
func TestReadsIsTheOnePredicate(t *testing.T) {
	const both = "Check your mail with BOTH `mg mail list p4f8c` AND `mg mail list mg-4f8c` and handle it."

	for _, name := range []string{"p4f8c", "mg-4f8c", "4f8c", "P4F8C"} {
		if !Reads(both, name) {
			t.Errorf("Reads(bothBoxes, %q) = false; that box IS opened by this message", name)
		}
	}
	for _, name := range []string{"v9ecf", "9ecf", "mayor", ""} {
		if Reads(both, name) {
			t.Errorf("Reads(bothBoxes, %q) = true; this message never opens that box", name)
		}
	}

	// A message naming no mailbox opens nothing. Callers treat that as "no
	// instruction to disagree with" rather than as a match, so it must not
	// report true for an arbitrary name.
	if Reads("Check your mail and handle any unread messages.", "p4f8c") {
		t.Error("Reads reported a match against a message that names no mailbox at all")
	}
}
