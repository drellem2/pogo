//go:build !darwin

package watch

import (
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// fsnotifyWatcher is the fsnotify-backed Watcher used off-darwin. fsnotify is
// not recursive, so Add walks the tree and registers every directory; this
// matches the indexer's historical behavior. On Linux each watch is a cheap
// kernel watch descriptor (not a per-file fd), so this does not leak fds the
// way the darwin kqueue backend did.
type fsnotifyWatcher struct {
	mu      sync.Mutex
	w       *fsnotify.Watcher
	watched map[string]bool
	events  chan Event
	errors  chan error
	wg      sync.WaitGroup
}

// New returns an fsnotify-backed Watcher.
func New() (Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &fsnotifyWatcher{
		w:       fw,
		watched: make(map[string]bool),
		events:  make(chan Event, 1024),
		errors:  make(chan error, 16),
	}
	w.wg.Add(1)
	go w.forward()
	return w, nil
}

func (w *fsnotifyWatcher) Add(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort: skip unreadable entries
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && IsExcludedDir(d.Name()) {
			return filepath.SkipDir
		}
		w.mu.Lock()
		already := w.watched[path]
		w.mu.Unlock()
		if already {
			return nil
		}
		if addErr := w.w.Add(path); addErr != nil {
			return nil // best-effort: a vanished dir should not abort the walk
		}
		w.mu.Lock()
		w.watched[path] = true
		w.mu.Unlock()
		return nil
	})
}

func (w *fsnotifyWatcher) Remove(root string) error {
	prefix := filepath.Clean(root) + string(filepath.Separator)
	w.mu.Lock()
	var toRemove []string
	for path := range w.watched {
		if path == filepath.Clean(root) || strings.HasPrefix(path, prefix) {
			toRemove = append(toRemove, path)
		}
	}
	for _, path := range toRemove {
		delete(w.watched, path)
	}
	w.mu.Unlock()

	for _, path := range toRemove {
		_ = w.w.Remove(path)
	}
	return nil
}

func (w *fsnotifyWatcher) Events() <-chan Event { return w.events }
func (w *fsnotifyWatcher) Errors() <-chan error { return w.errors }

func (w *fsnotifyWatcher) Close() error {
	err := w.w.Close()
	// Closing the fsnotify watcher closes its Events/Errors channels, which
	// ends the forward goroutine; wait for it before closing ours.
	w.wg.Wait()
	close(w.events)
	close(w.errors)
	return err
}

// forward translates fsnotify events onto the abstraction's channels until the
// underlying watcher is closed.
func (w *fsnotifyWatcher) forward() {
	defer w.wg.Done()
	for {
		select {
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			w.events <- Event{Path: ev.Name, Op: translateFsnotify(ev.Op)}
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			select {
			case w.errors <- err:
			default:
			}
		}
	}
}

// translateFsnotify maps fsnotify operation bits onto Op bits.
func translateFsnotify(op fsnotify.Op) Op {
	var out Op
	if op.Has(fsnotify.Create) {
		out |= Create
	}
	if op.Has(fsnotify.Write) {
		out |= Write
	}
	if op.Has(fsnotify.Remove) {
		out |= Remove
	}
	if op.Has(fsnotify.Rename) {
		out |= Rename
	}
	if op.Has(fsnotify.Chmod) {
		out |= Chmod
	}
	return out
}
