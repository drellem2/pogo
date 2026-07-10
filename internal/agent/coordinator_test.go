package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setCoordinator sets the process-wide coordinator name for the duration of a
// test and restores the previous value on cleanup.
func setCoordinator(t *testing.T, name string) {
	t.Helper()
	prev := CoordinatorName()
	SetCoordinatorName(name)
	t.Cleanup(func() { SetCoordinatorName(prev) })
}

func TestSetCoordinatorName(t *testing.T) {
	if got := CoordinatorName(); got != DefaultCoordinatorName {
		t.Fatalf("default CoordinatorName() = %q, want %q", got, DefaultCoordinatorName)
	}
	setCoordinator(t, "boss")
	if got := CoordinatorName(); got != "boss" {
		t.Errorf("CoordinatorName() = %q, want boss", got)
	}
	// Empty resets to the default rather than leaving an unusable name.
	SetCoordinatorName("")
	if got := CoordinatorName(); got != DefaultCoordinatorName {
		t.Errorf("CoordinatorName() after empty set = %q, want %q", got, DefaultCoordinatorName)
	}
}

func TestSubstituteCoordinator(t *testing.T) {
	setCoordinator(t, "boss")
	in := "mail {{.Coordinator}}; see {{.CoordinatorTitle}}'s stall-watch"
	want := "mail boss; see Boss's stall-watch"
	if got := substituteRoleNames(in); got != want {
		t.Errorf("substituteRoleNames = %q, want %q", got, want)
	}
}

func TestTitleFirst(t *testing.T) {
	cases := map[string]string{"": "", "boss": "Boss", "Mayor": "Mayor", "x": "X"}
	for in, want := range cases {
		if got := titleFirst(in); got != want {
			t.Errorf("titleFirst(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSynthesizeExtendsPromptNoPlaceholderStillBailsOut pins the
// no-customization fast path: a crew prompt with no extends directive, no
// drop-ins, and no coordinator placeholder must not produce a synthesized
// file.
func TestSynthesizeExtendsPromptNoPlaceholderStillBailsOut(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	crewPath := filepath.Join(CrewPromptDir(), "plain.md")
	if err := os.WriteFile(crewPath, []byte("# Plain\nNo placeholder here.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := SynthesizeExtendsPrompt(crewPath, filepath.Join(t.TempDir(), "synth.md"))
	if err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	if got != "" {
		t.Errorf("expected bail-out (\"\"), got %q", got)
	}
}

// TestSynthesizeExtendsPromptSubstitutesCoordinator verifies the substitution
// point recommended by the mg-7488 design: a static crew prompt carrying the
// {{.Coordinator}} placeholder is synthesized with the configured name, even
// without an extends directive or drop-ins.
func TestSynthesizeExtendsPromptSubstitutesCoordinator(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	setCoordinator(t, "boss")
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	crewPath := filepath.Join(CrewPromptDir(), "helper.md")
	body := "+++\nnudge_on_start = \"ping {{.Coordinator}}\"\n+++\n# Helper\nMail {{.Coordinator}} when stuck. {{.CoordinatorTitle}} owns dispatch.\n"
	if err := os.WriteFile(crewPath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "synth.md")
	got, err := SynthesizeExtendsPrompt(crewPath, outPath)
	if err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	if got != outPath {
		t.Fatalf("expected synthesized output at %q, got %q", outPath, got)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "Mail boss when stuck. Boss owns dispatch.") {
		t.Errorf("synthesized prompt missing substituted coordinator:\n%s", s)
	}
	// Frontmatter is preserved and substituted too, so nudge_on_start
	// resolved from the synthesized file addresses the configured name.
	if !strings.Contains(s, "nudge_on_start = \"ping boss\"") {
		t.Errorf("synthesized frontmatter not substituted:\n%s", s)
	}
	if strings.Contains(s, "{{.Coordinator") {
		t.Errorf("leftover coordinator placeholder in synthesized prompt:\n%s", s)
	}
}

// TestSynthesizePromptResolvesConfiguredCoordinator verifies that the prompt
// pipeline resolves the coordinator by its configured name (mapping it to the
// mayor.md file) and renders the shipped prompts without leftover
// placeholders under both the default and a renamed coordinator.
func TestSynthesizePromptResolvesConfiguredCoordinator(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatal(err)
	}

	// Default coordinator: resolved under the default display name "ringmaster"
	// (which maps to the frozen mayor.md file) and rendered with it.
	out, err := SynthesizePrompt("ringmaster", PreviewTemplateVars())
	if err != nil {
		t.Fatalf("SynthesizePrompt(ringmaster): %v", err)
	}
	if !strings.Contains(out, "mg mail list ringmaster") {
		t.Errorf("default coordinator prompt missing ringmaster mail command:\n%.400s", out)
	}
	if strings.Contains(out, "{{.Coordinator") {
		t.Errorf("leftover placeholder in default coordinator prompt")
	}

	// Renamed coordinator: resolved under the configured name, rendered with it.
	setCoordinator(t, "boss")
	if _, err := SynthesizePrompt("mayor", PreviewTemplateVars()); err == nil {
		t.Errorf("SynthesizePrompt(mayor) should not resolve the coordinator prompt when coordinator is renamed and no crew/mayor.md exists")
	}
	for _, name := range []string{"boss", "doctor", "polecat", "polecat-qa", "polecat-build-pr", "polecat-triage", "polecat-review"} {
		out, err := SynthesizePrompt(name, PreviewTemplateVars())
		if err != nil {
			t.Fatalf("SynthesizePrompt(%s): %v", name, err)
		}
		if strings.Contains(out, "{{.Coordinator") {
			t.Errorf("leftover placeholder in %s prompt", name)
		}
		if !strings.Contains(out, "boss") {
			t.Errorf("%s prompt does not mention configured coordinator:\n%.400s", name, out)
		}
	}
}

// TestListPromptsCoordinatorName verifies the top-level mayor.md is listed
// under the configured coordinator name (so autostart and `pogo agent start`
// address the coordinator correctly) while the category label stays "mayor".
func TestListPromptsCoordinatorName(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	setCoordinator(t, "boss")
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(PromptDir(), "mayor.md"), []byte("coordinator prompt"), 0644); err != nil {
		t.Fatal(err)
	}
	prompts, err := ListPrompts()
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d: %+v", len(prompts), prompts)
	}
	if prompts[0].Name != "boss" || prompts[0].Category != "mayor" {
		t.Errorf("got Name=%q Category=%q, want Name=boss Category=mayor", prompts[0].Name, prompts[0].Category)
	}
}

// TestExpandTemplateCoordinatorVar verifies polecat templates resolve
// {{.Coordinator}} natively through text/template, defaulting from the
// process-wide name when the caller leaves TemplateVars.Coordinator empty.
func TestExpandTemplateCoordinatorVar(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "polecat.md")
	if err := os.WriteFile(tmplPath, []byte("Task {{.Id}}: mail {{.Coordinator}} ({{.CoordinatorTitle}})\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := ExpandTemplate(tmplPath, TemplateVars{Id: "mg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task mg-1: mail ringmaster (Ringmaster)\n"; out != want {
		t.Errorf("default expansion = %q, want %q", out, want)
	}

	setCoordinator(t, "boss")
	out, err = ExpandTemplate(tmplPath, TemplateVars{Id: "mg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task mg-1: mail boss (Boss)\n"; out != want {
		t.Errorf("renamed expansion = %q, want %q", out, want)
	}
}

// TestStartCrewAgentCoordinatorRename verifies the spawn path end-to-end with
// a renamed coordinator: the configured name maps to the mayor.md prompt
// file, and the agent receives a synthesized prompt with the name substituted.
func TestStartCrewAgentCoordinatorRename(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	setCoordinator(t, "boss")
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	prompt := "# {{.CoordinatorTitle}}\nYou are the {{.Coordinator}}. Check `mg mail list {{.Coordinator}}`.\n"
	if err := os.WriteFile(filepath.Join(PromptDir(), "mayor.md"), []byte(prompt), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := startAgentViaAPI(t, reg, "boss")
	data, err := os.ReadFile(a.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "You are the boss. Check `mg mail list boss`.") {
		t.Errorf("spawned coordinator prompt not substituted:\n%s", s)
	}
	if !strings.Contains(s, "# Boss") {
		t.Errorf("spawned coordinator prompt missing title substitution:\n%s", s)
	}
}
