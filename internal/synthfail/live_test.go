package synthfail

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The 2026-07-22 fleet outage window, from mg-18d0's timeline
// (docs/investigations/fleet-auth-expiry-2026-07-22.md): the credential died
// between 23:01:28Z and 23:10:26Z on the 21st, and the first real model turn
// came back at 22:40:37Z on the 22nd.
var (
	incidentOnset = time.Date(2026, 7, 21, 23, 10, 0, 0, time.UTC)
	incidentEnd   = time.Date(2026, 7, 22, 22, 30, 30, 0, time.UTC)
)

// TestScan_LiveIncidentTranscripts verifies the detector against the ORIGINAL,
// unmodified transcripts of the incident — both halves of its job, over the
// identical window, on the same day, in the same fleet:
//
//   - pm-pogo consumed 143 nudges and failed every one. It must FIRE.
//   - doctor had no mail-check schedule, received no nudges, and therefore
//     emitted nothing at all — mg-18d0 records it at zero synthetic turns. That
//     is a real transcript of an agent producing no work, and it must STAY
//     SILENT. It is the natural experiment the incident handed us: identical
//     window, identical fleet, identical shared credential, opposite file-level
//     signature.
//
// A detector verified only against pm-pogo would prove nothing, because an
// unconditional "yes" passes that test. This one can only pass by
// discriminating.
//
// # It reads live developer state, so its guard must ask about the FIXTURE
//
// This test's inputs are one machine's real session transcripts, which the
// harness rotates. Its guard used to skip only when [Locate] found ZERO files
// for an agent — a presence check that cannot see the thing it is checking
// for. On 2026-09-03 it found 22 pm-pogo transcripts, every one of them NEWER
// than the incident, answered "the fixture is present", and turned main red for
// every merge request in the fleet (mg-ae5a). A test that depends on live
// developer state has a decay date nobody wrote down; the guard is what writes
// it down.
//
// So each sub-test now asks whether the transcripts reach ITS OWN asserted
// window, and skips naming that window when they do not. The guard is
// deliberately independent of [Scan] and of [turn.isSyntheticFailure] — it
// counts records by timestamp alone. A guard that asked the detector whether it
// found anything would skip whenever the detector broke, which is the one
// answer that must stay a failure.
//
// The checked-in testdata fixtures carry the same assertions into CI on every
// machine, including this one.
func TestScan_LiveIncidentTranscripts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	glob := func(agent string) string {
		return filepath.Join(".claude", "projects", "-Users-daniel--pogo-agents-"+agent, "*.jsonl")
	}
	// extent measures what is actually on this machine for one agent, over one
	// window, without consulting the detector under test.
	extent := func(t *testing.T, agent string, from, to time.Time) transcriptExtent {
		t.Helper()
		return measureTranscripts(t, Locate(home, []string{glob(agent)}, time.Time{}), from, to)
	}

	window := incidentEnd.Sub(incidentOnset)

	t.Run("pm-pogo FIRES across the outage window", func(t *testing.T) {
		// The assertion is about failing turns INSIDE the window, so the guard
		// is the strong one: some record must actually fall inside it.
		if e := extent(t, "pm-pogo", incidentOnset, incidentEnd); e.inWindow == 0 {
			t.Skip(e.absentMessage("pm-pogo", incidentOnset, incidentEnd))
		}
		got := Scan(home, []string{glob("pm-pogo")}, Options{Now: incidentEnd, Window: window})

		if got.State != StateFailing {
			t.Fatalf("state = %v, want StateFailing (report: %+v)", got.State, got)
		}
		if got.Reason != ReasonAuthFailed {
			t.Errorf("reason = %q, want %q", got.Reason, ReasonAuthFailed)
		}
		if !got.SuppressRestart() {
			t.Error("SuppressRestart() = false; this is the state in which ~66 restarts would have been issued")
		}
		// mg-18d0 counted 143 failed turns for pm-pogo across the whole
		// 23h30m. The window here is very slightly narrower than the incident,
		// so assert the order of magnitude rather than the exact number.
		if got.Count < 100 {
			t.Errorf("count = %d, want ~143 (mg-18d0's measurement)", got.Count)
		}
		t.Logf("pm-pogo: FIRING — %d failing turns, %s..%s, reason=%s, detail=%q",
			got.Count, got.First.Format(time.RFC3339), got.Last.Format(time.RFC3339), got.Reason, got.Detail)
	})

	t.Run("doctor STAYS SILENT across the identical window", func(t *testing.T) {
		// doctor's whole point is that it wrote NOTHING in this window, so
		// "some record inside the window" would be unsatisfiable by
		// construction and would skip this control even on the machine the
		// incident happened on. The satisfiable question is whether a doctor
		// transcript from that era is still here at all: one file whose own
		// records straddle the window. Without that, StateQuiet is a statement
		// about August and not about the outage.
		if e := extent(t, "doctor", incidentOnset, incidentEnd); !e.straddles {
			t.Skip(e.absentMessage("doctor", incidentOnset, incidentEnd))
		}
		got := Scan(home, []string{glob("doctor")}, Options{Now: incidentEnd, Window: window})

		if got.State != StateQuiet {
			t.Fatalf("state = %v, want StateQuiet — doctor emitted no synthetic turns that day; "+
				"a detector that fires here is not discriminating, it is just alarming (report: %+v)", got.State, got)
		}
		if got.SuppressRestart() {
			t.Fatal("SuppressRestart() = true for an agent that was merely producing nothing — this would disable wedge recovery")
		}
		t.Logf("doctor: SILENT — %d transcript files read, 0 failing turns, restart NOT suppressed", got.Files)
	})

	// doctor is a negative control for an agent producing NOTHING. It is not a
	// control for an agent producing PLENTY — its window is empty, so a
	// detector that naively counted every record would still pass it. (Measured:
	// it does. This sub-test exists because that control was run and came back
	// green against a deliberately broken reader.)
	//
	// pm-pogo's RECOVERY window is the missing control: the same agent, the same
	// file, the adjacent hour, 63 real model turns. A reader that counts
	// anything other than the structural signature fires here.
	t.Run("pm-pogo STAYS SILENT once it is doing real work again", func(t *testing.T) {
		recoveryStart := time.Date(2026, 7, 22, 22, 40, 0, 0, time.UTC)
		recoveryEnd := time.Date(2026, 7, 22, 23, 30, 0, 0, time.UTC)

		// This one asserts StateQuiet over a window that is supposed to be FULL
		// of real work. With the window empty it passes for the wrong reason —
		// which is exactly what it did on 2026-09-03, alongside the failure
		// that made the decay visible. Require the 63 real turns to be here.
		if e := extent(t, "pm-pogo", recoveryStart, recoveryEnd); e.inWindow == 0 {
			t.Skip(e.absentMessage("pm-pogo", recoveryStart, recoveryEnd))
		}

		got := Scan(home, []string{glob("pm-pogo")}, Options{
			Now:    recoveryEnd,
			Window: recoveryEnd.Sub(recoveryStart),
		})

		if got.State != StateQuiet {
			t.Fatalf("state = %v, want StateQuiet — this window holds 63 REAL model turns; "+
				"firing here means the reader is counting records rather than recognising the signature (report: %+v)", got.State, got)
		}
		if got.SuppressRestart() {
			t.Fatal("SuppressRestart() = true for an agent that had recovered and was working normally")
		}
		t.Logf("pm-pogo (recovered): SILENT across a window of real work — %d transcript files read", got.Files)
	})
}

// transcriptExtent is what a set of live transcript files actually contains,
// measured against one window. Every field is derived from record TIMESTAMPS
// only — never from the detector under test, so a broken detector cannot talk
// this guard into skipping.
type transcriptExtent struct {
	// files is how many transcripts were located and read.
	files int
	// earliest and latest bound every record read, across all files.
	earliest, latest time.Time
	// inWindow counts records timestamped inside the window, inclusive.
	inWindow int
	// straddles is true when SOME SINGLE file's own record span overlaps the
	// window — i.e. that file was being written during the window. It is the
	// weaker question, and the only satisfiable one for an agent whose correct
	// behaviour in the window was to write nothing.
	straddles bool
}

// absentMessage explains a skip in the terms that matter to whoever reads it a
// year from now: which window was asked for, and what is here instead.
func (e transcriptExtent) absentMessage(agent string, from, to time.Time) string {
	span := "nothing readable"
	if !e.earliest.IsZero() {
		span = e.earliest.UTC().Format(time.RFC3339) + ".." + e.latest.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("live incident fixture has rotated away: %s has %d transcript(s) on this machine "+
		"spanning %s, which do not reach the asserted window %s..%s. This check only ever ran on the machine "+
		"the 2026-07-22 outage happened on; the same assertions run everywhere against "+
		"testdata/auth-expired-2026-07-22.jsonl (mg-ae5a).",
		agent, e.files, span,
		from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
}

// timestampKey is the JSON field every harness record carries. The guard keys
// on it and on nothing else — no model attribution, no usage, no error flag —
// so it answers "is this era still on disk" rather than "does the detector like
// what it sees".
const timestampKey = `"timestamp":"`

// measureTranscripts reads the given transcript files and reports their extent
// against [from, to].
//
// It scans raw bytes rather than lines on purpose: a real assistant turn can be
// megabytes, and the package's own readLine drops any line over maxLineBytes.
// Inheriting that cap here would let a window full of large records read as an
// empty one, which is the failure this guard exists to prevent.
func measureTranscripts(t *testing.T, paths []string, from, to time.Time) transcriptExtent {
	t.Helper()
	var e transcriptExtent
	key := []byte(timestampKey)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Logf("transcript %s unreadable: %v", p, err)
			continue
		}
		e.files++
		var first, last time.Time
		for i := 0; i < len(b); {
			j := bytes.Index(b[i:], key)
			if j < 0 {
				break
			}
			i += j + len(key)
			k := bytes.IndexByte(b[i:], '"')
			if k < 0 {
				break
			}
			ts, err := time.Parse(time.RFC3339Nano, string(b[i:i+k]))
			i += k
			if err != nil {
				continue
			}
			if first.IsZero() || ts.Before(first) {
				first = ts
			}
			if ts.After(last) {
				last = ts
			}
			if !ts.Before(from) && !ts.After(to) {
				e.inWindow++
			}
		}
		if first.IsZero() {
			continue
		}
		if e.earliest.IsZero() || first.Before(e.earliest) {
			e.earliest = first
		}
		if last.After(e.latest) {
			e.latest = last
		}
		if !first.After(to) && !last.Before(from) {
			e.straddles = true
		}
	}
	return e
}

// TestMeasureTranscripts_SeesAWindowItIsGiven is the positive control for the
// guard above, and it is not optional.
//
// The guard's whole job is to answer "is the fixture here?" with NO. A NO that
// is produced by a broken instrument is indistinguishable from a NO that is
// true, and its consequence — a permanent silent skip — is precisely the
// outcome mg-ae5a says must not be how the next decay goes unnoticed. So the
// same instrument is run against a checked-in file whose contents are known:
// six records, all inside the incident window, on every machine.
//
// If the harness ever renames its timestamp field or changes its format, this
// fails HERE, loudly, in CI, instead of quietly disarming the live check.
func TestMeasureTranscripts_SeesAWindowItIsGiven(t *testing.T) {
	fixture := []string{filepath.Join("testdata", "auth-expired-2026-07-22.jsonl")}

	got := measureTranscripts(t, fixture, incidentOnset, incidentEnd)
	if got.files != 1 {
		t.Fatalf("files = %d, want 1", got.files)
	}
	if got.inWindow != 6 {
		t.Errorf("inWindow = %d, want 6 — the guard cannot see records it is pointed at", got.inWindow)
	}
	if !got.straddles {
		t.Error("straddles = false for a file lying entirely inside the window")
	}
	wantFirst := time.Date(2026, 7, 22, 0, 0, 24, 880000000, time.UTC)
	wantLast := time.Date(2026, 7, 22, 0, 50, 24, 783000000, time.UTC)
	if !got.earliest.Equal(wantFirst) || !got.latest.Equal(wantLast) {
		t.Errorf("span = %s..%s, want %s..%s",
			got.earliest.UTC().Format(time.RFC3339Nano), got.latest.UTC().Format(time.RFC3339Nano),
			wantFirst.Format(time.RFC3339Nano), wantLast.Format(time.RFC3339Nano))
	}

	// And the negative half: the same instrument on the same file, against a
	// window the file does not reach. An instrument that says YES to everything
	// would pass the control above and still never skip.
	away := measureTranscripts(t, fixture,
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC))
	if away.inWindow != 0 || away.straddles {
		t.Errorf("inWindow = %d, straddles = %v for a window 10 days after the file; want 0, false",
			away.inWindow, away.straddles)
	}
	if msg := away.absentMessage("pm-pogo", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)); !strings.Contains(msg, "2026-08-01T00:00:00Z") ||
		!strings.Contains(msg, "2026-07-22T00:00:24Z") {
		// The skip message is the only thing a future reader gets. It must name
		// the window that was asked for AND what is actually on disk.
		t.Errorf("absentMessage() = %q; want it to name both the asked-for window and the span present", msg)
	}
}

// TestMeasureTranscripts_ReadsRecordsLongerThanTheScannerCap pins the reason
// this guard scans bytes instead of reusing readLine: the package's line reader
// drops any line over maxLineBytes, and a real assistant turn carrying tool
// results routinely exceeds it. A guard that inherited that cap would report a
// window packed with large records as empty — the same false absence, one layer
// down.
func TestMeasureTranscripts_ReadsRecordsLongerThanTheScannerCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.jsonl")
	pad := strings.Repeat("x", maxLineBytes+1024)
	line := `{"type":"assistant","timestamp":"2026-07-22T01:00:00.000Z","message":{"model":"claude-opus-5","content":[{"type":"text","text":"` + pad + `"}]}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := measureTranscripts(t, []string{path}, incidentOnset, incidentEnd)
	if got.inWindow != 1 {
		t.Fatalf("inWindow = %d, want 1 — an oversized record is still evidence the era is on disk", got.inWindow)
	}
}
