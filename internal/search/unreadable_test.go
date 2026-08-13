package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"

	"github.com/drellem2/pogo/pkg/plugin"
)

// The gh#136 residue that mg-f32a's node-type predicate could not reach
// (mg-9c6b). A mode-0000 file IS a regular file, so it passes every mode check
// mg-f32a installed, and then takes the identical route to the identical
// permanent ERROR: `indexRec` put it in `proj.Paths` before attempting the
// read, `FileHashes` was populated only on read SUCCESS, so the mtime shortcut
// could never fire for it and the zoekt build re-read and re-logged it on
// every rebuild, forever.
//
// The remedy here is different in kind from mg-f32a's, and the choice between
// the two candidates is what these tests pin:
//
//   - TestUnreadableRegularFileIsNeitherIndexedNorReRead is the reproduction.
//   - TestUnreadableFileIsAnnouncedOncePerFileNotOncePerPass is the reason the
//     drop is not silent — and the reason the announcement is not the defect
//     wearing a Warn badge.
//   - TestUnreadableFileIsIndexedOnceItsPermissionsAreRepaired is the argument
//     against the OTHER candidate (a persisted failure marker the mtime
//     shortcut consults), made executable.
//   - TestRepairingPermissionsDoesNotChangeMtime pins the premise of that
//     argument, which is a property of the filesystem rather than of this code.
//   - TestBuildSideReadFailureDropsThePathSoTheWalkRetriesIt covers the route
//     into the same forever-state that the walk fix alone does NOT close.

// makeUnreadable writes a REGULAR file into dir and strips every permission
// bit from it, verifying both halves of the fixture before returning.
//
// The regular-file assertion is the control that keeps this whole file
// honest: if the fixture were not a regular file, mg-f32a's node-type
// predicate would skip it and every assertion below would pass without
// measuring anything about the case mg-9c6b is for.
//
// The readability check is a probe rather than a `os.Geteuid() == 0` test. A
// suite running as root reads a mode-0000 file happily, and so does a
// filesystem mounted without permission enforcement; either way the fixture
// cannot be built and the test must skip rather than pass vacuously.
func makeUnreadable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("// unreadable-fixture-body\n"), 0644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
	chmodUnreadable(t, path)
	// Restore before the tree is removed. RemoveAll only needs write on the
	// parent, so this is belt-and-braces, but a 0000 file is a nuisance if a
	// cleanup ever fails and leaves the tree behind.
	t.Cleanup(func() { _ = os.Chmod(path, 0644) })
	return path
}

// chmodUnreadable strips path's permission bits and asserts the result is a
// regular file this process genuinely cannot read.
func chmodUnreadable(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatalf("chmod 0000 %s: %v", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("control: fixture %s is not a regular file (mode %v) — mg-f32a's "+
			"node-type predicate would skip it and this test would prove nothing "+
			"about the regular-file case mg-9c6b is for", path, info.Mode())
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skipf("this process can read a mode-0000 file (running as root, or on a " +
			"filesystem that does not enforce permissions), so the unreadable-file " +
			"fixture cannot be built here")
	}
}

// TestUnreadableRegularFileIsNeitherIndexedNorReRead is the direct
// reproduction of pd864's measurement: a mode-0000 regular file in an indexed
// tree, two index passes with a content change between them so the zoekt
// rebuild actually runs on both, and the assertion is zero ERROR lines rather
// than one per pass.
func TestUnreadableRegularFileIsNeitherIndexedNorReRead(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// unreadable-fixture-tokenone\n")
	makeUnreadable(t, dir, "secret.go")

	bs, root, events := newProjectIn(t, dir)
	cap := captureLogs(t, bs, hclog.Error)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	proj := indexedProject(t, bs, root)
	requireSkipped(t, proj, "secret.go")
	requireIndexed(t, proj, "real.go")
	requireNoReadErrors(t, cap)

	// The second pass is the "forever" half. Before the fix the file was in
	// Paths and absent from FileHashes, so the mtime shortcut could never fire
	// for it and every rebuild re-read and re-logged it — 1 ERROR after pass
	// one, 2 cumulative after pass two, at the 2-minute default interval in
	// perpetuity.
	writeFixture(t, dir, "real.go", "// unreadable-fixture-tokentwo\n")
	bs.ReIndex(root)
	waitIndexed(t, events, root)

	proj = indexedProject(t, bs, root)
	requireSkipped(t, proj, "secret.go")
	requireIndexed(t, proj, "real.go")
	requireNoReadErrors(t, cap)
}

// TestUnreadableFileIsAnnouncedOncePerFileNotOncePerPass covers the half of
// the design choice that is not "stop the ERROR".
//
// Dropping the file silently would trade a noisy defect for an invisible one:
// the file stops being searchable and nothing says so. So the drop is
// announced — ONCE, when the file enters the unreadable set. An announcement
// per pass would be the defect being fixed, wearing a Warn badge instead of an
// ERROR one, so the count after three passes is the assertion that matters.
func TestUnreadableFileIsAnnouncedOncePerFileNotOncePerPass(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// announce-fixture-tokenone\n")
	path := makeUnreadable(t, dir, "secret.go")

	bs, root, events := newProjectIn(t, dir)
	cap := captureLogs(t, bs, hclog.Warn)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	warns := findLogged(cap.records(t), "warn", msgUnreadableFile)
	if len(warns) != 1 {
		t.Fatalf("want exactly 1 %q record after the first pass, got %d:\n%s",
			msgUnreadableFile, len(warns), formatRecords(cap.records(t)))
	}
	if got := warns[0]["path"]; got != path {
		t.Errorf("the warning must name the file in a 'path' field; want %q, got %v", path, got)
	}
	if _, ok := warns[0]["error"]; !ok {
		t.Errorf("the warning must carry the read failure in an 'error' field; got %v", warns[0])
	}

	// Two more passes, each with a real content change so the zoekt rebuild
	// runs — that is the pass shape the ERROR line fired on. The count is
	// cumulative because the capture is never reset.
	for pass, token := range []string{"tokentwo", "tokenthree"} {
		writeFixture(t, dir, "real.go", "// announce-fixture-"+token+"\n")
		bs.ReIndex(root)
		waitIndexed(t, events, root)

		if got := findLogged(cap.records(t), "warn", msgUnreadableFile); len(got) != 1 {
			t.Fatalf("after pass %d the cumulative %q count is %d, want 1 — a warning "+
				"per pass is the gh#136 mechanism at a different level:\n%s",
				pass+2, msgUnreadableFile, len(got), formatRecords(cap.records(t)))
		}
	}
}

// TestUnreadableFileIsIndexedOnceItsPermissionsAreRepaired is the argument
// against the other candidate remedy, made executable.
//
// That candidate was "record a negative result so the retry stops". Dropping
// the path keeps the retry instead, and the retry costs nothing: the walk
// re-attempts the read every pass anyway, so a repaired file is picked up on
// the very next tick with no marker to invalidate. A persisted marker would
// need an invalidation rule, and the only one available to this walk is mtime
// — which a chmod does not change (see
// TestRepairingPermissionsDoesNotChangeMtime), so the marker would outlive the
// repair and the file would stay out of the index until its CONTENT changed.
//
// The second half asserts the set is pruned rather than merely accumulated: a
// file that goes unreadable again is announced again, instead of being
// swallowed by a dedupe entry that was never cleared.
func TestUnreadableFileIsIndexedOnceItsPermissionsAreRepaired(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// repair-fixture-tokenone\n")
	path := makeUnreadable(t, dir, "secret.go")

	bs, root, events := newProjectIn(t, dir)
	cap := captureLogs(t, bs, hclog.Warn)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)
	requireSkipped(t, indexedProject(t, bs, root), "secret.go")

	if err := os.Chmod(path, 0644); err != nil {
		t.Fatalf("chmod 0644 %s: %v", path, err)
	}
	bs.ReIndex(root)
	waitIndexed(t, events, root)
	requireIndexed(t, indexedProject(t, bs, root), "secret.go")

	// Unreadable again. The content is unchanged and so is the mtime, so the
	// walk's mtime shortcut has a cached hash to reuse and never re-reads the
	// file; the build is what discovers the file is gone from under it, drops
	// it, and hands the next walk a path with no cached hash. Two passes,
	// therefore, and the ERROR fires exactly once on the way through.
	cap.reset()
	chmodUnreadable(t, path)
	writeFixture(t, dir, "real.go", "// repair-fixture-tokentwo\n")
	bs.ReIndex(root)
	waitIndexed(t, events, root)

	bs.ReIndex(root)
	waitIndexed(t, events, root)
	requireSkipped(t, indexedProject(t, bs, root), "secret.go")

	recs := cap.records(t)
	if got := findLogged(recs, "warn", msgUnreadableFile); len(got) != 1 {
		t.Errorf("a file that goes unreadable again must be announced again; want 1 %q "+
			"record, got %d:\n%s", msgUnreadableFile, len(got), formatRecords(recs))
	}
	requireLogged(t, recs, "error", "Error reading file", 1)

	// And it stays quiet from there: a third pass adds neither.
	writeFixture(t, dir, "real.go", "// repair-fixture-tokenthree\n")
	bs.ReIndex(root)
	waitIndexed(t, events, root)

	recs = cap.records(t)
	requireLogged(t, recs, "warn", msgUnreadableFile, 1)
	requireLogged(t, recs, "error", "Error reading file", 1)
}

// TestEvictClearsTheUnreadableSet is the leak half for a project that goes
// away. Every other entry leaves the set by being pruned on a later walk, but
// nothing will ever walk an evicted project's subtree again — so without this,
// its entries are permanent in a process that runs for weeks. Same argument,
// and same hook, as forgetGitTreeHashWarning.
func TestEvictClearsTheUnreadableSet(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// evict-fixture-tokenone\n")
	path := makeUnreadable(t, dir, "secret.go")

	bs, root, events := newProjectIn(t, dir)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	waitIndexed(t, events, root)

	bs.unreadableMu.Lock()
	_, held := bs.unreadableWarned[path]
	bs.unreadableMu.Unlock()
	if !held {
		t.Fatalf("control: %q is not in the unreadable set after the pass that dropped "+
			"it, so the assertion below would pass against an Evict that clears nothing", path)
	}

	bs.Evict(root)

	bs.unreadableMu.Lock()
	left := len(bs.unreadableWarned)
	bs.unreadableMu.Unlock()
	if left != 0 {
		t.Errorf("Evict left %d entry/entries in the unreadable set; an evicted project's "+
			"entries have no other route out", left)
	}
}

// TestUnreadableFileDoesNotMakeEveryPassLookChanged is this fix checked
// against the defect it remedies, in the other currency.
//
// gh#136 is a loop: an entry the pass can never resolve, retried every tick
// forever. The log line is how it was noticed, but a census that churns on
// every pass would be the same loop in work rather than words — every re-index
// would report changed content, the backoff-on-unchanged scheduler (mg-1236)
// would never back off, and every tick would rebuild the whole zoekt index.
// A drop is exactly the kind of change that could cause that, so the signal
// the scheduler reads is asserted directly.
func TestUnreadableFileDoesNotMakeEveryPassLookChanged(t *testing.T) {
	dir := shortTempDir(t)
	writeFixture(t, dir, "real.go", "// backoff-fixture-tokenone\n")
	makeUnreadable(t, dir, "secret.go")

	bs, root, events := newProjectIn(t, dir)

	req := plugin.IProcessProjectReq(plugin.ProcessProjectReq{PathVar: root})
	bs.Index(&req)
	if changed := waitIndexed(t, events, root); !changed {
		t.Fatalf("control: the initial index must report changed content, or the "+
			"assertions below cannot distinguish a working backoff from a broken "+
			"changed-signal (root %s)", root)
	}

	for pass := 2; pass <= 4; pass++ {
		bs.ReIndex(root)
		if changed := waitIndexed(t, events, root); changed {
			t.Fatalf("pass %d over an unchanged tree reported changed content; the "+
				"unreadable file is churning the census, so the periodic indexer "+
				"would rebuild zoekt on every tick forever", pass)
		}
	}
}

// TestRepairingPermissionsDoesNotChangeMtime pins the premise the design
// decision rests on, because it is a property of the filesystem rather than of
// this package and nothing else here would notice if it stopped holding.
//
// chmod updates ctime, not mtime. A failure marker keyed on mtime — the only
// key the walk has — would therefore survive the exact event that makes the
// file readable again, which is what rules that remedy out.
func TestRepairingPermissionsDoesNotChangeMtime(t *testing.T) {
	dir := shortTempDir(t)
	path := makeUnreadable(t, dir, "secret.go")

	before, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat before chmod: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatalf("chmod 0644: %v", err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat after chmod: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("chmod changed mtime on this filesystem (%v -> %v). The comment in "+
			"reconcileUnreadable rules out an mtime-keyed failure marker on the grounds "+
			"that it cannot be invalidated by a permission repair; that argument does "+
			"not hold here.", before.ModTime(), after.ModTime())
	}
}

// TestBuildSideReadFailureDropsThePathSoTheWalkRetriesIt covers the route into
// the forever-state that the walk-side fix alone does NOT close, and which
// this ticket's own remedy would otherwise have exhibited.
//
// A file that was readable when the walk hashed it, and became unreadable
// before the zoekt build reached it, is in Paths WITH a valid cached hash. The
// walk's mtime shortcut therefore fires for it on every subsequent pass, so
// the walk never re-attempts the read and never notices — while the build
// re-reads it and logs at ERROR on every rebuild. That is gh#136 exactly,
// reached from the other side.
//
// serializeProjectIndex is driven directly here: making the real walk lose a
// race with its own build is not something a test can arrange reliably, and
// the state it produces is the thing under test.
func TestBuildSideReadFailureDropsThePathSoTheWalkRetriesIt(t *testing.T) {
	dir := shortTempDir(t)
	g, root, _ := newProjectIn(t, dir)
	cap := captureLogs(t, g, hclog.Error)

	makeUnreadable(t, dir, "secret.go")
	writeFixture(t, dir, "real.go", "// buildside-fixture-tokenone\n")

	// The state a walk-then-chmod leaves behind: both files in the census,
	// both with hashes, neither carried as content.
	proj := &IndexedProject{
		Root:  root,
		Paths: []string{"real.go", "secret.go"},
		FileHashes: map[string]string{
			"real.go":   "deadbeef",
			"secret.go": "cafebabe",
		},
		FileMtimes: map[string]int64{"real.go": 1, "secret.go": 2},
		Status:     StatusIndexing,
	}
	t.Cleanup(func() { cleanPogoFolder(t, dir) })

	prev := IndexedProject{}
	g.serializeProjectIndex(proj, &prev, false, nil)

	requireLogged(t, cap.records(t), "error", "Error reading file", 1)

	// The drop is the point: with the path gone from Paths AND from
	// FileHashes, the next walk has no cached hash to shortcut on, so it must
	// re-read the file — and either succeeds, or fails once and says so.
	requireSkipped(t, proj, "secret.go")
	requireIndexed(t, proj, "real.go")

	// The in-memory entry the next pass compares against must carry the drop
	// too, otherwise the next pass reports spurious content churn forever.
	stored := indexedProject(t, g, root)
	requireSkipped(t, stored, "secret.go")

	// And so must the save file, which was written before the build ran: a
	// Load from it would otherwise restore the path the build just dropped.
	requireSaveFileSkips(t, root, "secret.go")

	// Re-running the same pass logs nothing further, because there is nothing
	// left in the census to fail on.
	cap.reset()
	g.serializeProjectIndex(proj, &prev, false, nil)
	requireLogged(t, cap.records(t), "error", "Error reading file", 0)
}

// requireSaveFileSkips asserts the persisted census does not carry rel.
func requireSaveFileSkips(t *testing.T, root, rel string) {
	t.Helper()
	saved := &IndexedProject{Root: root}
	searchDir, err := saved.makeSearchDir()
	if err != nil {
		t.Fatalf("makeSearchDir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(searchDir, saveFileName))
	if err != nil {
		t.Fatalf("read save file: %v", err)
	}
	var onDisk IndexedProject
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("parse save file: %v", err)
	}
	requireSkipped(t, &onDisk, rel)
}
