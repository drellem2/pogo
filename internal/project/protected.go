package project

import (
	"os"
	"path/filepath"
	"strings"
)

// protectedHomeSubdirs are the base names of directories directly under $HOME
// that macOS guards behind TCC (Transparency, Consent, and Control). Merely
// reading their contents triggers a permission popup, so pogod must never
// register a repo located inside them. See mg-cbc5 (original FSEvents-era
// $HOME guard) and mg-5cd6 (re-added for the timer-poll indexer after mg-5b0d
// removed the scanner that carried it). This lived in internal/search until
// the file-search indexer was deleted (mg-14647); the project index still
// needs it.
var protectedHomeSubdirs = map[string]bool{
	"Desktop":   true,
	"Documents": true,
	"Downloads": true,
	"Pictures":  true,
	"Movies":    true,
	"Library":   true,
}

// IsProtectedHomePath reports whether path is the user's home directory itself
// or lives within one of the macOS TCC-protected subdirectories of $HOME
// (Desktop, Documents, Downloads, Pictures, Movies, Library). pogod refuses to
// auto-register such paths: on macOS, reading them fires a TCC permission
// popup — Daniel's recurring daily friction.
//
// Ordinary dev paths under $HOME (e.g. ~/dev/foo) are NOT protected; only the
// specific TCC subtrees and $HOME itself are. When $HOME cannot be resolved the
// guard is a no-op, preserving zero-config behavior on machines without a home.
func IsProtectedHomePath(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	home = filepath.Clean(home)
	clean := filepath.Clean(path)
	if clean == home {
		return true
	}
	rel, err := filepath.Rel(home, clean)
	if err != nil {
		return false
	}
	// Not under $HOME at all (rel escapes upward) — nothing to protect.
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	// The immediate child of $HOME decides protection.
	first := strings.Split(rel, string(filepath.Separator))[0]
	return protectedHomeSubdirs[first]
}
