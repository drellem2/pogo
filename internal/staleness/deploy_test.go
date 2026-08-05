package staleness

import (
	"testing"
	"time"
)

// prodSchedule is the shipped one: 03/04/05 local with a 2h grace behind the
// last fire. Tests use it rather than a convenient invention so a change to the
// real schedule moves the tests too.
var prodSchedule = DeploySchedule{Hours: []int{3, 4, 5}, Minute: 0, Grace: DefaultGrace}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	// Local, because the deploy schedule and the date pogo-deploy.sh stamps are
	// both local. Parsing these as UTC would make every boundary test lie by
	// the machine's offset.
	ts, err := time.ParseInLocation("2006-01-02T15:04", s, time.Local)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// TestCheckDeployPositiveControl is the pair the ticket demanded: the SAME
// witness, the same schedule, one genuinely missed run and one healthy night.
// An alarm shown only to be silent has not been shown to work, and silence is
// exactly what the five missed nights of 2026-07-31..08-05 produced.
func TestCheckDeployPositiveControl(t *testing.T) {
	// RED — the real incident. The last recorded attempt is the night of
	// 07-31; the box was powered off through 08-01, 08-02, 08-03 and 08-04's
	// 03:00 windows, so none of them wrote anything at all. Four nights, no
	// output anywhere on the box to read, and the witness names all four.
	red := CheckDeploy("/tmp/stamp", "2026-07-31 1 0\n", true, at(t, "2026-08-04T12:00"), prodSchedule)
	if red.Clean() {
		t.Fatalf("witness stayed quiet through four missed nights: %+v", red)
	}
	if red.MissedTotal != 4 {
		t.Errorf("MissedTotal = %d, want 4 (08-01..08-04)", red.MissedTotal)
	}
	wantNights := map[string]bool{"2026-08-01": true, "2026-08-02": true, "2026-08-03": true, "2026-08-04": true}
	for _, m := range red.Missed {
		if !wantNights[m.Date] {
			t.Errorf("unexpected missed night %q", m.Date)
		}
		if m.Reason != "no-fire" {
			t.Errorf("night %s: Reason = %q, want no-fire (nothing ran, so there is no rc)", m.Date, m.Reason)
		}
		delete(wantNights, m.Date)
	}
	for d := range wantNights {
		t.Errorf("missed night %s not reported", d)
	}

	// QUIET — same witness, same clock, a night that deployed. The control has
	// to hold at the same instant as the RED case, otherwise it is testing the
	// clock rather than the record.
	quiet := CheckDeploy("/tmp/stamp", "2026-08-04 1 0\n", true, at(t, "2026-08-04T12:00"), prodSchedule)
	if !quiet.Clean() {
		t.Fatalf("witness fired on a healthy night: %+v", quiet)
	}
	if quiet.MissedTotal != 0 || len(quiet.Missed) != 0 {
		t.Errorf("MissedTotal = %d, Missed = %+v, want none", quiet.MissedTotal, quiet.Missed)
	}
}

// TestCheckDeployFailedNight covers the fifth night: the deploy DID fire and
// died one second in on a transient ssh failure, leaving "2026-08-05 1 1". A
// night that ran and failed is still a night the fleet did not get new code, so
// it is a finding — with a different reason, because it sends the reader to the
// log rather than to launchd.
func TestCheckDeployFailedNight(t *testing.T) {
	r := CheckDeploy("/tmp/stamp", "2026-08-05 1 1\n", true, at(t, "2026-08-05T18:50"), prodSchedule)
	if r.Clean() {
		t.Fatalf("a night that exited 1 read as clean: %+v", r)
	}
	if r.MissedTotal != 1 {
		t.Fatalf("MissedTotal = %d, want 1", r.MissedTotal)
	}
	if got := r.Missed[0]; got.Date != "2026-08-05" || got.Reason != "failed" || got.RC != 1 {
		t.Errorf("Missed[0] = %+v, want {2026-08-05 failed 1}", got)
	}
}

// TestCheckDeployGraceBoundary is the false-alarm control. The witness must not
// report a deploy that is at that moment succeeding: pogo-deploy.sh caps one
// attempt at 2h of drain, so a 05:00 fire can still be working at 06:30 with
// nothing wrong. The night becomes due only after the last fire plus the grace.
func TestCheckDeployGraceBoundary(t *testing.T) {
	// 04:00 — inside the fire window. Tonight has not settled, so the most
	// recent due night is yesterday, which the stamp covers.
	during := CheckDeploy("/tmp/stamp", "2026-08-04 1 0\n", true, at(t, "2026-08-05T04:00"), prodSchedule)
	if !during.Clean() {
		t.Errorf("fired mid-window on a night still in progress: %+v", during)
	}
	if during.LastDueNight != "2026-08-04" {
		t.Errorf("LastDueNight = %q at 04:00, want 2026-08-04", during.LastDueNight)
	}

	// 07:30 — past 05:00 + 2h. Tonight is now due and nothing recorded it.
	after := CheckDeploy("/tmp/stamp", "2026-08-04 1 0\n", true, at(t, "2026-08-05T07:30"), prodSchedule)
	if after.Clean() {
		t.Fatalf("stayed quiet past the grace on a night with no record: %+v", after)
	}
	if after.LastDueNight != "2026-08-05" || after.MissedTotal != 1 {
		t.Errorf("LastDueNight = %q, MissedTotal = %d; want 2026-08-05 and 1", after.LastDueNight, after.MissedTotal)
	}
}

// TestCheckDeployTonightNotYetJudged: a record written by tonight's fire, before
// tonight is due, is not second-guessed. A non-zero rc there may still be
// retried by a later fire (mg-8f7e), and reporting it now would alarm on a night
// the machinery is still working on.
func TestCheckDeployTonightNotYetJudged(t *testing.T) {
	r := CheckDeploy("/tmp/stamp", "2026-08-05 1 7\n", true, at(t, "2026-08-05T03:30"), prodSchedule)
	if !r.Clean() {
		t.Errorf("judged tonight's exit-7 before its retry fires: %+v", r)
	}
}

// TestCheckDeployBrokenInputIsNotAllClear. The two ways the record itself fails
// must not look like "0 missed nights" — an alarm whose broken-input case is
// indistinguishable from its all-clear case goes quiet exactly when its input
// rots, which is the failure this whole witness exists to end.
func TestCheckDeployBrokenInputIsNotAllClear(t *testing.T) {
	absent := CheckDeploy("/tmp/nope", "", false, at(t, "2026-08-05T12:00"), prodSchedule)
	if absent.Clean() {
		t.Errorf("a host with no attempt record at all read as clean: %+v", absent)
	}
	if absent.StampFound {
		t.Errorf("StampFound = true for an absent record")
	}

	for _, corrupt := range []string{"garbage\n", "2026-08-05 1\n", "not-a-date 1 0\n", "2026-08-05 x 0\n", "2026-08-05 1 rc\n", "\n"} {
		r := CheckDeploy("/tmp/stamp", corrupt, true, at(t, "2026-08-05T12:00"), prodSchedule)
		if r.Clean() {
			t.Errorf("corrupt stamp %q read as clean", corrupt)
		}
		if r.ParseErr == "" {
			t.Errorf("corrupt stamp %q produced no ParseErr", corrupt)
		}
		if r.MissedTotal != 0 {
			t.Errorf("corrupt stamp %q invented %d missed nights; it should report unreadable, not guess", corrupt, r.MissedTotal)
		}
	}
}

// TestCheckDeployCountSurvivesClip. The enumeration is clipped so a months-old
// stamp does not bury the number a reader acts on, but the COUNT must stay
// exact — a clipped count would understate an outage.
func TestCheckDeployCountSurvivesClip(t *testing.T) {
	r := CheckDeploy("/tmp/stamp", "2026-05-01 1 0\n", true, at(t, "2026-08-05T12:00"), prodSchedule)
	// 2026-05-01 exclusive through 2026-08-05 inclusive.
	want := int(at(t, "2026-08-05T00:00").Sub(at(t, "2026-05-01T00:00")).Hours() / 24)
	if r.MissedTotal != want {
		t.Errorf("MissedTotal = %d, want %d", r.MissedTotal, want)
	}
	if len(r.Missed) != maxEnumeratedNights {
		t.Errorf("enumerated %d nights, want the clip at %d", len(r.Missed), maxEnumeratedNights)
	}
	if !r.Truncated {
		t.Errorf("Truncated = false while the enumeration was clipped — the reader would take the list for the whole set")
	}
}

// TestLastDueNightIsIndependentOfTheRecord. The expectation half must be
// derivable with no input from the deploy at all; that independence is the only
// reason a run that produced nothing can be detected.
func TestLastDueNightIsIndependentOfTheRecord(t *testing.T) {
	cases := []struct {
		now  string
		want string
	}{
		{"2026-08-05T00:30", "2026-08-04"}, // yesterday settled at 07:00 yesterday; tonight has not fired
		{"2026-08-05T06:59", "2026-08-04"},
		{"2026-08-05T07:00", "2026-08-05"},
		{"2026-08-05T23:59", "2026-08-05"},
	}
	for _, c := range cases {
		got := prodSchedule.LastDueNight(at(t, c.now)).Format(stampDateLayout)
		if got != c.want {
			t.Errorf("LastDueNight(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

func TestParseAttempt(t *testing.T) {
	a, err := ParseAttempt("  2026-08-05 2 7 \n")
	if err != nil {
		t.Fatalf("ParseAttempt: %v", err)
	}
	if a.Date != "2026-08-05" || a.Attempts != 2 || a.RC != 7 {
		t.Errorf("got %+v, want {2026-08-05 2 7}", a)
	}
}
