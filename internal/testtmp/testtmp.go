// Package testtmp owns the temp directories a pogo TEST BINARY creates for
// itself — the ones the testing package's t.TempDir() cannot own because they
// outlive any single test.
//
// # Why this package exists
//
// Several packages resolve a test-mode default lazily and process-wide: the
// witness store (internal/agent), the event log (internal/events), the
// claim-release store (internal/agent), the ghintake and ghteardown mg stores.
// Each is a sync.Once wrapping os.MkdirTemp, and each is correct about the
// thing it was written for — never hand a test binary the LIVE store — while
// being silent about the thing it was not: os.MkdirTemp registers no cleanup,
// there is no *testing.T in scope to hang one on, and a test binary's generated
// main() ends in os.Exit, which runs no deferred function anywhere.
//
// So every `go test ./...` left one directory per package per purpose behind,
// forever. Measured 2026-08-12 on this box: 37,083 entries and 61 GB in a
// single $TMPDIR, of which ~12,000 were ours by prefix. It was not a curiosity
// — at 255 MiB free, ./build.sh failed in the refinery gate with "no space left
// on device" and a healthy branch was rejected as defective (mg-b41f).
//
// # What it does instead
//
// One directory in $TMPDIR — Root() — with every test-mode directory nested
// inside it, named for the process that owns it. That alone fixes the shape of
// the problem: $TMPDIR's entry count stops growing with the number of test runs
// and starts being 1.
//
// Nesting alone would not bound the DISK, so Root() is also swept. Reap runs
// once per process, on the first Dir call, and the rule it applies is
// OWNERSHIP:
//
//   - the name encodes a pid and that process is alive — keep, at any age;
//   - the name encodes a pid and that process is gone — remove;
//   - the name encodes no pid — remove once it is older than StaleAfter.
//
// Ownership rather than age, because age is the reading that gets this wrong in
// the expensive direction. This box runs several polecats and a refinery gate
// concurrently, so at any instant a sibling entry is very likely another
// agent's LIVE test binary; a sweep that deleted a running suite's fixtures
// would surface as a branch defect, which is exactly the failure this package
// exists to stop, arriving by a new route. A pid answers "is anyone still using
// this" directly, and signal 0 answers it without a race worth naming: a
// process that dies between the readdir and the remove needed the directory
// right up until it didn't.
//
// The cost of that rule is that a crashed run's fixtures are gone by the next
// `go test` rather than sitting in $TMPDIR for a while. That is the intended
// trade: the instrument for a failed test is its output, and the alternative —
// keeping unowned state around on the chance someone looks — is the habit that
// produced 61 GB.
//
// # What it deliberately does not do
//
// It does not touch anything outside Root(). In particular it has no opinion
// about $TMPDIR at large: reclaiming what has already leaked is a separate,
// careful operation from stopping the leak, and this package is the second one.
package testtmp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// RootName is the single directory this package owns inside $TMPDIR.
//
// Short and unmistakably ours: a human staring at a `ls $TMPDIR` that has gone
// wrong should be able to tell in one line whether pogo is the cause.
const RootName = "pogo-test-tmp"

// StaleAfter is how long an entry whose name encodes NO pid must have been idle
// before Reap will remove it.
//
// It is the fallback rule, not the main one — every name this package writes
// carries a pid, so an entry reaching this branch was put here by something
// else, or by a version of this package that predates the naming. Two hours,
// against a per-package test budget of 20 minutes (scripts/go-test-budget.sh)
// and a full suite that measures well under an hour; the margin is caution
// about the mtime reading, which does not advance for writes NESTED inside a
// directory, only for entries created or removed directly in it.
var StaleAfter = 2 * time.Hour

var (
	seq       atomic.Int64
	reapOnce  sync.Once
	rootOnce  sync.Once
	rootErr   error
	rootCache string
)

// Root returns the directory this package owns inside $TMPDIR, creating it on
// first use.
//
// It is resolved once per process. $TMPDIR is read at that moment, so a test
// that pins TMPDIR must do so before the first Dir call — which is what
// TestMain is for, and what every caller in this repo already does.
func Root() (string, error) {
	rootOnce.Do(func() {
		rootCache = filepath.Join(os.TempDir(), RootName)
		// Lstat, not Stat, and it runs before MkdirAll rather than instead of
		// it. $TMPDIR is per-user and 0700 on darwin, but os.TempDir falls back
		// to a world-writable /tmp when TMPDIR is unset — which is the case in
		// CI — and there a pre-planted symlink at this name would have the sweep
		// deleting a directory tree of somebody else's choosing. MkdirAll
		// follows the link and reports success, so the refusal has to be
		// explicit.
		if fi, err := os.Lstat(rootCache); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			rootErr = fmt.Errorf("testtmp: %s is a symlink; refusing to create or sweep through it", rootCache)
			return
		}
		// 0o700: these hold test fixtures that stand in for the fleet's own
		// state stores.
		rootErr = os.MkdirAll(rootCache, 0o700)
	})
	return rootCache, rootErr
}

// Dir creates and returns a directory private to this process, named for
// purpose.
//
// purpose is a short label naming what the directory holds ("witness",
// "events"), not a path: it appears verbatim in the directory name, so it must
// contain no dot and no separator. Dir rejects such a label rather than
// producing a name Reap cannot parse — an unparseable name is one Reap can only
// age out, which silently converts a pid-owned entry into a two-hour one.
//
// The first call in a process also runs Reap. That is the only sweep trigger:
// the sweep costs one readdir of a directory holding, at steady state, the live
// test binaries on this box, and paying it once per process keeps it off every
// subsequent lookup.
func Dir(purpose string) (string, error) {
	if purpose == "" {
		return "", fmt.Errorf("testtmp: empty purpose")
	}
	if strings.ContainsAny(purpose, "./"+string(filepath.Separator)) {
		return "", fmt.Errorf("testtmp: purpose %q must not contain a dot or a separator", purpose)
	}
	root, err := Root()
	if err != nil {
		return "", fmt.Errorf("testtmp: create %s: %w", root, err)
	}
	reapOnce.Do(func() { Reap(root) })

	dir := filepath.Join(root, entryName(purpose, os.Getpid(), seq.Add(1)))
	// os.Mkdir rather than MkdirAll, because "it already exists" has to be an
	// answer here rather than a success.
	//
	// pids are reused. The sweep keeps any entry whose pid is alive, and once
	// this process has been given a recycled pid, the DEAD namesake's directory
	// reads as live and is kept — so MkdirAll would hand this binary a stale
	// witness store or events log belonging to a run that ended days ago, and
	// the resulting phantom records would look like a defect in whatever test
	// read them. It cannot be a directory this process made: seq is monotonic
	// and this value has not been issued before. So it is the namesake's, its
	// owner is provably gone, and clearing it is the one correct move.
	if err := os.Mkdir(dir, 0o700); err != nil {
		if !os.IsExist(err) {
			return "", fmt.Errorf("testtmp: create %s: %w", dir, err)
		}
		if err := os.RemoveAll(dir); err != nil {
			return "", fmt.Errorf("testtmp: clear a recycled pid's stale %s: %w", dir, err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", fmt.Errorf("testtmp: create %s: %w", dir, err)
		}
	}
	return dir, nil
}

// entryName builds the on-disk name Reap parses back.
//
// purpose first so `ls` sorts by what a directory IS, which is what a human
// reading a swollen root wants grouped; pid second so ownership is one field
// away, not a search.
func entryName(purpose string, pid int, n int64) string {
	return fmt.Sprintf("%s.%d.%d", purpose, pid, n)
}

// ownerPID recovers the pid encoded in an entry name, or ok=false when the name
// is not one of ours.
func ownerPID(name string) (int, bool) {
	parts := strings.Split(name, ".")
	if len(parts) != 3 {
		return 0, false
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// pidAlive reports whether pid names a live process.
//
// Signal 0 is the portable existence probe: it delivers nothing and reports
// whether it COULD have. EPERM is a live process owned by another user, and is
// therefore an ALIVE answer, not an error — reading it as "gone" is how a sweep
// deletes a directory belonging to something it merely cannot signal.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

// Reap removes entries in root that no live process owns — see the package doc
// for the rule and why it is ownership rather than age. Errors are swallowed: a
// sweep that cannot delete something has lost nothing a caller can act on, and
// it must never be the reason a test fails.
//
// Exported so its behaviour can be tested directly against a fixture root,
// which is the only way to observe the direction that matters — that a LIVE
// owner's entry survives.
func Reap(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-StaleAfter)
	for _, e := range entries {
		if pid, ok := ownerPID(e.Name()); ok {
			if !pidAlive(pid) {
				os.RemoveAll(filepath.Join(root, e.Name()))
			}
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.RemoveAll(filepath.Join(root, e.Name()))
	}
}
