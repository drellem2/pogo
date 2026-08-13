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
	if got := res.Unreachable; len(got) != 1 || got[0] != "unindexed.md" {
		t.Fatalf("Unreachable = %v, want [unindexed.md]", got)
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
		t.Fatalf("in-parity dir reported defects: %v", res.Unreachable)
	}
	if len(res.Unreachable) != 0 {
		t.Errorf("Unreachable = %v, want empty", res.Unreachable)
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
		t.Fatalf("opted-out notes counted as defects: %v", res.Unreachable)
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
		// The TRAILING boundary, which the containment test this replaced did
		// not have: `.mdx` is a different file and names no note.
		{"a longer extension is a different file", "see a-note.mdx", "a-note.md", false},
		// The wikilink form, which is how notes name each other. Its `.md` is
		// implied, so the containment test could not see these edges at all.
		{"wikilink", "see [[a-note]] for the general rule", "a-note.md", true},
		{"wikilink with alias", "see [[a-note|the general rule]]", "a-note.md", true},
		{"wikilink with section", "see [[a-note#the-rule]]", "a-note.md", true},
		{"wikilink to a different note", "see [[other]]", "a-note.md", false},
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
		t.Errorf("index-only dir reported defects: %v", res.Unreachable)
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

// REACHABILITY, NOT ABSENCE FROM THE INDEX (mg-d97f).
//
// The tests below are the controls for the distinction that this check used to
// collapse. On the shared corpus it ships against, collapsing them reported 42
// notes as "on disk and unreachable by recall: nothing points at them" when
// something pointed at every one of them — and the number went straight into a
// corpus-policy argument about whether reachability and the size cap were
// jointly satisfiable, an argument that did not need to happen.
//
// A permissive reachability rule is the obvious way to get this wrong in the
// other direction, which is why each positive control below is paired with a
// discrimination control that must still fire.

// TestSubIndexHookMakesItsNotesReachable is the shape that motivated the change
// and the cheapest remedy the checker now recommends: ONE hooked secondary index
// makes everything it lists reachable, for one index line.
func TestSubIndexHookMakesItsNotesReachable(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [Recovered notes](_index-recovered.md) — 3 notes, one hop\n",
		map[string]string{
			"_index-recovered.md": "# Recovered\n\n- [A](a.md)\n- [B](b.md)\n",
			"a.md":                note("a", "listed in the sub-index"),
			"b.md":                note("b", "listed in the sub-index"),
		})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.InParity {
		t.Fatalf("notes listed in a HOOKED sub-index reported unreachable: %v", res.Unreachable)
	}
	if got := res.Indirect; len(got) != 2 || got[0] != "a.md" || got[1] != "b.md" {
		t.Fatalf("Indirect = %v, want [a.md b.md] — they are reachable, and the fact that the index does not name them is still worth reporting", got)
	}
	if res.Direct != 1 {
		t.Errorf("Direct = %d, want 1 (only the sub-index is named by MEMORY.md)", res.Direct)
	}
}

// TestUnhookedSubIndexDoesNotLaunder is the discrimination control for the test
// above, and the single most important one in this file. If a sub-index counted
// whether or not MEMORY.md reached it, then dropping the sub-index's own hook —
// a one-line diff that makes the size check happier — would leave every note it
// lists reported as reachable. That is the failure this axis exists to catch,
// laundered through the remedy the checker now recommends.
func TestUnhookedSubIndexDoesNotLaunder(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [Something else](kept.md) — a hook\n",
		map[string]string{
			"kept.md":             note("kept", "hooked"),
			"_index-recovered.md": "# Recovered\n\n- [A](a.md)\n- [B](b.md)\n",
			"a.md":                note("a", "listed only in an UNHOOKED sub-index"),
			"b.md":                note("b", "listed only in an UNHOOKED sub-index"),
		})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"_index-recovered.md", "a.md", "b.md"}
	if len(res.Unreachable) != len(want) {
		t.Fatalf("Unreachable = %v, want %v — a sub-index nothing hooks reaches nothing", res.Unreachable, want)
	}
	for i, w := range want {
		if res.Unreachable[i] != w {
			t.Fatalf("Unreachable = %v, want %v", res.Unreachable, want)
		}
	}
	if len(res.Indirect) != 0 {
		t.Errorf("Indirect = %v, want empty", res.Indirect)
	}
}

// TestWikilinkReachesANote pins the form the corpus actually uses between notes.
// Notes link to each other as `[[slug]]` with no `.md` anywhere in the string, so
// the filename-containment test this replaced could not see the corpus's own link
// graph at all — it read every one of those edges as absent.
func TestWikilinkReachesANote(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [Host](host.md) — a hook\n",
		map[string]string{
			"host.md":          "---\nname: host\n---\n\nSee [[folded-detail]] and [[aliased|some words]].\n",
			"folded-detail.md": note("folded-detail", "reached by wikilink"),
			"aliased.md":       note("aliased", "reached by an aliased wikilink"),
		})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.InParity {
		t.Fatalf("wikilinked notes reported unreachable: %v", res.Unreachable)
	}
	if len(res.Indirect) != 2 {
		t.Errorf("Indirect = %v, want both wikilinked notes", res.Indirect)
	}
}

// TestChainsMustStartAtTheIndex is the discrimination control for reachability in
// general: two orphans that link to each other are still orphans. A traversal
// seeded from "every note" rather than from the index would report this clean,
// and would report EVERY store clean, which is the way a reachability check
// silently stops being a check.
func TestChainsMustStartAtTheIndex(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [Hooked](hooked.md) — a hook\n",
		map[string]string{
			"hooked.md": note("hooked", "has a hook"),
			"lost-a.md": "---\nname: lost-a\n---\n\nsee [[lost-b]]\n",
			"lost-b.md": "---\nname: lost-b\n---\n\nsee [[lost-a]]\n",
		})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unreachable) != 2 {
		t.Fatalf("Unreachable = %v, want both mutually-linked orphans — a cycle off the index reaches nobody", res.Unreachable)
	}
}

// TestReachabilityIsTransitiveBeyondOneHop: the walk must not stop at the notes
// the index names. A two-hop chain is how a fold-with-a-link and a sub-index that
// points at another sub-index both stay reachable.
func TestReachabilityIsTransitiveBeyondOneHop(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [One](one.md) — a hook\n",
		map[string]string{
			"one.md":   "---\nname: one\n---\n\nsee [[two]]\n",
			"two.md":   "---\nname: two\n---\n\nsee [[three]]\n",
			"three.md": note("three", "two hops from the index"),
		})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.InParity {
		t.Fatalf("a two-hop chain reported unreachable: %v", res.Unreachable)
	}
}

// TestUnreadableNoteFailsTowardsTheDefect. A note that cannot be read cannot be
// traversed, so anything reachable ONLY through it reports as unreachable. That
// is the safe direction — a visible defect rather than a silent pass — and it is
// the same choice declaresUnindexed makes for an unreadable frontmatter.
func TestUnreadableNoteFailsTowardsTheDefect(t *testing.T) {
	idx := writeMemDir(t,
		"# Memory index\n\n- [Gate](gate.md) — a hook\n",
		map[string]string{
			"gate.md":   "---\nname: gate\n---\n\nsee [[behind]]\n",
			"behind.md": note("behind", "reachable only through gate"),
		})
	gate := filepath.Join(filepath.Dir(idx), "gate.md")
	if err := os.Chmod(gate, 0o000); err != nil {
		t.Skipf("cannot make a file unreadable here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(gate, 0o644) })
	if f, err := os.Open(gate); err == nil {
		f.Close()
		t.Skip("running as a user that can read a 0o000 file; the control cannot be staged")
	}

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unreachable) != 1 || res.Unreachable[0] != "behind.md" {
		t.Fatalf("Unreachable = %v, want [behind.md] — an untraversable gate must not silently vouch for what is behind it", res.Unreachable)
	}
}

// TestHostSizeIsMeasured pins the third metric (mg-d97f). Folding is free against
// the index cap and is therefore what a margin policy incentivises, so the cost
// lands in the host and nothing counted it: on the corpus this ships against, two
// notes had grown larger than the whole MEMORY.md that reaches them, and the
// parity number went DOWN as it happened.
func TestHostSizeIsMeasured(t *testing.T) {
	index := "# Memory index\n\n- [Host](host.md) — a hook\n- [Small](small.md) — a hook\n"
	idx := writeMemDir(t, index, map[string]string{
		"host.md":  "---\nname: host\n---\n\n" + strings.Repeat("folded content. ", 200),
		"small.md": note("small", "ordinary"),
	})

	res, err := CheckParity(idx)
	if err != nil {
		t.Fatal(err)
	}
	if res.IndexBytes != len(index) {
		t.Errorf("IndexBytes = %d, want %d", res.IndexBytes, len(index))
	}
	if res.FattestNote != "host.md" {
		t.Errorf("FattestNote = %q, want host.md", res.FattestNote)
	}
	if res.NotesOverIndex != 1 {
		t.Errorf("NotesOverIndex = %d, want 1 — the host outgrew the index that reaches it", res.NotesOverIndex)
	}
	if res.FattestNoteBytes <= res.IndexBytes {
		t.Errorf("FattestNoteBytes = %d, not larger than IndexBytes = %d", res.FattestNoteBytes, res.IndexBytes)
	}
}
