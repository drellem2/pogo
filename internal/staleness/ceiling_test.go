package staleness

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// fakeSource is a CeilingSource whose carried corpus is supplied inline, so the
// three-cell partition can be exercised without a git object store.
func fakeSource(name string, carried Corpus) CeilingSource {
	return CeilingSource{
		Name: name, How: "how-" + name,
		Corpus: func(context.Context) (Corpus, string, error) { return carried, "fake", nil },
	}
}

// TestJudgeCeilingsThreeCellsWithPositiveControl is the pair this witness owes
// itself: the same judge, over three installers whose only difference is what
// they carry, with a KNOWN-CAPABLE one in the same run.
//
// The positive control is not decoration here. The first version of this code
// had two cells and reported "carries 0 of 3 — it CANNOT close them" over both
// negative cases; every negative was true and the pair of them was misleading,
// which is exactly the failure a control catches and a lone negative does not.
func TestJudgeCeilingsThreeCellsWithPositiveControl(t *testing.T) {
	shippedBody := lines(200, "new")
	installedBody := lines(180, "old")
	thirdBody := lines(220, "newer")

	shipped := Corpus{"mayor.md": measure(shippedBody)}
	deltas := []PromptDelta{{
		Path: "mayor.md", Kind: "differs",
		ShippedHash:   measure(shippedBody).Hash,
		InstalledHash: measure(installedBody).Hash,
	}}

	rows := JudgeCeilings(context.Background(), shipped, deltas, []CeilingSource{
		// POSITIVE CONTROL — carries the reference.
		fakeSource("current", Corpus{"mayor.md": measure(shippedBody)}),
		// Carries what is already on disk: an install is a no-op.
		fakeSource("frozen", Corpus{"mayor.md": measure(installedBody)}),
		// Carries a third version — ahead of a lagging reference.
		fakeSource("ahead", Corpus{"mayor.md": measure(thirdBody)}),
	})

	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if got := rows[0]; !got.CarriesAll() || len(got.Closes) != 1 {
		t.Errorf("control row: want CarriesAll with 1 close, got %+v", got)
	}
	if got := rows[1]; got.CarriesAll() || len(got.Frozen) != 1 || len(got.Third) != 0 {
		t.Errorf("frozen row: want 1 frozen and 0 third, got %+v", got)
	}
	if got := rows[2]; got.CarriesAll() || len(got.Third) != 1 || len(got.Frozen) != 0 {
		t.Errorf("ahead row: want 1 third and 0 frozen, got %+v", got)
	}

	// The verdicts must READ differently, not merely carry different fields:
	// the human line is what a reader acts on.
	if rows[1].Verdict() == rows[2].Verdict() {
		t.Errorf("frozen and ahead produced the same verdict %q — the collapse this split exists to end", rows[1].Verdict())
	}
	for _, want := range []struct {
		row  InstallerCeiling
		text string
	}{
		{rows[0], "CAN close"},
		{rows[1], "no-op"},
		{rows[2], "THIRD"},
	} {
		if !contains(want.row.Verdict(), want.text) {
			t.Errorf("verdict %q does not mention %q", want.row.Verdict(), want.text)
		}
	}
}

// TestJudgeCeilingsNotInstalledIsNeverFrozen guards the one way two absences
// could match each other: a delta the live tree does not have carries no
// installed hash, and an installer that ships nothing for the path carries no
// hash either. Equal empties would report a file NOBODY has as "already
// installed", which is the most confident possible way to be wrong.
func TestJudgeCeilingsNotInstalledIsNeverFrozen(t *testing.T) {
	body := lines(10, "x")
	shipped := Corpus{"templates/polecat.md": measure(body)}
	deltas := []PromptDelta{{
		Path: "templates/polecat.md", Kind: "not-installed",
		ShippedHash: measure(body).Hash,
		// InstalledHash deliberately empty — the file is not installed.
	}}

	// An installer carrying an unrelated file, i.e. nothing for this path.
	rows := JudgeCeilings(context.Background(), shipped, deltas, []CeilingSource{
		fakeSource("empty", Corpus{"mayor.md": measure(body)}),
	})
	if len(rows[0].Frozen) != 0 {
		t.Fatalf("a not-installed path was reported frozen: %+v", rows[0])
	}
	if len(rows[0].Third) != 1 {
		t.Fatalf("want the path in the third cell, got %+v", rows[0])
	}
}

// TestJudgeCeilingsUnknownIsNeitherCapableNorIncapable — a source that cannot
// be read must produce a row that decides nothing, and must not be silently
// dropped: a witness that omits the reading it could not take reports a
// narrower gap than it measured.
func TestJudgeCeilingsUnknownIsNeitherCapableNorIncapable(t *testing.T) {
	body := lines(5, "x")
	shipped := Corpus{"mayor.md": measure(body)}
	deltas := []PromptDelta{{Path: "mayor.md", Kind: "differs", ShippedHash: measure(body).Hash}}

	rows := JudgeCeilings(context.Background(), shipped, deltas, []CeilingSource{
		RevisionCeilingSource("the running pogod", "boot", t.TempDir(), RevUnreachableFixture, "pogod did not answer"),
	})
	if len(rows) != 1 {
		t.Fatalf("the unreadable source was dropped: %+v", rows)
	}
	row := rows[0]
	if row.Known() {
		t.Fatalf("want an unknown row, got %+v", row)
	}
	if row.CarriesAll() {
		t.Errorf("an unknown row claimed capability")
	}
	if len(row.Closes)+len(row.Frozen)+len(row.Third) != 0 {
		t.Errorf("an unknown row partitioned deltas it never read: %+v", row)
	}
	if !contains(row.Verdict(), "UNKNOWN") || !contains(row.Verdict(), "pogod did not answer") {
		t.Errorf("verdict %q does not say what was unknown or why", row.Verdict())
	}
	// The sentinel itself belongs in the message: `<unreachable>` and a real
	// sha are different worlds and the row has to name which one it saw.
	if !contains(row.Verdict(), RevUnreachableFixture) {
		t.Errorf("verdict %q does not name the sentinel it was handed", row.Verdict())
	}
}

// RevUnreachableFixture mirrors selfdrift.RevUnreachable without importing it —
// internal/staleness must not depend on the daemon reader to know that an
// angle-bracketed string is not a revision.
const RevUnreachableFixture = "<unreachable>"

func TestIsRevision(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"7edd223c9a6b5a7f3563d2590fd2f87c61cecfcb", true},
		{"main", true},
		{"", false},
		{"<unreachable>", false},
		{"<missing>", false},
		{"<unstamped>", false},
	} {
		if got := IsRevision(c.in); got != c.want {
			t.Errorf("IsRevision(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestMeasureFSStripsTheStamp — the embed side must go through the same
// reduction as the git and on-disk sides, or a ceiling would answer a
// different question than the deltas it judges. An embed carries no stamp, so
// the property is only observable by feeding it one.
func TestMeasureFSStripsTheStamp(t *testing.T) {
	body := lines(30, "prompt")
	fsys := fstest.MapFS{
		"mayor.md":                {Data: stamped(body)},
		"templates/polecat.md":    {Data: body},
		"pm/pm-template.md":       {Data: body},
		"crew/does-not-matter.md": {Data: body},
	}
	got, err := MeasureFS(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := measure(body).Hash
	for rel, f := range got {
		if f.Hash != want {
			t.Errorf("%s hashed %s, want %s — the stamp was not stripped", rel, f.Hash, want)
		}
	}
	if _, ok := got["templates/polecat.md"]; !ok {
		t.Errorf("nested paths must be keyed slash-separated: %v", keysOf(got))
	}
}

func TestMeasureFSEmptyIsAnError(t *testing.T) {
	if _, err := MeasureFS(fstest.MapFS{}); err == nil {
		t.Fatal("an empty corpus must be an error, not a silently capable ceiling")
	}
}

// TestCeilingRemedyFollowsTheReading — the Fix line is derived, and the three
// shapes want three different actions. A remedy that named an install while
// every installer carried what was already on disk is the defect mg-1e8e was
// filed over.
func TestCeilingRemedyFollowsTheReading(t *testing.T) {
	capable := InstallerCeiling{Name: "disk", How: "next boot", Closes: []string{"a"}}
	frozen := InstallerCeiling{Name: "running", How: "boot", Frozen: []string{"a"}}
	ahead := InstallerCeiling{Name: "dev", How: "install", Third: []string{"a"}}
	unknown := InstallerCeiling{Name: "gone", How: "boot", Unknown: "no answer"}

	if got := CeilingRemedy([]InstallerCeiling{frozen, capable}); !contains(got, "next boot") {
		t.Errorf("a capable installer must be named: %q", got)
	}
	if got := CeilingRemedy([]InstallerCeiling{frozen}); !contains(got, "NOT an install") {
		t.Errorf("all-frozen must refuse to prescribe an install: %q", got)
	}
	if got := CeilingRemedy([]InstallerCeiling{frozen, ahead}); !contains(got, "--fetch") {
		t.Errorf("a third-version installer means the reference may be the stale side: %q", got)
	}
	if got := CeilingRemedy([]InstallerCeiling{unknown}); got != "" {
		t.Errorf("with nothing read, the caller must keep its own advice, got %q", got)
	}
}

// TestCheckPromptsCarriesCeilingsEndToEnd runs the whole witness over a git
// fixture with a real revision-backed source, and checks the two properties
// that only appear once the pieces are joined: ceilings are computed from the
// SAME shipped corpus the deltas came from, and a clean corpus produces none
// (a ceiling qualifies a remedy, and a clean run prescribes no remedy).
func TestCheckPromptsCarriesCeilingsEndToEnd(t *testing.T) {
	oldBody := lines(180, "old")
	newBody := lines(200, "new")

	// A repo whose HEAD ships the new corpus and whose first commit ships the
	// old one — the shape of a binary built before a prompt change.
	repo := fixtureRepo(t, map[string][]byte{"mayor.md": oldBody})
	oldRev := revParse(t, repo, "HEAD")
	writeFile(t, filepath.Join(repo, PromptsSubtree, "mayor.md"), newBody)
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "advance the corpus")

	stale := t.TempDir()
	writeFile(t, filepath.Join(stale, "mayor.md"), stamped(oldBody))

	rep := CheckPrompts(context.Background(), PromptOptions{
		Repo: repo, Ref: "main", InstalledRoot: stale, SkipRemote: true,
		Ceilings: []CeilingSource{
			RevisionCeilingSource("an old build", "its boot", repo, oldRev, "unreadable"),
			RevisionCeilingSource("a current build", "its boot", repo, revParse(t, repo, "HEAD"), "unreadable"),
		},
	})
	if rep.Err != "" {
		t.Fatalf("CheckPrompts: %s", rep.Err)
	}
	if len(rep.Deltas) != 1 {
		t.Fatalf("want 1 delta, got %v", rep.Deltas)
	}
	if len(rep.Ceilings) != 2 {
		t.Fatalf("want 2 ceilings, got %+v", rep.Ceilings)
	}
	// The old build carries EXACTLY what is installed — the live finding this
	// whole feature exists to state, reproduced from a fixture.
	if got := rep.Ceilings[0]; len(got.Frozen) != 1 {
		t.Errorf("the old build should be frozen on mayor.md, got %+v", got)
	}
	if got := rep.Ceilings[1]; !got.CarriesAll() {
		t.Errorf("the current build should carry the reference, got %+v", got)
	}

	// POSITIVE CONTROL for the "no ceilings when clean" rule: same sources,
	// same repo, an installed tree that matches. Without this the assertion
	// below cannot tell "suppressed because clean" from "never ran".
	fresh := t.TempDir()
	writeFile(t, filepath.Join(fresh, "mayor.md"), stamped(newBody))
	clean := CheckPrompts(context.Background(), PromptOptions{
		Repo: repo, Ref: "main", InstalledRoot: fresh, SkipRemote: true,
		Ceilings: []CeilingSource{
			RevisionCeilingSource("an old build", "its boot", repo, oldRev, "unreadable"),
		},
	})
	if len(clean.Deltas) != 0 {
		t.Fatalf("the control run was not clean: %v", clean.Deltas)
	}
	if len(clean.Ceilings) != 0 {
		t.Errorf("a clean run prescribes no remedy and needs no ceiling, got %+v", clean.Ceilings)
	}
}

// --- helpers ---------------------------------------------------------------

func revParse(t *testing.T, repo, ref string) string {
	t.Helper()
	out, err := gitOut(context.Background(), repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return trimSpace(string(out))
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func trimSpace(s string) string { return strings.TrimSpace(s) }

func keysOf(c Corpus) []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}
