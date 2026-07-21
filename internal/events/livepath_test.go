package events

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// This file is the mg-abbf guard: the residual after mg-3f1b.
//
// mg-3f1b fixed resolvePath. It did not fix the MECHANISM underneath —
// DefaultLogPath stayed exported and stayed memoised behind a sync.Once, so any
// caller that reached it directly (there were three) still got the operator's
// live log under a test binary, and still got whichever POGO_HOME happened to
// be set when the FIRST caller in the process ran. mg-abbf unexported it as
// livePath and dropped the memo.
//
// Two distinct properties are pinned here, each with its own positive control:
//
//  1. livePath tracks POGO_HOME instead of freezing it (memoisedLivePath is the
//     known-bad fixture that must fail this).
//  2. Under a test binary, the exported LogPath cannot name operator state at
//     all — asserted by making the operator's log physically un-openable and
//     showing the work still succeeds.

// --- known-bad fixture -------------------------------------------------------

var (
	memoOnce sync.Once
	memoPath string
)

// memoisedLivePath is a verbatim replica of the pre-mg-abbf DefaultLogPath body.
// It exists so the freeze check below can be OBSERVED to fail against the shape
// that shipped. A check that has never failed is not a check.
func memoisedLivePath() string {
	memoOnce.Do(func() {
		memoPath = filepath.Join(config.PogoHome(), "events.log")
	})
	return memoPath
}

// checkTracksPogoHome resolves through the given resolver at two different
// POGO_HOME values and reports whether the second answer followed the second
// home. Returns "" on pass, else the reason it failed.
func checkTracksPogoHome(t *testing.T, resolve func() string) string {
	t.Helper()

	homeA := t.TempDir()
	homeB := t.TempDir()

	t.Setenv("POGO_HOME", homeA)
	first := resolve()

	t.Setenv("POGO_HOME", homeB)
	second := resolve()

	wantSecond := filepath.Join(homeB, "events.log")
	if second != wantSecond {
		return "after re-pointing POGO_HOME to " + homeB + " the resolver still returned " +
			second + " (frozen at the first call, which saw " + first + ")"
	}
	return ""
}

// TestLivePathTracksPogoHomeAndIsNotFrozen is the freeze half of mg-abbf.
//
// The old memo meant the destination for the process lifetime was decided by
// CALL ORDER: whichever caller ran first pinned the path for everyone after it.
// b399 demonstrated the consequence by skipping one test in one file and
// watching 131 log lines relocate to a different destination — nothing else
// changed. That is the "lottery" in this ticket's title.
func TestLivePathTracksPogoHomeAndIsNotFrozen(t *testing.T) {
	// --- POSITIVE CONTROL --------------------------------------------------
	// The shape that shipped must FAIL this check. If it ever passes, the
	// check has gone vacuous and the assertion below proves nothing.
	if reason := checkTracksPogoHome(t, memoisedLivePath); reason == "" {
		t.Fatal("positive control FAILED: the memoised (pre-mg-abbf) resolver PASSED the " +
			"tracks-POGO_HOME check, so the check cannot observe the frozen-path defect it " +
			"exists to catch and the assertion below is vacuous")
	}

	// --- THE GUARD ---------------------------------------------------------
	if reason := checkTracksPogoHome(t, livePath); reason != "" {
		t.Fatalf("REGRESSION (mg-abbf): livePath is memoised again: %s", reason)
	}
}

// TestLogPathIsStableAndNeverOperatorStateAcrossCallOrders is the call-order
// half. The acceptance criterion asks for stability as well as safety, because
// a control that resolves once in one order proves nothing about a defect whose
// whole character is order-dependence.
//
// So: interleave LogPath with POGO_HOME churn in several orders and require the
// answer to be (a) identical every time and (b) never under ANY of the homes
// that were in play.
func TestLogPathIsStableAndNeverOperatorStateAcrossCallOrders(t *testing.T) {
	SetLogPathForTesting("")

	homes := []string{t.TempDir(), t.TempDir(), t.TempDir()}

	var first string
	for i, home := range homes {
		// Resolve both before and after the re-point, so the sequence covers
		// "called before POGO_HOME moved" and "called after" for each home.
		got, err := LogPath()
		if err != nil {
			t.Fatalf("LogPath (before setting home %d): %v", i, err)
		}
		t.Setenv("POGO_HOME", home)
		after, err := LogPath()
		if err != nil {
			t.Fatalf("LogPath (after setting home %d): %v", i, err)
		}

		for _, p := range []string{got, after} {
			if first == "" {
				first = p
			}
			if p != first {
				t.Fatalf("LogPath is not stable under POGO_HOME churn: got %s, earlier got %s "+
					"— the destination is call-order dependent, which is the mg-abbf lottery", p, first)
			}
		}
	}

	// (b) never under any home that was in play, including the operator's real
	// one (whatever POGO_HOME was when this binary started).
	for _, home := range append(homes, config.PogoHome()) {
		if rel, err := filepath.Rel(home, first); err == nil && !strings.HasPrefix(rel, "..") {
			t.Fatalf("LogPath resolved to %s, which is inside the pogo state dir %s", first, home)
		}
	}
}

// TestLogPathNeitherGrowsNorOpensTheOperatorLog is the end-to-end control the
// mayor asked for: the live log must be neither grown NOR opened.
//
// The two halves are separate subtests ON PURPOSE, because the setup that makes
// one of them firable makes the other vacuous. "Not opened" wants the live log
// un-openable (mode 0000) so that any touch fails loudly — but a 0000 file
// cannot be appended to, so a growth assertion over it is trivially satisfied
// and could never fire. "Not grown" therefore uses a writable live log, and
// "not opened" uses an un-openable one. Each was mutation-tested against a
// defect that only its own setup can observe.
func TestLogPathNeitherGrowsNorOpensTheOperatorLog(t *testing.T) {
	// "Not grown", by CENSUS. The census names where it looked and walks the
	// whole state dir rather than stat-ing one expected path — a count taken at
	// ONE path while the leak went somewhere else is how b399's "full suite
	// leaked 0" had to be retracted.
	//
	// Firable: any change that appends to the live log (or drops a second
	// events.log anywhere under the state dir) trips this, because the live log
	// here is writable.
	t.Run("not grown", func(t *testing.T) {
		home, live, before := seedOperatorLog(t)

		path, err := LogPath()
		if err != nil {
			t.Fatalf("LogPath: %v", err)
		}
		if path == live {
			t.Fatalf("REGRESSION (mg-abbf): LogPath returned the operator's live log %s under a test binary", live)
		}

		Emit(context.Background(), Event{
			EventType: "test_livepath_probe",
			Agent:     "cat-mg-abbf",
			Details:   map[string]any{"probe": "mg-abbf"},
		})

		after, err := os.Stat(live)
		if err != nil {
			t.Fatalf("stat operator log after: %v", err)
		}
		if after.Size() != before.Size() {
			t.Fatalf("REGRESSION (mg-abbf): the operator log %s grew from %d to %d bytes",
				live, before.Size(), after.Size())
		}

		var found []string
		if err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasPrefix(d.Name(), "events.log") {
				found = append(found, p)
			}
			return nil
		}); err != nil {
			t.Fatalf("census walk of %s: %v", home, err)
		}
		if len(found) != 1 || found[0] != live {
			t.Fatalf("REGRESSION (mg-abbf): census of the whole state dir %s found %v; "+
				"expected exactly the seeded %s and nothing else", home, found, live)
		}
		t.Logf("census: walked ALL of %s (recursive), found %d events.log* file(s): %v — "+
			"the seeded one, %d bytes, unchanged", home, len(found), found, after.Size())
	})

	// "Not opened", POSITIVELY. The operator's log is chmod 0000, so any open(2)
	// of it fails with EACCES. The test then does the work — resolve, Emit, read
	// back — and requires it to SUCCEED. Success is only possible if nothing
	// ever opened the live path.
	//
	// There is deliberately NO path comparison here: the assertion has to be the
	// open itself, or the control would keep failing at a cheaper check and the
	// open-based proof would never be exercised. Verified by mutation — pointing
	// the sandbox at livePath() fails this at the read-back with EACCES, naming
	// the live log.
	t.Run("not opened", func(t *testing.T) {
		_, live, _ := seedOperatorLog(t)
		if err := os.Chmod(live, 0o000); err != nil {
			t.Fatalf("chmod operator log: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(live, 0o644) })

		path, err := LogPath()
		if err != nil {
			t.Fatalf("LogPath: %v", err)
		}
		Emit(context.Background(), Event{
			EventType: "test_livepath_probe",
			Agent:     "cat-mg-abbf",
			Details:   map[string]any{"probe": "mg-abbf"},
		})

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("REGRESSION (mg-abbf): could not read back the record at the resolved path %s: %v "+
				"— that path is the operator's live log (%s), and this is EACCES from its 0000 mode, "+
				"i.e. resolution reached operator state", path, err, live)
		}
		if !strings.Contains(string(data), "test_livepath_probe") {
			t.Fatalf("Emit did not write to the resolved path %s", path)
		}
	})
}

// seedOperatorLog points POGO_HOME at a stand-in for the operator's ~/.pogo and
// seeds a live events.log with one sentinel record. It returns the state dir,
// the live log path, and its pre-work FileInfo.
//
// A temp dir rather than the real ~/.pogo is deliberate: no test may depend on
// — or perturb — the operator's actual state, and a control that reads the real
// events.log in order to prove it did not read it is self-defeating.
func seedOperatorLog(t *testing.T) (home, live string, before os.FileInfo) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("POGO_HOME", home)
	SetLogPathForTesting("")

	live = filepath.Join(home, "events.log")
	const sentinel = `{"schema_version":1,"event_type":"operator_sentinel","agent":"human","details":{}}`
	if err := os.WriteFile(live, []byte(sentinel+"\n"), 0o644); err != nil {
		t.Fatalf("seed operator log: %v", err)
	}
	before, err := os.Stat(live)
	if err != nil {
		t.Fatalf("stat operator log: %v", err)
	}
	return home, live, before
}

// TestNoExportedSymbolReturnsTheLivePathUnderTest states the property mg-abbf
// actually buys, in the form b399 asked for: "no way to express 'the live path'
// from test code at all."
//
// This is a compile-time-ish assertion written as a runtime one: LogPath is the
// only exported path accessor in this package, and it is sandboxed. If a future
// change re-exports a live-path accessor, the reviewer has to delete this test
// to do it, which is the signal.
func TestNoExportedSymbolReturnsTheLivePathUnderTest(t *testing.T) {
	SetLogPathForTesting("")

	home := t.TempDir()
	t.Setenv("POGO_HOME", home)

	got, err := LogPath()
	if err != nil {
		t.Fatalf("LogPath: %v", err)
	}
	if got == livePath() {
		t.Fatalf("LogPath returned the live path %s under a test binary", got)
	}
	if rel, err := filepath.Rel(home, got); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("LogPath returned %s, inside the pogo state dir %s", got, home)
	}
}
