package investigations

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeCorpus builds a small corpus whose README deliberately indexes only some
// of the files — the shape the real directory was in when this package was
// written (10 of 45 absent).
func writeCorpus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"README.md": "# Investigations\n\n| Doc | Covers |\n|---|---|\n" +
			"| [indexed-one.md](indexed-one.md) | the drain predicate |\n",
		"indexed-one.md": "# The drain predicate\n\n**Work item:** mg-1111 · **Date:** 2026-07-31\n\n" +
			"The drain waits on unmerged branches.\n",
		"absent-from-index.md": "# The registry is absent while the process is alive\n\n" +
			"**Work item:** mg-61a0 · **Date:** 2026-07-17\n\n" +
			"A PTY allocated by pogod outlives the registry entry.\n",
		"also-absent.md": "# Post-reboot launchd wedge\n\nnondemand spawn, pended forever.\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func fileSet(docs []Doc) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.File)
	}
	sort.Strings(out)
	return out
}

// TestSearch_FindsAFileTheIndexOmits is the design correction of mg-22c7,
// pinned. The first specification of this command searched README.md's
// Covers/Outcome columns; that build would have been blind to 22% of the corpus
// and would have answered "no investigation exists" from an instrument that
// could not see the candidate space. If this test ever fails because the search
// moved to the index, the whole point has been undone.
func TestSearch_FindsAFileTheIndexOmits(t *testing.T) {
	dir := writeCorpus(t)

	res, err := Search(dir, []string{"registry"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := fileSet(res.Docs); len(got) != 1 || got[0] != "absent-from-index.md" {
		t.Fatalf("searching for a term that appears ONLY in an unindexed file returned %v; "+
			"the corpus must be the files, not README.md", got)
	}
	if res.Docs[0].Indexed {
		t.Fatal("absent-from-index.md is not mentioned by README.md but was reported as indexed")
	}
	if res.Docs[0].Title != "The registry is absent while the process is alive" {
		t.Fatalf("title = %q, want the file's H1", res.Docs[0].Title)
	}
	if !strings.Contains(res.Docs[0].Meta, "mg-61a0") {
		t.Fatalf("meta = %q, want the byline naming the work item", res.Docs[0].Meta)
	}
}

// TestSearch_IndexIsNotItselfAResult keeps README.md out of the search domain.
// A hit on the index row instead of the investigation is the failure this
// command exists to end: the row summarises, the file answers.
func TestSearch_IndexIsNotItselfAResult(t *testing.T) {
	dir := writeCorpus(t)

	res, err := Search(dir, []string{"drain"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, d := range res.Docs {
		if d.File == indexName {
			t.Fatalf("%s was returned as a result; it is the index, not an investigation", indexName)
		}
	}
	if res.Searched != 3 {
		t.Fatalf("Searched = %d, want 3 (the corpus files, README excluded)", res.Searched)
	}
}

// TestSearch_StatesItsOwnDenominator: every count this returns has to carry the
// population it was taken from, and index coverage is reported as a diagnostic
// rather than used as a filter.
func TestSearch_StatesItsOwnDenominator(t *testing.T) {
	dir := writeCorpus(t)

	res, err := Search(dir, []string{"nothing-matches-this"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Docs) != 0 {
		t.Fatalf("Docs = %d, want 0", len(res.Docs))
	}
	if res.Searched != 3 {
		t.Fatalf("Searched = %d, want 3 — a zero result must still report what it looked at", res.Searched)
	}
	if res.Indexed != 1 {
		t.Fatalf("Indexed = %d, want 1", res.Indexed)
	}
	want := []string{"also-absent.md", "absent-from-index.md"}
	sort.Strings(want)
	got := append([]string(nil), res.Unindexed...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Unindexed = %v, want %v", got, want)
	}
}

// TestSearch_UnreadableIndexIsNotZeroCoverage: if README.md cannot be read, the
// coverage diagnostic is UNKNOWN, not zero. Reading a 0 from a source that
// could not have held the positive is the family of bug this ticket belongs to.
func TestSearch_UnreadableIndexIsNotZeroCoverage(t *testing.T) {
	dir := writeCorpus(t)
	if err := os.Remove(filepath.Join(dir, indexName)); err != nil {
		t.Fatalf("remove index: %v", err)
	}

	res, err := Search(dir, []string{"drain"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.IndexReadErr == "" {
		t.Fatal("IndexReadErr is empty with no README.md; coverage would read as 0 of 3 indexed")
	}
	if res.Indexed != 0 || len(res.Unindexed) != 0 {
		t.Fatalf("Indexed=%d Unindexed=%v — with an unreadable index neither may be populated, "+
			"or an unknown is reported as a measurement", res.Indexed, res.Unindexed)
	}
	if len(res.Docs) != 1 {
		t.Fatalf("Docs = %d, want 1 — the search itself does not depend on the index", len(res.Docs))
	}
}

// TestSearch_ReportsWhatItDidNotSearch. A file the walk declined is recorded
// with a reason, because a denominator that silently drops what it could not
// read is the same defect as an index that silently drops what nobody added.
func TestSearch_ReportsWhatItDidNotSearch(t *testing.T) {
	dir := writeCorpus(t)
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{'a', 0x00, 'b'}, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden.md"), []byte("drain\n"), 0o644); err != nil {
		t.Fatalf("write hidden: %v", err)
	}

	res, err := Search(dir, nil, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.File] = s.Reason
	}
	if reasons["blob.bin"] != "binary file" {
		t.Fatalf("blob.bin skip reason = %q, want %q", reasons["blob.bin"], "binary file")
	}
	if reasons[".hidden.md"] != "hidden file" {
		t.Fatalf(".hidden.md skip reason = %q, want %q", reasons[".hidden.md"], "hidden file")
	}
	if res.Searched != 3 {
		t.Fatalf("Searched = %d, want 3 — skipped files must not inflate the denominator", res.Searched)
	}
}

// TestSearch_AllTermsMustMatch: multiple terms are ANDed across the whole file,
// not per line.
func TestSearch_AllTermsMustMatch(t *testing.T) {
	dir := writeCorpus(t)

	both, err := Search(dir, []string{"registry", "pogod"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := fileSet(both.Docs); len(got) != 1 || got[0] != "absent-from-index.md" {
		t.Fatalf("two terms in one file returned %v, want [absent-from-index.md]", got)
	}

	split, err := Search(dir, []string{"registry", "drain"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(split.Docs) != 0 {
		t.Fatalf("terms spread across two files returned %v, want none — terms are ANDed", fileSet(split.Docs))
	}
}

// TestSearch_MatchesCaseInsensitivelyAndOnFilenames. The filenames in this
// corpus carry the topic and the date; a query naming either must find the file
// even when the prose never spells it.
func TestSearch_MatchesCaseInsensitivelyAndOnFilenames(t *testing.T) {
	dir := writeCorpus(t)

	res, err := Search(dir, []string{"REGISTRY"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Docs) != 1 {
		t.Fatalf("uppercase query matched %d docs, want 1", len(res.Docs))
	}

	byName, err := Search(dir, []string{"also-absent"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := fileSet(byName.Docs); len(got) != 1 || got[0] != "also-absent.md" {
		t.Fatalf("filename query returned %v, want [also-absent.md]", got)
	}
}

// TestSearch_NoTermsListsTheWholeCorpus — the listing mode, whose value is that
// it is derived from the files rather than from the index.
func TestSearch_NoTermsListsTheWholeCorpus(t *testing.T) {
	dir := writeCorpus(t)

	res, err := Search(dir, nil, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Docs) != 3 {
		t.Fatalf("Docs = %d, want 3 (every file, README excluded)", len(res.Docs))
	}
	for _, d := range res.Docs {
		if d.Hits != 0 || len(d.Matches) != 0 {
			t.Fatalf("%s carries %d match lines in listing mode; a listing is about documents", d.File, d.Hits)
		}
	}
}

// TestSearch_HitsSurvivesTruncation: showing 3 of 40 matching lines must not
// print as 3 matching lines.
func TestSearch_HitsSurvivesTruncation(t *testing.T) {
	dir := t.TempDir()
	body := "# Repeats\n" + strings.Repeat("the drain again\n", 20)
	if err := os.WriteFile(filepath.Join(dir, "repeats.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := Search(dir, []string{"drain"}, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Docs) != 1 {
		t.Fatalf("Docs = %d, want 1", len(res.Docs))
	}
	if len(res.Docs[0].Matches) != 3 {
		t.Fatalf("Matches = %d, want 3 (capped)", len(res.Docs[0].Matches))
	}
	if res.Docs[0].Hits != 20 {
		t.Fatalf("Hits = %d, want 20 (untruncated count)", res.Docs[0].Hits)
	}
}

// TestSearch_RanksByHits so the file that is mostly about the topic comes first.
func TestSearch_RanksByHits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mostly.md"),
		[]byte("# Mostly\ndrain\ndrain\ndrain\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mentions.md"),
		[]byte("# Mentions\nsomething else\ndrain once\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := Search(dir, []string{"drain"}, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Docs) != 2 || res.Docs[0].File != "mostly.md" {
		t.Fatalf("order = %v, want mostly.md first", fileSet(res.Docs))
	}
}

func TestFindCorpus_WalksUpFromASubdirectory(t *testing.T) {
	root := t.TempDir()
	corpus := filepath.Join(root, DefaultDir)
	if err := os.MkdirAll(corpus, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	deep := filepath.Join(root, "internal", "somepkg")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := FindCorpus(deep)
	if err != nil {
		t.Fatalf("FindCorpus: %v", err)
	}
	// t.TempDir can hand back a symlinked path (/var -> /private/var on macOS),
	// so compare resolved paths rather than the strings.
	wantResolved, _ := filepath.EvalSymlinks(corpus)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Fatalf("FindCorpus = %s, want %s", gotResolved, wantResolved)
	}
}

func TestFindCorpus_ReportsAbsenceRatherThanGuessing(t *testing.T) {
	if _, err := FindCorpus(t.TempDir()); err == nil {
		t.Fatal("FindCorpus returned no error for a tree with no corpus")
	}
}

// TestSearch_AgainstTheRealCorpus runs the real search over this repository's
// own docs/investigations and asserts the denominator equals the FILES ON DISK,
// counted independently by this test. It is the guard that the corpus is the
// directory and not the index: it cannot rot as the index is edited, because it
// never reads the index.
func TestSearch_AgainstTheRealCorpus(t *testing.T) {
	root, err := FindCorpus(".")
	if err != nil {
		t.Skipf("no corpus reachable from the test's working directory: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	want := 0
	for _, e := range entries {
		if e.IsDir() || e.Name() == indexName || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		want++
	}

	res, err := Search(root, nil, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Searched != want {
		t.Fatalf("Searched = %d but %d non-index files are on disk in %s; "+
			"the search domain must be the files", res.Searched, want, root)
	}
	if len(res.Docs) != want {
		t.Fatalf("listing returned %d of %d files", len(res.Docs), want)
	}
}
