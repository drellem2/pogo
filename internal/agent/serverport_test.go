package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// An agent must always talk to the pogod that spawned it (mg-0c1a5). Before
// this, pogod set no POGO_PORT on its agents, so an agent of a sandbox pogod on
// a reserved port resolved its CLIs' server to :10000 — the LIVE daemon.
//
// The inherited environment AND the crew base env both carry the live port
// here, so the assertion can only pass if the identity layer overrides them —
// on spawn and again on a crash restart.
func TestSpawnInjectsTheSpawningDaemonsPort(t *testing.T) {
	testsandbox.Isolate(t)
	t.Setenv("POGO_PORT", "10000")
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetServerPort(18123)

	dir := t.TempDir()
	spawnCapture := filepath.Join(dir, "spawn.env")
	respawnCapture := filepath.Join(dir, "respawn.env")

	a, err := reg.Spawn(SpawnRequest{
		Name:    "port-env",
		Type:    TypeCrew,
		Command: captureEnvCommand(spawnCapture),
		BaseEnv: []string{"POGO_PORT=10000"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitDone(t, a)
	a.Command = captureEnvCommand(respawnCapture)
	b, err := reg.Respawn("port-env")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)

	for what, path := range map[string]string{"spawn": spawnCapture, "respawn": respawnCapture} {
		if got := readEnvCapture(t, path)[ServerPortEnv]; got != "18123" {
			t.Errorf("%s: POGO_PORT = %q, want 18123 (the spawning pogod's port) — the agent's CLIs would reach another daemon", what, got)
		}
	}
}

// Control: a registry that was never told its port (a bare test registry)
// injects nothing, so the inherited value stands — the injection is driven by
// SetServerPort, not by a hard-wired default that would always read "right".
func TestSpawnWithoutServerPortLeavesPOGOPORTInherited(t *testing.T) {
	testsandbox.Isolate(t)
	t.Setenv("POGO_PORT", "17777")
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	capture := filepath.Join(t.TempDir(), "spawn.env")
	a, err := reg.Spawn(SpawnRequest{Name: "port-unset", Type: TypeCrew, Command: captureEnvCommand(capture)})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitDone(t, a)
	if got := readEnvCapture(t, capture)[ServerPortEnv]; got != "17777" {
		t.Errorf("POGO_PORT = %q, want the inherited 17777", got)
	}
}
