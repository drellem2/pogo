package driftwatch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// The MEASURED state that produced mg-5bd2, replayed as constants so the
// detector is tested against a real known-bad reading rather than an invented
// one. Taken from the incident:
//
//	curl -s localhost:10000/version | jq -r .revision
//	  d31297f493cdd757fc46654351e0a2c93e66f49b   (commit dated 2026-07-30)
//	origin/main
//	  091cd6e                                    (fetched 13:39 local, 2026-08-07)
//	85 commits behind.
const (
	staleRevisionSHA = "d31297f493cdd757fc46654351e0a2c93e66f49b"
	staleCommitTime  = "2026-07-30T00:34:07Z"
	staleBehindMain  = 85
	// measuredAt is when the gap above was measured (13:39 local BST = 12:39Z).
	measuredAt = "2026-08-07T12:39:00Z"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test timestamp %q: %v", s, err)
	}
	return ts.UTC()
}

// fixedRevision is a RevisionFunc that always reports the same stamp.
func fixedRevision(sha string, commit time.Time) RevisionFunc {
	return func() SelfRevision { return SelfRevision{Revision: sha, CommitTime: commit} }
}

// fixedBehind is a BehindFunc reporting a constant count.
func fixedBehind(n int) BehindFunc {
	return func(string) Behind { return Behind{Count: n, Known: true} }
}

// revCfg is a DriftWatchConfig with no mirrors involved, so these tests exercise
// the revision half of the runner in isolation.
func revCfg() config.DriftWatchConfig {
	return config.DriftWatchConfig{Enabled: true, Interval: 15 * time.Minute}
}

// TestPositiveControlAgainstTheMeasuredStaleDaemon is acceptance requirement 3:
// a detector introduced alongside its own fix has never been shown to fire on
// anything, and here a known-bad state was available, so it is replayed exactly.
// The reading is the one measured on 2026-08-07 — the daemon running a binary
// built from a 2026-07-30 commit, 85 commits behind main.
func TestPositiveControlAgainstTheMeasuredStaleDaemon(t *testing.T) {
	rec := &recorder{}
	now := mustTime(t, measuredAt)

	w := New(revCfg(), Options{
		Revision:   fixedRevision(staleRevisionSHA, mustTime(t, staleCommitTime)),
		Behind:     fixedBehind(staleBehindMain),
		BehindRepo: "/Users/daniel/dev/pogo",
		Mail:       rec.mail,
		Emit:       rec.emit,
	})

	w.Check(now)

	if rec.mailCount() != 1 {
		t.Fatalf("the measured 8-day-old daemon must raise exactly one notice, got %d", rec.mailCount())
	}
	m := rec.mails[0]
	if m.to != mailTo {
		t.Errorf("staleness notice must go to %q, went to %q", mailTo, m.to)
	}
	// The subject is what travels: it must carry the age, the revision and the
	// commits-behind number without the reader opening the body.
	for _, want := range []string{"8-day-old", "d31297f4", "2026-07-30", "85 commits behind main"} {
		if !strings.Contains(m.subject, want) {
			t.Errorf("subject must carry %q, got %q", want, m.subject)
		}
	}
	// The body must carry the full SHA so the reader can verify independently.
	if !strings.Contains(m.body, staleRevisionSHA) {
		t.Errorf("body must carry the full revision, got:\n%s", m.body)
	}
	if !strings.Contains(m.body, "REPORT-ONLY") {
		t.Errorf("body must say it did not redeploy, got:\n%s", m.body)
	}

	if len(rec.events) != 1 || rec.events[0].EventType != "revision_stale" {
		t.Fatalf("expected one revision_stale event, got %+v", rec.events)
	}
	d := rec.events[0].Details
	if d["age_days"] != 8 {
		t.Errorf("event age_days = %v, want 8", d["age_days"])
	}
	if d["behind_main"] != staleBehindMain {
		t.Errorf("event behind_main = %v, want %d", d["behind_main"], staleBehindMain)
	}
	if d["revision"] != staleRevisionSHA {
		t.Errorf("event revision = %v, want %s", d["revision"], staleRevisionSHA)
	}
}

// TestQuietAgainstACurrentDaemon is the other arm (acceptance requirement 4).
// The same watcher, the same threshold, a daemon running last night's commit:
// no mail, no event. A detector that only ever fires is as uninformative as one
// that never does.
func TestQuietAgainstACurrentDaemon(t *testing.T) {
	rec := &recorder{}
	now := mustTime(t, measuredAt)

	w := New(revCfg(), Options{
		// Built from a commit made last night — what a working nightly deploy
		// leaves behind.
		Revision: fixedRevision("091cd6e0000000000000000000000000000000aa", now.Add(-14*time.Hour)),
		Behind:   fixedBehind(0),
		Mail:     rec.mail,
		Emit:     rec.emit,
	})

	w.Check(now)

	if rec.mailCount() != 0 {
		t.Errorf("a current daemon must not raise anything, got %d mail(s): %+v", rec.mailCount(), rec.mails)
	}
	if len(rec.events) != 0 {
		t.Errorf("a current daemon must not emit a stale event, got %+v", rec.events)
	}
}

// TestThresholdBoundaryIsWhereNWasChosen records WHY N is 7 days by pinning the
// boundary against the incident's own timeline: the running binary's commit is
// 2026-07-30T00:34Z, so a 7-day threshold first fires on 2026-08-06 — one day
// before a human noticed the 85-commit gap by hand — and is quiet the evening
// before. This is the arithmetic that makes N a decision rather than a default.
func TestThresholdBoundaryIsWhereNWasChosen(t *testing.T) {
	rev := SelfRevision{Revision: staleRevisionSHA, CommitTime: mustTime(t, staleCommitTime)}

	quiet := evaluate(rev, nil, mustTime(t, "2026-08-05T23:00:00Z"), DefaultStaleAfter)
	if quiet.Stale {
		t.Errorf("N=7d must still be quiet on 08-05 (age %s)", formatAge(quiet.Age))
	}
	fires := evaluate(rev, nil, mustTime(t, "2026-08-06T01:00:00Z"), DefaultStaleAfter)
	if !fires.Stale {
		t.Errorf("N=7d must fire on 08-06 (age %s)", formatAge(fires.Age))
	}
	if DefaultStaleAfter != 7*24*time.Hour {
		t.Errorf("N moved to %s — update the rationale in revision.go before changing it", DefaultStaleAfter)
	}
}

// --- the three deploy failure modes -------------------------------------

// deployMode is how one night's nightly deploy job behaved.
type deployMode int

const (
	// deployWorked: the job ran, exited 0, and the new binary reached the daemon.
	deployWorked deployMode = iota
	// deployFailedLoudly: the job ran and exited non-zero. The OLD alarm — the
	// one indexed to the exit code — does see this one.
	deployFailedLoudly
	// deployNeverFired: no `pogo-deploy: start` line at all. No exit code, so
	// the old alarm has nothing to be indexed to and stays silent. This is what
	// happened on 08-01..08-04.
	deployNeverFired
	// deployExitedZeroWithoutInstalling: the job ran, reported success, and the
	// new binary never reached the daemon. The old alarm reads exit 0 and calls
	// it health. No existing instrument covers this one.
	deployExitedZeroWithoutInstalling
)

// nightlyDeploy models the deploy job and the daemon it is supposed to update.
// `installed` is the revision the daemon is actually running; `exitAlarm`
// records whether the legacy exit-code-indexed alarm would have raised anything.
// It exists so the tests can assert the structural claim in mg-5bd2 directly:
// two of the three modes are invisible to the legacy alarm and all three are
// visible to this detector.
type nightlyDeploy struct {
	installed     SelfRevision
	exitAlarmMail int
}

func (d *nightlyDeploy) night(mode deployMode, n int, commit time.Time) {
	switch mode {
	case deployWorked:
		d.installed = SelfRevision{Revision: fmt.Sprintf("%040x", n), CommitTime: commit}
	case deployFailedLoudly:
		// The binary is untouched, but there IS an exit code to alarm on.
		d.exitAlarmMail++
	case deployNeverFired, deployExitedZeroWithoutInstalling:
		// Binary untouched and nothing for the exit-code alarm to notice: no run
		// happened at all, or the run reported success.
	}
}

func (d *nightlyDeploy) revision() SelfRevision { return d.installed }

// runNights plays `nights` consecutive nights of the given mode against a daemon
// that starts on the measured stale binary, sampling the detector once a day,
// and returns the recorder plus the simulated deploy job.
func runNights(t *testing.T, mode deployMode, nights int) (*recorder, *nightlyDeploy) {
	t.Helper()
	rec := &recorder{}
	job := &nightlyDeploy{installed: SelfRevision{
		Revision:   staleRevisionSHA,
		CommitTime: mustTime(t, staleCommitTime),
	}}

	w := New(revCfg(), Options{
		Revision: job.revision,
		Mail:     rec.mail,
		Emit:     rec.emit,
	})

	// Start the clock the morning after the last good deploy and step a day at a
	// time: each night the job behaves as `mode`, each morning the detector runs.
	start := mustTime(t, staleCommitTime).Add(6 * time.Hour)
	for i := 1; i <= nights; i++ {
		day := start.Add(time.Duration(i) * 24 * time.Hour)
		job.night(mode, i, day)
		w.Check(day)
	}
	return rec, job
}

// TestFiresWhenTheDeployJobFailedLoudly is failure mode 1. The legacy alarm does
// cover this one; the point of asserting it here is that the new detector is not
// WORSE than what it backstops — it fires too, from a different observable.
func TestFiresWhenTheDeployJobFailedLoudly(t *testing.T) {
	rec, job := runNights(t, deployFailedLoudly, 10)

	if job.exitAlarmMail == 0 {
		t.Fatal("test setup wrong: a loud failure must produce legacy exit-code alarms")
	}
	if rec.mailCount() == 0 {
		t.Fatal("the staleness detector must also fire when the job fails loudly")
	}
	if !strings.Contains(rec.mails[0].subject, "d31297f4") {
		t.Errorf("notice must name the stuck revision, got %q", rec.mails[0].subject)
	}
}

// TestFiresWhenTheDeployJobNeverFired is failure mode 2 — the one that actually
// happened on 2026-08-01..08-04. The job produced no exit code, so the legacy
// alarm raised NOTHING across every night; the staleness detector fires anyway,
// because it never asked about the job.
func TestFiresWhenTheDeployJobNeverFired(t *testing.T) {
	rec, job := runNights(t, deployNeverFired, 10)

	if job.exitAlarmMail != 0 {
		t.Fatalf("test setup wrong: a job that never fires has no exit code to alarm on, got %d alarms", job.exitAlarmMail)
	}
	if rec.mailCount() == 0 {
		t.Fatal("four silent nights and nothing raised is the whole incident: the staleness detector must fire")
	}
	if !strings.Contains(rec.mails[0].body, "never fires") {
		t.Errorf("the notice must explain why the deploy alarms were silent, got:\n%s", rec.mails[0].body)
	}
}

// TestFiresWhenTheDeployJobExitedZeroWithoutInstalling is failure mode 3 — the
// one no existing instrument covers. Every night the job runs and reports
// success; the legacy alarm reads exit 0 and calls it health. Only a check that
// reads the RUNNING binary can tell the difference, and it does.
func TestFiresWhenTheDeployJobExitedZeroWithoutInstalling(t *testing.T) {
	rec, job := runNights(t, deployExitedZeroWithoutInstalling, 10)

	if job.exitAlarmMail != 0 {
		t.Fatalf("test setup wrong: exit 0 gives the legacy alarm nothing to raise, got %d alarms", job.exitAlarmMail)
	}
	if job.revision().Revision != staleRevisionSHA {
		t.Fatalf("test setup wrong: the daemon must still be on the old binary, got %s", job.revision().Revision)
	}
	if rec.mailCount() == 0 {
		t.Fatal("a job that exits 0 without installing must still be caught: the daemon is not current")
	}
}

// TestQuietWhileTheDeployJobKeepsWorking closes the loop on the three modes
// above: the same harness, the same threshold, a job that installs every night —
// and the detector never raises anything across ten nights. Without this arm the
// three tests above would pass on a detector that simply always fires.
func TestQuietWhileTheDeployJobKeepsWorking(t *testing.T) {
	rec, job := runNights(t, deployWorked, 10)

	if job.revision().Revision == staleRevisionSHA {
		t.Fatal("test setup wrong: a working deploy must replace the running binary")
	}
	if rec.mailCount() != 0 {
		t.Errorf("a working nightly deploy must stay quiet, got %d notice(s): %+v", rec.mailCount(), rec.mails)
	}
	if len(rec.events) != 0 {
		t.Errorf("a working nightly deploy must emit no stale events, got %d", len(rec.events))
	}
}

// --- bounding (acceptance requirement 5) --------------------------------

// TestNoticesAreBoundedWhileTheConditionHolds is the requirement that the alarm
// cannot become another thing people filter out. The condition holds for DAYS
// and the sampler runs every 15 minutes, so an unbounded detector would mail ~96
// times a day. This asserts the whole budget: four notices, at detection / +1d /
// +3d / +7d, and then silence for that revision — while the event log keeps
// recording every single sample, so exhausting the mail budget never makes the
// condition invisible.
func TestNoticesAreBoundedWhileTheConditionHolds(t *testing.T) {
	rec := &recorder{}
	start := mustTime(t, measuredAt)

	w := New(revCfg(), Options{
		Revision: fixedRevision(staleRevisionSHA, mustTime(t, staleCommitTime)),
		Mail:     rec.mail,
		Emit:     rec.emit,
	})

	// 14 days of heartbeat ticks at the ~30s cadence pogod actually runs.
	var mailTimes []time.Duration
	prev := 0
	for tick := time.Duration(0); tick <= 14*24*time.Hour; tick += 30 * time.Second {
		w.Check(start.Add(tick))
		if rec.mailCount() > prev {
			prev = rec.mailCount()
			mailTimes = append(mailTimes, tick)
		}
	}

	if len(mailTimes) != DefaultStaleMaxNotices {
		t.Fatalf("staleness held for 14 days and produced %d notices, want exactly %d: %v",
			len(mailTimes), DefaultStaleMaxNotices, mailTimes)
	}
	want := []time.Duration{0, 24 * time.Hour, 72 * time.Hour, 168 * time.Hour}
	for i, wt := range want {
		if mailTimes[i] != wt {
			t.Errorf("notice %d landed at %s, want %s (backoff must double)", i+1, mailTimes[i], wt)
		}
	}

	// Every notice says which one it is and that silence afterwards is not the
	// condition clearing.
	last := rec.mails[len(rec.mails)-1]
	if !strings.Contains(last.body, fmt.Sprintf("NOTICE %d OF %d", DefaultStaleMaxNotices, DefaultStaleMaxNotices)) {
		t.Errorf("the final notice must say it is the last one, got:\n%s", last.body)
	}
	if !strings.Contains(last.body, "silence from here is not the condition clearing") {
		t.Errorf("the final notice must say what its own silence means, got:\n%s", last.body)
	}

	// The unbounded half: the event log recorded every stale sample, so the
	// condition stays observable long after the mail budget is spent. 14 days of
	// 15-minute samples.
	wantEvents := int((14 * 24 * time.Hour) / (15 * time.Minute))
	if len(rec.events) < wantEvents {
		t.Errorf("event log must record EVERY stale sample (%d), got %d — the pull-side record is what makes the mail cap safe",
			wantEvents, len(rec.events))
	}
	// And the last event records that it was NOT mailed, so a reader can tell a
	// suppressed sample from a delivered one.
	lastEvent := rec.events[len(rec.events)-1]
	if lastEvent.Details["mailed"] != false {
		t.Errorf("a suppressed sample must record mailed=false, got %+v", lastEvent.Details)
	}
}

// TestRestartOntoTheSameStaleBinaryReArmsTheBudget guards the reset rule. The
// ratchet is keyed on the running revision, and a restart gives a fresh Watcher;
// on this box a bounce on 2026-08-04 re-launched the SAME 2026-07-30 binary, so
// a restart that did not fix staleness must be able to say so again rather than
// inheriting a spent budget.
func TestRestartOntoTheSameStaleBinaryReArmsTheBudget(t *testing.T) {
	start := mustTime(t, measuredAt)
	rec := &recorder{}
	w := New(revCfg(), Options{
		Revision: fixedRevision(staleRevisionSHA, mustTime(t, staleCommitTime)),
		Mail:     rec.mail, Emit: rec.emit,
	})
	for tick := time.Duration(0); tick <= 8*24*time.Hour; tick += time.Hour {
		w.Check(start.Add(tick))
	}
	if rec.mailCount() != DefaultStaleMaxNotices {
		t.Fatalf("pre-restart budget = %d, want %d", rec.mailCount(), DefaultStaleMaxNotices)
	}

	// pogod restarts. Same stale binary comes back up.
	rec2 := &recorder{}
	w2 := New(revCfg(), Options{
		Revision: fixedRevision(staleRevisionSHA, mustTime(t, staleCommitTime)),
		Mail:     rec2.mail, Emit: rec2.emit,
	})
	w2.Check(start.Add(9 * 24 * time.Hour))
	if rec2.mailCount() != 1 {
		t.Errorf("a restart that did not fix the staleness must raise again, got %d", rec2.mailCount())
	}
}

// TestNewRevisionResetsTheNoticeBudget proves the ratchet is keyed on the
// revision and not on the process: once a genuinely new (but still old) binary
// is running, its staleness gets its own budget rather than inheriting the
// previous one's silence.
func TestNewRevisionResetsTheNoticeBudget(t *testing.T) {
	rec := &recorder{}
	commit := mustTime(t, staleCommitTime)
	rev := staleRevisionSHA
	start := mustTime(t, measuredAt)

	w := New(revCfg(), Options{
		Revision: func() SelfRevision { return SelfRevision{Revision: rev, CommitTime: commit} },
		Mail:     rec.mail, Emit: rec.emit,
	})

	w.Check(start)
	w.Check(start.Add(time.Hour)) // inside the backoff — silent
	if rec.mailCount() != 1 {
		t.Fatalf("expected 1 notice before the revision changed, got %d", rec.mailCount())
	}

	// A deploy lands a binary that is itself already older than N (e.g. a
	// rebuild of an old tag). Different revision, so a fresh budget.
	rev = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeee"
	commit = start.Add(-30 * 24 * time.Hour)
	w.Check(start.Add(2 * time.Hour))
	if rec.mailCount() != 2 {
		t.Errorf("a new revision must get its own first notice, got %d total", rec.mailCount())
	}
	if !strings.Contains(rec.mails[1].subject, "aaaaaaaa") {
		t.Errorf("the second notice must name the new revision, got %q", rec.mails[1].subject)
	}
}

// --- blindness, gating, and the coarse interval --------------------------

// TestUnstampedBinaryDeclaresItsBlindnessOnce covers the build with no vcs
// stamp (every `go test` binary, and any `go run`). It cannot be dated, so it
// must not mail — but it must not be silently indistinguishable from a healthy
// daemon either, which is the mistake this whole ticket is about. It says so
// once, in the log and in the event stream, and then stays quiet.
func TestUnstampedBinaryDeclaresItsBlindnessOnce(t *testing.T) {
	rec := &recorder{}
	w := New(revCfg(), Options{
		Revision: func() SelfRevision { return SelfRevision{} },
		Mail:     rec.mail, Emit: rec.emit,
	})

	start := mustTime(t, measuredAt)
	for i := 0; i < 5; i++ {
		w.Check(start.Add(time.Duration(i) * time.Hour))
	}

	if rec.mailCount() != 0 {
		t.Errorf("an undateable binary must not mail, got %d", rec.mailCount())
	}
	if len(rec.events) != 1 || rec.events[0].EventType != "revision_stale_disarmed" {
		t.Fatalf("expected exactly one revision_stale_disarmed event across 5 samples, got %+v", rec.events)
	}
}

// TestBehindCountIsContextNeverAGate is the design point that keeps this
// detector from going dark the way the one it replaces did. origin/main is a
// remote-tracking ref that only a fetch refreshes, so "0 commits behind" can
// mean "current" or "nobody has fetched in a week". If that number could
// suppress the alarm, an unfetched repo would silence it — the same
// proxy-goes-dark shape, one layer down. It reports and never gates.
func TestBehindCountIsContextNeverAGate(t *testing.T) {
	rec := &recorder{}
	w := New(revCfg(), Options{
		Revision: fixedRevision(staleRevisionSHA, mustTime(t, staleCommitTime)),
		Behind:   fixedBehind(0), // a stale remote-tracking ref reads zero
		Mail:     rec.mail, Emit: rec.emit,
	})

	w.Check(mustTime(t, measuredAt))

	if rec.mailCount() != 1 {
		t.Fatalf("behind=0 must NOT suppress an 8-day-old revision, got %d notice(s)", rec.mailCount())
	}
	if !strings.Contains(rec.mails[0].subject, "0 commits behind main") {
		t.Errorf("the count is still reported, got %q", rec.mails[0].subject)
	}
}

// TestUnknownBehindCountStillAlarms covers the other half: no repo configured,
// or git failed. The verdict is computed from the binary's own stamp, so it does
// not depend on the repo at all.
func TestUnknownBehindCountStillAlarms(t *testing.T) {
	rec := &recorder{}
	w := New(revCfg(), Options{
		Revision: fixedRevision(staleRevisionSHA, mustTime(t, staleCommitTime)),
		Behind:   func(string) Behind { return Behind{} },
		Mail:     rec.mail, Emit: rec.emit,
	})

	w.Check(mustTime(t, measuredAt))

	if rec.mailCount() != 1 {
		t.Fatalf("an unavailable commits-behind count must not disarm the check, got %d", rec.mailCount())
	}
	if strings.Contains(rec.mails[0].subject, "commits behind") {
		t.Errorf("an unknown count must be omitted from the subject, not guessed: %q", rec.mails[0].subject)
	}
	if _, ok := rec.events[0].Details["behind_main"]; ok {
		t.Errorf("an unknown count must be absent from the event, got %+v", rec.events[0].Details)
	}
}

// TestForeignRevisionIsNamedNotShruggedAt covers a real state on this box,
// found while building the acceptance for mg-5bd2: ~/.pogo is itself a git
// repo, so `go build` run inside a polecat worktree (~/.pogo/polecats/*) walks
// up and stamps ~/.pogo's HEAD — a SHA that does not exist in the pogo repo at
// all, dated a month ago. Folding that into "commits-behind unavailable" would
// let the notice report a confident age measured against an unrelated project's
// history. It must say so instead, in the subject, where it cannot be missed.
func TestForeignRevisionIsNamedNotShruggedAt(t *testing.T) {
	rec := &recorder{}
	w := New(revCfg(), Options{
		Revision:   fixedRevision("ec68dc1a2c49d285521117d7307690f3d521f17f", mustTime(t, "2026-07-07T17:01:35Z")),
		Behind:     func(string) Behind { return Behind{Foreign: true} },
		BehindRepo: "/Users/daniel/dev/pogo",
		Mail:       rec.mail, Emit: rec.emit,
	})

	w.Check(mustTime(t, measuredAt))

	if rec.mailCount() != 1 {
		t.Fatalf("a foreign revision is still an undeployable daemon and must raise, got %d", rec.mailCount())
	}
	if !strings.Contains(rec.mails[0].subject, "NOT a commit in the pogo repo") {
		t.Errorf("the subject must say the revision is not from this repo, got %q", rec.mails[0].subject)
	}
	if strings.Contains(rec.mails[0].subject, "commits behind") {
		t.Errorf("a foreign revision has no meaningful behind-count to quote, got %q", rec.mails[0].subject)
	}
	if !strings.Contains(rec.mails[0].body, "not a statement about pogo") {
		t.Errorf("the body must warn that the age is measured against another history, got:\n%s", rec.mails[0].body)
	}
	if rec.events[0].Details["revision_foreign"] != true {
		t.Errorf("event must record revision_foreign, got %+v", rec.events[0].Details)
	}
}

// TestStalenessRidesTheSameCoarseThrottle confirms the new check obeys the
// existing runner's throttle rather than adding a second cadence: mg-345b's
// ruling was that this must not run on every ~30s tick, and revision.go samples
// inside the same slot as the mirror check.
func TestStalenessRidesTheSameCoarseThrottle(t *testing.T) {
	rec := &recorder{}
	samples := 0
	w := New(revCfg(), Options{
		Revision: func() SelfRevision {
			samples++
			return SelfRevision{Revision: staleRevisionSHA, CommitTime: mustTime(t, staleCommitTime)}
		},
		Mail: rec.mail, Emit: rec.emit,
	})

	start := mustTime(t, measuredAt)
	for i := 0; i < 30; i++ { // 15 minutes of 30s ticks
		w.Check(start.Add(time.Duration(i) * 30 * time.Second))
	}
	if samples != 1 {
		t.Fatalf("the staleness check must not sample every heartbeat tick: %d samples in one interval", samples)
	}
	w.Check(start.Add(15*time.Minute + time.Second))
	if samples != 2 {
		t.Fatalf("expected a second sample past the coarse interval, got %d", samples)
	}
}

// TestDisabledWatcherDoesNotCheckRevision confirms the Enabled flag is the off
// switch for BOTH halves of the runner.
func TestDisabledWatcherDoesNotCheckRevision(t *testing.T) {
	rec := &recorder{}
	samples := 0
	w := New(config.DriftWatchConfig{Enabled: false, Interval: time.Minute}, Options{
		Revision: func() SelfRevision {
			samples++
			return SelfRevision{Revision: staleRevisionSHA, CommitTime: mustTime(t, staleCommitTime)}
		},
		Mail: rec.mail, Emit: rec.emit,
	})
	w.Check(mustTime(t, measuredAt))
	if samples != 0 || rec.mailCount() != 0 {
		t.Errorf("disabled runner must be inert: samples=%d mails=%d", samples, rec.mailCount())
	}
}

// TestRevisionCheckArmsWithoutMirrors is the regression guard for the wiring
// change: before mg-5bd2 the whole runner was armed only when [reconcile]
// mirrors existed. The staleness check must not inherit that gate — a daemon
// with no mirrors configured is exactly as capable of running week-old code.
func TestRevisionCheckArmsWithoutMirrors(t *testing.T) {
	rec := &recorder{}
	w := New(revCfg(), Options{
		Mirrors:  nil,
		Revision: fixedRevision(staleRevisionSHA, mustTime(t, staleCommitTime)),
		Mail:     rec.mail, Emit: rec.emit,
	})
	w.Check(mustTime(t, measuredAt))
	if rec.mailCount() != 1 {
		t.Errorf("the staleness check must arm with no mirrors configured, got %d notice(s)", rec.mailCount())
	}
}

// TestMailFailureStillEmitsStaleEvent mirrors the mirror-check guarantee: a
// staleness that could not be reported is still recorded, so the condition is
// never lost to a down mail channel.
func TestMailFailureStillEmitsStaleEvent(t *testing.T) {
	rec := &recorder{mailErr: errFake}
	w := New(revCfg(), Options{
		Revision: fixedRevision(staleRevisionSHA, mustTime(t, staleCommitTime)),
		Mail:     rec.mail, Emit: rec.emit,
	})
	w.Check(mustTime(t, measuredAt))

	if len(rec.events) != 1 {
		t.Fatalf("expected the stale event even when mail failed, got %d", len(rec.events))
	}
	if _, ok := rec.events[0].Details["mail_error"]; !ok {
		t.Errorf("event must record mail_error when delivery failed, details=%+v", rec.events[0].Details)
	}
}

// --- the pieces that touch the real world --------------------------------

// TestBuildRevisionReadsTheRunningBinary exercises the production RevisionFunc.
// A `go test` binary carries no vcs stamp, so the assertion is the honest one:
// whatever it returns must be self-consistent — either fully stamped or
// recognised as unstamped — never a half-read that would be silently dated
// against the zero time (which would report a daemon as ~2000 years stale).
func TestBuildRevisionReadsTheRunningBinary(t *testing.T) {
	got := BuildRevision()
	if got.Stamped() {
		if got.CommitTime.IsZero() || got.Revision == "" {
			t.Fatalf("Stamped() must imply both fields are set, got %+v", got)
		}
	} else if evaluate(got, nil, time.Now(), DefaultStaleAfter).Stale {
		t.Fatalf("an unstamped build must never evaluate as stale, got %+v", got)
	}
	t.Logf("running binary: revision=%q commit=%v stamped=%v", got.Short(), got.CommitTime, got.Stamped())
}

// TestGitBehindCountsAgainstARealRepo exercises the context lookup against a
// real git repository, including the two ways it is allowed to fail (an unknown
// revision, a path that is not a repo) — each of which must report "unknown"
// rather than a wrong number, because a wrong count in a notice is worse than a
// missing one.
func TestGitBehindCountsAgainstARealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", name)
		run("commit", "-q", "-m", name)
		return run("rev-parse", "HEAD")
	}
	old := commit("a")
	commit("b")
	tip := commit("c")
	// Stand in for the fetched remote-tracking ref without needing a remote.
	run("update-ref", "refs/remotes/origin/main", tip)

	behind := GitBehind(dir)

	if b := behind(old); !b.Known || b.Count != 2 {
		t.Errorf("behind(old) = %+v; want Count 2, Known true", b)
	}
	if b := behind(tip); !b.Known || b.Count != 0 {
		t.Errorf("behind(tip) = %+v; want Count 0, Known true", b)
	}
	// A revision this repo has never heard of is FOREIGN, not merely unknown:
	// the binary was built somewhere else, which is a different fact and a more
	// alarming one than "the count was unavailable".
	if b := behind("0000000000000000000000000000000000000000"); b.Known || !b.Foreign {
		t.Errorf("an alien revision must report Foreign, got %+v", b)
	}
	// A path that is not a repo at all must NOT be called foreign — we cannot
	// tell, and claiming the binary came from elsewhere would be a guess.
	if b := GitBehind(filepath.Join(dir, "nope"))(tip); b.Known || b.Foreign {
		t.Errorf("an unreadable repo must report plain unknown, got %+v", b)
	}
}

// TestLiveDaemonStaleness is the reproducible form of acceptance requirement 3:
// point the real predicate at the real running daemon and print the notice it
// would send. It is OFF by default (it needs a live pogod on the loopback and
// would be a network dependency in CI) and is enabled with:
//
//	POGO_LIVE_STALENESS_CHECK=1 go test ./internal/driftwatch/ -run LiveDaemon -v
//
// It asserts nothing about staleness — the state of the box is not a property of
// this code — but it does assert the reading is well-formed, and it logs the
// verdict so the check can be re-run against the box on any future day rather
// than surviving only as a transcript in a commit message.
//
// It reads GET /version rather than this binary's own stamp because the target
// is another process. Note the caveat that keeps the production path in-process:
// a loopback probe can be answered by something that is not pogod (mg-e314), so
// this is a diagnostic, not the detector's own input.
func TestLiveDaemonStaleness(t *testing.T) {
	if os.Getenv("POGO_LIVE_STALENESS_CHECK") == "" {
		t.Skip("set POGO_LIVE_STALENESS_CHECK=1 to run against the live daemon")
	}
	addr := os.Getenv("POGO_LIVE_STALENESS_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:10000"
	}

	resp, err := http.Get(addr + "/version")
	if err != nil {
		t.Fatalf("GET %s/version: %v", addr, err)
	}
	defer resp.Body.Close()
	var v struct {
		Revision  string `json:"revision"`
		Time      string `json:"time"`
		Modified  bool   `json:"modified"`
		StartTime string `json:"start_time"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode /version: %v", err)
	}

	rev := SelfRevision{Revision: v.Revision, Modified: v.Modified}
	if ts, err := time.Parse(time.RFC3339, v.Time); err == nil {
		rev.CommitTime = ts.UTC()
	}
	if !rev.Stamped() {
		t.Fatalf("live daemon reports an undateable build: %+v", v)
	}

	var behind BehindFunc
	repo := os.Getenv("POGO_LIVE_STALENESS_REPO")
	if repo != "" {
		behind = GitBehind(repo)
	}

	now := time.Now().UTC()
	s := evaluate(rev, behind, now, DefaultStaleAfter)

	t.Logf("live daemon at %s: revision=%s commit=%s start_time=%s",
		addr, s.Short(), s.CommitTime.Format(time.RFC3339), v.StartTime)
	t.Logf("age=%s threshold=%s behind_main=%d foreign=%v STALE=%v",
		formatAge(s.Age), formatAge(s.Threshold), s.Behind, s.Foreign, s.Stale)
	if s.Stale {
		t.Logf("notice it would send:\nSubject: %s\n\n%s", staleSubject(s), staleBody(s, 1, DefaultStaleMaxNotices, repo))
	} else {
		t.Logf("no notice: the daemon is current (uptime says nothing about this — start_time is %s)", v.StartTime)
	}
}

// TestFormatAge keeps the notice's units readable: Go's default duration string
// renders eight days as "192h0m0s", which is not what belongs in a subject line
// a human is meant to skim.
func TestFormatAge(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{8*24*time.Hour + 12*time.Hour, "8d12h"},
		{7 * 24 * time.Hour, "7d"},
		{5 * time.Hour, "5h"},
		{-time.Hour, "0h"},
	} {
		if got := formatAge(tc.in); got != tc.want {
			t.Errorf("formatAge(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
