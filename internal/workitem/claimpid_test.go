package workitem

import (
	"os"
	"path/filepath"
	"testing"
)

// ListAllFrom reads the claimant pid off a claim-suffixed name; ListFrom never
// sees those names, and a suffix that is not a pid yields zero (mg-5dec2).
func TestListAllFromPopulatesClaimPID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claimed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := func(id string) []byte { return []byte("---\nid: " + id + "\n---\n# t\n") }
	for name, id := range map[string]string{
		"mg-aaaa.md.50235": "mg-aaaa",
		"mg-bbbb.md":       "mg-bbbb",
		"mg-cccc.md.x1":    "mg-cccc",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body(id), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	items, err := ListAllFrom(root, "claimed")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, it := range items {
		got[it.ID] = it.ClaimPID
	}
	want := map[string]int{"mg-aaaa": 50235, "mg-bbbb": 0, "mg-cccc": 0}
	if len(got) != len(want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	for id, pid := range want {
		if got[id] != pid {
			t.Errorf("%s: ClaimPID=%d, want %d", id, got[id], pid)
		}
	}
}
