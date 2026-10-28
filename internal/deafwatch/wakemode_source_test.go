package deafwatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/agenttest"
	"github.com/drellem2/pogo/internal/testsandbox"
)

type noMailChecks struct{}

func (noMailChecks) HasMailCheck(string) bool { return false }

// TestRegistrySource_PointerWakeDeclaredIsNotAnnounced is deafwatch's half of
// drellem2/pogo#232, #236, end to end through a real registry: two RUNNING crew
// agents with no mail-check, one declaring pointer wake. Only the undeclared
// one may become a finding — and under the [agents] wake = "pointer" default,
// only one whose frontmatter overrides the default back to mail-check.
func TestRegistrySource_PointerWakeDeclaredIsNotAnnounced(t *testing.T) {
	home := testsandbox.Isolate(t).Home
	t.Setenv("POGO_HOME", filepath.Join(home, ".pogo"))
	if err := agent.InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	prompts := map[string]string{
		"declared":   "+++\nauto_start = true\nwake = \"pointer\"\n+++\n",
		"undeclared": "+++\nauto_start = true\n+++\n",
		"optout":     "+++\nauto_start = true\nwake = \"mail-check\"\n+++\n",
	}
	reg, err := agent.NewRegistry(agenttest.SocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for name, fm := range prompts {
		if err := os.WriteFile(filepath.Join(agent.CrewPromptDir(), name+".md"), []byte(fm+"# "+name+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Spawn(agent.SpawnRequest{Name: name, Type: agent.TypeCrew, Command: []string{"sleep", "60"}}); err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
		n := name
		t.Cleanup(func() { _ = reg.Stop(n, 5*time.Second) })
	}
	reg.SetMailCheckProvider(noMailChecks{})

	findings := func() map[string]bool {
		t.Helper()
		snap, err := RegistrySource(reg)(time.Now())
		if err != nil {
			t.Fatalf("RegistrySource: %v", err)
		}
		got := map[string]bool{}
		for _, f := range snap.Missing {
			got[f.Name] = true
		}
		return got
	}

	got := findings()
	if got["declared"] || !got["undeclared"] || !got["optout"] || len(got) != 2 {
		t.Errorf("no default: findings = %v, want undeclared and optout only", got)
	}

	_ = agent.SetDefaultWakeMode("pointer")
	t.Cleanup(func() { _ = agent.SetDefaultWakeMode("") })
	got = findings()
	if got["declared"] || got["undeclared"] || !got["optout"] || len(got) != 1 {
		t.Errorf("[agents] wake = pointer: findings = %v, want optout only", got)
	}
}
