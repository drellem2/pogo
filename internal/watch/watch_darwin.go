//go:build darwin

package watch

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"
)

// fsEventsLatency is how long FSEvents coalesces changes before delivering
// them. A small latency keeps re-indexing responsive while still batching
// bursts (e.g. a git checkout).
const fsEventsLatency = 250 * time.Millisecond

var errWatcherClosed = errors.New("watch: watcher is closed")

// darwinWatcher is the FSEvents-backed Watcher. Each registered root gets one
// recursive EventStream, so the file-descriptor cost is ~constant per tree
// regardless of how many files it contains.
type darwinWatcher struct {
	mu      sync.Mutex
	streams map[string]*stream
	events  chan Event
	errors  chan error
	closed  bool
	wg      sync.WaitGroup
}

// stream tracks one FSEvents EventStream and the goroutine forwarding it.
type stream struct {
	es   *fsevents.EventStream
	done chan struct{}
	// dispRoot is the root as the caller registered it (cleaned). realRoot is
	// its symlink-resolved form. FSEvents reports resolved paths (e.g.
	// /private/var/...); we rewrite them back to dispRoot so callers see the
	// path they asked to watch.
	dispRoot string
	realRoot string
}

// New returns an FSEvents-backed Watcher.
func New() (Watcher, error) {
	return &darwinWatcher{
		streams: make(map[string]*stream),
		events:  make(chan Event, 1024),
		errors:  make(chan error, 16),
	}, nil
}

func (w *darwinWatcher) Add(root string) error {
	disp := filepath.Clean(root)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errWatcherClosed
	}
	if _, ok := w.streams[disp]; ok {
		return nil // already watching
	}

	real := disp
	if resolved, err := filepath.EvalSymlinks(disp); err == nil {
		real = resolved
	}

	es := &fsevents.EventStream{
		Paths:   []string{disp},
		Latency: fsEventsLatency,
		Flags:   fsevents.FileEvents,
	}
	if err := es.Start(); err != nil {
		return err
	}

	s := &stream{
		es:       es,
		done:     make(chan struct{}),
		dispRoot: disp,
		realRoot: real,
	}
	w.streams[disp] = s
	w.wg.Add(1)
	go w.forward(s)
	return nil
}

func (w *darwinWatcher) Remove(root string) error {
	disp := filepath.Clean(root)

	w.mu.Lock()
	s, ok := w.streams[disp]
	if ok {
		delete(w.streams, disp)
	}
	w.mu.Unlock()

	if ok {
		s.es.Stop()
		close(s.done)
	}
	return nil
}

func (w *darwinWatcher) Events() <-chan Event { return w.events }
func (w *darwinWatcher) Errors() <-chan error { return w.errors }

func (w *darwinWatcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	streams := w.streams
	w.streams = make(map[string]*stream)
	w.mu.Unlock()

	for _, s := range streams {
		s.es.Stop()
		close(s.done)
	}
	// Wait for every forward goroutine to exit before closing the shared
	// channels, so no send can race the close.
	w.wg.Wait()
	close(w.events)
	close(w.errors)
	return nil
}

// forward translates one EventStream's events onto the shared channel until
// the stream is removed. The stream is stopped before done is closed, so no
// FSEvents callback can race this goroutine's exit.
func (w *darwinWatcher) forward(s *stream) {
	defer w.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case msg, ok := <-s.es.Events:
			if !ok {
				return
			}
			for _, ev := range msg {
				op := translateFlags(ev.Flags)
				// MustScanSubDirs means the kernel dropped events for a
				// subtree; surface it as a Create on the directory so the
				// consumer re-walks and re-indexes it.
				if ev.Flags&fsevents.MustScanSubDirs != 0 {
					op |= Create
				}
				if op == 0 {
					continue
				}
				select {
				case w.events <- Event{Path: s.rewrite(ev.Path), Op: op}:
				case <-s.done:
					return
				}
			}
		}
	}
}

// rewrite maps an FSEvents-reported (symlink-resolved) path back to the root
// form the caller registered.
func (s *stream) rewrite(p string) string {
	p = filepath.Clean(p)
	if s.realRoot == s.dispRoot {
		return p
	}
	if p == s.realRoot {
		return s.dispRoot
	}
	if rest := strings.TrimPrefix(p, s.realRoot+string(filepath.Separator)); rest != p {
		return filepath.Join(s.dispRoot, rest)
	}
	return p
}

// translateFlags maps FSEvents item flags onto Op bits.
func translateFlags(f fsevents.EventFlags) Op {
	var op Op
	if f&fsevents.ItemCreated != 0 {
		op |= Create
	}
	if f&fsevents.ItemRemoved != 0 {
		op |= Remove
	}
	if f&fsevents.ItemRenamed != 0 {
		op |= Rename
	}
	if f&fsevents.ItemModified != 0 {
		op |= Write
	}
	if f&(fsevents.ItemInodeMetaMod|fsevents.ItemChangeOwner|fsevents.ItemXattrMod) != 0 {
		op |= Chmod
	}
	return op
}
