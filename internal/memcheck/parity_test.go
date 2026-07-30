package memcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMemDir stages a memory dir: an index body plus a set of notes keyed by
// basename. Returns the index path.
func writeMemDir(t *testing.T, index string, notes map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	idx := filepath.Join(dir, "MEMORY.md")
	if err := os.WriteFile(idx, []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range notes {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return idx
}

// note returns a minimal memory note with the standard frontmatter.
func note(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\nmetadata:\n  type: project\n---\n\nbody\n"
}

// unindexedNote returns a note that declares the parity opt-out.
func unindexedNote(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\nmetadata:\n  type: project\n  " + UnindexedMarker + "\n---\n\nbody\n"
}

// TestPositiveControl_ParityFiresOnUnindexedNote is the positive control the
// ticket required before either new check could be trusted: PLANT an unindexed
// note and observe the check FIRE.
//
// A check that has never been seen to fail is not evidence when it passes, and a
// parity check is especially easy to write so that it can never fire — reference
// detection that is too permissive silently matches everything. This is the
// asymmetry that makes the control non-negotiable here: 13 of the 16 real memory
// dirs are at exact parity, so a completely broken check would look correct on
// most of the corpus.
func TestPositiveControl_ParityFiresOnUnindexedNote(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [Indexed one](indexed.md) — a hook\n",
		map[string]string{
			"indexed.md":   note("indexed", "has a hook"),
			"unindexed.md": note("unindexed", "nothing points at this"),
		})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if res.InParity {
		t.Fatal("parity check reported in-parity with a planted unindexed note — the check cannot fire")
	}
	if got := res.Unreferenced; len(got) != 1 || got[0] != "unindexed.md" {
		t.Fatalf("Unreferenced = %v, want [unindexed.md]", got)
	}
	if res.Notes != 2 {
		t.Errorf("Notes = %d, want 2 (MEMORY.md must not count itself)", res.Notes)
	}
}

// TestParityNegativeControl_SilentAtParity is the other half of the control: with
// the planted note removed, the same check must go quiet. A check that fires on
// everything is as useless as one that never fires.
func TestParityNegativeControl_SilentAtParity(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [Indexed one](indexed.md) — a hook\n",
		map[string]string{"indexed.md": note("indexed", "has a hook")})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.InParity {
		t.Fatalf("in-parity dir reported defects: %v", res.Unreferenced)
	}
	if len(res.Unreferenced) != 0 {
		t.Errorf("Unreferenced = %v, want empty", res.Unreferenced)
	}
}

// TestParityRespectsUnindexedOptOut pins the requirement that made a bare count
// unshippable. Deliberate non-indexing is a CORRECT action that produces a parity
// "defect" — two of the eight real defects in the largest memory dir were exactly
// that — and without an opt-out those arrive as permanent warns until the check
// gets tuned out entirely, taking the real defects with it.
func TestParityRespectsUnindexedOptOut(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [Indexed one](indexed.md) — a hook\n",
		map[string]string{
			"indexed.md": note("indexed", "has a hook"),
			// The shape of the real case: a hook deliberately dropped because it
			// asserted an open review for an item that had since been archived.
			"deliberately_deindexed.md": unindexedNote("deliberately_deindexed", "hook dropped on purpose"),
			// The other real shape: a working scratch queue, not a recall note.
			"pending_digest_items.md": unindexedNote("pending_digest_items", "scratch queue"),
		})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.InParity {
		t.Fatalf("opted-out notes counted as defects: %v", res.Unreferenced)
	}
	if len(res.OptedOut) != 2 {
		t.Fatalf("OptedOut = %v, want both opted-out notes reported separately", res.OptedOut)
	}
	// Reported, but not as defects — a reader needs to see the opt-outs exist
	// without them inflating the defect count.
	if res.Notes != 3 {
		t.Errorf("Notes = %d, want 3", res.Notes)
	}
}

// TestOptOutOnlyHonouredInFrontmatter guards the one strictness the marker needs.
// This package's own documentation contains the marker text in prose; a note that
// merely discusses indexing must not thereby silence a real defect.
func TestOptOutOnlyHonouredInFrontmatter(t *testing.T) {
	body := "---\nname: talks_about_it\ndescription: prose\nmetadata:\n  type: project\n---\n\n" +
		"Some notes use `" + UnindexedMarker + "` to opt out of the parity check.\n"
	idx := writeMemDir(t, "# Memory index\n", map[string]string{"talks_about_it.md": body})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if res.InParity {
		t.Fatal("marker in the BODY silenced the check; it must only be honoured in frontmatter")
	}
	if len(res.OptedOut) != 0 {
		t.Errorf("OptedOut = %v, want empty", res.OptedOut)
	}
}

// TestOptOutNeedsFrontmatterBlock: a note with no frontmatter at all has no way
// to opt out, and absence of the declaration is the default.
func TestOptOutNeedsFrontmatterBlock(t *testing.T) {
	idx := writeMemDir(t, "# Memory index\n", map[string]string{
		"bare.md": UnindexedMarker + "\n\njust a bare file\n",
	})
	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if res.InParity {
		t.Fatal("marker outside any frontmatter block silenced the check")
	}
}

// TestOptOutIsGreppable pins the constraint the ticket set explicitly: the marker
// must be findable without reading every note in full. A plain line-oriented scan
// of a note's head must locate it, which is what `grep -rl` does.
func TestOptOutIsGreppable(t *testing.T) {
	body := unindexedNote("x", "y")
	var found bool
	for _, ln := range strings.Split(body, "\n") {
		if strings.TrimSpace(ln) == UnindexedMarker {
			found = true
		}
	}
	if !found {
		t.Fatalf("UnindexedMarker %q does not appear as a whole line in a note that declares it — "+
			"`grep -rl %q` would not find it", UnindexedMarker, UnindexedMarker)
	}
	if strings.ContainsAny(UnindexedMarker, "*?[]\\") {
		t.Errorf("UnindexedMarker %q contains glob/regex metacharacters; it must be greppable literally", UnindexedMarker)
	}
}

// TestOptOutScanIsBounded pins that the cost of checking a directory does not
// scale with the size of its largest note. The real case is a 127KB scratch queue
// living in a memory dir; reading it whole to discover a few bytes of intent
// would cost more than the check is worth.
func TestOptOutScanIsBounded(t *testing.T) {
	// Marker present, but pushed past the scan limit by a huge frontmatter.
	var b strings.Builder
	b.WriteString("---\nname: huge\n")
	for b.Len() < frontmatterScanLimit*2 {
		b.WriteString("description: " + strings.Repeat("x", 200) + "\n")
	}
	b.WriteString("  " + UnindexedMarker + "\n---\n\nbody\n")

	idx := writeMemDir(t, "# Memory index\n", map[string]string{"huge.md": b.String()})
	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	// The marker is NOT honoured, because it was never read. That is the correct
	// direction to fail: a defect is reported (visible, fixable by moving the
	// marker up) rather than silently excused.
	if res.InParity {
		t.Fatalf("a marker beyond frontmatterScanLimit=%d was honoured — the scan is not bounded", frontmatterScanLimit)
	}
}

// TestReferenceDetectionBoundaries pins the one place reference detection must be
// strict. It is deliberately permissive about FORM — a note named in prose is
// still reachable by recall — but a filename must not be satisfied by being a
// suffix of a different filename.
func TestReferenceDetectionBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		index string
		base  string
		want  bool
	}{
		{"canonical markdown link", "- [T](a-note.md) — hook", "a-note.md", true},
		{"named in prose", "superseded by a-note.md, see there", "a-note.md", true},
		{"bare at line start", "a-note.md is the one", "a-note.md", true},
		{"absent", "- [T](other.md) — hook", "a-note.md", false},
		// The boundary case: `a.md` must not be satisfied by `xa.md`.
		{"suffix of a longer name", "- [T](xa.md) — hook", "a.md", false},
		{"prefix stem of a longer name", "- [T](feedback_drive_dont_ask.md) — hook", "feedback_drive.md", false},
		{"longer name is not satisfied by its stem", "- [T](feedback_drive.md) — hook", "feedback_drive_dont_ask.md", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := references(tc.index, tc.base); got != tc.want {
				t.Errorf("references(%q, %q) = %v, want %v", tc.index, tc.base, got, tc.want)
			}
		})
	}
}

// TestParityIgnoresIndexItself: MEMORY.md must not be required to reference
// itself, and must not be counted as a note.
func TestParityIgnoresIndexItself(t *testing.T) {
	idx := writeMemDir(t, "# Memory index\n", nil)
	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Notes != 0 {
		t.Errorf("Notes = %d, want 0 for an index-only dir", res.Notes)
	}
	if !res.InParity {
		t.Errorf("index-only dir reported defects: %v", res.Unreferenced)
	}
}

// TestParityMissingIndexIsAnError: a memory dir with notes and no index is a real
// condition (one exists on the development box). CheckParity surfaces it as an
// error rather than silently reporting parity — every note in such a dir is
// unrecallable, which is the maximal form of the defect, not the absence of one.
func TestParityMissingIndexIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orphan.md"), []byte(note("orphan", "d")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckParity(filepath.Join(dir, "MEMORY.md")); err == nil {
		t.Fatal("CheckParity on a dir with no MEMORY.md returned nil error; a silent pass would hide a whole unrecallable dir")
	}
}

// TestParityIsReadOnly pins that checking never mutates the store. The sibling
// size check is documented as detect-only for the same reason (mg-15c0), and
// parity has a tempting auto-fix — append the missing hooks — that must not
// exist: it would happily index a 127KB scratch queue.
func TestParityIsReadOnly(t *testing.T) {
	idxBody := "# Memory index\n\n- [Indexed](indexed.md) — a hook\n"
	noteBody := note("unindexed", "nothing points at this")
	idx := writeMemDir(t, idxBody, map[string]string{
		"indexed.md":   note("indexed", "has a hook"),
		"unindexed.md": noteBody,
	})
	if _, err := CheckParity(idx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != idxBody {
		t.Errorf("CheckParity rewrote MEMORY.md:\n got %q\nwant %q", got, idxBody)
	}
	got, err = os.ReadFile(filepath.Join(filepath.Dir(idx), "unindexed.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != noteBody {
		t.Errorf("CheckParity rewrote a note")
	}
}
