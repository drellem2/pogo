package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// pogoHomeSandbox points HOME/XDG_CONFIG_HOME/POGO_HOME at fresh temp dirs so
// the coordinator record never touches the developer's real state dir.
func pogoHomeSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("POGO_HOME", state)
	return state
}

// liveProcess starts a long-lived child and returns its pid. The child is killed
// and reaped on cleanup, so nothing outlives the test.
func liveProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// deadProcess starts a child, waits for it to exit, and returns its (now reaped,
// therefore dead) pid. Using a real reaped pid rather than a made-up number keeps
// the test honest about what pidRunning actually probes.
func deadProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	return cmd.Process.Pid
}

// (b) A rename of a RUNNING coordinator is refused, whatever config says. This is
// the scenario the config pin used to be the only defense against: something
// drops the [agents] pin, Load resolves the flipped default, and the next thing
// that reads a role name addresses a coordinator nobody is running.
func TestGuardRunningCoordinator_RefusesRenameOfRunningCoordinator(t *testing.T) {
	pogoHomeSandbox(t)
	pid := liveProcess(t)
	if err := RecordRunningCoordinator("mayor", pid); err != nil {
		t.Fatal(err)
	}

	// Config says ringmaster — the flipped default, as an unpinned Load returns it.
	cfg := &Config{}
	cfg.Agents.Coordinator = "ringmaster"
	cfg.StallWatch.Agent = "ringmaster" // Load points it at the coordinator when unset.

	cfg, refusal := GuardRunningCoordinator(cfg)

	if refusal == nil {
		t.Fatal("rename of a running coordinator was allowed; it must be refused")
	}
	if refusal.Running != "mayor" || refusal.Configured != "ringmaster" || refusal.PID != pid {
		t.Errorf("refusal = %+v, want running=mayor configured=ringmaster pid=%d", refusal, pid)
	}
	if cfg.Agents.Coordinator != "mayor" {
		t.Errorf("coordinator = %q, want mayor — the guard must keep the running name", cfg.Agents.Coordinator)
	}
	if cfg.StallWatch.Agent != "mayor" {
		t.Errorf("stall watch agent = %q, want mayor — it followed the coordinator and must keep following", cfg.StallWatch.Agent)
	}
	if got := cfg.Agents.CoordinatorName(); got != "mayor" {
		t.Errorf("CoordinatorName() = %q, want mayor", got)
	}
}

// (c) A rename of a STOPPED coordinator still works — it is the documented way to
// rename the role. A record left behind by a coordinator that has since exited
// (or by a pogod that was SIGKILLed before its exit hook ran) must not freeze the
// name forever.
func TestGuardRunningCoordinator_AllowsRenameOfStoppedCoordinator(t *testing.T) {
	pogoHomeSandbox(t)
	if err := RecordRunningCoordinator("mayor", deadProcess(t)); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	cfg.Agents.Coordinator = "boss"
	cfg.StallWatch.Agent = "boss"

	cfg, refusal := GuardRunningCoordinator(cfg)

	if refusal != nil {
		t.Fatalf("rename of a stopped coordinator refused: %v", refusal)
	}
	if cfg.Agents.Coordinator != "boss" {
		t.Errorf("coordinator = %q, want boss — a stopped coordinator may be renamed", cfg.Agents.Coordinator)
	}
	if cfg.StallWatch.Agent != "boss" {
		t.Errorf("stall watch agent = %q, want boss", cfg.StallWatch.Agent)
	}
}

// No record at all — a daemon that never started a coordinator — is not a
// refusal. Every fresh install lives here.
func TestGuardRunningCoordinator_NoRecordIsNoOp(t *testing.T) {
	pogoHomeSandbox(t)

	cfg := &Config{}
	cfg.Agents.Coordinator = "ringmaster"
	cfg, refusal := GuardRunningCoordinator(cfg)
	if refusal != nil || cfg.Agents.Coordinator != "ringmaster" {
		t.Errorf("coordinator = %q, refusal = %v; want ringmaster / nil", cfg.Agents.Coordinator, refusal)
	}
}

// The names already agreeing is the steady state on every boot: no refusal, no
// log line, nothing to explain to the operator.
func TestGuardRunningCoordinator_MatchingNameIsNoOp(t *testing.T) {
	pogoHomeSandbox(t)
	if err := RecordRunningCoordinator("mayor", liveProcess(t)); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	cfg.Agents.Coordinator = "mayor"
	cfg, refusal := GuardRunningCoordinator(cfg)
	if refusal != nil {
		t.Errorf("unexpected refusal for an unchanged name: %v", refusal)
	}
	if cfg.Agents.Coordinator != "mayor" {
		t.Errorf("coordinator = %q, want mayor", cfg.Agents.Coordinator)
	}
}

// An explicitly configured [stall_watch] agent pointing somewhere other than the
// coordinator is not dragged along by the refusal.
func TestGuardRunningCoordinator_LeavesUnrelatedStallWatchAgent(t *testing.T) {
	pogoHomeSandbox(t)
	if err := RecordRunningCoordinator("mayor", liveProcess(t)); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	cfg.Agents.Coordinator = "ringmaster"
	cfg.StallWatch.Agent = "doctor"

	cfg, refusal := GuardRunningCoordinator(cfg)
	if refusal == nil {
		t.Fatal("expected a refusal")
	}
	if cfg.StallWatch.Agent != "doctor" {
		t.Errorf("stall watch agent = %q, want doctor — an explicit target must not follow the coordinator", cfg.StallWatch.Agent)
	}
}

// The record round-trips, and RunningCoordinator reports liveness off the pid
// rather than the file's mere existence.
func TestRunningCoordinator_LivenessComesFromThePID(t *testing.T) {
	pogoHomeSandbox(t)

	if got := RunningCoordinator(); got != nil {
		t.Fatalf("RunningCoordinator() = %+v with no record, want nil", got)
	}

	pid := liveProcess(t)
	if err := RecordRunningCoordinator("mayor", pid); err != nil {
		t.Fatal(err)
	}
	rec := RunningCoordinator()
	if rec == nil || rec.Name != "mayor" || rec.PID != pid {
		t.Fatalf("RunningCoordinator() = %+v, want mayor/%d", rec, pid)
	}
	if rec.StartedAt.IsZero() {
		t.Error("StartedAt not recorded")
	}

	if err := RecordRunningCoordinator("mayor", deadProcess(t)); err != nil {
		t.Fatal(err)
	}
	if got := RunningCoordinator(); got != nil {
		t.Errorf("RunningCoordinator() = %+v for a dead pid, want nil", got)
	}
}

// Clearing is scoped to the exact (name, pid) that wrote the record, so an
// exiting coordinator cannot disarm the guard its own respawn just re-armed.
func TestClearRunningCoordinator_OnlyClearsItsOwnRecord(t *testing.T) {
	pogoHomeSandbox(t)
	newPID := liveProcess(t)
	if err := RecordRunningCoordinator("mayor", newPID); err != nil {
		t.Fatal(err)
	}

	// The predecessor's exit hook fires late, carrying the OLD pid.
	ClearRunningCoordinator("mayor", newPID+100000)
	if RunningCoordinator() == nil {
		t.Fatal("a stale exit cleared the successor's record")
	}
	// A different agent's exit must not clear it either.
	ClearRunningCoordinator("doctor", newPID)
	if RunningCoordinator() == nil {
		t.Fatal("another agent's exit cleared the coordinator record")
	}

	ClearRunningCoordinator("mayor", newPID)
	if got := RunningCoordinator(); got != nil {
		t.Errorf("RunningCoordinator() = %+v after its own clear, want nil", got)
	}
	if _, err := os.Stat(RunningCoordinatorPath()); !os.IsNotExist(err) {
		t.Errorf("record file still present after clear: %v", err)
	}
}

// A garbage record is not a refusal — it must not wedge role resolution.
func TestGuardRunningCoordinator_CorruptRecordIsIgnored(t *testing.T) {
	pogoHomeSandbox(t)
	if err := os.WriteFile(RunningCoordinatorPath(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	cfg.Agents.Coordinator = "ringmaster"
	cfg, refusal := GuardRunningCoordinator(cfg)
	if refusal != nil || cfg.Agents.Coordinator != "ringmaster" {
		t.Errorf("coordinator = %q, refusal = %v; a corrupt record must be ignored", cfg.Agents.Coordinator, refusal)
	}
}
