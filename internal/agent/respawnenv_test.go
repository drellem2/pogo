package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// captureEnvCommand returns argv that writes the process environment to path
// and exits. The path is baked into argv rather than passed through env so the
// instrument does not depend on the thing under test.
func captureEnvCommand(path string) []string {
	return []string{"sh", "-c", "env > '" + path + "'"}
}

// readEnvCapture parses an `env` dump into a map. exec.Cmd de-duplicates keys
// keeping the LAST assignment, so the map is the effective environment.
func readEnvCapture(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env capture %s: %v", path, err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

func waitDone(t *testing.T, a *Agent) {
	t.Helper()
	select {
	case <-a.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("agent %s did not exit", a.Name)
	}
}

// TestRespawnReappliesSpawnEnv is drellem2/pogo#165 gap 1: an env supplied at
// spawn must survive a respawn, in the same last-wins order, so the respawned
// process has the environment its spawn had — including the worker budget and
// a POGO_ROLE a dispatcher cannot override.
func TestRespawnReappliesSpawnEnv(t *testing.T) {
	testsandbox.Isolate(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	dir := t.TempDir()
	spawnCapture := filepath.Join(dir, "spawn.env")
	respawnCapture := filepath.Join(dir, "respawn.env")

	// Polecat-shaped: the same helper the spawn-polecat route uses, with a
	// dispatcher override of the budget (must win) and of POGO_ROLE (must not).
	budget := WorkerBudgetFor(10, config.DefaultDispatchCapConfig(), config.DefaultDispatchLoadGateConfig())
	env := polecatSpawnEnv(budget, []string{
		"CLAUDE_CODE_EFFORT_LEVEL=medium",
		WorkerCoresEnv + "=7",
		"POGO_ROLE=impostor",
	})

	a, err := reg.Spawn(SpawnRequest{
		Name:    "respawn-env",
		Type:    TypePolecat,
		Command: captureEnvCommand(spawnCapture),
		Env:     env,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitDone(t, a)

	a.Command = captureEnvCommand(respawnCapture)
	b, err := reg.Respawn("respawn-env")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)

	spawned := readEnvCapture(t, spawnCapture)
	respawned := readEnvCapture(t, respawnCapture)

	// Positive control: the injected identity catalogue reached the respawned
	// process, so an absence below is a real absence, not an unwritten file.
	if respawned["POGO_AGENT_NAME"] != "respawn-env" {
		t.Fatalf("control failed: POGO_AGENT_NAME = %q in respawn capture", respawned["POGO_AGENT_NAME"])
	}

	want := map[string]string{
		"CLAUDE_CODE_EFFORT_LEVEL": "medium",
		WorkerCoresEnv:             "7",
		HostCoresEnv:               spawned[HostCoresEnv],
		"POGO_ROLE":                "polecat",
	}
	if want[HostCoresEnv] == "" {
		t.Fatalf("spawn capture has no %s; budget not injected: %v", HostCoresEnv, budget)
	}
	for k, v := range want {
		if spawned[k] != v {
			t.Errorf("spawn: %s = %q, want %q", k, spawned[k], v)
		}
		if respawned[k] != v {
			t.Errorf("respawn: %s = %q, want %q (spawn env not carried across respawn)", k, respawned[k], v)
		}
	}
}

// TestRespawnEnvIsNotReported pins the standing constraint carried from triage:
// an env can hold secrets, so the carried copy must not reach the agent's JSON
// encoding or AgentInfo. A second restart must still carry it (the copy is
// passed along, not consumed).
func TestRespawnEnvIsNotReported(t *testing.T) {
	testsandbox.Isolate(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	const secret = "s3cr3t-value-165"
	reqEnv := []string{"POGO_TEST_SECRET=" + secret}
	a, err := reg.Spawn(SpawnRequest{
		Name:    "respawn-env-secret",
		Type:    TypeCrew,
		Command: []string{"true"},
		Env:     reqEnv,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// The carried copy must not alias the caller's slice.
	reqEnv[0] = "POGO_TEST_SECRET=mutated"
	waitDone(t, a)

	b, err := reg.Respawn("respawn-env-secret")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	waitDone(t, b)

	capture := filepath.Join(t.TempDir(), "second.env")
	b.Command = captureEnvCommand(capture)
	c, err := reg.Respawn("respawn-env-secret")
	if err != nil {
		t.Fatalf("second Respawn: %v", err)
	}
	waitDone(t, c)
	if got := readEnvCapture(t, capture)["POGO_TEST_SECRET"]; got != secret {
		t.Errorf("after two respawns POGO_TEST_SECRET = %q, want %q", got, secret)
	}

	for _, ag := range []*Agent{a, b, c} {
		raw, err := json.Marshal(ag)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		info, err := json.Marshal(agentInfo(ag))
		if err != nil {
			t.Fatalf("Marshal AgentInfo: %v", err)
		}
		for what, data := range map[string][]byte{"Agent JSON": raw, "AgentInfo JSON": info} {
			if strings.Contains(string(data), secret) || strings.Contains(string(data), "POGO_TEST_SECRET") {
				t.Errorf("%s leaks the spawn env: %s", what, data)
			}
		}
	}
}
