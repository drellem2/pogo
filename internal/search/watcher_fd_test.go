package search

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/drellem2/pogo/pkg/plugin"
)

// countOpenFDs returns the number of open file descriptors for this process.
// On darwin /dev/fd lists the calling process's descriptors. Readdirnames is
// used rather than os.ReadDir because the latter lstat()s each entry, which
// races against transient descriptors and fails with "bad file descriptor".
func countOpenFDs(t *testing.T) int {
	t.Helper()
	f, err := os.Open("/dev/fd")
	if err != nil {
		t.Fatalf("could not open /dev/fd: %v", err)
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		t.Fatalf("could not read /dev/fd: %v", err)
	}
	return len(names)
}

// TestWatcherFDUsageBounded is the mg-d205 regression guard. It builds one flat
// directory of 5,000 files, indexes and watches it, and asserts the process's
// open-fd count barely moved.
//
// The old fsnotify/kqueue backend consumed one fd per file inside every
// watched directory, so this tree alone would have leaked ~5,000 fds. The
// FSEvents backend uses a single recursive kernel stream per tree, so fd cost
// is ~constant regardless of file count. This test FAILS on the kqueue path
// and PASSES on FSEvents — a real regression guard, not a tautology.
func TestWatcherFDUsageBounded(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("fd-usage assertion encodes FSEvents behavior; darwin only")
	}
	if testing.Short() {
		t.Skip("skipping fd regression test in short mode")
	}

	dir := t.TempDir()
	// Resolve symlinks (/var -> /private/var) so the indexed root and the
	// watcher's reported paths line up.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	const nFiles = 5000
	for i := 0; i < nFiles; i++ {
		p := filepath.Join(dir, "file"+strconv.Itoa(i)+".go")
		if err := os.WriteFile(p, []byte("package x\n"), 0644); err != nil {
			t.Fatalf("could not write fixture file: %v", err)
		}
	}
	root := dir + string(os.PathSeparator)

	runtime.GC()
	time.Sleep(300 * time.Millisecond)
	before := countOpenFDs(t)

	bs := createBasicSearch()
	defer cleanPogoFolder(t, dir)
	defer bs.Close()

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)

	// Wait for indexing + watch registration to settle.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		bs.mu.RLock()
		st := bs.projects[root].Status
		bs.mu.RUnlock()
		if st == StatusReady {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Let the FSEvents stream finish coming up.
	time.Sleep(1 * time.Second)
	runtime.GC()

	after := countOpenFDs(t)
	delta := after - before
	t.Logf("open fds before=%d after=%d delta=%d (indexed %d files)", before, after, delta, nFiles)

	if bs.watchCount.Load() == 0 {
		t.Errorf("expected the project root to be watched")
	}
	if delta >= 50 {
		t.Errorf("watcher fd usage grew by %d (>=50); the FSEvents backend must keep fd cost "+
			"~constant regardless of file count (the old kqueue path would consume ~%d)", delta, nFiles)
	}
}
