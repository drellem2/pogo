package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// Every other test in this package passes a nil registry to New(), and
// transitionToIndexOnly guards with `if agents != nil` — so the whole existing
// suite is structurally incapable of seeing gh #108. It is not that a test was
// missing; the harness could not have failed. The tests below build a REAL
// agent.Registry with real (cat) processes for exactly that reason.

// catCommandConfig spawns `cat` for every agent type: a process that stays
// alive on a PTY without doing anything, which is what a registry-level test
// needs from a harness.
type catCommandConfig struct{}

func (catCommandConfig) AgentCommand(string) string  { return "cat" }
func (catCommandConfig) AgentProvider(string) string { return "" }

// crewFixture stands up an isolated $POGO_HOME with one auto-start crew prompt
// and a real registry pointed at it. The returned name is the agent that
// should be running whenever the daemon is in full mode.
func crewFixture(t *testing.T) (*agent.Registry, string) {
	t.Helper()
	testsandbox.Isolate(t)

	if err := agent.InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	const name = "scout"
	path := filepath.Join(agent.CrewPromptDir(), name+".md")
	prompt := "+++\nauto_start = true\nrestart_on_crash = true\n+++\n# scout\n"
	if err := os.WriteFile(path, []byte(prompt), 0644); err != nil {
		t.Fatalf("write crew prompt: %v", err)
	}

	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	reg.SetCommandConfig(catCommandConfig{})
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	return reg, name
}

// sweepStarter is the AgentStarter pogod wires in production, minus the
// config gate: re-run the auto-start sweep and report the results.
func sweepStarter(reg *agent.Registry) AgentStarter {
	return func() AgentStartOutcome {
		return AgentStartOutcome{Results: reg.AutoStartAgents()}
	}
}

// TestStartOrchestrationRestartsCrewAgents is the regression test for gh #108.
//
// The reported symptom is that a full -> index-only -> full round-trip returns
// to full mode having restarted nothing, and reports success anyway. The
// positive control is therefore not "SetMode returned nil" — that is what the
// defect already produces — but the agent being observably back in the
// registry, and named in the report the CLI prints.
func TestStartOrchestrationRestartsCrewAgents(t *testing.T) {
	reg, name := crewFixture(t)

	// Boot: the sweep pogod runs at startup.
	if res := reg.AutoStartAgents(); len(res) == 0 {
		t.Fatalf("fixture did not auto-start anything: %+v", res)
	}
	if reg.Get(name) == nil {
		t.Fatalf("fixture agent %q not running before the round-trip", name)
	}

	s := New(reg, nil)
	s.SetAgentStarter(sweepStarter(reg))

	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode(index-only): %v", err)
	}
	if a := reg.Get(name); a != nil {
		t.Fatalf("agent %q survived the drain: %+v", name, a)
	}

	report, err := s.StartOrchestration()
	if err != nil {
		t.Fatalf("StartOrchestration: %v", err)
	}

	// (1) The agent is actually back. This is the assertion that fails on the
	// unfixed transition, which flips the mode and never touches s.agents.
	if reg.Get(name) == nil {
		t.Errorf("agent %q was NOT restarted by the return to full mode — "+
			"the daemon is in full mode with no crew (gh #108)", name)
	}

	// (2) The report NAMES it. A transition that restores the fleet but still
	// reports a bare success leaves the operator trap in place: the reporter's
	// complaint was that a green return is indistinguishable from the bug.
	if !contains(report.AgentsStarted, name) {
		t.Errorf("report.AgentsStarted = %v, want it to name %q", report.AgentsStarted, name)
	}
	if report.Mode != config.ModeFull.String() {
		t.Errorf("report.Mode = %q, want %q", report.Mode, config.ModeFull.String())
	}
	if len(report.AgentsFailed) != 0 {
		t.Errorf("report.AgentsFailed = %+v, want none", report.AgentsFailed)
	}
}

// TestStartOrchestrationClearsShutdownLatch covers the half of gh #108 that is
// invisible from the outside: StopAll's latch was one-way, so after ONE
// stop-orchestration, crash-respawn was inert for the life of the process.
//
// It reads as healthy on every instrument, which is why it needs its own test:
// Spawn never consulted the latch, so `pogo agent start` kept working and the
// fleet looked hand-recoverable. Only Respawn — the path a crash takes — was
// dead.
func TestStartOrchestrationClearsShutdownLatch(t *testing.T) {
	reg, _ := crewFixture(t)
	reg.AutoStartAgents()

	s := New(reg, nil)
	s.SetAgentStarter(sweepStarter(reg))

	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode(index-only): %v", err)
	}
	if _, err := s.StartOrchestration(); err != nil {
		t.Fatalf("StartOrchestration: %v", err)
	}

	// Spawn an agent and let it exit, then take the path a crash would take.
	// Spawn succeeding proves nothing about the latch — that asymmetry IS the
	// bug — so the assertion is on Respawn.
	const crasher = "latch-probe"
	probe, err := reg.Spawn(agent.SpawnRequest{
		Name:    crasher,
		Type:    agent.TypeCrew,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	select {
	case <-probe.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("probe agent %q never exited", crasher)
	}

	if _, err := reg.Respawn(crasher); err != nil {
		t.Fatalf("Respawn after a stop/start round-trip failed: %v — StopAll's shutdown "+
			"latch was never cleared, so restart_on_crash is silently inert for the "+
			"life of this process (gh #108)", err)
	}
}

// TestStartOrchestrationHonoursAutoStartDisabled: a daemon configured never to
// spawn a fleet must not acquire a side door into doing so via a mode
// round-trip — and must SAY that nothing started on purpose, because
// "restarted 0 agents" silently is the shape of the original bug.
func TestStartOrchestrationHonoursAutoStartDisabled(t *testing.T) {
	reg, name := crewFixture(t)
	reg.AutoStartAgents()

	s := New(reg, nil)
	s.SetAgentStarter(func() AgentStartOutcome {
		return AgentStartOutcome{Skipped: "[agents] autostart = false"}
	})

	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode(index-only): %v", err)
	}
	report, err := s.StartOrchestration()
	if err != nil {
		t.Fatalf("StartOrchestration: %v", err)
	}

	if a := reg.Get(name); a != nil {
		t.Errorf("agent %q was started despite autostart being disabled: %+v", name, a)
	}
	if report.AgentStartSkipped == "" {
		t.Error("report does not say why no agents started; a silent zero is the bug being fixed")
	}
	if len(report.AgentsStarted) != 0 {
		t.Errorf("report.AgentsStarted = %v, want none", report.AgentsStarted)
	}
}

// TestStartOrchestrationWithoutStarterSaysSo: a server with no AgentStarter
// wired restarts no agents. That is allowed — but it is reported, not
// swallowed, so the report can never claim a fleet it did not restore.
func TestStartOrchestrationWithoutStarterSaysSo(t *testing.T) {
	s := New(nil, nil)
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatal(err)
	}
	report, err := s.StartOrchestration()
	if err != nil {
		t.Fatal(err)
	}
	if report.AgentStartSkipped == "" {
		t.Error("report.AgentStartSkipped is empty; a server with no agent starter must say it started nothing")
	}
	if len(report.AgentsStarted) != 0 {
		t.Errorf("report.AgentsStarted = %v, want none", report.AgentsStarted)
	}
}

// TestStartOrchestrationAlreadyFull: a server already in full mode stopped
// nothing and restarted no refinery, and must not imply otherwise.
func TestStartOrchestrationAlreadyFull(t *testing.T) {
	s := New(nil, nil)
	report, err := s.StartOrchestration()
	if err != nil {
		t.Fatal(err)
	}
	if !report.AlreadyFull {
		t.Error("report.AlreadyFull = false for a server that was already in full mode")
	}
	if report.RefineryRestarted {
		t.Error("report.RefineryRestarted = true; an already-full start must leave the refinery alone")
	}
}

// TestStartOrchestrationInFullModeStartsAMissingCrewAgent is the regression test
// for mg-060c, reported three times and twice closed against the wrong artifact
// (mg-e463 rewrote the README).
//
// The state under test is the one the reporter keeps hitting and the one the
// 2026-08-07 outage sat in for 10h39m: pogod up, full mode, port answering,
// schedules firing — and no mayor, so nothing is dispatched. Nothing else in the
// daemon notices, because the mayor is the only agent that spawns agents.
//
// The positive control is therefore not the report, and not the mode: it is
// reg.Get(name) being non-nil AFTER a start that never changed the mode. On the
// unfixed server StartOrchestration returns AlreadyFull without touching
// s.agents, so this assertion is the one that fails.
func TestStartOrchestrationInFullModeStartsAMissingCrewAgent(t *testing.T) {
	reg, name := crewFixture(t)

	s := New(reg, nil)
	s.SetAgentStarter(sweepStarter(reg))

	// The daemon is in full mode and the crew agent is NOT running — a mayor
	// that crashed, was stopped by hand, or lost its boot spawn.
	if s.Mode() != config.ModeFull {
		t.Fatalf("fixture server mode = %v, want full", s.Mode())
	}
	if a := reg.Get(name); a != nil {
		t.Fatalf("fixture agent %q is already running: %+v", name, a)
	}

	report, err := s.StartOrchestration()
	if err != nil {
		t.Fatalf("StartOrchestration: %v", err)
	}

	if reg.Get(name) == nil {
		t.Errorf("agent %q was NOT started by a start against a full-mode daemon — "+
			"this is mg-060c: `pogo server start` reports success and leaves the fleet with no mayor", name)
	}
	if !contains(report.AgentsStarted, name) {
		t.Errorf("report.AgentsStarted = %v, want it to name %q — a start that recovers the "+
			"coordinator and does not say so is indistinguishable from the no-op it replaced",
			report.AgentsStarted, name)
	}
	if !report.AlreadyFull {
		t.Error("report.AlreadyFull = false; the mode was full throughout and the report must say so")
	}
	if len(report.AgentsFailed) != 0 {
		t.Errorf("report.AgentsFailed = %+v, want none", report.AgentsFailed)
	}
}

// TestStartOrchestrationInFullModeIsIdempotent: the sweep now runs on the common
// path, including against an entirely healthy fleet. Doing so must be a no-op
// that REPORTS the fleet — not a restart of it, and not a failure. An agent
// reported as failed sets the CLI's exit code, so getting this wrong would make
// a routine start exit non-zero on a healthy daemon.
func TestStartOrchestrationInFullModeIsIdempotent(t *testing.T) {
	reg, name := crewFixture(t)
	reg.AutoStartAgents()

	before := reg.Get(name)
	if before == nil {
		t.Fatalf("fixture agent %q did not start", name)
	}

	s := New(reg, nil)
	s.SetAgentStarter(sweepStarter(reg))

	report, err := s.StartOrchestration()
	if err != nil {
		t.Fatalf("StartOrchestration: %v", err)
	}

	after := reg.Get(name)
	if after == nil {
		t.Fatalf("agent %q disappeared across an already-full start", name)
	}
	if after.PID != before.PID {
		t.Errorf("agent %q was restarted (pid %d -> %d); a start against a healthy fleet must not bounce it",
			name, before.PID, after.PID)
	}
	if !contains(report.AgentsAlreadyRunning, name) {
		t.Errorf("report.AgentsAlreadyRunning = %v, want it to name %q", report.AgentsAlreadyRunning, name)
	}
	if len(report.AgentsStarted) != 0 {
		t.Errorf("report.AgentsStarted = %v, want none", report.AgentsStarted)
	}
	if len(report.AgentsFailed) != 0 {
		t.Errorf("report.AgentsFailed = %+v, want none — a healthy fleet must not make `pogo server start` exit non-zero",
			report.AgentsFailed)
	}
}

// TestStartOrchestrationInFullModeHonoursTheSkipGate: the sweep on the
// already-full path goes through the same AgentStarter as the transition, so a
// daemon configured never to spawn a fleet still says so rather than reporting
// an empty success.
func TestStartOrchestrationInFullModeHonoursTheSkipGate(t *testing.T) {
	s := New(nil, nil)
	s.SetAgentStarter(func() AgentStartOutcome {
		return AgentStartOutcome{Skipped: "[agents] autostart = false"}
	})

	report, err := s.StartOrchestration()
	if err != nil {
		t.Fatal(err)
	}
	if report.AgentStartSkipped != "[agents] autostart = false" {
		t.Errorf("report.AgentStartSkipped = %q, want the gate's reason", report.AgentStartSkipped)
	}
	if len(report.AgentsStarted) != 0 {
		t.Errorf("report.AgentsStarted = %v, want none", report.AgentsStarted)
	}
}

// TestTransitionToFullReportsFailedAgents: an agent whose spawn errored is
// named as a failure. Restoring the fleet partially is not success, and the
// report is the only place that distinction can be made.
func TestTransitionToFullReportsFailedAgents(t *testing.T) {
	s := New(nil, nil)
	s.SetAgentStarter(func() AgentStartOutcome {
		return AgentStartOutcome{Results: []agent.AutoStartResult{
			{Name: "ok", Status: agent.AutoStartStatusStarted},
			{Name: "broken", Status: agent.AutoStartStatusFailed, Error: "pty start: no ptys"},
			{Name: "sleepy", Status: agent.AutoStartStatusSkippedParked},
			{Name: "up", Status: agent.AutoStartStatusSkippedRunning},
			{Name: "ignored", Status: agent.AutoStartStatusSkippedNoFlag},
		}}
	})
	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatal(err)
	}
	report, err := s.StartOrchestration()
	if err != nil {
		t.Fatal(err)
	}

	if got := report.AgentsStarted; len(got) != 1 || got[0] != "ok" {
		t.Errorf("AgentsStarted = %v, want [ok]", got)
	}
	if got := report.AgentsAlreadyRunning; len(got) != 1 || got[0] != "up" {
		t.Errorf("AgentsAlreadyRunning = %v, want [up]", got)
	}
	if got := report.AgentsParked; len(got) != 1 || got[0] != "sleepy" {
		t.Errorf("AgentsParked = %v, want [sleepy]", got)
	}
	if len(report.AgentsFailed) != 1 || report.AgentsFailed[0].Name != "broken" {
		t.Fatalf("AgentsFailed = %+v, want one entry for \"broken\"", report.AgentsFailed)
	}
	if !strings.Contains(report.AgentsFailed[0].Error, "no ptys") {
		t.Errorf("AgentsFailed[0].Error = %q, want the underlying spawn error", report.AgentsFailed[0].Error)
	}
}

// TestTransitionToFullAbortsWholeWhenRefineryFails: a transition that cannot
// complete must not half-apply. If the refinery fails to restart, the mode
// stays index-only, no agents are swept, and the shutdown latch stays SET —
// re-arming crash-respawn for a transition that did not happen would leave the
// daemon supervising a fleet it never restarted. This is the "clearing the
// latch too early" failure, tested rather than asserted.
func TestTransitionToFullAbortsWholeWhenRefineryFails(t *testing.T) {
	reg, name := crewFixture(t)
	reg.AutoStartAgents()

	swept := false
	s := New(reg, nil)
	s.SetRefineryStarter(func() (*refinery.Refinery, error) {
		return nil, errors.New("refinery would not start")
	})
	s.SetAgentStarter(func() AgentStartOutcome {
		swept = true
		return AgentStartOutcome{Results: reg.AutoStartAgents()}
	})

	if err := s.SetMode(config.ModeIndexOnly); err != nil {
		t.Fatalf("SetMode(index-only): %v", err)
	}
	if _, err := s.StartOrchestration(); err == nil {
		t.Fatal("StartOrchestration returned nil error despite a refinery that cannot start")
	}

	if s.Mode() != config.ModeIndexOnly {
		t.Errorf("mode = %s after a failed transition, want index-only", s.Mode())
	}
	if swept {
		t.Error("the auto-start sweep ran during a transition that failed before the mode flip")
	}
	if a := reg.Get(name); a != nil {
		t.Errorf("agent %q was restarted by a failed transition: %+v", name, a)
	}
	if _, err := reg.Respawn(name); err == nil || !strings.Contains(err.Error(), "shut down") {
		t.Errorf("Respawn error = %v, want the shutdown latch still set after a failed transition", err)
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
