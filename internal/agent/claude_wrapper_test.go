package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPogoClaudeScriptEmbedded(t *testing.T) {
	if len(pogoClaudeScript) == 0 {
		t.Fatal("embedded pogo-claude.sh script is empty")
	}
	// Verify it starts with a shebang
	if string(pogoClaudeScript[:2]) != "#!" {
		t.Errorf("pogo-claude.sh should start with shebang, got %q", string(pogoClaudeScript[:10]))
	}
}

func TestClaudeWrapperPath(t *testing.T) {
	// Override HOME to avoid polluting real ~/.pogo/bin
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// First call should install
	path, err := ClaudeWrapperPath()
	if err != nil {
		t.Fatalf("ClaudeWrapperPath() failed: %v", err)
	}

	expected := filepath.Join(tmpHome, ".pogo", "bin", "pogo-claude")
	if path != expected {
		t.Errorf("got path %q, want %q", path, expected)
	}

	// Verify file exists and is executable
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("wrapper not installed: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("wrapper is not executable")
	}

	// Verify content matches embedded script
	installed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed wrapper: %v", err)
	}
	if string(installed) != string(pogoClaudeScript) {
		t.Error("installed content does not match embedded script")
	}

	// Second call should be a no-op (idempotent)
	path2, err := ClaudeWrapperPath()
	if err != nil {
		t.Fatalf("second ClaudeWrapperPath() failed: %v", err)
	}
	if path2 != path {
		t.Errorf("second call returned different path: %q vs %q", path2, path)
	}
}
