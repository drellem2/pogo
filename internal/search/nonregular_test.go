package search

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/drellem2/pogo/pkg/plugin"
	"github.com/hashicorp/go-hclog"
)

// These are the gh#136 guards. The indexer treated every non-directory as an
// indexable file, so a unix socket entered proj.Paths and the zoekt build
// os.ReadFile'd it and logged at ERROR. Because FileHashes is populated only
// on read SUCCESS, the mtime shortcut could never fire for it: the same node
// failed the same way on every rebuild, forever. Sockets are the instance;
// FIFOs, device nodes, dangling symlinks and symlinks-to-directories are the
// class, and a FIFO is worse than noise because os.ReadFile on one with no
// writer blocks the walk outright.
//
// The predicate under test stats THROUGH the link, so
// TestSymlinkToRegularFileIsStillIndexed is as much the point as the socket
// case: an Lstat-mode check would pass every test above it while silently
// dropping every symlinked source file from the index.

// shortTempDir returns a temp directory short enough to hold a bound unix
// socket. A sockaddr_un path is capped at 104 bytes on darwin and 108 on
// linux, and t.TempDir() spends most of that budget on the test's own name —
// the socket fixtures here cannot use it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pg")
	if err != nil {
		t.Fatalf("could not create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		dir = resolved
	}
	return dir
}

// newProjectIn wires a BasicSearch to dir the way newTestProject does, but
// over a caller-supplied directory so the fixture can hold nodes that
// os.WriteFile cannot create.
//
// The Quiesce drain is registered here, after dir's own RemoveAll: cleanups
// run LIFO, so the drain lands first and the index goroutines are done before
// the tree is removed (mg-36d9).
func newProjectIn(t *testing.T, dir string) (*BasicSearch, string, chan indexEvent) {
	t.Helper()
	root, err := absolute(dir)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}
	bs := createBasicSearch()
	quiesceOnCleanup(t, bs)
	events := make(chan indexEvent, 16)
	bs.SetOnIndexed(func(r string, changed bool) {
		events <- indexEvent{root: r, changed: changed}
	})
	return bs, root, events
}

func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

// bindSocket creates a bound unix socket inside dir — the exact node shape
// from the report (an agent's attach socket sitting in an indexed tree).
func bindSocket(t *testing.T, dir, name string) string {
	t.Helper()
	sockPath := filepath.Join(dir, name)
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("could not bind unix socket at %s (%d bytes): %v", sockPath, len(sockPath), err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if info, serr := os.Lstat(sockPath); serr != nil {
		t.Fatalf("socket not on disk after bind: %v", serr)
	} else if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("fixture at %s is not a socket (mode %v)", sockPath, info.Mode())
	}
	return sockPath
}

func indexedProject(t *testing.T, bs *BasicSearch, root string) *IndexedProject {
	t.Helper()
	proj, ok := bs.projects[root]
	if !ok {
		t.Fatalf("no indexed project recorded for %s", root)
	}
	return &proj
}

// requireIndexed asserts a path is carried all the way through: present in the
// file census AND in the hash cache. FileHashes is the half that matters —
// a path in Paths but absent from FileHashes is precisely the forever-retry
// state gh#136 describes.
func requireIndexed(t *testing.T, proj *IndexedProject, rel string) {
	t.Helper()
	if !slices.Contains(proj.Paths, rel) {
		t.Errorf("%q missing from Paths %v", rel, proj.Paths)
	}
	if _, ok := proj.FileHashes[rel]; !ok {
		t.Errorf("%q missing from FileHashes (keys: %v)", rel, hashKeys(proj))
	}
}

// requireSkipped asserts a path never entered the walk's output at all: not in
// Paths (so no read is attempted and the "Indexed N files" census is honest),
// and not in FileMtimes (so it consumes no incremental-index bookkeeping).
func requireSkipped(t *testing.T, proj *IndexedProject, rel string) {
	t.Helper()
	if slices.Contains(proj.Paths, rel) {
		t.Errorf("%q must not be indexed, but it is in Paths %v", rel, proj.Paths)
	}
	if _, ok := proj.FileMtimes[rel]; ok {
		t.Errorf("%q must not be indexed, but it has a FileMtimes entry", rel)
	}
	if _, ok := proj.FileHashes[rel]; ok {
		t.Errorf("%q must not be indexed, but it has a FileHashes entry", rel)
	}
}

func hashKeys(proj *IndexedProject) []string {
	out := make([]string, 0, len(proj.FileHashes))
	for k := range proj.FileHashes {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// requireNoReadErrors fails if the pass logged any read failure. Both messages
// are checked because a skipped node could otherwise fail in the other one:
// absolute() Lstats the path, so a dangling link errors there rather than at
// the read.
func requireNoReadErrors(t *testing.T, cap *logCapture) {
	t.Helper()
	recs := cap.records(t)
	requireLogged(t, recs, "error", "Error reading file", 0)
	requireLogged(t, recs, "error", "Error getting absolute path - file may not exist", 0)
}

// TestSocketInTreeIsNeitherIndexedNorRead is the direct gh#136 reproduction:
// a bound unix socket in an indexed tree, two index passes with a content
// change between them (the report's harness), and the assertion is zero
// ERROR lines rather than one per pass.
func TestSocketInTreeIsNeitherIndexedNorRead(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// socket-fixture-tokenone\n")
	bindSocket(t, dir, "a.sock")

	bs, root, events := newProjectIn(t, dir)
	cap := captureLogs(t, bs, hclog.Error)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	proj := indexedProject(t, bs, root)
	requireSkipped(t, proj, "a.sock")
	requireIndexed(t, proj, "real.go")
	requireNoReadErrors(t, cap)

	// The second pass is the "forever" half. Before the fix the socket was in
	// Paths and absent from FileHashes, so the mtime shortcut could never fire
	// for it and every rebuild re-read and re-logged it.
	writeFixture(t, dir, "real.go", "// socket-fixture-tokentwo\n")
	bs.ReIndex(root)
	waitIndexed(t, events, root)

	proj = indexedProject(t, bs, root)
	requireSkipped(t, proj, "a.sock")
	requireIndexed(t, proj, "real.go")
	requireNoReadErrors(t, cap)
}

// TestSymlinkToRegularFileIsStillIndexed pins the behaviour the naive form of
// this fix would have broken. The walk stats with os.Lstat, for which a
// symlink is never regular — so `!fileInfo.Mode().IsRegular()` would drop
// every symlinked source file from the index while passing every other test
// in this file. Symlinks to regular files are indexed today and must stay so.
func TestSymlinkToRegularFileIsStillIndexed(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// symlink-fixture-tokenone\n")
	if err := os.Symlink(filepath.Join(dir, "real.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Fatalf("could not create symlink: %v", err)
	}

	bs, root, events := newProjectIn(t, dir)
	cap := captureLogs(t, bs, hclog.Error)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	proj := indexedProject(t, bs, root)
	requireIndexed(t, proj, "real.go")
	requireIndexed(t, proj, "link.go")
	requireNoReadErrors(t, cap)
}

// TestDanglingSymlinkIsSkipped covers the variant already live in this box's
// pogod.log (an emacs .#lock symlink): os.Stat fails on it, so it is not a
// regular file and never enters the census.
func TestDanglingSymlinkIsSkipped(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// dangling-fixture-tokenone\n")
	if err := os.Symlink(filepath.Join(dir, "gone.go"), filepath.Join(dir, "broken.go")); err != nil {
		t.Fatalf("could not create dangling symlink: %v", err)
	}

	bs, root, events := newProjectIn(t, dir)
	cap := captureLogs(t, bs, hclog.Error)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	proj := indexedProject(t, bs, root)
	requireSkipped(t, proj, "broken.go")
	requireIndexed(t, proj, "real.go")
	requireNoReadErrors(t, cap)
}

// TestSymlinkToDirectoryIsSkipped is the same class from the other side: the
// walk's IsDir() test is Lstat-based, so a symlinked directory is not
// descended into — it fell through to the file branch and was read as a file,
// failing with EISDIR on every rebuild. Skipping it (rather than descending)
// also keeps the walk free of symlink cycles.
func TestSymlinkToDirectoryIsSkipped(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// dirlink-fixture-tokenone\n")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatalf("could not create subdir: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "sublink")); err != nil {
		t.Fatalf("could not create dir symlink: %v", err)
	}

	bs, root, events := newProjectIn(t, dir)
	cap := captureLogs(t, bs, hclog.Error)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	proj := indexedProject(t, bs, root)
	requireSkipped(t, proj, "sublink")
	requireIndexed(t, proj, "real.go")
	requireNoReadErrors(t, cap)
}

// TestFifoInTreeDoesNotWedgeTheIndexer is the wedge, not the noise: os.ReadFile
// on a FIFO with no writer BLOCKS, so before the fix a named pipe anywhere in
// an indexed tree hung the walk indefinitely. This test's failure mode is
// waitIndexed's own timeout — the pass simply never completes.
func TestFifoInTreeDoesNotWedgeTheIndexer(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// fifo-fixture-tokenone\n")
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0644); err != nil {
		t.Fatalf("could not create fifo: %v", err)
	}

	bs, root, events := newProjectIn(t, dir)
	cap := captureLogs(t, bs, hclog.Error)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	proj := indexedProject(t, bs, root)
	requireSkipped(t, proj, "pipe")
	requireIndexed(t, proj, "real.go")
	requireNoReadErrors(t, cap)
}

// TestReadFailuresNameThePathAsAField covers the second half of the ticket.
// The spurious firings are gone, but the remaining legitimate ones were
// unreadable: both calls passed a bare trailing value where hclog wants
// key/value pairs, so the path landed in a synthetic EXTRA_VALUE_AT_END field
// that nothing consuming the log can query. Driving serializeProjectIndex
// directly is the only way to reach these lines now that the walk refuses to
// enqueue unreadable nodes.
func TestReadFailuresNameThePathAsAField(t *testing.T) {
	g := createBasicSearch()
	cap := captureLogs(t, g, hclog.Error)

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0755); err != nil {
		t.Fatalf("could not create subdir: %v", err)
	}
	root, err := absolute(dir)
	if err != nil {
		t.Fatalf("absolute: %v", err)
	}

	// "adir" resolves but cannot be read as a file (EISDIR) → the read error.
	// "missing.txt" does not resolve at all → the absolute-path error.
	proj := &IndexedProject{
		Root:       root,
		Paths:      []string{"adir", "missing.txt"},
		FileHashes: map[string]string{},
		FileMtimes: map[string]int64{},
		Status:     StatusIndexing,
	}
	prev := IndexedProject{}
	g.serializeProjectIndex(proj, &prev, false, nil)

	recs := cap.records(t)

	readErrs := findLogged(recs, "error", "Error reading file")
	if len(readErrs) != 1 {
		t.Fatalf("want 1 read error, got %d:\n%s", len(readErrs), formatRecords(recs))
	}
	wantRead := filepath.Join(root, "adir") + "/"
	if got := readErrs[0]["path"]; got != wantRead {
		t.Errorf("read error must carry the path in a 'path' field; want %q, got %v", wantRead, got)
	}

	absErrs := findLogged(recs, "error", "Error getting absolute path - file may not exist")
	if len(absErrs) != 1 {
		t.Fatalf("want 1 absolute-path error, got %d:\n%s", len(absErrs), formatRecords(recs))
	}
	if got := absErrs[0]["path"]; got != "missing.txt" {
		t.Errorf("absolute-path error must carry the path in a 'path' field; want %q, got %v", "missing.txt", got)
	}

	for _, rec := range recs {
		if _, bad := rec["EXTRA_VALUE_AT_END"]; bad {
			t.Errorf("log record still has a synthetic EXTRA_VALUE_AT_END field:\n%s", formatRecords([]map[string]any{rec}))
		}
	}
}
