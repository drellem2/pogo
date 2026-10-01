package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestExpectedRespawnRefusalsAreDistinguishable pins the classification the
// alarm path depends on: a respawn DECLINED by a guard is telling its caller
// something different from a respawn that was TRIED and FAILED, and until
// mg-0208 there was no way for a caller in another package to tell them apart
// except by matching on error prose.
//
// Both arms matter. Misclassifying a guard refusal as a failure is the observed
// defect (five false restart_failed conditions on a deliberate fleet stop);
// misclassifying a genuine failure as a guard refusal would silence the alarm
// that row exists for.
func TestExpectedRespawnRefusalsAreDistinguishable(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{Name: "refusal", Type: TypeCrew, Command: []string{"true"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-a.Done()

	// GENUINE FAILURE, normal operation: latch clear, generation unmoved, and
	// the respawn fails because of this agent. Must NOT be classified as a
	// guard refusal.
	gen := reg.Generation()
	reg.Remove("refusal")
	if _, err = reg.RespawnFromGeneration("refusal", gen); err == nil {
		t.Fatal("respawn of a removed agent succeeded")
	} else if IsExpectedRespawnRefusal(err) {
		t.Errorf("%v classified as a guard refusal — a genuine respawn failure must still alarm", err)
	}

	// GENERATION MOVED: the stop->start round-trip a deferred respawn sleeps
	// through. Expected, and the message must still name the mismatch (the
	// wrapping must not swallow the detail an operator reads).
	if _, err := reg.Spawn(SpawnRequest{Name: "gen", Type: TypeCrew, Command: []string{"true"}}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	stale := reg.Generation()
	reg.StopAll(2 * time.Second)
	reg.Resume()

	_, err = reg.RespawnFromGeneration("gen", stale)
	if err == nil {
		t.Fatal("a respawn from a stale generation was admitted")
	}
	if !IsExpectedRespawnRefusal(err) {
		t.Errorf("generation refusal %v not classified as expected", err)
	}
	if !errors.Is(err, ErrRespawnSuperseded) {
		t.Errorf("generation refusal %v does not wrap ErrRespawnSuperseded", err)
	}
	if !strings.Contains(err.Error(), "generation") {
		t.Errorf("generation refusal %v no longer names the mismatch", err)
	}

	// SHUTDOWN LATCH: every exit produced by a fleet stop lands here, because
	// StopAll raises the latch before it stops anybody.
	if _, err := reg.Spawn(SpawnRequest{Name: "latched", Type: TypeCrew, Command: []string{"true"}}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	reg.StopAll(2 * time.Second)

	_, err = reg.Respawn("latched")
	if err == nil {
		t.Fatal("a respawn was admitted with the shutdown latch set")
	}
	if !IsExpectedRespawnRefusal(err) {
		t.Errorf("shutdown refusal %v not classified as expected", err)
	}
	if !errors.Is(err, ErrRegistryShutDown) {
		t.Errorf("shutdown refusal %v is not ErrRegistryShutDown", err)
	}

	// The park backstop is NOT in the expected set: parking is deliberate, but
	// it is one agent, not the fleet, and has its own sentinel (ErrRespawnParked,
	// mg-fcb49) that callers test for by name. Pinned so a future edit to
	// IsExpectedRespawnRefusal has to be deliberate about the boundary.
	if IsExpectedRespawnRefusal(fmt.Errorf("agent %q %w", "x", ErrRespawnParked)) {
		t.Error("the park backstop was classified as a fleet guard refusal")
	}
	if IsExpectedRespawnRefusal(errors.New(`agent "x" is parked`)) {
		t.Error("an unrelated error was classified as a guard refusal")
	}
}

// TestRespawnOfAParkedAgentIsErrRespawnParked pins the park backstop's refusal
// (mg-fcb49): a respawn scheduled before a park and firing after it returns the
// sentinel, with the message an operator reads unchanged. Control: the same
// crashed agent, not parked, respawns.
func TestRespawnOfAParkedAgentIsErrRespawnParked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	for _, park := range []bool{false, true} {
		name := "unparked"
		if park {
			name = "parked"
		}
		a, err := reg.Spawn(SpawnRequest{Name: name, Type: TypeCrew, Command: []string{"true"}, RestartOnCrash: true})
		if err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
		<-a.Done()
		if park {
			if _, err := reg.Park(name, 2*time.Second); err != nil {
				t.Fatalf("Park: %v", err)
			}
		}
		_, err = reg.Respawn(name)
		if !park {
			if err != nil {
				t.Fatalf("control: respawn of an unparked crashed agent failed: %v", err)
			}
			continue
		}
		if !errors.Is(err, ErrRespawnParked) {
			t.Fatalf("respawn of a parked agent returned %v, want ErrRespawnParked", err)
		}
		if err.Error() != `agent "parked" is parked` {
			t.Errorf("message changed: %q", err.Error())
		}
	}
}

// TestRespawnOfARunningAgentIsErrRespawnAgentAlive pins the refusal a deferred
// respawn gets when something else started the agent inside its backoff
// (drellem2/pogo#159, #217): the sentinel, with the message unchanged.
func TestRespawnOfARunningAgentIsErrRespawnAgentAlive(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{Name: "racer", Type: TypeCrew, Command: []string{"cat"}, RestartOnCrash: true})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	_, err = reg.Respawn("racer")
	if !errors.Is(err, ErrRespawnAgentAlive) {
		t.Fatalf("respawn of a running agent returned %v, want ErrRespawnAgentAlive", err)
	}
	if err.Error() != `agent "racer" is still running` {
		t.Errorf("message changed: %q", err.Error())
	}
	if IsExpectedRespawnRefusal(err) {
		t.Error("ErrRespawnAgentAlive was classified as a guard refusal")
	}
	if requested, _ := a.StopRequested(); requested {
		t.Error("StopRequested is true before any stop")
	}
	if err := reg.Stop("racer", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if requested, cause := a.StopRequested(); !requested || cause != StopCauseRequest {
		t.Errorf("StopRequested after Stop = (%v, %q), want (true, %q)", requested, cause, StopCauseRequest)
	}
}
