package search

import "strings"

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
