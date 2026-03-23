package refinery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckQAGate_NoMacguffinDir(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
		MacguffinDir: "", // disabled
	})
	if err != nil {
		t.Fatal(err)
	}

	result, _ := r.checkQAGate("mg-1234")
	if result != QAGateProceed {
		t.Errorf("expected proceed when macguffin dir is empty, got %d", result)
	}
}

func TestCheckQAGate_NoQAItem(t *testing.T) {
	mgDir := t.TempDir()
	// Create empty directories
	for _, d := range []string{"available", "claimed", "done", "pending", "archive"} {
		os.MkdirAll(filepath.Join(mgDir, d), 0755)
	}

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, _ := r.checkQAGate("mg-1234")
	if result != QAGateProceed {
		t.Errorf("expected proceed when no QA item exists, got %d", result)
	}
}

func TestCheckQAGate_QAItemDone(t *testing.T) {
	mgDir := t.TempDir()
	os.MkdirAll(filepath.Join(mgDir, "done"), 0755)

	// Write a done QA item
	writeWorkItem(t, filepath.Join(mgDir, "done", "qa-001.md"), "qa-001", "qa", "mg-1234")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, qaID := r.checkQAGate("mg-1234")
	if result != QAGateProceed {
		t.Errorf("expected proceed when QA item is done, got %d", result)
	}
	if qaID != "qa-001" {
		t.Errorf("expected qa item id qa-001, got %s", qaID)
	}
}

func TestCheckQAGate_QAItemArchived(t *testing.T) {
	mgDir := t.TempDir()
	archiveDir := filepath.Join(mgDir, "archive", "2026-03")
	os.MkdirAll(archiveDir, 0755)

	writeWorkItem(t, filepath.Join(archiveDir, "qa-002.md"), "qa-002", "qa", "mg-5678")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, qaID := r.checkQAGate("mg-5678")
	if result != QAGateProceed {
		t.Errorf("expected proceed when QA item is archived, got %d", result)
	}
	if qaID != "qa-002" {
		t.Errorf("expected qa item id qa-002, got %s", qaID)
	}
}

func TestCheckQAGate_QAItemPending(t *testing.T) {
	mgDir := t.TempDir()
	for _, d := range []string{"available", "claimed", "done"} {
		os.MkdirAll(filepath.Join(mgDir, d), 0755)
	}

	// QA item is in claimed (not done)
	writeWorkItem(t, filepath.Join(mgDir, "claimed", "qa-003.md"), "qa-003", "qa", "mg-9999")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, qaID := r.checkQAGate("mg-9999")
	if result != QAGateHold {
		t.Errorf("expected hold when QA item is pending, got %d", result)
	}
	if qaID != "qa-003" {
		t.Errorf("expected qa item id qa-003, got %s", qaID)
	}
}

func TestCheckQAGate_QAItemAvailable(t *testing.T) {
	mgDir := t.TempDir()
	os.MkdirAll(filepath.Join(mgDir, "available"), 0755)

	writeWorkItem(t, filepath.Join(mgDir, "available", "qa-004.md"), "qa-004", "qa", "mg-aaaa")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, _ := r.checkQAGate("mg-aaaa")
	if result != QAGateHold {
		t.Errorf("expected hold when QA item is available (not done), got %d", result)
	}
}

func TestCheckQAGate_NonQAItemIgnored(t *testing.T) {
	mgDir := t.TempDir()
	os.MkdirAll(filepath.Join(mgDir, "claimed"), 0755)

	// A non-QA item with matching source should be ignored
	writeWorkItem(t, filepath.Join(mgDir, "claimed", "mg-task.md"), "mg-task", "task", "mg-1234")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, _ := r.checkQAGate("mg-1234")
	if result != QAGateProceed {
		t.Errorf("expected proceed when only non-QA items exist, got %d", result)
	}
}

func TestCheckQAGate_WrongSourceIgnored(t *testing.T) {
	mgDir := t.TempDir()
	os.MkdirAll(filepath.Join(mgDir, "claimed"), 0755)

	// QA item with different source
	writeWorkItem(t, filepath.Join(mgDir, "claimed", "qa-005.md"), "qa-005", "qa", "mg-other")

	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  t.TempDir(),
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, _ := r.checkQAGate("mg-1234")
	if result != QAGateProceed {
		t.Errorf("expected proceed when QA item has wrong source, got %d", result)
	}
}

func TestHoldMergeRequest(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	mr := &MergeRequest{
		ID:       "mr-hold-test",
		RepoPath: "/tmp/repo",
		Branch:   "feature-1",
		Author:   "mg-1234",
		Status:   StatusQueued,
	}
	r.byID[mr.ID] = mr

	r.holdMergeRequest(mr, "qa-001")

	if mr.Status != StatusHeld {
		t.Errorf("expected status held, got %s", mr.Status)
	}
	if len(r.queue) != 1 {
		t.Fatalf("expected 1 item in queue, got %d", len(r.queue))
	}
	if r.queue[0].ID != "mr-hold-test" {
		t.Errorf("expected held MR to be re-queued")
	}
}

func TestProcessNextWithQAHold(t *testing.T) {
	mgDir := t.TempDir()
	os.MkdirAll(filepath.Join(mgDir, "claimed"), 0755)
	writeWorkItem(t, filepath.Join(mgDir, "claimed", "qa-hold.md"), "qa-hold", "qa", "mg-blocked")

	dir := t.TempDir()
	r, err := New(Config{
		Enabled:      true,
		PollInterval: time.Hour,
		WorktreeDir:  dir,
		MacguffinDir: mgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Submit an MR whose author matches the pending QA item
	id, err := r.Submit(MergeRequest{
		RepoPath: "/tmp/repo",
		Branch:   "feature-1",
		Author:   "mg-blocked",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Process — should hold, not merge
	r.processNext()

	mr := r.Get(id)
	if mr == nil {
		t.Fatal("MR not found")
	}
	if mr.Status != StatusHeld {
		t.Errorf("expected held, got %s", mr.Status)
	}

	// MR should be back in the queue
	queue := r.Queue()
	if len(queue) != 1 {
		t.Fatalf("expected 1 item in queue after hold, got %d", len(queue))
	}
}

func TestParseWorkItemFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	writeWorkItem(t, path, "qa-123", "qa", "mg-abc")

	id, isQA, source := parseWorkItemFrontmatter(path)
	if id != "qa-123" {
		t.Errorf("expected id qa-123, got %s", id)
	}
	if !isQA {
		t.Error("expected isQA to be true")
	}
	if source != "mg-abc" {
		t.Errorf("expected source mg-abc, got %s", source)
	}
}

func TestParseWorkItemFrontmatter_NonQA(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.md")
	writeWorkItem(t, path, "mg-456", "task", "")

	id, isQA, source := parseWorkItemFrontmatter(path)
	if id != "mg-456" {
		t.Errorf("expected id mg-456, got %s", id)
	}
	if isQA {
		t.Error("expected isQA to be false")
	}
	if source != "" {
		t.Errorf("expected empty source, got %s", source)
	}
}

// writeWorkItem creates a macguffin work item file with YAML frontmatter.
func writeWorkItem(t *testing.T, path, id, typ, source string) {
	t.Helper()
	content := "---\nid: " + id + "\ntype: " + typ + "\n"
	if source != "" {
		content += "source: " + source + "\n"
	}
	content += "---\n\n# " + id + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
