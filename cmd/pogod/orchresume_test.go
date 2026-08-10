package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/server"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// The two acceptance arms of mg-5af1, driven against the REAL *server.Server
// rather than a stub of it. The arming half and the firing half live in
// different packages and the defect is precisely that a two-step sequence had no
// owner spanning both steps; a test that faked either side would be testing the
// half that was never in doubt.
//
//	NEGATIVE  — kill the stopper after it stops the fleet and before it restarts
//	            it. The crew comes back anyway, and something says so.
//	POSITIVE  — a stop that IS followed by its restart must not produce a second
//	            restart or an alarm. Without this arm, "the crew is always up" is
//	            also satisfied by a watchdog that fights every ordinary deploy.
//
// Both are run in the same file, against the same fixture, because they are one
// claim with two polarities and separating them is how the second one quietly
// stops being run.

// crewLight is the fleet, reduced to the only property these arms need: is it
// up, and who turned it on. It stands in for AutoStartAgents, whose real
// behaviour (PTYs, prompts, providers) is covered by internal/server's own
// round-trip tests.
type crewLight struct {
	up     bool
	starts int
}

func (c *crewLight) starter() server.AgentStarter {
	return func() server.AgentStartOutcome {
		c.up = true
		c.starts++
		return server.AgentStartOutcome{}
	}
}

// resumeFixture is a daemon whose fleet is up, plus a resumer wired to it with a
// recording mailer, plus a clock the test drives.
type resumeFixture struct {
	srv  *server.Server
	crew *crewLight
	mail *condRecorder
	res  *orchResumer
	now  time.Time
}

func newResumeFixture(t *testing.T, grace time.Duration) *resumeFixture {
	t.Helper()
	testsandbox.Isolate(t)

	crew := &crewLight{up: true}
	srv := server.New(nil, nil)
	srv.SetAgentStarter(crew.starter())
	srv.SetResumeGrace(grace)

	mail := &condRecorder{}
	ann, _ := newTestAnnunciator(t, mail.send, nil)

	f := &resumeFixture{
		srv:  srv,
		crew: crew,
		mail: mail,
		// The wall clock of the incident. The server stamps obligations from
		// this too (SetResumeClock), so "16 minutes later" in a test really is
		// 16 minutes later to the deadline arithmetic rather than a number the
		// test and the server disagree about.
		now: time.Date(2026, 8, 8, 0, 44, 20, 0, time.UTC),
	}
	srv.SetResumeClock(func() time.Time { return f.now })
	f.res = newOrchResumer(func() orchResumeServer { return srv }, ann, "mayor",
		config.OrchestrationResumeConfig{Enabled: true, Grace: grace})
	return f
}

// stopTheFleet is step 1 of the two-step sequence — the deploy quiescing the
// crew. It does NOT go on to step 2; whether step 2 happens is what each arm is
// about.
func (f *resumeFixture) stopTheFleet(t *testing.T) {
	t.Helper()
	if _, err := f.srv.StopOrchestrationWithCause(
		server.Cause{Trigger: "http", Detail: "POST /server/stop-orchestration",
			Client: "pogo-self-deploy", ClientPid: "32439"}, 0); err != nil {
		t.Fatalf("stop: %v", err)
	}
	f.crew.up = false
	if f.srv.Mode() != config.ModeIndexOnly {
		t.Fatalf("mode after stop is %v", f.srv.Mode())
	}
}

func (f *resumeFixture) advance(d time.Duration) { f.now = f.now.Add(d) }
func (f *resumeFixture) tick() bool              { return f.res.Check(f.now) }

// TestNegativeArm_TheStopperDiesAndTheCrewComesBackAnyway.
//
// This is the 2026-08-08 shape reduced to its mechanism: the crew is stopped at
// 00:44:20Z and the process that stopped it never reaches its restart. It hung
// for 31h39m; nothing in the sequence has a timeout, so "it hung" and "it was
// SIGKILLed" are the same thing from the fleet's point of view — in both, step 2
// simply never runs.
func TestNegativeArm_TheStopperDiesAndTheCrewComesBackAnyway(t *testing.T) {
	f := newResumeFixture(t, 15*time.Minute)

	f.stopTheFleet(t)
	// ... and here the deploy dies. Nothing else happens. Ever.

	// Inside the window, the resumer must still do nothing: an obligation that
	// fires early is the watchdog-fights-the-deploy failure wearing the other
	// arm's clothes.
	f.advance(14 * time.Minute)
	if f.tick() {
		t.Fatal("the resumer restarted the fleet 14 minutes into a 15-minute window")
	}
	if f.srv.Mode() != config.ModeIndexOnly {
		t.Fatalf("mode moved inside the window: %v", f.srv.Mode())
	}

	f.advance(2 * time.Minute) // 16m: past the deadline
	if !f.tick() {
		t.Fatal("the deadline passed on a fleet nobody is going to restart, and the resumer did " +
			"NOTHING. This is the defect: the crew's restart obligation has no owner that " +
			"survives the deploy dying")
	}

	if f.srv.Mode() != config.ModeFull {
		t.Errorf("mode is %v, want full — the fleet did not come back", f.srv.Mode())
	}
	if !f.crew.up {
		t.Error("full mode was restored but the crew was never started; mode is not a fleet")
	}
	if len(f.mail.sent) != 1 {
		t.Fatalf("the fleet was down for 16 minutes and pogod restarted it, and %d mails were "+
			"sent. Bringing the crew back silently means the thing that abandoned it is never "+
			"found", len(f.mail.sent))
	}

	m := f.mail.sent[0]
	if m.to != "mayor" {
		t.Errorf("alarm went to %q, not the coordinator", m.to)
	}
	for _, want := range []string{"pogo-self-deploy", "32439", "2026-08-08T00:44:20Z"} {
		if !strings.Contains(m.body, want) {
			t.Errorf("the alarm does not name %q — an outage nobody can attribute is one that "+
				"recurs; the body was:\n%s", want, m.body)
		}
	}
	for _, want := range []string{"WHAT IT COSTS WHILE UNFIXED", "WHAT TO DO", "WHY THIS IS MAIL"} {
		if !strings.Contains(m.body, want) {
			t.Errorf("the alarm is missing the %q section", want)
		}
	}
}

// TestNegativeArm_FailsWithoutTheDeadline is the "seen to fail before" half,
// kept as a permanent control rather than as a claim about a build that no
// longer exists.
//
// grace <= 0 is exactly the pre-mg-5af1 world and exactly what
// `[orchestration_resume] enabled = false` configures: the stop is recorded,
// nothing holds the obligation, and the identical sequence leaves the fleet
// down forever. If this ever passes, the mechanism has been disabled by
// something other than the switch that is supposed to disable it.
func TestNegativeArm_FailsWithoutTheDeadline(t *testing.T) {
	f := newResumeFixture(t, 0)

	f.stopTheFleet(t)
	f.advance(33 * time.Hour) // the measured outage

	if f.tick() {
		t.Fatal("a daemon with no resume deadline restored the fleet anyway — then the config " +
			"switch does not mean what it says")
	}
	if f.srv.Mode() != config.ModeIndexOnly {
		t.Fatalf("mode is %v", f.srv.Mode())
	}
	if f.crew.up {
		t.Fatal("the crew came back with no mechanism to bring it back")
	}
	if len(f.mail.sent) != 0 {
		t.Fatalf("%d mails from a disarmed resumer", len(f.mail.sent))
	}
	// This is what the fleet looked like for 33 hours on 2026-08-08, and the
	// point of the arm above is that it no longer can.
}

// TestPositiveControl_AnOrdinaryStopAndRestartIsNotTOUCHED.
//
// The arm that stops "the crew is always up" from being satisfiable by a
// watchdog that restarts the fleet out from under every deploy. A correct
// stop/restart sequence must produce: no resume, no second crew start, no mail —
// and it must leave the resumer able to fire on the NEXT stop, which is the
// latching failure this also rules out.
func TestPositiveControl_AnOrdinaryStopAndRestartIsNotTouched(t *testing.T) {
	f := newResumeFixture(t, 15*time.Minute)

	f.stopTheFleet(t)

	// The deploy does its work. Several ticks pass inside the window.
	for _, d := range []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute} {
		f.advance(d)
		if f.tick() {
			t.Fatalf("the resumer restarted the fleet %s into a 15-minute window — it is fighting "+
				"an ordinary deploy", f.now.Sub(time.Date(2026, 8, 8, 0, 44, 20, 0, time.UTC)))
		}
	}

	// Step 2: the deploy finishes and restarts the fleet itself.
	if _, err := f.srv.StartOrchestration(); err != nil {
		t.Fatalf("deploy's own restart: %v", err)
	}
	if !f.crew.up || f.crew.starts != 1 {
		t.Fatalf("after the deploy's own restart: up=%v starts=%d", f.crew.up, f.crew.starts)
	}

	// Well past when the deadline WOULD have been. Nothing may happen.
	f.advance(2 * time.Hour)
	if f.tick() {
		t.Fatal("the resumer acted after the fleet was already restored — a duplicate restart")
	}
	if f.crew.starts != 1 {
		t.Errorf("the crew was started %d times for one stop; a duplicate restart sweep is the "+
			"cost this arm exists to bound", f.crew.starts)
	}
	if len(f.mail.sent) != 0 {
		t.Fatalf("an ordinary, correct deploy raised %d alarm(s): %+v. An alarm that fires on the "+
			"healthy path is an alarm that gets filtered", len(f.mail.sent), f.mail.sent)
	}

	// And the detector is not latched OFF by the quiet cycle either: a second
	// stop that IS abandoned must still fire.
	f.stopTheFleet(t)
	f.advance(20 * time.Minute)
	if !f.tick() {
		t.Fatal("after one healthy cycle the resumer no longer fires — it latched")
	}
	if len(f.mail.sent) != 1 {
		t.Errorf("the second, real breach produced %d mails, want 1", len(f.mail.sent))
	}
}

// TestADeclaredHoldIsNotFought. `pogo server stop --hold 4h` is the answer to
// "I meant it"; if the resumer ignored it, the only way to keep a fleet down on
// purpose would be to turn the mechanism off fleet-wide.
func TestADeclaredHoldIsNotFought(t *testing.T) {
	f := newResumeFixture(t, 15*time.Minute)
	if _, err := f.srv.StopOrchestrationWithCause(
		server.Cause{Trigger: "http", Client: "pogo server stop"}, 4*time.Hour); err != nil {
		t.Fatalf("stop: %v", err)
	}
	f.crew.up = false

	f.advance(3 * time.Hour)
	if f.tick() {
		t.Fatal("a declared 4h hold was overridden at 3h")
	}
	f.advance(90 * time.Minute) // 4h30m
	if !f.tick() {
		t.Fatal("a declared hold became an INDEFINITE one — a hold is a window, not an opt-out")
	}
}

// A restore that cannot take is the one case where the fleet really is still down
// as the notice is written, so: the condition must NOT clear, the retry must be
// bounded so a broken daemon is not re-swept every 30 seconds, and the notice
// must say it may have no in-fleet reader.
//
// failingServer is an orchResumeServer whose restore always fails. Driving the
// failure through the real *Server would mean manufacturing a refinery that
// cannot start, which tests the refinery rather than the resumer.
type failingServer struct {
	ob    server.ResumeObligation
	calls int
}

func (f *failingServer) ResumeObligation() (server.ResumeObligation, bool) { return f.ob, true }
func (f *failingServer) StartOrchestrationWithCause(server.Cause) (server.StartReport, error) {
	f.calls++
	return server.StartReport{}, errors.New("restart refinery: disk full")
}

func TestFailedRestoreViaStub(t *testing.T) {
	testsandbox.Isolate(t)
	base := time.Date(2026, 8, 8, 0, 44, 20, 0, time.UTC)
	fs := &failingServer{ob: server.ResumeObligation{
		Since: base, Due: base.Add(15 * time.Minute),
		Cause: server.Cause{Client: "pogo-self-deploy"},
	}}
	mail := &condRecorder{}
	ann, _ := newTestAnnunciator(t, mail.send, nil)
	r := newOrchResumer(func() orchResumeServer { return fs }, ann, "mayor",
		config.OrchestrationResumeConfig{Retry: time.Minute})

	now := base.Add(16 * time.Minute)
	if r.Check(now) {
		t.Fatal("Check reported success on a restore that errored")
	}
	if fs.calls != 1 {
		t.Fatalf("restore attempted %d times", fs.calls)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("a FAILED restore sent %d mails; the fleet is still down and this is the loudest "+
			"thing available", len(mail.sent))
	}
	if !strings.Contains(mail.sent[0].body, "NO IN-FLEET READER") {
		t.Error("the failure notice does not admit that the crew it is addressed to is the crew " +
			"that is down; an alarm overstating its own delivery is the defect one level up")
	}
	if !strings.Contains(mail.sent[0].subject, "could NOT restart") {
		t.Errorf("the subject reads like a success: %q", mail.sent[0].subject)
	}

	// Inside the retry floor: no second attempt, no second mail.
	if r.Check(now.Add(30 * time.Second)); fs.calls != 1 {
		t.Errorf("a failing restore was re-attempted inside the %s floor (%d calls)", time.Minute, fs.calls)
	}
	// Past it: retried, but the fingerprint is the STOP, so the annunciator's
	// own suppression keeps it from mailing again every minute.
	r.Check(now.Add(2 * time.Minute))
	if fs.calls != 2 {
		t.Errorf("a failing restore was never retried past the floor (%d calls)", fs.calls)
	}
	if len(mail.sent) != 1 {
		t.Errorf("a retried failure mailed again (%d mails) — one outage, one alarm", len(mail.sent))
	}
}

// TestResumerIsInertBeforeTheServerExists. pogod builds this closure hundreds of
// lines before server.New runs, so a nil read here is the normal boot ordering,
// not an error state.
func TestResumerIsInertBeforeTheServerExists(t *testing.T) {
	testsandbox.Isolate(t)
	mail := &condRecorder{}
	ann, _ := newTestAnnunciator(t, mail.send, nil)
	r := newOrchResumer(func() orchResumeServer { return nil }, ann, "mayor",
		config.OrchestrationResumeConfig{})
	if r.Check(time.Now()) {
		t.Fatal("acted with no server")
	}
	if len(mail.sent) != 0 {
		t.Fatalf("mailed with no server: %+v", mail.sent)
	}
}
