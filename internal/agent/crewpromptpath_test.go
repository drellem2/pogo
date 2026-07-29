package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// promptPathSandbox gives a test its own HOME/POGO_HOME with the prompt dirs
// scaffolded but NO prompts installed, so every candidate file in a case is
// one the case wrote itself.
func promptPathSandbox(t *testing.T) {
	t.Helper()
	isolateParkState(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatalf("InitPromptDirs: %v", err)
	}
}

// TestCrewPromptPath_SplitIsPreserved is the positive control for the mg-4469
// fallback: the coordinator/crew split it lives inside is deliberate and
// load-bearing, so the fallback must not move any resolution that works today.
// Both the shipped default (ringmaster) and Daniel's pinned name (mayor)
// resolve to agents/mayor.md, and an ordinary crew name resolves under crew/.
func TestCrewPromptPath_SplitIsPreserved(t *testing.T) {
	promptPathSandbox(t)
	mayorFile := writePrompt(t, PromptDir(), "mayor", "# coordinator\n")
	scoutFile := writePrompt(t, CrewPromptDir(), "scout", "# scout\n")

	for _, coordinator := range []string{DefaultCoordinatorName, "mayor", "boss"} {
		setCoordinator(t, coordinator)
		got, err := crewPromptPath(coordinator)
		if err != nil {
			t.Fatalf("coordinator %q: crewPromptPath: %v", coordinator, err)
		}
		if got != mayorFile {
			t.Errorf("coordinator %q resolved to %q, want %q", coordinator, got, mayorFile)
		}
	}

	setCoordinator(t, DefaultCoordinatorName)
	got, err := crewPromptPath("scout")
	if err != nil {
		t.Fatalf("crewPromptPath(scout): %v", err)
	}
	if got != scoutFile {
		t.Errorf("crew agent resolved to %q, want %q", got, scoutFile)
	}
}

// TestCrewPromptPath_CoordinatorFallsThroughToCrewPrompt covers the mg-4469
// defect directly: with [agents] coordinator pinned to a name that has a crew
// prompt and no agents/mayor.md on disk, the old single-stat branch failed
// with `prompt file not found: agents/mayor.md` — a file the operator never
// configured. The name now falls through to the prompt they did configure.
func TestCrewPromptPath_CoordinatorFallsThroughToCrewPrompt(t *testing.T) {
	promptPathSandbox(t)
	setCoordinator(t, "doctor")
	doctorFile := writePrompt(t, CrewPromptDir(), "doctor", "# doctor\n")
	// Deliberately no agents/mayor.md.
	if _, err := os.Stat(filepath.Join(PromptDir(), "mayor.md")); !os.IsNotExist(err) {
		t.Fatalf("precondition: agents/mayor.md must not exist (stat err = %v)", err)
	}

	got, err := crewPromptPath("doctor")
	if err != nil {
		t.Fatalf("crewPromptPath(doctor): %v", err)
	}
	if got != doctorFile {
		t.Errorf("resolved to %q, want the configured crew prompt %q", got, doctorFile)
	}
}

// TestCrewPromptPath_CollisionKeepsCoordinatorAndNamesShadowedPrompt pins the
// other half: when BOTH files exist the coordinator prompt still wins — the
// name is the coordinator's, which is the policy half of the split — but the
// crew prompt the collision made unreachable is named in the log instead of
// being dropped in silence.
func TestCrewPromptPath_CollisionKeepsCoordinatorAndNamesShadowedPrompt(t *testing.T) {
	promptPathSandbox(t)
	setCoordinator(t, "doctor")
	mayorFile := writePrompt(t, PromptDir(), "mayor", "# coordinator\n")
	doctorFile := writePrompt(t, CrewPromptDir(), "doctor", "# doctor\n")

	logs := captureLog(t)
	got, err := crewPromptPath("doctor")
	if err != nil {
		t.Fatalf("crewPromptPath(doctor): %v", err)
	}
	if got != mayorFile {
		t.Errorf("resolved to %q, want the coordinator prompt %q — the split must not invert", got, mayorFile)
	}
	out := logs()
	for _, want := range []string{doctorFile, mayorFile, "unreachable"} {
		if !strings.Contains(out, want) {
			t.Errorf("collision log missing %q; got: %s", want, out)
		}
	}
}

// TestCrewPromptPath_NotFoundNamesEveryPathSearched pins the diagnostic. With
// a collision-shaped coordinator name and neither file present, the error must
// name the path the operator actually configured (crew/<name>.md) alongside
// the mechanism file — naming only agents/mayor.md is the misleading message
// mg-4469 was filed for.
func TestCrewPromptPath_NotFoundNamesEveryPathSearched(t *testing.T) {
	promptPathSandbox(t)
	setCoordinator(t, "doctor")

	_, err := crewPromptPath("doctor")
	if err == nil {
		t.Fatal("crewPromptPath(doctor) with no prompts on disk: want error, got nil")
	}
	if !errors.Is(err, ErrPromptNotFound) {
		t.Errorf("errors.Is(err, ErrPromptNotFound) = false; err = %v", err)
	}
	var pnf *PromptNotFoundError
	if !errors.As(err, &pnf) {
		t.Fatalf("errors.As(*PromptNotFoundError) = false; err = %v", err)
	}
	coordFile := filepath.Join(PromptDir(), "mayor.md")
	crewFile := filepath.Join(CrewPromptDir(), "doctor.md")
	if want := []string{coordFile, crewFile}; len(pnf.Searched) != 2 ||
		pnf.Searched[0] != want[0] || pnf.Searched[1] != want[1] {
		t.Errorf("Searched = %v, want %v", pnf.Searched, want)
	}
	if pnf.Path != coordFile {
		t.Errorf("Path = %q, want the primary candidate %q (the 404 body keeps its single-path shape)", pnf.Path, coordFile)
	}
	for _, want := range []string{coordFile, crewFile, "pogo agent prompt install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing %q; got: %v", want, err)
		}
	}
}

// TestCrewPromptPath_CrewMissingStillNamesOnlyItsOwnPath is the control for
// the single-candidate case: a non-coordinator name searches exactly one path
// and its error names exactly that one, unchanged by the fallback.
func TestCrewPromptPath_CrewMissingStillNamesOnlyItsOwnPath(t *testing.T) {
	promptPathSandbox(t)
	setCoordinator(t, DefaultCoordinatorName)
	writePrompt(t, PromptDir(), "mayor", "# coordinator\n")

	_, err := crewPromptPath("ghost")
	var pnf *PromptNotFoundError
	if !errors.As(err, &pnf) {
		t.Fatalf("errors.As(*PromptNotFoundError) = false; err = %v", err)
	}
	crewFile := filepath.Join(CrewPromptDir(), "ghost.md")
	if pnf.Path != crewFile || len(pnf.Searched) != 0 {
		t.Errorf("Path/Searched = %q/%v, want %q/[] — a crew lookup must not offer the coordinator file", pnf.Path, pnf.Searched, crewFile)
	}
	if strings.Contains(err.Error(), "mayor.md") {
		t.Errorf("crew error names the coordinator prompt: %v", err)
	}
}

// TestStartCrewAgent_CoordinatorFallsThroughToCrewPrompt proves the fallback
// end to end rather than only at the resolver: a daemon whose coordinator is
// pinned to a name with a crew prompt and no mayor.md actually starts the
// agent, where it used to fail the start with the misleading path.
func TestStartCrewAgent_CoordinatorFallsThroughToCrewPrompt(t *testing.T) {
	promptPathSandbox(t)
	setCoordinator(t, "doctor")
	writePrompt(t, CrewPromptDir(), "doctor", "+++\nrestart_on_crash = false\n+++\n# doctor\n")

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	reg.SetCommandConfig(catCommandConfig{})

	a, err := reg.StartCrewAgent("doctor")
	if err != nil {
		t.Fatalf("StartCrewAgent(doctor): %v", err)
	}
	if a == nil {
		t.Fatal("StartCrewAgent returned nil agent")
	}
}
