package workitem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeItemWithFrontmatter(t *testing.T, root, status, id, extra string) {
	t.Helper()
	dir := filepath.Join(root, status)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := "---\nid: " + id + "\ntype: task\n" + extra + "---\n\n# " + id + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", id, err)
	}
}

// TestCreatedIsParsedFromFrontmatter. `created:` is the only FILING time the
// store records, and it is not derivable from anything else on WorkItem:
// ModTime tracks the file and moves on a body edit, CompletedAt is the other end
// of the item's life.
func TestCreatedIsParsedFromFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeItemWithFrontmatter(t, root, "available", "mg-0001", "created: 2026-08-12T02:46:48Z\n")

	items, err := ListAllFrom(root, "available")
	if err != nil {
		t.Fatalf("ListAllFrom: %v", err)
	}
	got := find(t, items, "mg-0001").Created
	want := time.Date(2026, 8, 12, 2, 46, 48, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Created = %v, want %v", got, want)
	}
}

// TestCreatedIsZeroWhenAbsentOrUnparseable, and the two are the SAME answer on
// purpose: "the store recorded no filing time I can use".
//
// It must not be a guess. mg-253e's detector scopes review tickets on this field
// against a convention start date, and a stamp silently defaulted to any
// particular instant would drop items on one side of that boundary without
// anyone being able to see it happen. Zero is a third answer, and consumers
// handle it as one.
func TestCreatedIsZeroWhenAbsentOrUnparseable(t *testing.T) {
	root := t.TempDir()
	writeItemWithFrontmatter(t, root, "available", "mg-0001", "")                        // absent
	writeItemWithFrontmatter(t, root, "available", "mg-0002", "created: last Tuesday\n") // unparseable
	writeItemWithFrontmatter(t, root, "available", "mg-0003", "created: 2026-08-12\n")   // date only, not RFC3339

	items, err := ListAllFrom(root, "available")
	if err != nil {
		t.Fatalf("ListAllFrom: %v", err)
	}
	for _, id := range []string{"mg-0001", "mg-0002", "mg-0003"} {
		if got := find(t, items, id).Created; !got.IsZero() {
			t.Errorf("%s: Created = %v, want zero", id, got)
		}
	}
}

// TestCreatedIsNormalisedToUTC. mg writes Z-suffixed stamps, but a consumer
// comparing against a boundary must not depend on that: a stamp with an offset
// has to compare as the same instant, not as a different wall clock.
func TestCreatedIsNormalisedToUTC(t *testing.T) {
	root := t.TempDir()
	writeItemWithFrontmatter(t, root, "available", "mg-0001", "created: 2026-08-12T05:51:59+01:00\n")

	items, err := ListAllFrom(root, "available")
	if err != nil {
		t.Fatalf("ListAllFrom: %v", err)
	}
	got := find(t, items, "mg-0001").Created
	if want := time.Date(2026, 8, 12, 4, 51, 59, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Created = %v, want %v", got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("Created location = %v, want UTC", got.Location())
	}
}

// TestCreatedDoesNotDisturbTheCarrierParse. `created:` is frontmatter and the
// carrier block is body — adding a frontmatter key must not shift where the
// body scan starts.
func TestCreatedDoesNotDisturbTheCarrierParse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "available")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: mg-0001\ncreated: 2026-08-12T02:46:48Z\n---\n\n" +
		"# review the PR\nworkflow: gh-issue\nstage: review\nreviews: mg-aaf6\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "mg-0001.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := ListAllFrom(root, "available")
	if err != nil {
		t.Fatalf("ListAllFrom: %v", err)
	}
	it := find(t, items, "mg-0001")
	if it.Stage != "review" || it.Reviews != "mg-aaf6" || it.Workflow != "gh-issue" {
		t.Errorf("carrier = %+v, want workflow=gh-issue stage=review reviews=mg-aaf6", it)
	}
	if it.CarrierUnreadable {
		t.Error("the carrier read as unreachable")
	}
	if it.Title != "review the PR" {
		t.Errorf("Title = %q", it.Title)
	}
}
