package service

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/server"
)

// mg-6515. installLaunchd stops fleet-wide dispatch at step 1 and, before this
// change, delegated its only restore to step 7 succeeding. Five fallible steps
// sit in between, and on each of them the restart that would have restored full
// mode is the thing that just failed — the failure mode disabled its own
// recovery, silently.
//
// These tests are the positive control the ticket asks for: force each of the
// five early returns against a live daemon and read back the mode the box was
// left in. The daemon is a fake because the real one is behind :10000 and
// cannot be driven into `launchctl kickstart failed` from a test; what is real
// is the sequence under test — runOrchestratedInstall is the production
// function, entered through the production default restore.
//
// The control is known to be able to go red: TestPreFixInstallLeavesDispatchDark
// runs the same table through the same function with the restore neutered, and
// asserts the mode stays index-only. If the restore ever stops running, the
// green tests below turn into that one.

// fakePogod stands in for the daemon behind :10000. Its mode field is the
// observable the ticket asks for.
type fakePogod struct {
	mode     string
	alive    bool
	stopErr  error
	startErr error
	report   server.StartReport
	stops    int
	starts   int
}

func newFakePogod() *fakePogod {
	return &fakePogod{mode: config.ModeFull.String(), alive: true}
}

func (f *fakePogod) Alive() bool { return f.alive }

func (f *fakePogod) Stop() error {
	f.stops++
	if f.stopErr != nil {
		return f.stopErr
	}
	f.mode = config.ModeIndexOnly.String()
	return nil
}

func (f *fakePogod) Start() (server.StartReport, error) {
	f.starts++
	if f.startErr != nil {
		return server.StartReport{Mode: f.mode}, f.startErr
	}
	if f.report.Mode != "" {
		// A daemon that answers without error but names a mode of its own —
		// transitionToFull bailing out partway leaves exactly this shape.
		return f.report, nil
	}
	if f.mode == config.ModeFull.String() {
		return server.StartReport{Mode: config.ModeFull.String(), AlreadyFull: true}, nil
	}
	f.mode = config.ModeFull.String()
	report := f.report
	report.Mode = config.ModeFull.String()
	return report, nil
}

// okSteps is an install sequence in which every step succeeds. Tests fail
// exactly one step at a time by overwriting one field.
func okSteps() installSteps {
	return installSteps{
		unloadPrior: func() {},
		stopPogod:   func() {},
		drainPort:   func() error { return nil },
		writePlist:  func() error { return nil },
		loadPlist:   func() error { return nil },
		kickstart:   func() error { return nil },
		verify:      func() error { return nil },
	}
}

// theFiveEarlyReturns is the exact list from mg-6515: every return between the
// quiesce (step 1) and the new pogod booting (step 7). Each entry breaks one
// step of okSteps.
var theFiveEarlyReturns = []struct {
	name      string
	step      string
	breakStep func(*installSteps, error)
}{
	{"step 4 waitForPogodPortDrain", "drainPort", func(s *installSteps, err error) { s.drainPort = func() error { return err } }},
	{"step 5 os.WriteFile(plistPath)", "writePlist", func(s *installSteps, err error) { s.writePlist = func() error { return err } }},
	{"step 5 launchctl load", "loadPlist", func(s *installSteps, err error) { s.loadPlist = func() error { return err } }},
	{"step 5b launchctl kickstart -k", "kickstart", func(s *installSteps, err error) { s.kickstart = func() error { return err } }},
	{"step 6 verifyLaunchdRunning", "verify", func(s *installSteps, err error) { s.verify = func() error { return err } }},
}

func TestEachEarlyReturnRestoresDispatchWithPogodStillAlive(t *testing.T) {
	// The positive control, green arm. The old pogod is still answering —
	// which is not a contrived case: step 3's stop is best-effort and step 4
	// times out precisely when something still holds :10000, so "old pogod
	// alive at an early return" is the case those steps exist to handle.
	for _, tc := range theFiveEarlyReturns {
		t.Run(tc.name, func(t *testing.T) {
			pogod := newFakePogod()
			steps := okSteps()
			boom := fmt.Errorf("forced failure at %s", tc.step)
			tc.breakStep(&steps, boom)

			restore, err := runOrchestratedInstall(pogod, steps)

			if !errors.Is(err, boom) {
				t.Fatalf("expected the forced %s failure to propagate, got %v", tc.step, err)
			}
			if pogod.stops != 1 {
				t.Fatalf("expected the sequence to have quiesced exactly once, got %d stops", pogod.stops)
			}
			if pogod.mode != config.ModeFull.String() {
				t.Errorf("install failed at %s and left the daemon in mode %q — dispatch is dark fleet-wide (mg-6515)", tc.step, pogod.mode)
			}
			if !restore.Attempted {
				t.Error("restore was not attempted, but the sequence had stopped orchestration")
			}
			if !restore.OK {
				t.Errorf("restore reported failure: %s", restore.Detail)
			}
		})
	}
}

func TestPreFixInstallLeavesDispatchDark(t *testing.T) {
	// The same control, red arm — the thing that proves the assertion above
	// is capable of failing. Same production sequence, same five failures,
	// with the restore neutered to reproduce the pre-fix code shape: step 1
	// stops orchestration and only step 7 would have put it back.
	//
	// If this test ever goes green, the green arm above has stopped meaning
	// anything.
	noRestore := func(orchestrator, bool) orchestrationRestore { return orchestrationRestore{} }

	for _, tc := range theFiveEarlyReturns {
		t.Run(tc.name, func(t *testing.T) {
			pogod := newFakePogod()
			steps := okSteps()
			steps.restore = noRestore
			tc.breakStep(&steps, fmt.Errorf("forced failure at %s", tc.step))

			if _, err := runOrchestratedInstall(pogod, steps); err == nil {
				t.Fatal("expected the forced failure to propagate")
			}
			if pogod.mode != config.ModeIndexOnly.String() {
				t.Fatalf("without the restore the daemon should have been left in %q (the pre-fix defect); got %q — the control cannot go red, so the green arm proves nothing",
					config.ModeIndexOnly.String(), pogod.mode)
			}
			if pogod.starts != 0 {
				t.Errorf("no restore should have run, got %d starts", pogod.starts)
			}
		})
	}
}

func TestSuccessfulInstallLeavesTheRestoreToTheNewPogod(t *testing.T) {
	// Step 7's contract, asserted rather than assumed: on the success path
	// the installer does NOT call start-orchestration. The new pogod boots in
	// ModeFull, and calling it here would be talking to the daemon that just
	// died. The fake stays in index-only for exactly that reason — it models
	// the OLD daemon, which by then is gone.
	pogod := newFakePogod()

	restore, err := runOrchestratedInstall(pogod, okSteps())
	if err != nil {
		t.Fatalf("all steps succeed, expected no error, got %v", err)
	}
	if pogod.starts != 0 {
		t.Errorf("the installer must not restore on the success path (the new pogod does); got %d starts", pogod.starts)
	}
	if restore.Attempted {
		t.Errorf("a successful install should report no restore, got %q", restore.Detail)
	}
}

func TestNoRestoreWhenPogodWasNeverRunning(t *testing.T) {
	// Nothing was quiesced, so nothing is owed. A restore here would start
	// orchestration on a box the operator had deliberately left stopped.
	pogod := newFakePogod()
	pogod.alive = false
	steps := okSteps()
	steps.drainPort = func() error { return errors.New("drain timed out") }

	restore, err := runOrchestratedInstall(pogod, steps)
	if err == nil {
		t.Fatal("expected the drain failure to propagate")
	}
	if pogod.stops != 0 || pogod.starts != 0 {
		t.Errorf("no pogod was answering: expected no stop and no start, got %d/%d", pogod.stops, pogod.starts)
	}
	if restore.Attempted {
		t.Errorf("expected no restore attempt, got %q", restore.Detail)
	}
}

func TestQuiesceRestoresEvenWhenTheStopReportedAnError(t *testing.T) {
	// A StopOrchestration that errors may still have stopped the crew — an
	// error is often the response arriving late, not the crew staying up. The
	// restore is idempotent (AlreadyFull), so attempting it after a stop that
	// did not take costs nothing; skipping it after a stop that did take is
	// the ten-hour outage. So the obligation is created by the attempt.
	pogod := newFakePogod()
	pogod.stopErr = errors.New("server returned 500")
	steps := okSteps()
	steps.verify = func() error { return errors.New("verification failed") }

	restore, err := runOrchestratedInstall(pogod, steps)
	if err == nil {
		t.Fatal("expected the verify failure to propagate")
	}
	if pogod.starts != 1 {
		t.Fatalf("expected the restore to run after an attempted stop, got %d starts", pogod.starts)
	}
	if !restore.OK {
		t.Errorf("restore should have succeeded: %s", restore.Detail)
	}
}

func TestFailedRestoreIsLoudAndNamesTheResidualState(t *testing.T) {
	// Requirement 2: a silent failed restore is the same defect one layer
	// down. Each case asserts the operator is told (a) that dispatch is still
	// down and (b) what to run.
	cases := []struct {
		name         string
		pogod        *fakePogod
		wantContains []string
	}{
		{
			name: "start-orchestration returned an error",
			pogod: func() *fakePogod {
				p := newFakePogod()
				p.mode = config.ModeIndexOnly.String()
				p.startErr = errors.New("server returned 503")
				return p
			}(),
			wantContains: []string{"NOT RESTORED", "503", "pogo server start"},
		},
		{
			name: "nothing answers on the port any more",
			pogod: func() *fakePogod {
				p := newFakePogod()
				p.mode = config.ModeIndexOnly.String()
				p.alive = false
				return p
			}(),
			wantContains: []string{"NOT RESTORED", pogodPort, "pogo server start"},
		},
		{
			name: "start returned success but the daemon is not in full mode",
			pogod: func() *fakePogod {
				p := newFakePogod()
				p.mode = config.ModeIndexOnly.String()
				p.report = server.StartReport{Mode: config.ModeIndexOnly.String()}
				return p
			}(),
			wantContains: []string{"NOT RESTORED", "index-only"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var loud bytes.Buffer
			restore := restoreOrchestrationTo(&loud, tc.pogod, true)

			if restore.OK {
				t.Fatalf("expected the restore to be reported as failed, got %q", restore.Detail)
			}
			if !restore.Attempted {
				t.Fatal("a failed restore must still count as attempted, or the failure mail says nothing")
			}
			if loud.Len() == 0 {
				t.Fatal("a failed restore wrote nothing to the loud channel — that is mg-6515 one layer down")
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(loud.String(), want) {
					t.Errorf("loud output does not mention %q:\n%s", want, loud.String())
				}
			}
			if got := installFailureSubject(restore); !strings.Contains(got, "ORCHESTRATION STILL STOPPED") {
				t.Errorf("failure mail subject does not escalate: %q", got)
			}
		})
	}
}

func TestSuccessfulRestoreSubjectDoesNotEscalate(t *testing.T) {
	// The escalated subject has to mean something, so it must not fire on an
	// install that failed with the fleet intact.
	for _, restore := range []orchestrationRestore{
		{}, // never quiesced
		{Attempted: true, OK: true, Detail: "fine"}, // quiesced and restored
	} {
		if got := installFailureSubject(restore); strings.Contains(got, "ORCHESTRATION STILL STOPPED") {
			t.Errorf("subject escalated for restore %+v: %q", restore, got)
		}
	}
}

func TestZeroRestoreStringSaysNothingWasTaken(t *testing.T) {
	// installLaunchd can fail before it ever reaches the quiesce (a bad
	// plist render, an unwritable log dir). The mail must not read as though
	// dispatch might be down.
	got := orchestrationRestore{}.String()
	if !strings.Contains(got, "untouched") {
		t.Errorf("zero-value restore should say orchestration was untouched, got %q", got)
	}
}

func TestSummarizeStartReportNamesTheFleetNotJustTheMode(t *testing.T) {
	// gh #108: a transition can succeed while restoring no agents at all, and
	// "restarted" is exactly what that defect printed.
	empty := summarizeStartReport(server.StartReport{Mode: config.ModeFull.String()})
	if !strings.Contains(empty, "no crew agents started") {
		t.Errorf("a restore that brought back nothing must say so, got %q", empty)
	}
	if !strings.Contains(empty, "refinery NOT restarted") {
		t.Errorf("expected the refinery outcome to be named, got %q", empty)
	}

	full := summarizeStartReport(server.StartReport{
		Mode:              config.ModeFull.String(),
		RefineryRestarted: true,
		AgentsStarted:     []string{"mayor", "architect"},
		AgentsFailed:      []server.AgentStartFailure{{Name: "pm-pogo", Error: "spawn refused"}},
	})
	for _, want := range []string{"mayor, architect", "refinery restarted", "FAILED to start pm-pogo", "spawn refused"} {
		if !strings.Contains(full, want) {
			t.Errorf("summary is missing %q: %s", want, full)
		}
	}
}
