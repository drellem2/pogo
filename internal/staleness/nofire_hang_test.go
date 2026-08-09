package staleness

import (
	"strings"
	"testing"
	"time"
)

// The HANG half of the did-not-run witness (mg-56ac).
//
// Every test here is paired: a RED against a run that hung, and a GREEN at the
// SAME instant against the same record with the hang taken out. That pairing is
// the point of the ticket. `deploy_nofire` had never produced a red for a hung
// run — not because no run had hung, but because a hung run landed on its good
// branch — and a check that has only ever been green is a check of unknown
// polarity.

// hangLog is the real ~/Library/Logs/pogo/pogo-deploy.log across the 2026-08-08
// outage, verbatim, with the interior of the 08-07 run elided (it is not what
// is being judged) and NOTHING added.
//
// This is the whole finding in nine lines. The 08-08 fire starts on time, says
// five things in one second, and then says nothing for 31 hours 39 minutes —
// through the night, through the following working day — before completing at
// 09:43 the next morning. The crew was stopped at 00:44Z on 08-08 and nothing
// restarted it until this run finally got there.
//
// What the old witness said about this record, on the morning of 08-09:
//
//	missed: [2026-08-09, 2026-08-04, 2026-08-03, 2026-08-02, 2026-08-01]
//
// 2026-08-08 is not in that list, because it has a start line. The night the
// deploy hung for 31h39m was scored as a night that ran, and 08-09 — which this
// very run deployed at 09:43 — was scored as a night that did not, because the
// run stamped its attempt with the date it woke up on.
const hangLog = `[2026-08-07T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-07T02:00:05Z] window: local hour 03 is inside [2,6)
[2026-08-07T02:00:05Z] attempt: first of the night (2026-08-07)
[2026-08-07T02:00:10Z] alert: mailed 'pm-pogo' and 'human'
[2026-08-07T02:00:10Z] attempt recorded: 2026-08-07 attempt=1 rc=6 (/Users/daniel/.pogo/deploy-attempt.stamp)
[2026-08-08T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-08T02:00:05Z] window: local hour 03 is inside [2,6)
[2026-08-08T02:00:05Z] attempt: first of the night (2026-08-08)
[2026-08-08T02:00:05Z] budget: drain gets up to 7200s (window ends 6:00, reserve 1200s, cap 7200s)
[2026-08-08T02:00:05Z] mg: resolved macguffin at /Users/daniel/go/bin/mg
[2026-08-08T02:00:05Z] pogo: resolved CLI at /Users/daniel/go/bin/pogo
[2026-08-08T02:00:05Z] nc: resolved a probe that can return a definite refusal at /usr/bin/nc (-G 5)
[2026-08-08T02:00:05Z] git: resolved working git at /usr/bin/git (git version 2.50.1 (Apple Git-155))
[2026-08-08T02:00:05Z] GH_TOKEN: sourced from /Users/daniel/.zshenv (present, 40 chars)
[2026-08-09T09:39:43Z] sync: /Users/daniel/.pogo/deploy-src at main 738e322
[2026-08-09T09:39:45Z] drift: work owed — proceeding to redeploy
[2026-08-09T09:41:23Z] redeploy: exit 0 after 98s — OK
[2026-08-09T09:43:23Z] pogo-deploy: done — pogod redeployed to 738e322
[2026-08-09T09:43:23Z] attempt recorded: 2026-08-09 attempt=1 rc=0 (/Users/daniel/.pogo/deploy-attempt.stamp)
`

// healthyLog is hangLog with the 08-08 run finishing when it should have: the
// same fire, the same lines, the same next-morning content moved back under the
// night it belongs to. Everything else is byte-identical.
//
// It is the negative control, and it is judged at the SAME instant as the RED.
// A red that needs a different clock than its green is testing the clock.
const healthyLog = `[2026-08-07T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-07T02:00:05Z] window: local hour 03 is inside [2,6)
[2026-08-07T02:00:05Z] attempt: first of the night (2026-08-07)
[2026-08-07T02:00:10Z] alert: mailed 'pm-pogo' and 'human'
[2026-08-07T02:00:10Z] attempt recorded: 2026-08-07 attempt=1 rc=6 (/Users/daniel/.pogo/deploy-attempt.stamp)
[2026-08-08T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-08T02:00:05Z] window: local hour 03 is inside [2,6)
[2026-08-08T02:00:05Z] attempt: first of the night (2026-08-08)
[2026-08-08T02:00:05Z] budget: drain gets up to 7200s (window ends 6:00, reserve 1200s, cap 7200s)
[2026-08-08T02:00:05Z] GH_TOKEN: sourced from /Users/daniel/.zshenv (present, 40 chars)
[2026-08-08T02:00:07Z] sync: /Users/daniel/.pogo/deploy-src at main 738e322
[2026-08-08T02:00:09Z] drift: work owed — proceeding to redeploy
[2026-08-08T02:01:47Z] redeploy: exit 0 after 98s — OK
[2026-08-08T02:03:47Z] pogo-deploy: done — pogod redeployed to 738e322
[2026-08-08T02:03:47Z] attempt recorded: 2026-08-08 attempt=1 rc=0 (/Users/daniel/.pogo/deploy-attempt.stamp)
[2026-08-09T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-09T02:00:05Z] drift: none — running pogod and installed binaries are at main. NOT bouncing the fleet. Exit 0.
`

// TestHangRedOnTheRealAugust8Log is the acceptance the dispatch asked for:
// prove the detector goes RED against a hang, not only GREEN against a healthy
// run. The hang is not constructed — it is the record this box actually holds.
func TestHangRedOnTheRealAugust8Log(t *testing.T) {
	// 2026-08-09 midday, the morning after, in the zone the incident happened
	// in — the same instant the old witness reported five missed nights and
	// said nothing about 08-08.
	now := atBST(t, "2026-08-09T12:00")

	red := CheckNoFire(logPath, hangLog, true, now, prodSchedule)
	if red.Clean() {
		t.Fatalf("witness reported CLEAN across a 31h39m hang: %+v", red)
	}
	if red.HungTotal != 1 {
		t.Fatalf("HungTotal = %d, want 1; hung = %+v", red.HungTotal, red.Hung)
	}
	h := red.Hung[0]
	if h.Night != "2026-08-08" {
		t.Errorf("hung night = %q, want 2026-08-08 — the night the run STARTED, not the date it stamped when it woke up", h.Night)
	}
	if !h.Terminated {
		t.Errorf("Terminated = false, want true — this run did finish, 31h39m late; the finding is the length, not the absence")
	}
	// 02:00:05Z -> 09:43:23Z the next day.
	if want := 31*3600 + 43*60 + 18; h.ElapsedSeconds != want {
		t.Errorf("ElapsedSeconds = %d (%s), want %d", h.ElapsedSeconds, HumanDuration(h.ElapsedSeconds), want)
	}
	// The stall itself: GH_TOKEN at 02:00:05Z -> sync at 09:39:43Z.
	if want := 31*3600 + 39*60 + 38; h.SilentSeconds != want {
		t.Errorf("SilentSeconds = %d (%s), want %d", h.SilentSeconds, HumanDuration(h.SilentSeconds), want)
	}
	if !strings.Contains(h.StalledAfter, "GH_TOKEN: sourced") {
		t.Errorf("StalledAfter = %q, want the GH_TOKEN line — the last thing the run said before it stopped saying anything, which is what places the stall inside the sync", h.StalledAfter)
	}

	// The 08-07 run, which failed loudly and finished in five seconds, is NOT a
	// hang. A witness that reported every failed night as hung would be
	// unusable, and it would repeat the conflation this ticket is about in the
	// other direction.
	for _, g := range red.Hung {
		if g.Night == "2026-08-07" {
			t.Errorf("the 08-07 run exited 6 in five seconds and was reported hung")
		}
	}

	// GREEN — same clock, same schedule, same log with the run finishing on
	// time.
	green := CheckNoFireWithin(logPath, healthyLog, true, now, prodSchedule, DefaultHungAfter)
	if green.HungTotal != 0 {
		t.Fatalf("negative control reported %d hang(s) on a log where every run finished in minutes: %+v", green.HungTotal, green.Hung)
	}
	if !green.Clean() {
		t.Fatalf("negative control was not clean: missed=%v hung=%v", green.Missed, green.Hung)
	}
}

// TestSummaryAndSubjectLeadWithTheHang guards the part that travels. mg-56ac's
// finding is that the hang was on the good branch; a summary that leads with a
// missed-night count and mentions the hang in a clause puts it back there for
// every reader who skims.
func TestSummaryAndSubjectLeadWithTheHang(t *testing.T) {
	now := atBST(t, "2026-08-09T12:00")
	red := CheckNoFire(logPath, hangLog, true, now, prodSchedule)

	sum := red.Summary()
	if !strings.Contains(sum, "HUNG") {
		t.Errorf("Summary() = %q — the word a reader acts on is missing", sum)
	}
	if !strings.HasPrefix(sum, "a nightly deploy HUNG") {
		t.Errorf("Summary() = %q, want the hang FIRST", sum)
	}
	// The missed nights are still reported; the hang just goes first.
	if red.MissedTotal > 0 && !strings.Contains(sum, "DID NOT RUN at all") {
		t.Errorf("Summary() = %q dropped the %d missed night(s) entirely", sum, red.MissedTotal)
	}
	if !strings.Contains(sum, "31h43m") {
		t.Errorf("Summary() = %q — one hang of 31h and one of 6h01m are the same count and not the same event, so the duration has to travel", sum)
	}
}

// TestNeverTerminatedNeedsTheEndMarkerToBeJudged is the honesty test for the
// second arm, and it is the one that would have manufactured an outage if it
// were wrong.
//
// A run that wrote no terminal line is only evidence of a hang once the runner
// is known to write terminal lines. Before that, the same record is exactly
// what an OLD runner produces on a perfectly ordinary failing night — the real
// 2026-07-31 run exited 9 at 02:30 and its log looks identical to a run that
// never came back.
func TestNeverTerminatedNeedsTheEndMarkerToBeJudged(t *testing.T) {
	now := atBST(t, "2026-08-06T12:00")

	// An old-runner log: a run that ended (loudly, at 02:30) but wrote no
	// terminal line, followed five days later by the next fire.
	old := `[2026-07-31T02:00:00Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)
[2026-07-31T02:00:03Z] sync: /Users/daniel/.pogo/deploy-src at main b3efaa2
[2026-07-31T02:30:19Z] alert: mailed 'pm-pogo' and 'human'
[2026-08-05T02:00:03Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-05T02:00:04Z] attempt recorded: 2026-08-05 attempt=1 rc=1 (/Users/daniel/.pogo/deploy-attempt.stamp)
[2026-08-06T02:00:04Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-06T04:00:25Z] attempt recorded: 2026-08-06 attempt=1 rc=7 (/Users/daniel/.pogo/deploy-attempt.stamp)
`
	rep := CheckNoFire(logPath, old, true, now, prodSchedule)
	if rep.HungTotal != 0 {
		t.Errorf("a five-day gap after a run that ENDED at 02:30 was reported as a hang: %+v — that is an outage manufactured out of the witness's own newness", rep.Hung)
	}
	if rep.HangArmed {
		t.Errorf("HangArmed = true on a log with no `pogo-deploy: end` line — nothing here establishes that this runner writes one")
	}
	if rep.HangUnjudged != 1 {
		t.Errorf("HangUnjudged = %d, want 1 — the unjudgeable run must be counted and named, not folded into either verdict", rep.HangUnjudged)
	}

	// The SAME shape, once the runner is known to write end lines: the earlier
	// run now IS judged, and it is a hang.
	current := `[2026-08-05T02:00:03Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-05T02:00:09Z] pogo-deploy: end (rc=0 after 6s)
[2026-08-06T02:00:04Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-06T02:00:05Z] GH_TOKEN: sourced from /Users/daniel/.zshenv (present, 40 chars)
`
	rep = CheckNoFire(logPath, current, true, now, prodSchedule)
	if !rep.HangArmed {
		t.Fatalf("HangArmed = false on a log that holds a `pogo-deploy: end` line")
	}
	if rep.HungTotal != 1 {
		t.Fatalf("HungTotal = %d, want 1 — the 08-06 run started, said one thing and never terminated: %+v", rep.HungTotal, rep.Hung)
	}
	if rep.Hung[0].Terminated {
		t.Errorf("Terminated = true for a run with no terminal line")
	}
	if rep.Hung[0].Night != "2026-08-06" {
		t.Errorf("hung night = %q, want 2026-08-06", rep.Hung[0].Night)
	}
	// start 02:00:04Z -> now 12:00 BST (11:00Z) = 8h59m56s.
	if got := rep.Hung[0].ElapsedSeconds; got < 8*3600 || got > 9*3600 {
		t.Errorf("ElapsedSeconds = %d (%s), want ~9h — for a run that never terminated the bound is now", got, HumanDuration(got))
	}
	if rep.HangUnjudged != 0 {
		t.Errorf("HangUnjudged = %d, want 0 — both runs are past the end-line horizon", rep.HangUnjudged)
	}
}

// TestRunStillInsideItsWindowIsNotAHang is the false-alarm control for the
// never-terminated arm. Tonight's run, three minutes old, has no terminal line
// either — and it is not a hang, it is a deploy. Reporting it would mail a RED
// on every healthy night.
func TestRunStillInsideItsWindowIsNotAHang(t *testing.T) {
	log := `[2026-08-09T02:00:03Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-09T02:00:09Z] pogo-deploy: end (rc=0 after 6s)
[2026-08-10T02:00:04Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-10T02:00:05Z] budget: drain gets up to 7200s (window ends 6:00, reserve 1200s, cap 7200s)
`
	// 03:03 BST = 02:03Z, three minutes into the run.
	early := atBST(t, "2026-08-10T03:03")
	rep := CheckNoFireWithin(logPath, log, true, early, prodSchedule, DefaultHungAfter)
	if rep.HungTotal != 0 {
		t.Fatalf("a three-minute-old run in flight was reported as a hang: %+v", rep.Hung)
	}

	// The same record, six hours later, with the run still not having said
	// anything. NOW it is a hang — this is the boundary, driven from both sides
	// at the same threshold.
	late := atBST(t, "2026-08-10T09:03")
	rep = CheckNoFireWithin(logPath, log, true, late, prodSchedule, DefaultHungAfter)
	if rep.HungTotal != 1 {
		t.Fatalf("a run seven hours past its start with no terminal line was NOT reported: %+v", rep)
	}
	if rep.Hung[0].Terminated {
		t.Errorf("Terminated = true for a run that has written nothing since 02:00")
	}
}

// TestDryRunIsNotJudgedForHanging. A `--dry-run` is a human at a terminal, and
// one sits in the real log at 2026-07-29T18:32:12Z. It deploys nothing, so it
// cannot cost a night, and a human who backgrounds one and goes to bed must not
// mail a RED.
func TestDryRunIsNotJudgedForHanging(t *testing.T) {
	log := `[2026-08-09T02:00:03Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-09T02:00:09Z] pogo-deploy: end (rc=0 after 6s)
[2026-08-09T18:32:12Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=true)
[2026-08-09T18:32:12Z] window: guard BYPASSED (POGO_DEPLOY_SKIP_WINDOW=1) at hour 19
`
	now := atBST(t, "2026-08-10T12:00")
	rep := CheckNoFire(logPath, log, true, now, prodSchedule)
	for _, h := range rep.Hung {
		if h.Night == "2026-08-09" && strings.HasPrefix(h.Start, "2026-08-09T19:32") {
			t.Errorf("a dry run was reported as a hung deploy: %+v", h)
		}
	}
	if rep.HungTotal != 0 {
		t.Errorf("HungTotal = %d, want 0: %+v", rep.HungTotal, rep.Hung)
	}
}

// TestUndatedAlertProseIsNotReadAsATerminalLine. The runner echoes whole alert
// bodies to the same log, unprefixed, and those bodies contain sentences about
// exits. A terminal marker matched inside one would date a run's end to
// whenever the prose happened to sit, which is the prose-matching mistake this
// repo already refused once (gh#113). Only timestamped lines are acts of a run.
func TestUndatedAlertProseIsNotReadAsATerminalLine(t *testing.T) {
	log := `[2026-08-09T02:00:03Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-09T02:00:09Z] pogo-deploy: end (rc=0 after 6s)
[2026-08-10T02:00:03Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-10T02:00:04Z] ERROR: ALERT: [pogo-deploy] ABORTED
Nothing was deployed and the running pogod is untouched. NOT deploying. Exit 0.
This is the remedy paragraph, and it is not a line the run wrote about itself.
`
	now := atBST(t, "2026-08-10T12:00")
	rep := CheckNoFire(logPath, log, true, now, prodSchedule)
	if rep.HungTotal != 1 {
		t.Fatalf("HungTotal = %d, want 1 — the undated 'Exit 0.' prose was read as the run's terminal line, so a run that never came back was scored as one that finished in a second: %+v", rep.HungTotal, rep.Hung)
	}
	if rep.Hung[0].Terminated {
		t.Errorf("Terminated = true — the only terminal evidence in this log is prose with no timestamp")
	}
}

// TestHungRunDoesNotBecomeAMissedNight, and its mirror. The two findings are
// different facts about different nights and neither may be reported as the
// other — which is precisely what happened on 2026-08-09, when the real witness
// reported 08-09 missed (a night this run deployed) and said nothing about
// 08-08 (the night it hung).
func TestHungRunDoesNotBecomeAMissedNight(t *testing.T) {
	now := atBST(t, "2026-08-09T12:00")
	rep := CheckNoFire(logPath, hangLog, true, now, prodSchedule)

	for _, night := range rep.Missed {
		if night == "2026-08-08" {
			t.Errorf("08-08 has a start line and was reported as a night the job DID NOT RUN")
		}
	}
	for _, h := range rep.Hung {
		for _, night := range rep.Missed {
			if h.Night == night {
				t.Errorf("night %s reported as both hung and missed", night)
			}
		}
	}
	// 08-09 genuinely has no start line of its own — the run that deployed it
	// belongs to 08-08 — so it IS missed, and both findings stand together.
	found := false
	for _, night := range rep.Missed {
		if night == "2026-08-09" {
			found = true
		}
	}
	if !found {
		t.Errorf("08-09 has no start line and was not reported missed; missed = %v", rep.Missed)
	}
}

// TestHumanDuration pins the rendering that carries the finding.
func TestHumanDuration(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{31*3600 + 43*60 + 18, "31h43m"},
		{6 * 3600, "6h00m"},
		{90, "1m30s"},
		{9, "9s"},
		{-1, "0s"},
	} {
		if got := HumanDuration(tc.in); got != tc.want {
			t.Errorf("HumanDuration(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHangThresholdIsAParameterNotAConstant — the threshold is driven from both
// sides at one instant. A test that can only assert the default has not shown
// the comparison is live.
func TestHangThresholdIsAParameterNotAConstant(t *testing.T) {
	log := `[2026-08-09T02:00:00Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-09T04:00:00Z] pogo-deploy: end (rc=0 after 7200s)
`
	now := atBST(t, "2026-08-09T12:00")

	if rep := CheckNoFireWithin(logPath, log, true, now, prodSchedule, 3*time.Hour); rep.HungTotal != 0 {
		t.Errorf("a 2h run was reported hung under a 3h threshold: %+v", rep.Hung)
	}
	rep := CheckNoFireWithin(logPath, log, true, now, prodSchedule, 1*time.Hour)
	if rep.HungTotal != 1 {
		t.Fatalf("a 2h run was not reported hung under a 1h threshold: %+v", rep)
	}
	if rep.HungAfterSeconds != 3600 {
		t.Errorf("HungAfterSeconds = %d, want 3600 — a reader cannot judge the judgement without the threshold it used", rep.HungAfterSeconds)
	}
}
