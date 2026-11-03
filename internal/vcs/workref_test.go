package vcs

import "testing"

// mg-4377f: an enth submission has no branch, so every "branch %s" message
// printed "branch " with nothing after it. WorkRef/WorkField/WorkKV name the
// item instead; the git rows are the control, and each must be exactly what
// the old format string produced.
func TestWorkRefNamesTheEnthItemAndLeavesGitAlone(t *testing.T) {
	for _, c := range []struct {
		branch, item   string
		ref, field, kv string
	}{
		// git: byte-identical to "branch %s", "%s", "branch=%s" — with or
		// without an item beside the branch.
		{"polecat-g1", "", "branch polecat-g1", "polecat-g1", "branch=polecat-g1"},
		{"polecat-g1", "mg-g1", "branch polecat-g1", "polecat-g1", "branch=polecat-g1"},
		// Neither: nothing to name, so the old rendering stands.
		{"", "", "branch ", "", "branch="},
		// enth: no branch, the item names the work.
		{"", "mg-e1", "enth item mg-e1", "(none: enth item mg-e1)", "item=mg-e1"},
		{"", " mg-e1 ", "enth item mg-e1", "(none: enth item mg-e1)", "item=mg-e1"},
	} {
		if got := WorkRef(c.branch, c.item); got != c.ref {
			t.Errorf("WorkRef(%q,%q) = %q, want %q", c.branch, c.item, got, c.ref)
		}
		if got := WorkField(c.branch, c.item); got != c.field {
			t.Errorf("WorkField(%q,%q) = %q, want %q", c.branch, c.item, got, c.field)
		}
		if got := WorkKV(c.branch, c.item); got != c.kv {
			t.Errorf("WorkKV(%q,%q) = %q, want %q", c.branch, c.item, got, c.kv)
		}
	}
}
