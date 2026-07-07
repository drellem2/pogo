package search

import (
	"path/filepath"
	"testing"
)

// TestIsProtectedHomePath verifies the macOS TCC-protected home guard
// (mg-5cd6): $HOME itself and the protected subtrees are refused, while
// ordinary dev paths under $HOME and paths outside $HOME are allowed.
func TestIsProtectedHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	protected := []string{
		home,
		filepath.Join(home, "Desktop"),
		filepath.Join(home, "Documents", "repo"),
		filepath.Join(home, "Downloads", "a", "b"),
		filepath.Join(home, "Pictures"),
		filepath.Join(home, "Movies", "clip-repo"),
		filepath.Join(home, "Library", "Application Support", "x"),
		filepath.Join(home, "Documents") + string(filepath.Separator), // trailing slash
	}
	for _, p := range protected {
		if !IsProtectedHomePath(p) {
			t.Errorf("expected %s to be a protected home path", p)
		}
	}

	allowed := []string{
		filepath.Join(home, "dev", "pogo"),
		filepath.Join(home, "src", "Documents-clone"), // "Documents-clone" != "Documents"
		filepath.Join(home, "code"),
		"/Users/someone/dev/project", // outside this test's $HOME
		"/tmp/whatever",
	}
	for _, p := range allowed {
		if IsProtectedHomePath(p) {
			t.Errorf("expected %s NOT to be a protected home path", p)
		}
	}
}
