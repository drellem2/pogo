package agent

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed scripts/pogo-claude.sh
var pogoClaudeScript []byte

// ClaudeWrapperPath returns the path to the pogo-claude wrapper script,
// installing it if it doesn't exist or is outdated. The wrapper centralizes
// operational plumbing (mg init, env setup) so agent prompts stay clean.
//
// Install location: ~/.pogo/bin/pogo-claude
func ClaudeWrapperPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}

	binDir := filepath.Join(home, ".pogo", "bin")
	wrapperPath := filepath.Join(binDir, "pogo-claude")

	// Ensure directory exists
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}

	// Check if wrapper exists and is current (by content hash)
	existing, _ := os.ReadFile(wrapperPath)
	if contentHash(existing) == contentHash(pogoClaudeScript) {
		return wrapperPath, nil
	}

	// Install or update the wrapper
	if err := os.WriteFile(wrapperPath, pogoClaudeScript, 0755); err != nil {
		return "", fmt.Errorf("install pogo-claude wrapper: %w", err)
	}

	return wrapperPath, nil
}
