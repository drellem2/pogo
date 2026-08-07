package staleness

import (
	"strings"
	"testing"
	"time"
)

// bst is the zone the incident happened in, pinned so these tests judge the
// same nights on a runner in any timezone. Night naming is LOCAL, so a test
// that took the runner's zone would name different nights on a CI box in UTC
// and quietly stop testing the case it was written for.
var bst = time.FixedZone("BST", 60*60)

func atBST(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02T15:04", s, bst)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// realLog is the `pogo-deploy: start` lines from the actual
// ~/Library/Logs/pogo/pogo-deploy.log as it stood on 2026-08-07, verbatim
// (timestamps and fields as written; the surrounding per-run output is elided
// because this witness reads only the start lines and the log's earliest
// timestamp).
//
// It is reproduced rather than invented because the four-night gap is the whole
// subject: 08-01, 08-02, 08-03 and 08-04 have NO line here, and there is no
// other artifact anywhere on the box that records their silence. Note also what
// is present — the 07-29T18:32 DRY RUN, which deployed nothing.
const realLog = `[2026-07-29T02:00:04Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)
[2026-07-29T02:00:04Z] window: local hour 03 is inside [2,5)
[2026-07-29T18:32:12Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=true)
[2026-07-30T02:00:02Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)
[2026-07-31T02:00:00Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)
[2026-07-31T04:11:57Z] pogo-deploy: exit 7 (drain stalled)
[2026-08-05T02:00:03Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-06T02:00:04Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-07T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
`

const logPath = "/Users/daniel/Library/Logs/pogo/pogo-deploy.log"

// TestCheckNoFirePositiveControlOnTheRealOutage replays the real log and
// requires the witness to name the four nights the job never started — the half
// of the eight-night outage that no detector we owned could see.
//
// It is paired with a QUIET case at the same instant, because an alarm shown
// only to be silent has not been shown to work, and silence is precisely what
// 08-01..08-04 produced from every other alarm on the box.
func TestCheckNoFirePositiveControlOnTheRealOutage(t *testing.T) {
	now := atBST(t, "2026-08-07T12:00")

	// RED — the real record.
	red := CheckNoFire(logPath, realLog, true, now, prodSchedule)
	if red.Clean() {
		t.Fatalf("witness stayed quiet through four nights the job never started: %+v", red)
	}
	if red.MissedTotal != 4 {
		t.Errorf("MissedTotal = %d, want 4 (08-01..08-04); missed = %v", red.MissedTotal, red.Missed)
	}
	want := map[string]bool{"2026-08-01": true, "2026-08-02": true, "2026-08-03": true, "2026-08-04": true}
	for _, night := range red.Missed {
		if !want[night] {
			t.Errorf("unexpected silent night %q — a night with a start line must never be reported", night)
		}
		delete(want, night)
	}
	for night := range want {
		t.Errorf("silent night %s not reported", night)
	}

	// The four nights the job DID run are not silent nights, including the one
	// it ran and exited 7 on. This witness answers "did it run", not "did it
	// succeed" — conflating them is how the failure alarm came to stand in for
	// a run alarm in the first place.
	for _, ran := range []string{"2026-07-30", "2026-07-31", "2026-08-05", "2026-08-06", "2026-08-07"} {
		for _, night := range red.Missed {
			if night == ran {
				t.Errorf("night %s had a start line and was reported silent", ran)
			}
		}
	}

	// QUIET — the same witness, the same clock, the same schedule, with the four
	// missing fires present. The control must hold at the SAME instant as the
	// RED case, otherwise it is testing the clock rather than the record.
	healed := realLog +
		"[2026-08-01T02:00:01Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)\n" +
		"[2026-08-02T02:00:01Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)\n" +
		"[2026-08-03T02:00:01Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)\n" +
		"[2026-08-04T02:00:01Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)\n"
	quiet := CheckNoFire(logPath, healed, true, now, prodSchedule)
	if !quiet.Clean() {
		t.Fatalf("witness fired on a log where every due night has a fire: %+v", quiet)
	}
}

// TestCheckNoFireNeedsNoOutcomeAtAll is the ticket's central constraint as a
// test: the detector must not require the job to have run.
//
// The log here contains ONLY start lines — no exit line, no rc, no stamp, no
// success or failure of any kind — and the witness still names the silent
// night. Any future rewrite that starts consulting an outcome fails here.
func TestCheckNoFireNeedsNoOutcomeAtAll(t *testing.T) {
	onlyStarts := "[2026-08-05T02:00:03Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n" +
		"[2026-08-06T02:00:04Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n"
	if strings.Contains(onlyStarts, "exit") {
		t.Fatal("fixture must contain no outcome line at all")
	}

	r := CheckNoFire(logPath, onlyStarts, true, atBST(t, "2026-08-07T12:00"), prodSchedule)
	if r.MissedTotal != 1 || len(r.Missed) != 1 || r.Missed[0] != "2026-08-07" {
		t.Fatalf("want exactly the 08-07 night reported silent from start lines alone, got %+v", r)
	}
}

// TestCheckNoFireDryRunDoesNotSatisfyANight guards the one line in the real log
// that could quietly cover a silent night: a human's `--dry-run` invocation
// writes a start line and deploys nothing.
func TestCheckNoFireDryRunDoesNotSatisfyANight(t *testing.T) {
	dryOnly := "[2026-08-06T02:00:00Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n" +
		"[2026-08-07T09:15:00Z] pogo-deploy: start (src=/x window=2-6 dry_run=true)\n"

	r := CheckNoFire(logPath, dryOnly, true, atBST(t, "2026-08-07T12:00"), prodSchedule)
	if r.MissedTotal != 1 || r.Missed[0] != "2026-08-07" {
		t.Errorf("a dry run covered a night it deployed nothing on: %+v", r)
	}
	if r.DryRuns != 1 {
		t.Errorf("DryRuns = %d, want 1 — excluded dry runs must be counted, not silently dropped", r.DryRuns)
	}
	if r.Fires != 1 {
		t.Errorf("Fires = %d, want 1 (the dry run is not a fire)", r.Fires)
	}
}

// TestCheckNoFireHorizonNeverManufacturesAnOutage is the rotation guard, and it
// is the reason this witness has a horizon at all.
//
// A log that begins on 08-05 says nothing whatsoever about 08-01..08-04. If
// those nights were reported as silent, every rotation would page a human with
// a fabricated four-night outage — and an alarm that cries wolf on housekeeping
// gets filtered, which is how the five `pogo-deploy` REDs before this one
// stopped being read.
func TestCheckNoFireHorizonNeverManufacturesAnOutage(t *testing.T) {
	rotated := "[2026-08-05T02:00:03Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n" +
		"[2026-08-06T02:00:04Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n" +
		"[2026-08-07T02:00:05Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n"

	r := CheckNoFire(logPath, rotated, true, atBST(t, "2026-08-07T12:00"), prodSchedule)
	if r.MissedTotal != 0 {
		t.Errorf("reported %d silent night(s) from a log that does not cover them: %v", r.MissedTotal, r.Missed)
	}
	if !r.HorizonLimited {
		t.Error("HorizonLimited = false; the un-judged nights before the log's first line must be declared, " +
			"or a rotated-away outage reads as an all-clear")
	}
	if r.EarliestJudged != "2026-08-05" {
		t.Errorf("EarliestJudged = %q, want 2026-08-05 — the oldest night this log can speak for", r.EarliestJudged)
	}
	// And the other direction: within its coverage the same rotated log still
	// detects. A horizon must limit the claim, not disable the detector.
	gap := "[2026-08-04T02:00:03Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n"
	r2 := CheckNoFire(logPath, gap, true, atBST(t, "2026-08-07T12:00"), prodSchedule)
	if r2.MissedTotal != 3 {
		t.Errorf("MissedTotal = %d, want 3 (08-05/06/07 are all inside this log's coverage): %+v", r2.MissedTotal, r2)
	}
}

// TestCheckNoFireMissingLogIsNeverClean covers the loudest reading available:
// the job has not written a single line on a host that is supposed to run it.
// It must not degrade to "0 missed nights", which is what an empty fire-set
// would otherwise produce.
func TestCheckNoFireMissingLogIsNeverClean(t *testing.T) {
	r := CheckNoFire(logPath, "", false, atBST(t, "2026-08-07T12:00"), prodSchedule)
	if r.Clean() {
		t.Fatal("a missing deploy log reported clean — an absent input must never answer 'fine'")
	}
	if r.LogFound {
		t.Error("LogFound = true for a log that does not exist")
	}
	if !strings.Contains(r.Summary(), "NEVER") {
		t.Errorf("Summary() = %q, want it to say the deploy has never written a line", r.Summary())
	}
}

// TestCheckNoFireEmptyLogIsNeverClean is the sibling of the above and the more
// dangerous one: a file that EXISTS and is empty (or holds nothing datable) has
// an empty fire-set, which without the horizon rule would read as a clean run
// of nights that simply had no misses.
func TestCheckNoFireEmptyLogIsNeverClean(t *testing.T) {
	for name, text := range map[string]string{
		"empty":        "",
		"no timestamp": "pogo-deploy: start (src=/x window=2-6 dry_run=false)\n",
	} {
		r := CheckNoFire(logPath, text, true, atBST(t, "2026-08-07T12:00"), prodSchedule)
		if !r.HorizonLimited {
			t.Errorf("%s log: HorizonLimited = false, want true — nothing in it could date a night", name)
		}
		if r.MissedTotal != 0 {
			t.Errorf("%s log: MissedTotal = %d, want 0 — an undatable log must claim nothing, in either direction", name, r.MissedTotal)
		}
	}
}

// TestCheckNoFireUnparsedStartLineIsCountedNotCredited: a start line whose
// timestamp will not parse proves a fire happened but cannot say which night it
// belongs to. Crediting it to the nearest night would let one corrupt line
// vouch for a night that was in fact silent.
func TestCheckNoFireUnparsedStartLineIsCountedNotCredited(t *testing.T) {
	corrupt := "[2026-08-06T02:00:04Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n" +
		"[not-a-timestamp] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n"

	r := CheckNoFire(logPath, corrupt, true, atBST(t, "2026-08-07T12:00"), prodSchedule)
	if r.Unparsed != 1 {
		t.Errorf("Unparsed = %d, want 1 — an unreadable line must be named, not dropped", r.Unparsed)
	}
	if r.MissedTotal != 1 || r.Missed[0] != "2026-08-07" {
		t.Errorf("the unreadable line covered a night it cannot be dated to: %+v", r)
	}
}

// TestCheckNoFireDoesNotJudgeTonightYet: the night currently inside its grace
// window has not missed anything. Firing on it would produce a false alarm
// every single morning between the fire and the grace expiring.
func TestCheckNoFireDoesNotJudgeTonightYet(t *testing.T) {
	// 06:00 local on 08-07: the last fire is 05:00 and the grace is 2h, so
	// 08-07 is not due yet and 08-06 is the last due night.
	upToYesterday := "[2026-08-06T02:00:04Z] pogo-deploy: start (src=/x window=2-6 dry_run=false)\n"
	r := CheckNoFire(logPath, upToYesterday, true, atBST(t, "2026-08-07T06:00"), prodSchedule)
	if r.LastDueNight != "2026-08-06" {
		t.Fatalf("LastDueNight = %q, want 2026-08-06 (08-07 is still inside its grace)", r.LastDueNight)
	}
	if !r.Clean() {
		t.Errorf("fired on a night still inside its grace window: %+v", r)
	}
}

// TestNoFireSummaryCarriesTheCount checks the part that travels. The subject
// line is what gets skimmed and filtered; a count that grows is what stops a
// constant-subject filter rule from swallowing the alarm the way it swallowed
// the five deploy REDs.
func TestNoFireSummaryCarriesTheCount(t *testing.T) {
	r := CheckNoFire(logPath, realLog, true, atBST(t, "2026-08-07T12:00"), prodSchedule)
	s := r.Summary()
	if !strings.Contains(s, "4") {
		t.Errorf("Summary() = %q, want the number of silent nights in it", s)
	}
	if !strings.Contains(s, "DID NOT RUN") {
		t.Errorf("Summary() = %q, want it to say the job did not RUN (not that it failed)", s)
	}
}

// TestFirstFireIsTheHorizonTest pins the instant the horizon compares against.
// Using the LAST fire, or the settle time, would let a log that opened at 04:00
// judge a 03:00 fire it could not have seen — a fabricated silent night.
func TestFirstFireIsTheHorizonTest(t *testing.T) {
	day := time.Date(2026, 8, 7, 0, 0, 0, 0, bst)
	got := prodSchedule.FirstFire(day)
	want := time.Date(2026, 8, 7, 3, 0, 0, 0, bst)
	if !got.Equal(want) {
		t.Errorf("FirstFire = %s, want %s (the FIRST scheduled fire, not the last)", got, want)
	}
}
