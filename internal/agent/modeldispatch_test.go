package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// End-to-end coverage for model selection through the dispatch path
// (`pogo agent spawn-polecat --model` → POST /agents/spawn-polecat → argv), the
// acceptance bar mg-e7f5 names first. The unit-level chain lives in
// model_test.go; what these tests add is the wiring, which is the half that
// silently does nothing when a field is added to a struct and forgotten in a
// handler.

// modelTestRegistry returns a registry that spawns `cat` for everything, with a
// single provider registered under the given id and model flag. A registered
// provider is required: a bare registry resolves to a nil provider, which
// cannot express a model at all.
func modelTestRegistry(t *testing.T, providerID, modelFlag string) *Registry {
	t.Helper()
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	reg.SetCommandConfig(catCommandConfig{})
	reg.RegisterProvider(modelProvider(providerID, modelFlag))
	reg.SetDefaultProvider(providerID)
	return reg
}

// spawnPolecatStatus calls the handler and returns the recorder, without
// asserting a status — for the refusal paths, where 201 is the failure.
func spawnPolecatStatus(t *testing.T, reg *Registry, spawnReq SpawnPolecatAPIRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(spawnReq)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	reg.handleSpawnPolecat(rr, httptest.NewRequest("POST", "/agents/spawn-polecat", bytes.NewReader(body)))
	return rr
}

// TestDispatchModelPrecedence is the headline wiring check: --model beats the
// template's model: frontmatter, frontmatter applies on its own, and a template
// with neither produces argv carrying NO model flag.
//
// The last row is the one worth defending. It is the state every shipped
// template is in, so it is the behaviour of the entire fleet — and the whole
// reason this feature adds a per-spawn selection rather than a default.
func TestDispatchModelPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		frontEnd  string // template frontmatter line, or ""
		flag      string // --model
		wantArgv  []string
		wantModel string
	}{
		{
			name:      "flag beats frontmatter",
			frontEnd:  "model = \"claude-opus-5\"\n",
			flag:      "fable",
			wantArgv:  []string{"cat", "--model", "fable"},
			wantModel: "fable",
		},
		{
			name:      "frontmatter alone applies",
			frontEnd:  "model = \"fable\"\n",
			flag:      "",
			wantArgv:  []string{"cat", "--model", "fable"},
			wantModel: "fable",
		},
		{
			name:      "flag alone applies",
			frontEnd:  "",
			flag:      "fable",
			wantArgv:  []string{"cat", "--model", "fable"},
			wantModel: "fable",
		},
		{
			name:      "neither pins nothing",
			frontEnd:  "",
			flag:      "",
			wantArgv:  []string{"cat"},
			wantModel: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testsandbox.Isolate(t)
			writeTemplate(t, "modeled", "+++\nworktree = false\n"+tc.frontEnd+"+++\nbody {{.Id}}\n")
			reg := modelTestRegistry(t, "claude", "--model")

			a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
				Name: "pc-modeled", Template: "modeled", Id: "wi-model", Model: tc.flag,
			})
			if !equalArgv(a.Command, tc.wantArgv) {
				t.Errorf("spawned argv = %v, want %v", a.Command, tc.wantArgv)
			}
			if a.Model != tc.wantModel {
				t.Errorf("agent Model = %q, want %q", a.Model, tc.wantModel)
			}
		})
	}
}

// TestDispatchModelSurfacedInAgentInfo verifies the selection is answerable
// through the API without parsing argv — and, just as importantly, that "no
// model" is legible as such rather than as missing data.
func TestDispatchModelSurfacedInAgentInfo(t *testing.T) {
	testsandbox.Isolate(t)
	writeTemplate(t, "modeled", "+++\nworktree = false\n+++\nbody\n")
	reg := modelTestRegistry(t, "claude", "--model")

	pinned := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name: "pc-pinned", Template: "modeled", Id: "wi-a", Model: "fable",
	})
	if got := agentInfo(pinned).Model; got != "fable" {
		t.Errorf("AgentInfo.Model = %q, want fable", got)
	}
	unpinned := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name: "pc-unpinned", Template: "modeled", Id: "wi-b",
	})
	if got := agentInfo(unpinned).Model; got != "" {
		t.Errorf("AgentInfo.Model = %q, want empty for an unpinned agent", got)
	}
	// And the field is omitted from JSON when empty, so a reader cannot mistake
	// "pogo pinned nothing" for "pogo pinned the empty string".
	blob, err := json.Marshal(agentInfo(unpinned))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte(`"model"`)) {
		t.Errorf("unpinned AgentInfo JSON should omit model, got %s", blob)
	}
}

// TestDispatchRefusesUnusableModel verifies each way a model request can be
// unsatisfiable is a REFUSED dispatch (400) with nothing left behind — not a
// worker started on a model nobody chose. That silence is the failure the
// architect's acceptance criteria single out.
func TestDispatchRefusesUnusableModel(t *testing.T) {
	cases := []struct {
		name         string
		providerFlag string // provider's ModelFlag ("" = cannot express a model)
		frontEnd     string
		flag         string
		wantInBody   string
	}{
		{
			name:         "provider cannot express a model",
			providerFlag: "",
			flag:         "fable",
			wantInBody:   "cannot express a model selection",
		},
		{
			name:         "flag value would be read as a flag",
			providerFlag: "--model",
			flag:         "-p",
			wantInBody:   "read as a flag",
		},
		{
			name:         "flag value carries a shell metacharacter",
			providerFlag: "--model",
			flag:         "$(whoami)",
			wantInBody:   "disallowed character",
		},
		{
			// The frontmatter tier must be refused too, and this is the row
			// that proves the parser's decision NOT to validate (see
			// TestParsePromptFrontmatterKeepsBadModelOutOfTheParse) does not
			// leave a hole: the template parses, and the dispatch still stops.
			name:         "frontmatter value would be read as a flag",
			providerFlag: "--model",
			frontEnd:     "model = \"-p\"\n",
			wantInBody:   "read as a flag",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testsandbox.Isolate(t)
			writeTemplate(t, "modeled", "+++\nworktree = false\n"+tc.frontEnd+"+++\nbody\n")
			reg := modelTestRegistry(t, "mystery", tc.providerFlag)

			rr := spawnPolecatStatus(t, reg, SpawnPolecatAPIRequest{
				Name: "pc-bad", Template: "modeled", Id: "wi-bad", Model: tc.flag,
			})
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 — an unsatisfiable model must refuse the dispatch; body=%s",
					rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantInBody) {
				t.Errorf("refusal body = %q, want it to mention %q", rr.Body.String(), tc.wantInBody)
			}
			if reg.Get("pc-bad") != nil {
				t.Error("a refused dispatch registered an agent anyway")
			}
		})
	}
}

// TestDispatchModelIsPerSpawn verifies two polecats dispatched back-to-back
// through the same registry and provider get their own models — the mixed-fleet
// property, on the model axis. Nothing caches a model globally, so nothing can
// leak one onto the next worker.
func TestDispatchModelIsPerSpawn(t *testing.T) {
	testsandbox.Isolate(t)
	writeTemplate(t, "modeled", "+++\nworktree = false\n+++\nbody\n")
	reg := modelTestRegistry(t, "claude", "--model")

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name: "pc-a", Template: "modeled", Id: "wi-a", Model: "fable",
	})
	b := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name: "pc-b", Template: "modeled", Id: "wi-b", Model: "claude-opus-5",
	})
	c := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name: "pc-c", Template: "modeled", Id: "wi-c",
	})

	if !equalArgv(a.Command, []string{"cat", "--model", "fable"}) {
		t.Errorf("pc-a argv = %v", a.Command)
	}
	if !equalArgv(b.Command, []string{"cat", "--model", "claude-opus-5"}) {
		t.Errorf("pc-b argv = %v", b.Command)
	}
	if !equalArgv(c.Command, []string{"cat"}) {
		t.Errorf("pc-c argv = %v, want no model flag — a model must not leak onto "+
			"the next dispatch", c.Command)
	}
}

// TestCrewModelFromFrontmatter verifies the crew path honours the same key.
// Crew has no --model flag (as it has no --provider flag), so frontmatter is the
// only tier that can speak — and a crew prompt declaring model: must not be
// silently ignored, which is what would happen if only the polecat path were
// wired.
func TestCrewModelFromFrontmatter(t *testing.T) {
	testsandbox.Isolate(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writeCrewPromptRaw(t, "modeled-crew", "+++\nmodel = \"fable\"\n+++\n# crew\n")
	reg := modelTestRegistry(t, "claude", "--model")

	a := startAgentViaAPI(t, reg, "modeled-crew")
	if a.Model != "fable" {
		t.Errorf("crew agent Model = %q, want fable", a.Model)
	}
	if !equalArgv(a.Command, []string{"cat", "--model", "fable"}) {
		t.Errorf("crew argv = %v, want [cat --model fable]", a.Command)
	}
}

// TestCrewWithoutModelPinsNothing is the crew half of the no-default guard: the
// state every shipped crew prompt is in must keep producing argv with no model
// flag. A regression here would put the mayor itself on a pinned model, which is
// how the 2026-07-06 outage became fleet-wide rather than per-worker.
func TestCrewWithoutModelPinsNothing(t *testing.T) {
	testsandbox.Isolate(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writeCrewPromptRaw(t, "plain-crew", "# crew with no frontmatter\n")
	reg := modelTestRegistry(t, "claude", "--model")

	a := startAgentViaAPI(t, reg, "plain-crew")
	if a.Model != "" {
		t.Errorf("crew agent Model = %q, want empty", a.Model)
	}
	if !equalArgv(a.Command, []string{"cat"}) {
		t.Errorf("crew argv = %v, want [cat] — pogo must pin no model by default", a.Command)
	}
}

// writeCrewPromptRaw writes a crew prompt with verbatim content. It is the
// raw-content sibling of writeCrewPrompt, which synthesizes frontmatter for the
// auto_start key only.
func writeCrewPromptRaw(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join(CrewPromptDir(), name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write crew prompt: %v", err)
	}
}

// TestCrewRefusesUnusableModel is the crew half of the refusal, and it exists
// because the crew path is where a silent ignore would be easiest: StartCrewAgent
// discards frontmatter PARSE errors by design, so the model check has to be its
// own fatal step rather than riding on the parse. A crew agent that came up on
// the wrong model would be a fleet-wide condition discovered hours later; one
// that refuses to start is loud immediately.
func TestCrewRefusesUnusableModel(t *testing.T) {
	testsandbox.Isolate(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
	writeCrewPromptRaw(t, "bad-crew", "+++\nauto_start = true\nmodel = \"-p\"\n+++\n# crew\n")
	reg := modelTestRegistry(t, "claude", "--model")

	a, err := reg.StartCrewAgent("bad-crew")
	if err == nil {
		t.Fatalf("StartCrewAgent should have refused a model of %q; got agent %v", "-p", a)
	}
	if !strings.Contains(err.Error(), ModelTierFrontmatter) {
		t.Errorf("error %q should name the %s tier", err, ModelTierFrontmatter)
	}
	if reg.Get("bad-crew") != nil {
		t.Error("a refused crew start registered an agent anyway")
	}
}

// equalArgv compares two argv slices element-wise.
func equalArgv(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
