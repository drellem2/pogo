package agent

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/testsandbox"
)

// TestSpawnedPolecatIsToldItsShareOfTheHost is the end-to-end half of mg-eb47's
// budget: a real spawn, through the real handler, and the number reaches the
// worker's prompt.
//
// The env var and the prompt prose are two halves of one control and the prose
// is the half that gets read. A worker that receives $POGO_WORKER_CORES and is
// never told to pass it to `-j` behaves exactly like the Lean build that held
// 9.0 of 10 cores — which is the failure this exists to prevent, so the prompt
// is where the assertion belongs.
func TestSpawnedPolecatIsToldItsShareOfTheHost(t *testing.T) {
	testsandbox.Isolate(t)

	writeTemplate(t, "budgeted",
		"# polecat\n{{if .WorkerCores}}BUDGET: {{.WorkerCores}} of {{.HostCores}}{{else}}BUDGET: none{{end}}\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})
	reg.SetDispatchCap(config.DefaultDispatchCapConfig())

	want := reg.WorkerBudget()
	if !want.Known() {
		t.Fatalf("no budget derivable on this host: %+v", want)
	}

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name:       "pc-budgeted",
		Template:   "budgeted",
		Id:         "wi-budgeted",
		NoWorktree: true,
	})
	data, err := os.ReadFile(a.PromptFile)
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	body := string(data)
	line := fmt.Sprintf("BUDGET: %d of %d", want.Cores, want.HostCores)
	if !strings.Contains(body, line) {
		t.Errorf("prompt does not carry the budget %q; got:\n%s", line, body)
	}
	if strings.Contains(body, "BUDGET: none") {
		t.Error("the budget rendered as absent on a host whose core count is known")
	}
}

// TestShippedTemplatesCarryTheCoreBudget. The bullet is the enforcement
// mechanism — nothing else honours the env var — so it has to be in every
// worker template, not only the build one. A triage or review worker still runs
// the repo's suite and still shares the box with the refinery gate.
func TestShippedTemplatesCarryTheCoreBudget(t *testing.T) {
	entries, err := os.ReadDir("prompts/templates")
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		seen++
		body, err := os.ReadFile("prompts/templates/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		s := string(body)
		for _, want := range []string{"{{if .WorkerCores}}", WorkerCoresEnv, "{{.HostCores}}"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s does not mention %s — a worker reading it is never told its share",
					e.Name(), want)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no templates found: this test would pass vacuously")
	}
}
