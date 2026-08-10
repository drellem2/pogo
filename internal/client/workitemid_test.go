package client

import "testing"

// TestLooksLikeWorkItemID pins both polarities of the guard that decides whether
// pogod's merge path may call `mg done` on an MR's author (mg-be37).
//
// BOTH POLARITIES, because each failure direction costs something different and
// a test suite that only fed it ids would pass for `func(string) bool { return
// true }`:
//
//   - a wrong NO leaves a merged item open, which is the defect this guard was
//     added to close: four items sat in available/ on 2026-08-09 with their work
//     on main, and priority-wake advertised them.
//   - a wrong YES turns every crew merge into a logged `mg done mayor` failure.
func TestLooksLikeWorkItemID(t *testing.T) {
	for _, id := range []string{
		"mg-be37", "mg-9a19", "MG-BE37", "gh-0042", "mg-b468",
		"mg-3119@2026-05", // mg's partition qualifier for a colliding short id
		"mg-abcdef123",    // long ids exist; the 4-hex form is a convention, not a limit
		" mg-56ac",        // an author string is free text and can arrive padded
	} {
		if !LooksLikeWorkItemID(id) {
			t.Errorf("LooksLikeWorkItemID(%q) = false; its merged item would never be closed", id)
		}
	}

	for _, name := range []string{
		"", "mayor", "pm-pogo", "pm-onethird", "architect", "doctor", "pa", "daniel",
		"q56ac", // an AGENT name, not its work item — this is the near miss
		"mg-",   // prefix with no id
		"mg-a",  // too short to be an id
		"polecat-mg-be37",
		"refinery",
		"mg be37", // a space is not a separator mg uses
	} {
		if LooksLikeWorkItemID(name) {
			t.Errorf("LooksLikeWorkItemID(%q) = true; pogod would run `mg done %s` on every merge "+
				"it authors, which is an error rather than a completion", name, name)
		}
	}
}
