package agent

import (
	"strings"
	"testing"
	"time"
)

func TestIsEarlyExit(t *testing.T) {
	const window = 60 * time.Second
	tests := []struct {
		name          string
		typ           AgentType
		stopRequested bool
		lived         time.Duration
		want          bool
	}{
		{"polecat dies 3s in (#177)", TypePolecat, false, 3 * time.Second, true},
		{"polecat dies after its cold start", TypePolecat, false, 2 * time.Minute, false},
		{"polecat stopped on request 2s in", TypePolecat, true, 2 * time.Second, false},
		{"crew dies 3s in", TypeCrew, false, 3 * time.Second, false},
		{"exactly at the window edge is no longer early", TypePolecat, false, window, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEarlyExit(tt.typ, tt.stopRequested, tt.lived, window); got != tt.want {
				t.Errorf("isEarlyExit = %v, want %v", got, tt.want)
			}
		})
	}
}

// waitExited blocks until the agent's exit handling has finished.
func waitExited(t *testing.T, a *Agent) {
	t.Helper()
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not exit")
	}
}

// TestEarlyExitOnAHarnessThatDiesAtStartup is #177's shape end to end: a
// process that prints something and exits 1 before any composer appears. The
// exit must read as early, with the composer never seen and the output kept.
func TestEarlyExitOnAHarnessThatDiesAtStartup(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "early-exit",
		Type:    TypePolecat,
		Command: []string{"sh", "-c", "printf 'No, exit\\n'; exit 1"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitExited(t, a)

	e, ok := a.EarlyExit()
	if !ok {
		t.Fatal("a polecat that exited 1 at startup was not reported as an early exit")
	}
	if e.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", e.ExitCode)
	}
	if e.ComposerSeen {
		t.Error("ComposerSeen = true for a harness that never drew one")
	}
	if !strings.Contains(e.LastOutput, "No, exit") {
		t.Errorf("LastOutput = %q, want the harness's last words", e.LastOutput)
	}
	if e.Window != DefaultNudgeProfile.InitialNudgeTimeout {
		t.Errorf("Window = %v, want the cold-start budget %v", e.Window, DefaultNudgeProfile.InitialNudgeTimeout)
	}
}

// TestRequestedStopIsNotAnEarlyExit: an operator stopping a polecat a moment
// after spawning it is not a dead worker, and must not page anyone.
func TestRequestedStopIsNotAnEarlyExit(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "stopped-early",
		Type:    TypePolecat,
		Command: []string{"sleep", "30"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, ok := a.EarlyExit(); ok {
		t.Error("a RUNNING agent reported an early exit")
	}
	if err := reg.Stop(a.Name, 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitExited(t, a)

	if _, ok := a.EarlyExit(); ok {
		t.Error("a requested stop was reported as an early exit")
	}
}
