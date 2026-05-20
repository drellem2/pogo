package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/watch"
)

// Scanner watches parent directories of known projects for new sibling repos.
// When a project at ~/src/foo is registered, Scanner watches ~/src/.
// If ~/src/bar appears with a .git directory, it gets auto-registered.
//
// The watch backend is recursive (FSEvents on darwin), so the scanner
// restricts discovery to direct children of a watched parent — see loop.
type Scanner struct {
	watcher watch.Watcher
	watched map[string]bool // parent dirs currently watched
	mu      sync.Mutex
	quit    chan struct{}
	done    chan struct{}
}

var scanner *Scanner

// StartScanner creates and starts the background repo scanner.
// Call after Init() so existing projects seed the watch list.
func StartScanner() error {
	w, err := watch.New()
	if err != nil {
		return fmt.Errorf("scanner: failed to create watcher: %w", err)
	}

	scanner = &Scanner{
		watcher: w,
		watched: make(map[string]bool),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	// Seed watches from existing projects
	for _, p := range projects {
		scanner.watchParent(p.Path)
	}

	go scanner.loop()
	return nil
}

// StopScanner shuts down the background scanner.
func StopScanner() {
	if scanner == nil {
		return
	}
	close(scanner.quit)
	<-scanner.done
	scanner.watcher.Close()
	scanner = nil
}

// NotifyProjectAdded tells the scanner to watch the parent of a newly added project.
func NotifyProjectAdded(projectPath string) {
	if scanner == nil {
		return
	}
	scanner.watchParent(projectPath)
}

// watchParent adds the parent directory of projectPath to the watch set.
func (s *Scanner) watchParent(projectPath string) {
	// Strip trailing slash for clean filepath operations
	clean := filepath.Clean(projectPath)
	parent := filepath.Dir(clean)

	if parent == clean {
		// Already at filesystem root
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.watched[parent] {
		return
	}

	// Never watch inside ~/.pogo (pogo's own data directory)
	if isInsidePogoData(parent) {
		return
	}

	// Never watch $HOME directly. On macOS, FSEvents registration on $HOME
	// triggers TCC permission popups for protected subtrees (Documents,
	// Downloads, Desktop, Pictures, Movies, Library) on every cold start.
	// Sibling-repo discovery for projects sitting directly under $HOME is
	// sacrificed; users can register new top-level repos with `pogo visit`.
	if isHomeDir(parent) {
		logger.Info("scanner: refusing to watch $HOME (would trigger macOS TCC popups)", "path", parent)
		return
	}

	// Check for .pogo_stop in the parent directory
	if fileExists(filepath.Join(parent, ".pogo_stop")) {
		logger.Info("scanner: skipping parent dir with .pogo_stop", "path", parent)
		return
	}

	err := s.watcher.Add(parent)
	if err != nil {
		logger.Error("scanner: failed to watch parent dir", "path", parent, "error", err)
		return
	}

	s.watched[parent] = true
	logger.Info("scanner: watching parent dir for new repos", "path", parent)
}

// loop processes filesystem events.
func (s *Scanner) loop() {
	defer close(s.done)

	for {
		select {
		case <-s.quit:
			return
		case event, ok := <-s.watcher.Events():
			if !ok {
				return
			}
			if !event.Has(watch.Create) {
				continue
			}
			// The watch backend is recursive; restrict sibling-repo
			// discovery to direct children of a watched parent dir. This
			// preserves the non-recursive semantics the scanner was built
			// around and keeps deep file events cheap to discard.
			s.mu.Lock()
			direct := s.watched[filepath.Dir(event.Path)]
			s.mu.Unlock()
			if direct {
				s.handleCreate(event.Path)
			}
		case err, ok := <-s.watcher.Errors():
			if !ok {
				return
			}
			logger.Error("scanner: watcher error", "error", err)
		}
	}
}

// handleCreate checks if a newly created directory is a git repo.
func (s *Scanner) handleCreate(path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return
	}

	// Never index anything inside ~/.pogo (pogo's own data directory)
	if isInsidePogoData(path) {
		return
	}

	// Check for .pogo_stop in the new directory
	if fileExists(filepath.Join(path, ".pogo_stop")) {
		return
	}

	// Check if this directory has a .git subdirectory.
	// Brief retry: .git may appear shortly after the parent dir is created.
	gitDir := filepath.Join(path, ".git")
	found := false
	for i := 0; i < 5; i++ {
		if fileExists(gitDir) {
			found = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !found {
		return
	}

	// Check if already registered
	normalizedPath := addSlashToPath(path)
	if GetProjectByPath(normalizedPath) != nil {
		return
	}

	// Honor the optional index-roots allowlist (mg-d205).
	if !withinIndexRoots(normalizedPath) {
		logger.Info("scanner: repo outside configured index_roots; not registering", "path", normalizedPath)
		return
	}

	logger.Info("scanner: discovered new repo", "path", path)
	p := Project{
		Id:   0,
		Path: normalizedPath,
	}
	Add(&p)
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// pogoDataDir returns the ~/.pogo directory path.
func pogoDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pogo")
}

// isInsidePogoData reports whether path is inside the ~/.pogo directory.
// Pogo's own data directory should never be indexed as a project.
func isInsidePogoData(path string) bool {
	dataDir := pogoDataDir()
	if dataDir == "" {
		return false
	}
	clean := filepath.Clean(path)
	// Check if path is dataDir itself or a child of it
	return clean == dataDir || strings.HasPrefix(clean, dataDir+string(filepath.Separator))
}

// isHomeDir reports whether path is the user's home directory.
func isHomeDir(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return filepath.Clean(path) == filepath.Clean(home)
}
