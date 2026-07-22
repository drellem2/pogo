package synthfail

import (
	"os"
	"path/filepath"
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
// It skips wherever those transcripts are absent, which is every machine except
// the one the incident happened on — and the checked-in testdata fixtures carry
// the same assertions into CI.
func TestScan_LiveIncidentTranscripts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	glob := func(agent string) string {
		return filepath.Join(".claude", "projects", "-Users-daniel--pogo-agents-"+agent, "*.jsonl")
	}
	if len(Locate(home, []string{glob("pm-pogo")}, time.Time{})) == 0 {
		t.Skip("live incident transcripts not present on this machine")
	}

	window := incidentEnd.Sub(incidentOnset)

	t.Run("pm-pogo FIRES across the outage window", func(t *testing.T) {
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
		if len(Locate(home, []string{glob("doctor")}, time.Time{})) == 0 {
			t.Skip("doctor transcript not present")
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
