package workitem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func setupTestWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// Create status directories
	for _, dir := range []string{"available", "claimed", "done"} {
		os.MkdirAll(filepath.Join(root, dir), 0o755)
	}

	// Write test work items
	writeItem(t, filepath.Join(root, "available", "mg-0001.md"), `---
id: mg-0001
type: task
assignee: ""
priority: high
tags: [backend, api]
---
# Add user authentication
`)

	writeItem(t, filepath.Join(root, "claimed", "mg-0002.md"), `---
id: mg-0002
type: bug
assignee: alice
priority: medium
tags: [frontend]
---
# Fix login page crash
`)

	writeItem(t, filepath.Join(root, "done", "mg-0003.md"), `---
id: mg-0003
type: task
assignee: bob
priority: low
tags: []
---
# Update README
`)

	return root
}

func writeItem(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListFrom(t *testing.T) {
	root := setupTestWorkspace(t)

	items, err := ListFrom(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	// Items come in order: available, claimed, done
	if items[0].ID != "mg-0001" || items[0].Status != "available" {
		t.Errorf("item 0: got id=%s status=%s", items[0].ID, items[0].Status)
	}
	if items[0].Title != "Add user authentication" {
		t.Errorf("item 0: got title=%q", items[0].Title)
	}
	if items[0].Tags != "backend, api" {
		t.Errorf("item 0: got tags=%q", items[0].Tags)
	}

	if items[1].ID != "mg-0002" || items[1].Status != "claimed" || items[1].Assignee != "alice" {
		t.Errorf("item 1: got id=%s status=%s assignee=%s", items[1].ID, items[1].Status, items[1].Assignee)
	}
	if items[1].Title != "Fix login page crash" {
		t.Errorf("item 1: got title=%q", items[1].Title)
	}

	if items[2].ID != "mg-0003" || items[2].Status != "done" {
		t.Errorf("item 2: got id=%s status=%s", items[2].ID, items[2].Status)
	}
}

func TestListFromStatusFilter(t *testing.T) {
	root := setupTestWorkspace(t)

	items, err := ListFrom(root, "available")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 available item, got %d", len(items))
	}
	if items[0].ID != "mg-0001" || items[0].Status != "available" {
		t.Errorf("got id=%s status=%s", items[0].ID, items[0].Status)
	}

	items, err = ListFrom(root, "claimed", "done")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items for claimed+done, got %d", len(items))
	}
	if items[0].Status != "claimed" || items[1].Status != "done" {
		t.Errorf("got statuses %s, %s", items[0].Status, items[1].Status)
	}

	items, err = ListFrom(root, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items for unknown status, got %d", len(items))
	}
}

// TestListFromFilterSkipsOtherDirs proves the filter applies at the directory
// level: with done/ made unreadable, a filtered list must still succeed
// because it never opens the directory, while an unfiltered list fails.
func TestListFromFilterSkipsOtherDirs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission bits are ignored when running as root")
	}
	root := setupTestWorkspace(t)
	doneDir := filepath.Join(root, "done")
	if err := os.Chmod(doneDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(doneDir, 0o755) })

	items, err := ListFrom(root, "available")
	if err != nil {
		t.Fatalf("filtered list touched done/: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 available item, got %d", len(items))
	}

	if _, err := ListFrom(root); err == nil {
		t.Fatal("unfiltered list should fail on unreadable done/")
	}
}

func TestListFromEmptyDir(t *testing.T) {
	root := t.TempDir()
	items, err := ListFrom(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestHandleWorkItems(t *testing.T) {
	// Override the workspace dir for testing by using the handler directly
	// with a temp workspace. We test the handler via httptest.
	root := setupTestWorkspace(t)

	// Temporarily override listFrom by testing via the handler
	// We'll test the HTTP handler end-to-end by calling it directly
	// but first we need to make List() point at our test dir.
	// Since List() calls ListFrom(workspaceDir()), we test the handler
	// integration by calling listFrom and HandleWorkItems separately.

	// Test listFrom directly for the data layer
	items, err := ListFrom(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	// Test the HTTP handler shape
	req := httptest.NewRequest("GET", "/workitems", nil)
	rec := httptest.NewRecorder()
	HandleWorkItems(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var result []WorkItem
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	// Handler reads from real workspace; just verify it returns valid JSON array
}

func TestHandleWorkItemsMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest("POST", "/workitems", nil)
	rec := httptest.NewRecorder()
	HandleWorkItems(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleWorkItemsStatusFilter(t *testing.T) {
	req := httptest.NewRequest("GET", "/workitems?status=nonexistent", nil)
	rec := httptest.NewRecorder()
	HandleWorkItems(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []WorkItem
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	// With a nonexistent status, we might get 0 or more depending on real workspace
	// but the response should be valid JSON
}

func TestParseFrontmatterLine(t *testing.T) {
	tests := []struct {
		line    string
		wantKey string
		wantVal string
		wantOK  bool
	}{
		{"id: mg-0001", "id", "mg-0001", true},
		{"tags: [a, b]", "tags", "a, b", true},
		{"assignee: ", "assignee", "", true},
		{"no colon here", "", "", false},
	}

	for _, tt := range tests {
		key, val, ok := parseFrontmatterLine(tt.line)
		if key != tt.wantKey || val != tt.wantVal || ok != tt.wantOK {
			t.Errorf("parseFrontmatterLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.line, key, val, ok, tt.wantKey, tt.wantVal, tt.wantOK)
		}
	}
}

// TestTagList pins the split that lets a consumer ask about ONE tag. The
// dispatch-gate advisory (mg-6fb0) reads `blocked-on-*` out of this, so a split
// that silently produced the whole line as a single tag would make the advisory
// permanently quiet — a warning that never fires being the failure mode the
// ticket named explicitly.
func TestTagList(t *testing.T) {
	tests := []struct {
		name string
		tags string
		want []string
	}{
		// The post-parseFrontmatterLine form (brackets already stripped) — this
		// is what ListFrom actually hands to a consumer.
		{"parsed form", "pogo, ops, blocked-on-daniel",
			[]string{"pogo", "ops", "blocked-on-daniel"}},
		// The raw frontmatter form, for a hand-built WorkItem.
		{"raw bracketed form", "[pogo, ops, blocked-on-daniel]",
			[]string{"pogo", "ops", "blocked-on-daniel"}},
		{"single tag", "pogo", []string{"pogo"}},
		{"quoted tags", `"pogo", 'ops'`, []string{"pogo", "ops"}},
		{"ragged spacing", "  pogo ,  ops  ", []string{"pogo", "ops"}},
		{"empty entries dropped", "pogo, , ops,", []string{"pogo", "ops"}},

		// Both spellings of "no tags".
		{"empty value", "", nil},
		{"empty list", "[]", nil},
		{"only separators", " , , ", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WorkItem{Tags: tt.tags}.TagList()
			if len(got) != len(tt.want) {
				t.Fatalf("TagList(%q) = %v, want %v", tt.tags, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("TagList(%q)[%d] = %q, want %q", tt.tags, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestTagListReadsFromParsedFile is the end-to-end half: a real work-item file
// through parseWorkItem and out as individual tags. The two-step (frontmatter
// parse, then split) is where a format assumption could rot silently, so it is
// tested against a file rather than a struct literal.
func TestTagListReadsFromParsedFile(t *testing.T) {
	dir := t.TempDir()
	avail := filepath.Join(dir, "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: mg-tag1\ntype: task\n" +
		"tags: [pogo, ops, blocked-on-daniel]\nassignee: pm-pogo\n---\n# titled\n"
	if err := os.WriteFile(filepath.Join(avail, "mg-tag1.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := ListFrom(dir, "available")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	got := items[0].TagList()
	want := []string{"pogo", "ops", "blocked-on-daniel"}
	if len(got) != len(want) {
		t.Fatalf("TagList() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("TagList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestParsesRepoAndDepends — both fields are new (mg-0e24) and both are read by
// the dispatch-pairing gate, so both are pinned against a real file rather than
// a struct literal. `repo:` is the field the gate ROUTES on; getting it wrong
// silently means the gate covers nothing.
func TestParsesRepoAndDepends(t *testing.T) {
	dir := t.TempDir()
	avail := filepath.Join(dir, "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	writeItem(t, filepath.Join(avail, "mg-rd01.md"), `---
id: mg-rd01
type: task
depends: [mg-aaaa, mg-bbbb]
tags: [onethird, research]
repo: /Users/daniel/research/onethird_program
---
# a research ticket
`)

	items, err := ListFrom(dir, "available")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got, want := items[0].Repo, "/Users/daniel/research/onethird_program"; got != want {
		t.Errorf("Repo = %q, want %q", got, want)
	}
	got := items[0].DependsList()
	want := []string{"mg-aaaa", "mg-bbbb"}
	if len(got) != len(want) {
		t.Fatalf("DependsList() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DependsList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// An empty depends list and a missing line both yield nil, matching TagList.
	writeItem(t, filepath.Join(avail, "mg-rd02.md"), "---\nid: mg-rd02\ndepends: []\n---\n# none\n")
	writeItem(t, filepath.Join(avail, "mg-rd03.md"), "---\nid: mg-rd03\n---\n# none\n")
	items, err = ListFrom(dir, "available")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == "mg-rd01" {
			continue
		}
		if d := it.DependsList(); d != nil {
			t.Errorf("%s: DependsList() = %v, want nil", it.ID, d)
		}
	}
}

// TestListAllFromSeesClaimedItems is the reason ListAllFrom exists. A claim
// renames the file to `<id>.md.<pid>`, and ListFrom's ".md" suffix filter drops
// it — so a caller SEARCHING the store would read "claimed" as "absent". For the
// dispatch-pairing gate that means refusing an item whose pair is filed and
// merely being worked on, which is a false refusal that looks exactly like the
// gate working correctly.
func TestListAllFromSeesClaimedItems(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"available", "claimed", "done"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeItem(t, filepath.Join(root, "available", "mg-open.md"), "---\nid: mg-open\n---\n# open\n")
	writeItem(t, filepath.Join(root, "claimed", "mg-held.md.4242"), "---\nid: mg-held\n---\n# held\n")

	// The existing narrow reader is unchanged: it still cannot see the claim.
	narrow, err := ListFrom(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range narrow {
		if it.ID == "mg-held" {
			t.Fatal("ListFrom() now returns claim-suffixed items; its documented " +
				"behaviour changed, and its callers were not reviewed for it")
		}
	}

	all, err := ListAllFrom(root)
	if err != nil {
		t.Fatal(err)
	}
	var sawHeld, sawOpen bool
	for _, it := range all {
		switch it.ID {
		case "mg-held":
			sawHeld = true
			if it.Status != "claimed" {
				t.Errorf("mg-held status = %q, want claimed", it.Status)
			}
		case "mg-open":
			sawOpen = true
		}
	}
	if !sawHeld {
		t.Error("ListAllFrom() did not return the claimed item")
	}
	if !sawOpen {
		t.Error("ListAllFrom() dropped an ordinary available item")
	}
}

// TestPendingIsReachableOnlyByName pins both halves of adding pending/ to
// statusDirs, and they are equally load-bearing.
//
// pending/ holds items mg has parked because a gate on them has not opened —
// most often an unmet `depends:`. Those are FILED items, so a caller SEARCHING
// the store must be able to reach them; the dispatch-pairing gate is exactly
// such a caller, and a pre-filed pair lives in pending/ by construction.
//
// But every caller written before pending/ existed says "all" and means
// available+claimed+done. If an unfiltered scan started returning parked items,
// each of those callers would silently begin counting work nobody can start.
// So: reachable by naming it, invisible otherwise.
func TestPendingIsReachableOnlyByName(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"available", "claimed", "done", "pending"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeItem(t, filepath.Join(root, "available", "mg-open.md"), "---\nid: mg-open\n---\n# open\n")
	writeItem(t, filepath.Join(root, "pending", "mg-park.md"),
		"---\nid: mg-park\ndepends: [mg-open]\n---\n# parked on an unmet dependency\n")

	has := func(items []WorkItem, id string) bool {
		for _, it := range items {
			if it.ID == id {
				return true
			}
		}
		return false
	}

	// An unfiltered scan, on both readers, must not have moved.
	for _, tc := range []struct {
		name  string
		items []WorkItem
	}{
		{"ListFrom", mustList(t, func() ([]WorkItem, error) { return ListFrom(root) })},
		{"ListAllFrom", mustList(t, func() ([]WorkItem, error) { return ListAllFrom(root) })},
	} {
		if has(tc.items, "mg-park") {
			t.Errorf("%s() with no filter returned a pending item; every existing caller "+
				"means available+claimed+done by \"all\" and would silently start "+
				"counting work nobody can start", tc.name)
		}
		if !has(tc.items, "mg-open") {
			t.Errorf("%s() with no filter lost an ordinary available item", tc.name)
		}
	}

	// Named explicitly, it is there — and carries the right status.
	named := mustList(t, func() ([]WorkItem, error) { return ListAllFrom(root, "available", "pending") })
	if !has(named, "mg-park") {
		t.Fatal(`ListAllFrom(root, "available", "pending") did not return the pending item, ` +
			"so a pre-filed pair parked on its target reads as never filed")
	}
	for _, it := range named {
		if it.ID == "mg-park" && it.Status != "pending" {
			t.Errorf("mg-park status = %q, want pending", it.Status)
		}
	}
	if !has(named, "mg-open") {
		t.Error(`ListAllFrom(root, "available", "pending") dropped the available item`)
	}

	// FindFrom is a by-id lookup used by two dispatch gates to decide whether an
	// item may be executed. It searched available+claimed+done before pending/
	// became reachable and must still: a gate that started resolving parked items
	// would change its verdict for a reason unrelated to why pending/ was added.
	if _, found, err := FindFrom(root, "mg-park"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Error("FindFrom() now resolves pending items; its callers include two " +
			"dispatch gates and were not reviewed for that change")
	}
	if _, found, err := FindFrom(root, "mg-open"); err != nil || !found {
		t.Errorf("FindFrom() lost an ordinary available item: found=%v err=%v", found, err)
	}
}

func mustList(t *testing.T, f func() ([]WorkItem, error)) []WorkItem {
	t.Helper()
	items, err := f()
	if err != nil {
		t.Fatal(err)
	}
	return items
}
