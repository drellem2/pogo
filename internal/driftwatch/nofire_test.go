package driftwatch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/staleness"
)

// nofireBST pins the zone the incident happened in, so these tests name the
// same nights on a runner in any timezone.
var nofireBST = time.FixedZone("BST", 60*60)

func nofireAt(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02T15:04", s, nofireBST)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// nofireSchedule is the shipped one — 03/04/05 local, 2h grace.
var nofireSchedule = staleness.DeploySchedule{Hours: []int{3, 4, 5}, Minute: 0, Grace: staleness.DefaultGrace}

// incidentLog is the start lines of the real ~/Library/Logs/pogo/pogo-deploy.log
// as it stood on 2026-08-07. 08-01, 08-02, 08-03 and 08-04 are absent — that
// absence is the whole subject.
const incidentLog = `[2026-07-29T02:00:04Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)
[2026-07-29T18:32:12Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=true)
[2026-07-30T02:00:02Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)
[2026-07-31T02:00:00Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-5 dry_run=false)
[2026-08-05T02:00:03Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-06T02:00:04Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
[2026-08-07T02:00:05Z] pogo-deploy: start (src=/Users/daniel/.pogo/deploy-src window=2-6 dry_run=false)
`

const testLogPath = "/Users/daniel/Library/Logs/pogo/pogo-deploy.log"

// fixedLog is a DeployLogFunc returning constant text.
func fixedLog(text string) DeployLogFunc {
	return func() (string, bool, error) { return text, true, nil }
}

func installed() DeployInstalledFunc {
	return func() (bool, string) { return true, "/Users/daniel/Library/LaunchAgents/com.pogo.deploy.plist" }
}

// noFireOpts builds Options exercising the no-fire half alone — no mirrors, no
// revision — so these tests prove the check is armed INDEPENDENTLY. A no-fire
// detector that only runs when a mirror or a vcs stamp happens to be present is
// the inert-detector shape this ticket exists to remove.
func noFireOpts(rec *recorder, text string) Options {
	return Options{
		Mail:            rec.mail,
		Emit:            rec.emit,
		DeployLog:       fixedLog(text),
		DeployLogPath:   testLogPath,
		DeployInstalled: installed(),
		Schedule:        nofireSchedule,
	}
}

func noFireCfg() config.DriftWatchConfig {
	return config.DriftWatchConfig{Enabled: true, Interval: 15 * time.Minute}
}

// TestNoFirePositiveControlOnTheRealIncident is the acceptance case: the runner,
// given the log exactly as it stood, must mail `human` and name the four nights
// the job never started.
//
// Everything about this outage was invisible precisely because it produced no
// output, so the detector's own proof has to come from a replayed record rather
// than from having watched it fire.
func TestNoFirePositiveControlOnTheRealIncident(t *testing.T) {
	rec := &recorder{}
	w := New(noFireCfg(), noFireOpts(rec, incidentLog))

	w.Check(nofireAt(t, "2026-08-07T12:00"))

	if rec.mailCount() != 1 {
		t.Fatalf("mails = %d, want 1 — four nights the job never ran produced no notice", rec.mailCount())
	}
	m := rec.mails[0]
	if m.to != "human" {
		t.Errorf("mailed %q, want human — a deploy that stopped running is for a person, not the mayor's coordination inbox", m.to)
	}
	if !strings.Contains(m.subject, "DID NOT RUN") {
		t.Errorf("subject = %q, want it to say the job DID NOT RUN — 'failed' is the wrong claim and sends the reader to the rc", m.subject)
	}
	if !strings.Contains(m.subject, "4") {
		t.Errorf("subject = %q, want the count of silent nights in the part that travels", m.subject)
	}
	for _, night := range []string{"2026-08-01", "2026-08-02", "2026-08-03", "2026-08-04"} {
		if !strings.Contains(m.body, night) {
			t.Errorf("body does not name silent night %s", night)
		}
	}
	// The body must send the reader somewhere useful, and must warn them off the
	// one reading that looks authoritative and is not: launchd's `runs` counter
	// was measured at 0 on this box AFTER seven fires, because re-installing the
	// plist resets it.
	if !strings.Contains(m.body, "runs") {
		t.Error("body does not warn that launchctl's `runs` counter cannot answer this")
	}

	var fired bool
	for _, e := range rec.events {
		if e.EventType == "deploy_nofire" {
			fired = true
			if got := e.Details["missed_total"]; got != 4 {
				t.Errorf("event missed_total = %v, want 4", got)
			}
		}
	}
	if !fired {
		t.Error("no deploy_nofire event emitted")
	}
}

// TestNoFireQuietOnAHealthyLog is the other half of the control, at the same
// instant and with the same schedule: an alarm only shown to be loud has not
// been shown to discriminate.
func TestNoFireQuietOnAHealthyLog(t *testing.T) {
	healthy := incidentLog +
		"[2026-08-01T02:00:01Z] pogo-deploy: start (src=/x window=2-5 dry_run=false)\n" +
		"[2026-08-02T02:00:01Z] pogo-deploy: start (src=/x window=2-5 dry_run=false)\n" +
		"[2026-08-03T02:00:01Z] pogo-deploy: start (src=/x window=2-5 dry_run=false)\n" +
		"[2026-08-04T02:00:01Z] pogo-deploy: start (src=/x window=2-5 dry_run=false)\n"

	rec := &recorder{}
	w := New(noFireCfg(), noFireOpts(rec, healthy))
	w.Check(nofireAt(t, "2026-08-07T12:00"))

	if rec.mailCount() != 0 {
		t.Fatalf("mailed on a log where every due night fired: %q", rec.mails[0].subject)
	}
}

// TestNoFireIsArmedWithoutMirrorsOrRevision states the arming property as its
// own test. The recurring defect in this lineage (mg-5701) is a detector that
// stays inert because some unrelated input was absent.
func TestNoFireIsArmedWithoutMirrorsOrRevision(t *testing.T) {
	rec := &recorder{}
	opts := noFireOpts(rec, incidentLog)
	if len(opts.Mirrors) != 0 || opts.Revision != nil {
		t.Fatal("fixture must carry neither mirrors nor a revision func")
	}
	w := New(noFireCfg(), opts)
	w.Check(nofireAt(t, "2026-08-07T12:00"))

	if rec.mailCount() != 1 {
		t.Fatalf("no-fire check did not run with no mirrors and no vcs stamp: mails = %d", rec.mailCount())
	}
}

// TestNoFireDisarmsLoudlyWithoutTheLaunchAgent: on a host with no nightly there
// is nothing to be silent, so the check must not alarm — but it must also not
// go quiet without saying so. A blind detector and a healthy host producing
// identical silence is the exact failure being removed.
func TestNoFireDisarmsLoudlyWithoutTheLaunchAgent(t *testing.T) {
	rec := &recorder{}
	opts := noFireOpts(rec, "")
	opts.DeployLog = func() (string, bool, error) { return "", false, nil }
	opts.DeployInstalled = func() (bool, string) { return false, "" }
	w := New(noFireCfg(), opts)

	w.Check(nofireAt(t, "2026-08-07T12:00"))

	if rec.mailCount() != 0 {
		t.Errorf("alarmed about a nightly that is not installed on this host: %q", rec.mails[0].subject)
	}
	var declared bool
	for _, e := range rec.events {
		if e.EventType == "deploy_nofire_disarmed" {
			declared = true
		}
	}
	if !declared {
		t.Error("no deploy_nofire_disarmed event — an unarmed detector must declare itself, not just stay quiet")
	}
}

// TestNoFireMissingLogAlarms: the deploy agent is installed and has never
// written a line. That is the loudest available reading and must not degrade to
// "0 missed nights".
func TestNoFireMissingLogAlarms(t *testing.T) {
	rec := &recorder{}
	opts := noFireOpts(rec, "")
	opts.DeployLog = func() (string, bool, error) { return "", false, nil }
	w := New(noFireCfg(), opts)

	w.Check(nofireAt(t, "2026-08-07T12:00"))

	if rec.mailCount() != 1 {
		t.Fatalf("mails = %d, want 1 for an installed nightly that has never logged", rec.mailCount())
	}
	if !strings.Contains(rec.mails[0].subject, "NEVER") {
		t.Errorf("subject = %q, want it to say the nightly has never run", rec.mails[0].subject)
	}
}

// TestNoFireUnreadableLogIsBlindNotHealthy: a log that exists and cannot be read
// tells us nothing. It must be declared once and must never be mailed as an
// outage — and it must certainly never report clean.
func TestNoFireUnreadableLogIsBlindNotHealthy(t *testing.T) {
	rec := &recorder{}
	opts := noFireOpts(rec, "")
	opts.DeployLog = func() (string, bool, error) { return "", true, errors.New("permission denied") }
	w := New(noFireCfg(), opts)

	w.Check(nofireAt(t, "2026-08-07T12:00"))

	if rec.mailCount() != 0 {
		t.Errorf("mailed an outage for a log it could not read: %q", rec.mails[0].subject)
	}
	var declared bool
	for _, e := range rec.events {
		if e.EventType == "deploy_nofire_disarmed" && e.Details["reason"] == "deploy log unreadable" {
			declared = true
		}
	}
	if !declared {
		t.Error("an unreadable deploy log left no record that the check was blind")
	}
}

// TestNoFireNoticeBudgetIsBoundedButANewNightAlwaysMails is the noise argument.
//
// The five `pogo-deploy` REDs that preceded this incident all reached Daniel and
// were all filtered, so a sixth alarm on the same channel is only an improvement
// if it cannot become background noise. An UNCHANGED outage must go quiet after
// the budget; another silent night is new information and must always get
// through.
func TestNoFireNoticeBudgetIsBoundedButANewNightAlwaysMails(t *testing.T) {
	rec := &recorder{}
	opts := noFireOpts(rec, incidentLog)
	opts.NoFireRenotify = time.Hour
	opts.NoFireMaxNotices = 2
	// A fine interval so the throttle does not hide the ratchet under test.
	w := New(config.DriftWatchConfig{Enabled: true, Interval: time.Second}, opts)

	base := nofireAt(t, "2026-08-07T12:00")
	// Notice 1, then 2 an hour later, then the budget is spent no matter how
	// long the same four nights stay unchanged.
	//
	// Every offset stays inside 08-07 on purpose: crossing into 08-08 would make
	// a FIFTH night due, which is a different condition and legitimately re-arms
	// the budget. That is the behaviour asserted below, and mixing it in here
	// would leave both halves untested.
	for _, offset := range []time.Duration{0, time.Hour, 4 * time.Hour, 9 * time.Hour} {
		w.Check(base.Add(offset))
	}
	if rec.mailCount() != 2 {
		t.Fatalf("mails = %d, want 2 — an unchanged outage must stop mailing at the budget", rec.mailCount())
	}

	// A FIFTH silent night. Same watcher, budget already spent, and it must
	// still mail: the count changed, so this is a different condition.
	w.deployLog = fixedLog(incidentLog)
	fifth := nofireAt(t, "2026-08-08T12:00")
	w.Check(fifth)
	if rec.mailCount() != 3 {
		t.Fatalf("mails = %d, want 3 — a new silent night was swallowed by a spent budget", rec.mailCount())
	}
	if !strings.Contains(rec.mails[2].subject, "5") {
		t.Errorf("subject = %q, want the grown count (5 nights)", rec.mails[2].subject)
	}
}

// TestNoFireEventOutlivesTheMailBudget: the mail cap must never make the
// condition invisible, or "pogod stopped mailing" becomes readable as "the
// nightly started firing again".
func TestNoFireEventOutlivesTheMailBudget(t *testing.T) {
	rec := &recorder{}
	opts := noFireOpts(rec, incidentLog)
	opts.NoFireRenotify = time.Hour
	opts.NoFireMaxNotices = 1
	w := New(config.DriftWatchConfig{Enabled: true, Interval: time.Second}, opts)

	base := nofireAt(t, "2026-08-07T12:00")
	for i := 0; i < 4; i++ {
		w.Check(base.Add(time.Duration(i) * time.Hour))
	}

	if rec.mailCount() != 1 {
		t.Fatalf("mails = %d, want 1 (budget of 1)", rec.mailCount())
	}
	var emitted int
	for _, e := range rec.events {
		if e.EventType == "deploy_nofire" {
			emitted++
		}
	}
	if emitted != 4 {
		t.Errorf("deploy_nofire events = %d, want 4 — the pull-side record is not capped", emitted)
	}
}

// TestNoFireRespectsTheCoarseThrottle: the heartbeat ticks every ~30s and this
// check reads a file, so it must sample on the coarse slot like the other two.
func TestNoFireRespectsTheCoarseThrottle(t *testing.T) {
	rec := &recorder{}
	var reads int
	opts := noFireOpts(rec, incidentLog)
	opts.DeployLog = func() (string, bool, error) { reads++; return incidentLog, true, nil }
	w := New(config.DriftWatchConfig{Enabled: true, Interval: 15 * time.Minute}, opts)

	base := nofireAt(t, "2026-08-07T12:00")
	for i := 0; i < 10; i++ {
		w.Check(base.Add(time.Duration(i) * 30 * time.Second))
	}
	if reads != 1 {
		t.Errorf("read the deploy log %d times across 10 heartbeat ticks, want 1 (coarse throttle)", reads)
	}
}

// TestNoFireReportsOnlyAndHasNoRepairSeam. The suspected mechanism is a launchd
// wedge, and a loop kickstarting a wedged job would hammer it rather than fix
// it. The guarantee is structural — there is no repair function to inject — so
// the test asserts on the Options surface, which is where such a seam would have
// to appear.
func TestNoFireReportsOnlyAndHasNoRepairSeam(t *testing.T) {
	rec := &recorder{}
	w := New(noFireCfg(), noFireOpts(rec, incidentLog))
	w.Check(nofireAt(t, "2026-08-07T12:00"))

	if rec.mailCount() != 1 {
		t.Fatalf("expected the detection path to have run")
	}
	if !strings.Contains(rec.mails[0].body, "REPORT-ONLY") {
		t.Error("the notice does not tell the reader pogod took no action")
	}
}

// TestNoFireBodyDoesNotClaimAFailure guards the distinction the whole ticket
// turns on. Four of the eight nights were not failures, and a notice that says
// "the deploy failed" sends the reader to an rc that does not exist.
func TestNoFireBodyDoesNotClaimAFailure(t *testing.T) {
	rec := &recorder{}
	w := New(noFireCfg(), noFireOpts(rec, incidentLog))
	w.Check(nofireAt(t, "2026-08-07T12:00"))

	body := rec.mails[0].body
	// Claims, not explanations. The body is allowed — required, in fact — to
	// mention rcs when explaining why the rc-indexed alarms could not see this;
	// what it must never do is assert that a run happened and went wrong.
	for _, wrong := range []string{"exit code was", "the deploy failed", "the deploy exited", "exited nonzero"} {
		if strings.Contains(body, wrong) {
			t.Errorf("notice claims an outcome that does not exist for a job that never started: %q", wrong)
		}
	}
	if !strings.Contains(body, "no exit code") {
		t.Error("notice does not explain that there is no exit code because there was no run")
	}
	if !strings.Contains(body, "DID NOT START") {
		t.Error("notice does not state the finding as a job that did not start")
	}
}
