package project

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateLegacyProjectFile covers the one-time projects.json migration
// for machines carrying the legacy POGO_HOME=$HOME layout (mg-3dc3): the
// registry moves into the normalized $HOME/.pogo, and an existing canonical
// file is never clobbered.
func TestMigrateLegacyProjectFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", home) // legacy layout normalized to $HOME/.pogo

	origName := ProjectFileName
	ProjectFileName = "projects-test.json"
	t.Cleanup(func() { ProjectFileName = origName })

	legacyContent := []byte(`{"projects":[{"id":1,"path":"/x"}]}`)
	if err := os.WriteFile(filepath.Join(home, ProjectFileName), legacyContent, 0644); err != nil {
		t.Fatal(err)
	}

	canonical := filepath.Join(home, ".pogo", ProjectFileName)
	migrateLegacyProjectFile(canonical)

	data, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("expected migrated file at %s: %v", canonical, err)
	}
	if string(data) != string(legacyContent) {
		t.Errorf("migrated content = %q, want %q", data, legacyContent)
	}

	// An existing canonical file must never be overwritten.
	if err := os.WriteFile(canonical, []byte(`{"projects":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	migrateLegacyProjectFile(canonical)
	data, _ = os.ReadFile(canonical)
	if string(data) != `{"projects":[]}` {
		t.Errorf("existing canonical file was clobbered: %q", data)
	}

	// No-op when POGO_HOME needs no normalization.
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	other := filepath.Join(home, ".pogo", "other-"+ProjectFileName)
	migrateLegacyProjectFile(other)
	if _, err := os.Lstat(other); !os.IsNotExist(err) {
		t.Errorf("migration ran despite POGO_HOME already canonical")
	}
}
