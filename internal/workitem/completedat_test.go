package workitem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeDoneItem(t *testing.T, dir, id string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, id+".md")
	body := "---\nid: " + id + "\ntype: task\ntags: [x]\n---\n\n# " + id + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func find(t *testing.T, items []WorkItem, id string) WorkItem {
	t.Helper()
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("item %s not in %v", id, items)
	return WorkItem{}
}

// TestCompletedAtIsNotModTime is the whole reason CompletedAt exists.
//
// `mg done` RENAMES the item file into done/, and a rename preserves mtime — so
// a done item that was never body-edited carries its FILING time in ModTime,
// hours or days before it finished. Measured on the live store: every audit
// checked had ModTime equal to its `created:` frontmatter to the second, while
// the sibling result.json was written at the moment of the merge. Anything that
// aged a done item by ModTime would be measuring how long ago it was FILED and
// reporting it as how long ago it FINISHED.
func TestCompletedAtIsNotModTime(t *testing.T) {
	root := t.TempDir()
	done := filepath.Join(root, "done")
	itemPath := writeDoneItem(t, done, "mg-aud")

	filed := time.Date(2026, 7, 30, 3, 54, 48, 0, time.UTC)
	if err := os.Chtimes(itemPath, filed, filed); err != nil {
		t.Fatalf("chtimes item: %v", err)
	}
	merged := time.Date(2026, 7, 30, 6, 43, 39, 0, time.UTC)
	resultPath := filepath.Join(done, "mg-aud.result.json")
	if err := os.WriteFile(resultPath, []byte(`{"completed_by":"refinery"}`), 0o644); err != nil {
		t.Fatalf("writing result: %v", err)
	}
	if err := os.Chtimes(resultPath, merged, merged); err != nil {
		t.Fatalf("chtimes result: %v", err)
	}

	items, err := ListFrom(root, "done")
	if err != nil {
		t.Fatalf("ListFrom: %v", err)
	}
	it := find(t, items, "mg-aud")
	if !it.CompletedAt.Equal(merged) {
		t.Errorf("CompletedAt = %v, want the result.json mtime %v", it.CompletedAt, merged)
	}
	if !it.ModTime.Equal(filed) {
		t.Errorf("ModTime = %v, want the item file's own %v", it.ModTime, filed)
	}
	if it.CompletedAt.Equal(it.ModTime) {
		t.Error("CompletedAt and ModTime came out identical; the test no longer distinguishes the two")
	}
}

// TestCompletedAtZeroWhenNoResultFile: zero is a real answer meaning "no
// recorded completion time", and must not be confused with an epoch timestamp or
// silently substituted from ModTime. A consumer that filled the gap from ModTime
// would age every result-less item from its filing date.
func TestCompletedAtZeroWhenNoResultFile(t *testing.T) {
	root := t.TempDir()
	writeDoneItem(t, filepath.Join(root, "done"), "mg-noresult")
	items, err := ListFrom(root, "done")
	if err != nil {
		t.Fatalf("ListFrom: %v", err)
	}
	if got := find(t, items, "mg-noresult").CompletedAt; !got.IsZero() {
		t.Errorf("CompletedAt = %v, want zero", got)
	}
}

// TestCompletedAtOnlyForDone: an available or claimed item has not completed, so
// a stray result.json beside it must not read as a completion time.
func TestCompletedAtOnlyForDone(t *testing.T) {
	root := t.TempDir()
	avail := filepath.Join(root, "available")
	writeDoneItem(t, avail, "mg-open")
	if err := os.WriteFile(filepath.Join(avail, "mg-open.result.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("writing stray result: %v", err)
	}
	items, err := ListFrom(root, "available")
	if err != nil {
		t.Fatalf("ListFrom: %v", err)
	}
	if got := find(t, items, "mg-open").CompletedAt; !got.IsZero() {
		t.Errorf("CompletedAt = %v on an available item, want zero", got)
	}
}

// TestCompletedAtResolvesThroughAClaimSuffix: a claim renames the file to
// `<id>.md.<pid>`, and such a name can end up in done/. The result file is still
// `<id>.result.json`, so the lookup strips the suffix rather than appending to
// the whole entry name.
func TestCompletedAtResolvesThroughAClaimSuffix(t *testing.T) {
	root := t.TempDir()
	done := filepath.Join(root, "done")
	if err := os.MkdirAll(done, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nid: mg-aud\ntype: task\n---\n\n# mg-aud\n"
	if err := os.WriteFile(filepath.Join(done, "mg-aud.md.4242"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing item: %v", err)
	}
	merged := time.Date(2026, 7, 30, 6, 43, 39, 0, time.UTC)
	resultPath := filepath.Join(done, "mg-aud.result.json")
	if err := os.WriteFile(resultPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("writing result: %v", err)
	}
	if err := os.Chtimes(resultPath, merged, merged); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	items, err := ListAllFrom(root, "done")
	if err != nil {
		t.Fatalf("ListAllFrom: %v", err)
	}
	if got := find(t, items, "mg-aud").CompletedAt; !got.Equal(merged) {
		t.Errorf("CompletedAt = %v, want %v", got, merged)
	}
}

// TestResultFileIsNotItselfAWorkItem guards the scan: `<id>.result.json` sits in
// the same directory as the items and must never be parsed as one.
func TestResultFileIsNotItselfAWorkItem(t *testing.T) {
	root := t.TempDir()
	done := filepath.Join(root, "done")
	writeDoneItem(t, done, "mg-aud")
	if err := os.WriteFile(filepath.Join(done, "mg-aud.result.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("writing result: %v", err)
	}
	both := [][]WorkItem{
		mustList(t, func() ([]WorkItem, error) { return ListFrom(root, "done") }),
		mustList(t, func() ([]WorkItem, error) { return ListAllFrom(root, "done") }),
	}
	for _, items := range both {
		if len(items) != 1 {
			t.Errorf("scan returned %d items, want 1 — the result file was counted as a work item", len(items))
		}
	}
}
