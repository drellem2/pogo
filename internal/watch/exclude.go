package watch

import "strings"

// excludedDirs lists directory base names that are skipped during indexing and
// watching. These are conventional locations for VCS metadata, dependencies,
// and generated build artifacts. Deep-walking them wastes index cost and — on
// the pre-FSEvents kqueue backend — leaked one file descriptor per contained
// file. See mg-d205.
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
// skipped during indexing and watching.
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
