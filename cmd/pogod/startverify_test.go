package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeStartVerifyItem drops a minimal macguffin work-item file into a status
// directory under root.
func writeStartVerifyItem(t *testing.T, root, status, id string) {
	t.Helper()
	dir := filepath.Join(root, status)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: " + id + "\ntype: task\n---\n# " + id + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestNewStartVerifier maps the mg-claim state to the started-signal: an item
// still in available/ is unstarted; one that has moved to claimed/ (or is absent
// entirely) is started. This is the HARD signal the auto-renudge watcher gates
// on (mg-feb3).
func TestNewStartVerifier(t *testing.T) {
	root := t.TempDir()
	writeStartVerifyItem(t, root, "available", "mg-avail")
	writeStartVerifyItem(t, root, "claimed", "mg-claimed")

	verify := newStartVerifier(root)

	// Still in available/ → not yet claimed → unstarted.
	if started, err := verify("mg-avail"); err != nil || started {
		t.Errorf("available item should read as unstarted; got started=%v err=%v", started, err)
	}
	// Moved to claimed/ → started.
	if started, err := verify("mg-claimed"); err != nil || !started {
		t.Errorf("claimed item should read as started; got started=%v err=%v", started, err)
	}
	// Absent from every queue → treated as started (not sitting unclaimed).
	if started, err := verify("mg-missing"); err != nil || !started {
		t.Errorf("absent item should read as started; got started=%v err=%v", started, err)
	}
}

// TestNewStartVerifier_MissingRoot: a nonexistent work root is not an error for
// ListFrom (absent status dirs are skipped), so the item reads as started rather
// than triggering a blind renudge.
func TestNewStartVerifier_MissingRoot(t *testing.T) {
	verify := newStartVerifier(filepath.Join(t.TempDir(), "does-not-exist"))
	if started, err := verify("mg-anything"); err != nil || !started {
		t.Errorf("missing root should read as started with no error; got started=%v err=%v", started, err)
	}
}
