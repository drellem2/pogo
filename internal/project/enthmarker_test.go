package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// X1/X2 (enth-vcs slice 5, mg-cc5cd): .enth/checkout is a repo marker beside
// .git for discovery; a directory holding both is skipped, not guessed at.
// The git repo is the positive control that the scan runs at all.
func TestDiscoverNewReposSeesAnEnthCheckout(t *testing.T) {
	ProjectFileName = "projects-enth-marker-test.json"
	Init()
	defer RemoveSaveFile()
	home := testsandbox.Isolate(t).Home
	root := filepath.Join(home, "dev")

	gitRepo := filepath.Join(root, "g")
	os.MkdirAll(filepath.Join(gitRepo, ".git"), 0o755)
	enthRepo := filepath.Join(root, "e")
	writeEnthState(t, enthRepo)
	both := filepath.Join(root, "both")
	writeEnthState(t, both)
	os.MkdirAll(filepath.Join(both, ".git"), 0o755)
	halfEnth := filepath.Join(root, "half") // .enth/ with no checkout file
	os.MkdirAll(filepath.Join(halfEnth, ".enth"), 0o755)

	SetIndexRoots([]string{root})
	defer SetIndexRoots(nil)
	discoverNewRepos()

	if GetProjectByPath(addSlashToPath(gitRepo)) == nil {
		t.Errorf("positive control: the git repo was not registered")
	}
	if GetProjectByPath(addSlashToPath(enthRepo)) == nil {
		t.Errorf("an enth checkout was not registered")
	}
	if GetProjectByPath(addSlashToPath(both)) != nil {
		t.Errorf("a directory holding both markers was registered")
	}
	if GetProjectByPath(addSlashToPath(halfEnth)) != nil {
		t.Errorf("a .enth directory without a checkout file was registered")
	}
}

func writeEnthState(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".enth"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{"format":"enth-checkout","version":1,"base":"b","origin":{"remote":"https://e","repo":"o/r"},"paths":[]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".enth", "checkout"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
}
