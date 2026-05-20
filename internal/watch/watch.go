// Package watch provides a filesystem-tree watcher abstraction with
// platform-specific backends.
//
// On darwin the backend is FSEvents (see watch_darwin.go): one recursive
// kernel notification stream per watched tree, with ~constant file-descriptor
// cost regardless of tree size. The previous fsnotify/kqueue backend consumed
// one fd per file inside every watched directory, which exhausted
// kern.maxfilesperproc on large trees and blocked child-process creation
// (mg-d205).
//
// On every other platform the backend wraps fsnotify (see watch_other.go).
// The leak was kqueue-specific; Linux inotify uses one instance fd plus cheap
// kernel watch descriptors, so fsnotify is fine off-darwin.
//
// The Watcher contract is uniform across platforms: Add(root) watches the
// entire subtree rooted at root recursively, and events report paths relative
// to the root that was registered (the darwin backend un-resolves symlinks so
// callers see the path they asked to watch).
package watch

// Op is a set of filesystem operation bits. It mirrors fsnotify.Op so callers
// can switch backends without restructuring their event-handling logic.
type Op uint32

const (
	// Create is set when a file or directory is created.
	Create Op = 1 << iota
	// Write is set when a file's contents are modified.
	Write
	// Remove is set when a file or directory is deleted.
	Remove
	// Rename is set when a file or directory is renamed.
	Rename
	// Chmod is set when metadata (permissions, owner) changes.
	Chmod
)

// Has reports whether o includes the given operation bit.
func (o Op) Has(other Op) bool { return o&other != 0 }

// String renders the set operation bits for logging.
func (o Op) String() string {
	var s string
	add := func(name string) {
		if s != "" {
			s += "|"
		}
		s += name
	}
	if o.Has(Create) {
		add("CREATE")
	}
	if o.Has(Write) {
		add("WRITE")
	}
	if o.Has(Remove) {
		add("REMOVE")
	}
	if o.Has(Rename) {
		add("RENAME")
	}
	if o.Has(Chmod) {
		add("CHMOD")
	}
	if s == "" {
		return "NONE"
	}
	return s
}

// Event is a filesystem change notification. Path is absolute and reported
// relative to the root the caller registered with Add.
type Event struct {
	Path string
	Op   Op
}

// Has reports whether the event includes the given operation bit.
func (e Event) Has(op Op) bool { return e.Op.Has(op) }

// String renders the event for logging.
func (e Event) String() string { return e.Op.String() + " " + e.Path }

// Watcher watches filesystem trees recursively. Implementations are safe for
// concurrent use by multiple goroutines.
type Watcher interface {
	// Add begins watching the entire subtree rooted at root. It is
	// idempotent: re-adding an already-watched root is a no-op (on the
	// non-darwin backend it also picks up directories created since the
	// previous Add).
	Add(root string) error
	// Remove stops watching the subtree rooted at root.
	Remove(root string) error
	// Events returns the channel on which change notifications are delivered.
	Events() <-chan Event
	// Errors returns the channel on which backend errors are delivered.
	Errors() <-chan error
	// Close stops all watching and releases backend resources.
	Close() error
}
