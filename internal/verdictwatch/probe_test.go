package verdictwatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/mgcontract"
)

// This file is the reason the port exists.
//
// The original verdictwatch's two CONSTRUCTIVE probes were dead for two days
// while its read-only census stayed green, so a reader looking at the census had
// no way to tell a working detector from a broken one. These tests are in
// `go test ./...`, which `build.sh` runs, which the refinery gate runs on every
// merge — so the same class of breakage now costs one merge, not two days.
//
// THE TWO OUTCOMES ARE KEPT APART DELIBERATELY:
//
//	mg is not installed                 -> SKIP, as every live mg-driven test in
//	                                       this suite has always done.
//	mg IS installed and the fixture
//	cannot be built                     -> FAIL. This is the mg-d639 shape
//	                                       exactly, and it is the branch that
//	                                       must never be quiet.
//
// A t.Skip on the second would reproduce the original defect inside its own
// repair.

// clausesTheProbeRestsOn are the mg behaviours the constructed fixture needs.
// Naming them means that when mg moves, the red lands ONCE, by name, in
// internal/mgcontract's TestTheDeclaredMgContractStillHolds — which says which
// behaviour changed, which mg item created the one pogo expects, and everything
// resting on it — instead of as an unexplained failure in a detector's probe.
var clausesTheProbeRestsOn = []string{
	mgcontract.NewPrintsTheCreatedID,
	mgcontract.NewRecordsTheCreatorInFrontmatter,
	mgcontract.DoneResultSidecarRecordsTheBranch,
	// The verdict in that same sidecar is what the ROUTING class and the report's
	// emitted retrieval instruction both rest on (mg-4e02): if `mg done --result`
	// stopped preserving it, every recoverable row would read as an unrecoverable
	// one and the path the report prints would return null.
	mgcontract.DoneResultSidecarPreservesTheVerdict,
	mgcontract.EventsJSONLRecordsLandings,
	mgcontract.MailSendCreateRegistersANewMailbox,
	mgcontract.MailIsAMaildirUnderTheStore,
}

// TestTheProbeGoesRedOnAKnownBadInputAndGreenOnAKnownGood is the acceptance
// criterion for mg-f5dd, in its sharpened form: not "the probe runs" but the
// probe GOES RED on a known-bad input and GREEN on a known-good one, both
// demonstrated. Same instrument, two inputs, two verdicts.
func TestTheProbeGoesRedOnAKnownBadInputAndGreenOnAKnownGood(t *testing.T) {
	mgcontract.Require(t, clausesTheProbeRestsOn...)

	res := Probe()
	if res.InstrumentFailure() {
		// NOT a skip. mg is installed (Require established that), so a fixture
		// that will not build is a real breakage in the probe's premise — the
		// state the original sat in, undetected, for two days.
		t.Fatalf("the constructive probe could not be BUILT, so it proved nothing:\n%s\n\n"+
			"mg IS installed, so this is not an absent-tooling skip. Something the fixture "+
			"needs has moved. If it is a DECLARED mg behaviour, internal/mgcontract names it; "+
			"if not, that behaviour needs a clause before this test can be trusted again.", res.Blind)
	}
	if !res.Passed() {
		t.Fatalf("the constructive probe did not pass:\n%s", res.Render())
	}

	// The pass above is not enough on its own: a probe whose two arms agreed
	// would also "pass" if the detector answered the same thing to everything.
	// Assert the DISCRIMINATION explicitly.
	if len(res.Arms) < 2 {
		t.Fatalf("the probe produced %d arm(s); the matched pair needs two", len(res.Arms))
	}
	bad, good := res.Arms[0], res.Arms[1]
	if bad.Got == good.Got {
		t.Fatalf("both arms of the matched pair got %s; a detector that answers the same thing "+
			"to a dropped verdict and a delivered one is an alarm, not a detector", bad.Got)
	}
	if bad.Got != Dropped || good.Got != Delivered {
		t.Fatalf("known-bad got %s (want %s), known-good got %s (want %s)",
			bad.Got, Dropped, good.Got, Delivered)
	}
	t.Logf("known-bad input  -> %s (RED)\nknown-good input -> %s (GREEN)", bad.Got, good.Got)
}

// TestAProbeThatCannotBeBuiltIsBlindNotAPass exercises the branch that matters
// most and is hardest to observe in the wild: the probe's own setup failing.
//
// mg-d639 landed here. It was a CORRECT change — an unknown mail recipient
// became a refusal rather than a silent create — and it killed the original's
// constructive probes about 22 hours later, silently. This asserts that the same
// class of breakage cannot read as a pass.
func TestAProbeThatCannotBeBuiltIsBlindNotAPass(t *testing.T) {
	orig := mgLookup
	t.Cleanup(func() { mgLookup = orig })
	mgLookup = func() (string, error) { return "", errors.New("no mg for this test") }

	res := Probe()
	if !res.InstrumentFailure() {
		t.Fatal("a probe that could not resolve mg did not report itself blind")
	}
	if res.Passed() {
		t.Fatal("a blind probe reports Passed(); a run that measured nothing must never read as a pass")
	}
	out := res.Render()
	if !strings.HasPrefix(out, "INSTRUMENT FAILURE") {
		t.Errorf("the blind probe's report does not LEAD with the banner:\n%s", out)
	}
}

// TestTheProbeWritesNothingOutsideItsThrowawayStore. The probe is the only code
// in this package that WRITES, and it drives a binary whose default store is the
// fleet's live one. A probe that filed work items and sent mail into ~/.macguffin
// would be worse than no probe at all.
func TestTheProbeWritesNothingOutsideItsThrowawayStore(t *testing.T) {
	mgcontract.Require(t, clausesTheProbeRestsOn...)

	res := Probe()
	if res.InstrumentFailure() {
		t.Fatalf("probe could not be built: %s", res.Blind)
	}
	if res.Store == "" {
		t.Fatal("the probe reported no store path, so where it wrote cannot be checked")
	}
	if !strings.Contains(res.Store, "verdictwatch-probe-") {
		t.Errorf("the probe's store is %s, want a throwaway temp directory", res.Store)
	}
	if sandbox != nil && !sandbox.Contains(res.Store) {
		// os.MkdirTemp honours TMPDIR, which the sandbox does not pin, so this
		// is a warning rather than a failure — the load-bearing checks are that
		// the path is a temp dir and that it is gone.
		t.Logf("note: the probe's store %s is outside the sandbox root %s (TMPDIR is not sandboxed)",
			res.Store, sandbox.Root)
	}
	if _, err := os.Stat(res.Store); !os.IsNotExist(err) {
		t.Errorf("the throwaway store %s still exists after the probe returned (%v); it must be removed", res.Store, err)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if strings.HasPrefix(res.Store, filepath.Join(home, ".macguffin")) {
			t.Fatalf("the probe built its fixture INSIDE a macguffin store at %s", res.Store)
		}
	}
}

// ---------------------------------------------------------------- edge cases

// liveStore stands up a throwaway macguffin workspace driven by the real binary.
func liveStore(t *testing.T) *store {
	t.Helper()
	bin, err := mgBinary()
	if err != nil {
		t.Skipf("%v", err)
	}
	s := &store{bin: bin, root: filepath.Join(t.TempDir(), "macguffin")}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code, err := s.run("", "init"); err != nil {
		t.Fatalf("mg init: %v", err)
	} else if code != 0 {
		t.Fatalf("mg init exited %d:\n%s", code, out)
	}
	return s
}

func (s *store) scanFiler(t *testing.T, filer string) Report {
	t.Helper()
	rep, err := Scan(Options{Root: s.root, Filer: filer})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rep.InstrumentFailure() {
		t.Fatalf("the scan of the constructed store reported itself blind: %v", rep.Blind)
	}
	return rep
}

// TestLiveEdgeCases drives the real mg through the six constructions the
// original named, three of which its own predictions document flagged in advance
// as the ones most likely to break the first form of this code.
//
// These are live rather than hand-built on purpose: a hand-built fixture asserts
// what this package BELIEVES mg writes, and cannot notice mg writing something
// else. The hermetic tests in verdictwatch_test.go cover the same predicate
// against a fixture; neither half substitutes for the other.
func TestLiveEdgeCases(t *testing.T) {
	mgcontract.Require(t, clausesTheProbeRestsOn...)
	s := liveStore(t)
	const filer = "filer-a"

	// Seed the filer's mailbox so it EXISTS. Without this every case below would
	// also be a mailbox-less filer, and the assertions would pass for the wrong
	// reason — which is its own kind of vacuous.
	if _, err := s.sendMail(filer, "somebody-else", "unrelated", "seed\n"); err != nil {
		t.Fatalf("seed the filer's mailbox: %v", err)
	}

	t.Run("an archived item is still judged", func(t *testing.T) {
		id, err := s.newItem(filer, "archived, verdict dropped", "no verdict\n")
		if err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if err := s.land(id, "wc1"); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if err := s.archiveItem(id); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if got := statusOf(s.scanFiler(t, filer), id); got != Dropped {
			t.Errorf("an archived item with no verdict is %s, want %s", got, Dropped)
		}
	})

	t.Run("a verdict the filer archived is still delivered", func(t *testing.T) {
		id, err := s.newItem(filer, "verdict delivered then archived", "\n")
		if err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		msg, err := s.sendMail(filer, "wd1", id+" VERDICT: then archived", "verdict\n")
		if err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if err := s.land(id, "wd1"); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if err := s.archiveMail(filer, msg); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		// VERIFY THE SETUP, do not report it. The original asserted DELIVERED
		// against a verdict that had never been archived, because its archive
		// step silently no-opped: a vacuous pass in the file whose whole job was
		// to prove the detector was not vacuous.
		list, code, err := s.run("", "mail", "list", filer, "--all", "--json")
		if err != nil || code != 0 {
			t.Fatalf("verify the setup: %v %d %s", err, code, list)
		}
		if strings.Contains(list, msg) {
			t.Fatalf("SETUP DID NOT TAKE: %s is still in the active mailbox, so this case would assert nothing:\n%s", msg, list)
		}
		if got := statusOf(s.scanFiler(t, filer), id); got != Delivered {
			t.Errorf("a verdict the filer archived is %s, want %s", got, Delivered)
		}
	})

	t.Run("a filer with no mailbox at all", func(t *testing.T) {
		const nobox = "filer-nobox"
		id, err := s.newItem(nobox, "filer has never received any mail", "\n")
		if err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if err := s.land(id, "we1"); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		rep := s.scanFiler(t, nobox)
		if got := statusOf(rep, id); got != Dropped {
			t.Errorf("a mailbox-less filer's item is %s, want %s", got, Dropped)
		}
		if len(rep.MissingBoxes) != 1 || rep.MissingBoxes[0] != nobox {
			t.Errorf("MissingBoxes = %v, want [%s]; an absent mailbox must not be silently treated as an empty one",
				rep.MissingBoxes, nobox)
		}
	})

	t.Run("a verdict mailed to somebody who is not the filer", func(t *testing.T) {
		id, err := s.newItem(filer, "verdict mailed to mayor, not the filer", "\n")
		if err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if _, err := s.sendMail("mayor", "wf1", id+" VERDICT sent to the wrong addressee", "the filer never sees this\n"); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if err := s.land(id, "wf1"); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		rep := s.scanFiler(t, filer)
		if got := statusOf(rep, id); got != Dropped {
			t.Errorf("a verdict mailed only to mayor is %s, want %s", got, Dropped)
		}
		for _, row := range rep.Rows {
			if row.ID == id && row.MailsElsewhere < 1 {
				t.Errorf("MailsElsewhere = %d, want at least 1; a live worker that mailed the wrong "+
					"addressee must not look like a dead one", row.MailsElsewhere)
			}
		}
	})

	t.Run("an item that has not landed is absent", func(t *testing.T) {
		id, err := s.newItem(filer, "claimed, still in flight", "\n")
		if err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if out, code, err := s.run("wg1", "claim", id); err != nil || code != 0 {
			t.Fatalf("build fixture: %v %d %s", err, code, out)
		}
		if got := statusOf(s.scanFiler(t, filer), id); got != "ABSENT" {
			t.Errorf("an item still in flight is reported as %s; it must not be in the report at all", got)
		}
	})

	t.Run("a sender whose name merely contains the worker's", func(t *testing.T) {
		id, err := s.newItem(filer, "near-miss sender name", "\n")
		if err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if _, err := s.sendMail(filer, "wh1x", id+" not from the worker", "near miss\n"); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if err := s.land(id, "wh1"); err != nil {
			t.Fatalf("build fixture: %v", err)
		}
		if got := statusOf(s.scanFiler(t, filer), id); got != Dropped {
			t.Errorf("a message from wh1x for a worker named wh1 is %s, want %s", got, Dropped)
		}
	})
}
