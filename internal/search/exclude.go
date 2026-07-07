package search

import (
	"os"
	"path/filepath"
	"strings"
)

// excludedDirs lists directory base names that are skipped during indexing.
// These are conventional locations for VCS metadata, dependencies, and
// generated build artifacts. Deep-walking them wastes index cost. See mg-d205.
//
// This helper previously lived in internal/watch; it moved here when the
// event-based watch subsystem was removed (mg-5b0d) — the indexer still needs
// it, the watcher no longer exists.
var excludedDirs = map[string]bool{
	".git":         true,
	".pogo":        true,
	".hg":          true,
	".svn":         true,
	".cache":       true,
	".next":        true,
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"build":        true,
	"dist":         true,
	"IndexedDB":    true,
}

// IsExcludedDir reports whether a directory with the given base name should be
// skipped during indexing.
func IsExcludedDir(name string) bool {
	if excludedDirs[name] {
		return true
	}
	// macOS application bundles (Foo.app) are opaque trees of resources, not
	// source code.
	if strings.HasSuffix(name, ".app") {
		return true
	}
	return false
}

// protectedHomeSubdirs are the base names of directories directly under $HOME
// that macOS guards behind TCC (Transparency, Consent, and Control). Merely
// reading their contents triggers a permission popup, so pogod must never
// register a repo located inside them nor descend into them while indexing.
// See mg-cbc5 (original FSEvents-era $HOME guard) and mg-5cd6 (re-added for the
// timer-poll indexer after mg-5b0d removed the scanner that carried it).
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
// auto-register or index such paths: on macOS, reading them fires a TCC
// permission popup on every indexer tick — Daniel's recurring daily friction.
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
