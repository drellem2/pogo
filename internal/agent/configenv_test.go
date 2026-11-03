package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// envConfig is an AgentCommandConfig that also implements AgentEnvConfig, the
// shape *config.AgentsConfig has in production.
type envConfig struct {
	byType map[string][]string
}

func (envConfig) AgentCommand(string) string  { return "" }
func (envConfig) AgentProvider(string) string { return "" }
func (c envConfig) AgentEnv(agentType string) []string {
	return c.byType[agentType]
}

// plainConfig implements AgentCommandConfig only: a config that predates
// AgentEnvConfig must keep working and contribute no environment.
type plainConfig struct{}

func (plainConfig) AgentCommand(string) string  { return "" }
func (plainConfig) AgentProvider(string) string { return "" }

// waitOutput waits for a short-lived agent to exit and returns what it printed.
func waitOutput(t *testing.T, a *Agent) string {
	t.Helper()
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("agent %s did not exit", a.Name)
	}
	return string(a.RecentOutput(64 * 1024))
}

func assertEnvLines(t *testing.T, what, output string, want, notWant []string) {
	t.Helper()
	lines := map[string]bool{}
	for _, l := range strings.Split(output, "\n") {
		lines[strings.TrimRight(l, "\r")] = true
	}
	for _, w := range want {
		if !lines[w] {
			t.Errorf("%s: environment is missing %q", what, w)
		}
	}
	for _, n := range notWant {
		if lines[n] {
			t.Errorf("%s: environment has %q, which a later layer should have overridden", what, n)
		}
	}
}

// TestSpawnAppliesConfigEnvBeneathPogoAndSpawnEnv: the configured env reaches
// the process, ${POGO_AGENT_NAME} becomes the agent's name, and the order in
// processEnv holds — pogo's identity variables beat config (config cannot
// rename an agent), and the spawn's own --env beats config.
func TestSpawnAppliesConfigEnvBeneathPogoAndSpawnEnv(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(envConfig{byType: map[string][]string{
		"crew": {
			"CFG_ONLY=1",
			"WHO=" + AgentNamePlaceholder,
			"TAGGED=pre-" + AgentNamePlaceholder + "-post",
			AgentNameEnv + "=hijacked",
			"OVERRIDE=from-config",
		},
	}})

	a, err := reg.Spawn(SpawnRequest{
		Name:    "cfgenv",
		Type:    TypeCrew,
		Command: []string{"env"},
		Env:     []string{"OVERRIDE=from-spawn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEnvLines(t, "spawn", waitOutput(t, a),
		[]string{"CFG_ONLY=1", "WHO=cfgenv", "TAGGED=pre-cfgenv-post", AgentNameEnv + "=cfgenv", "OVERRIDE=from-spawn"},
		[]string{AgentNameEnv + "=hijacked", "OVERRIDE=from-config", "WHO=" + AgentNamePlaceholder},
	)
}

// TestSpawnConfigEnvIsPerType: a polecat gets the polecat list, not crew's.
func TestSpawnConfigEnvIsPerType(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(envConfig{byType: map[string][]string{
		"crew":    {"KIND=crew"},
		"polecat": {"KIND=polecat"},
	}})

	a, err := reg.Spawn(SpawnRequest{Name: "cfgenv-cat", Type: TypePolecat, Command: []string{"env"}})
	if err != nil {
		t.Fatal(err)
	}
	assertEnvLines(t, "polecat spawn", waitOutput(t, a), []string{"KIND=polecat"}, []string{"KIND=crew"})
}

// TestRespawnKeepsSpawnEnvAndConfigEnv is the bug this change fixes. Before
// it, the restart path rebuilt the environment from the identity catalogue
// alone, so a restarted agent lost every --env it was spawned with.
func TestRespawnKeepsSpawnEnvAndConfigEnv(t *testing.T) {
	// Respawn's park backstop reads $HOME/.pogo/agents/<name>/.parked.
	testsandbox.Isolate(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(envConfig{byType: map[string][]string{
		"crew": {"CFG_ONLY=1", "WHO=" + AgentNamePlaceholder},
	}})

	a, err := reg.Spawn(SpawnRequest{
		Name:    "cfgenv-restart",
		Type:    TypeCrew,
		Command: []string{"env"},
		Env:     []string{"FROM_SPAWN=yes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"CFG_ONLY=1", "WHO=cfgenv-restart", "FROM_SPAWN=yes", AgentNameEnv + "=cfgenv-restart"}
	assertEnvLines(t, "first spawn", waitOutput(t, a), want, nil)

	b, err := reg.Respawn("cfgenv-restart")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	if b.RestartCount != 1 {
		t.Errorf("RestartCount = %d, want 1", b.RestartCount)
	}
	assertEnvLines(t, "after restart", waitOutput(t, b), want, nil)

	// And again: the carried env must survive a restart of a restart.
	c, err := reg.Respawn("cfgenv-restart")
	if err != nil {
		t.Fatalf("second Respawn: %v", err)
	}
	assertEnvLines(t, "after second restart", waitOutput(t, c), want, nil)
}

// TestRespawnedPolecatKeepsRole: a polecat's spawn env is the core budget, the
// dispatcher's --env and POGO_ROLE (polecatSpawnEnv). The restart path used to
// drop all three, so a polecat restarted after a crash came back without the
// frozen role identifier.
func TestRespawnedPolecatKeepsRole(t *testing.T) {
	testsandbox.Isolate(t)
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	budget := WorkerBudget{Cores: 2, HostCores: 8, Basis: "test"}
	a, err := reg.Spawn(SpawnRequest{
		Name:    "cfgenv-cat-restart",
		Type:    TypePolecat,
		Command: []string{"env"},
		Env:     polecatSpawnEnv(budget, []string{"DISPATCHED=1"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{RoleEnv + "=polecat", WorkerCoresEnv + "=2", "DISPATCHED=1"}
	assertEnvLines(t, "polecat spawn", waitOutput(t, a), want, nil)

	b, err := reg.Respawn("cfgenv-cat-restart")
	if err != nil {
		t.Fatalf("Respawn: %v", err)
	}
	assertEnvLines(t, "polecat restart", waitOutput(t, b), want, nil)
}

// TestConfigEnvWithoutAgentEnvConfig: a config that implements only
// AgentCommandConfig, or no config at all, contributes nothing.
func TestConfigEnvWithoutAgentEnvConfig(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	if got := reg.configEnvLocked(TypeCrew, "x"); got != nil {
		t.Errorf("no config: configEnvLocked = %q, want nil", got)
	}
	reg.SetCommandConfig(plainConfig{})
	if got := reg.configEnvLocked(TypeCrew, "x"); got != nil {
		t.Errorf("plain config: configEnvLocked = %q, want nil", got)
	}
}

// TestConfiguredEnvRows: config-injected variables appear in the catalogue
// view `pogo agent env` prints, with the placeholder left visible and
// explained, and unusable entries left out as the spawn path leaves them out.
func TestConfiguredEnvRows(t *testing.T) {
	rows := ConfiguredEnv([]string{"A=1", "WHO=" + AgentNamePlaceholder, "BROKEN", "=x"})
	if len(rows) != 2 {
		t.Fatalf("ConfiguredEnv rows = %+v, want 2", rows)
	}
	if rows[0].Name != "A" || rows[0].Value != "1" || !rows[0].Set || rows[0].When != "" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Name != "WHO" || rows[1].Value != AgentNamePlaceholder || !strings.Contains(rows[1].When, AgentNamePlaceholder) {
		t.Errorf("row 1 = %+v", rows[1])
	}
	for _, r := range rows {
		if r.Why == "" || r.Note == "" {
			t.Errorf("row %s has an empty Why or Note: %+v", r.Name, r)
		}
	}
}
