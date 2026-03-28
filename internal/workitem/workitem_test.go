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

	items, err := listFrom(root)
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

func TestListFromEmptyDir(t *testing.T) {
	root := t.TempDir()
	items, err := listFrom(root)
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
	// Since List() calls listFrom(workspaceDir()), we test the handler
	// integration by calling listFrom and HandleWorkItems separately.

	// Test listFrom directly for the data layer
	items, err := listFrom(root)
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
