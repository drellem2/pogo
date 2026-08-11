package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
	"github.com/drellem2/pogo/internal/turnlog"
)

// The mg-a270 coverage tests: EVERY crew agent's rendered prompt carries the
// turn-completion clause, whatever route its prompt took to get here.
//
// The three routes are not interchangeable, and that is the reason this file
// exists rather than a paragraph appended to the shipped corpus:
//
//	embed          crew/doctor.md — installed, stamped, staleness-compared
//	extends        the PM tier — crew/pm-<x>.md redirects to pm-template.md
//	user-authored  crew/architect.md, crew/pa.md — written by the operator,
//	               present in no embed, and explicitly out of bounds for the
//	               installer (see docs/design/prompt-customization-design.md)
//
// The third route is where the gap was. mayor, pa and architect wrote no
// artifact only a completed turn could produce, so no liveness instrument could
// examine them, and the 22-hour outage of 2026-08-10/11 was diagnosed from the
// two agents that happened to be on the second route. A fix that edited shipped
// prompts would have covered routes one and two and missed the agents the
// ticket named.

// mustSynth renders a crew prompt through the SPAWN path and returns its text.
func mustSynth(t *testing.T, promptPath string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "synthesized-prompt.md")
	got, err := SynthesizeExtendsPrompt(promptPath, out)
	if err != nil {
		t.Fatalf("SynthesizeExtendsPrompt(%s): %v", promptPath, err)
	}
	if got == "" {
		t.Fatalf("%s: no synthesized prompt — the agent would spawn against the stub, "+
			"and a stub carries no turn-completion clause", promptPath)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertHasClause(t *testing.T, label, body string) {
	t.Helper()
	if n := strings.Count(body, turnlog.ClauseMarker); n != 1 {
		t.Errorf("%s: %d turn-completion clauses, want exactly 1 — this agent would be "+
			"unverifiable by any liveness check", label, n)
	}
	if !strings.Contains(body, "pogo turn-done") {
		t.Errorf("%s: clause does not name the command that writes the artifact", label)
	}
	if !strings.Contains(body, "turnlog") {
		t.Errorf("%s: clause does not name the path the artifact lands at", label)
	}
}

// TestTurnLogClauseReachesEveryCrewRoute covers all three prompt routes through
// the real spawn-time renderer.
func TestTurnLogClauseReachesEveryCrewRoute(t *testing.T) {
	testsandbox.Isolate(t)
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatal(err)
	}

	// Route 1 — the coordinator and a shipped crew prompt, both from the embed.
	assertHasClause(t, "mayor.md", mustSynth(t, filepath.Join(PromptDir(), "mayor.md")))
	assertHasClause(t, "crew/doctor.md", mustSynth(t, filepath.Join(CrewPromptDir(), "doctor.md")))

	// Route 2 — the PM tier, via `extends ... with config ...`.
	if err := os.MkdirAll(filepath.Join(PromptDir(), "pm"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(PromptDir(), "pm", "pogo.toml"),
		[]byte("[scope]\nname = \"pogo\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pmStub := filepath.Join(CrewPromptDir(), "pm-pogo.md")
	if err := os.WriteFile(pmStub,
		[]byte("+++\nauto_start = true\n+++\n\nextends pm-template with config pm/pogo.toml\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pm := mustSynth(t, pmStub)
	assertHasClause(t, "crew/pm-pogo.md (extends)", pm)
	// The PM tier is NOT special-cased: it gets the same clause through the
	// same injection point as everyone else, alongside the sweep.log heartbeat
	// it already had. That heartbeat exists for the coordinator's stall-watch
	// and was never designed as a liveness primitive.
	if !strings.Contains(pm, "sweep.log") {
		t.Errorf("pm render lost its existing sweep.log heartbeat")
	}

	// Route 3 — the shape the ticket was about: an operator-authored crew
	// prompt, in no embed, that the installer never writes.
	userAuthored := filepath.Join(CrewPromptDir(), "architect.md")
	if err := os.WriteFile(userAuthored,
		[]byte("+++\nauto_start = true\n+++\n\n# Architect\n\nYou review designs.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	body := mustSynth(t, userAuthored)
	assertHasClause(t, "crew/architect.md (user-authored)", body)
	if !strings.Contains(body, "You review designs.") {
		t.Errorf("the operator's own prompt body was lost:\n%s", body)
	}
}

// TestTurnLogClauseSurvivesPreviewRender: `pogo agent prompt show` must render
// what the agent actually receives. A preview that omitted the clause would
// tell an operator auditing their fleet that an instrumented agent is not.
func TestTurnLogClauseSurvivesPreviewRender(t *testing.T) {
	testsandbox.Isolate(t)
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{DefaultCoordinatorName, "doctor"} {
		out, err := SynthesizePrompt(name, PreviewTemplateVars())
		if err != nil {
			t.Fatalf("SynthesizePrompt(%s): %v", name, err)
		}
		assertHasClause(t, "prompt show "+name, out)
	}
}

// TestTurnLogClauseIsNotDuplicated. The plausible way to get a second copy is
// an operator copying a synthesized prompt back into crew/ — the file is right
// there in the agent's working directory — and a prompt that says the same
// thing twice invites the reader to treat both as boilerplate.
func TestTurnLogClauseIsNotDuplicated(t *testing.T) {
	testsandbox.Isolate(t)
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(CrewPromptDir(), "recycled.md")
	if err := os.WriteFile(p, []byte("# Recycled\n"+turnlog.PromptClause), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "synth.md")
	got, err := SynthesizeExtendsPrompt(p, out)
	if err != nil {
		t.Fatal(err)
	}
	body := "# Recycled\n" + turnlog.PromptClause
	if got != "" {
		data, err := os.ReadFile(got)
		if err != nil {
			t.Fatal(err)
		}
		body = string(data)
	}
	if n := strings.Count(body, turnlog.ClauseMarker); n != 1 {
		t.Errorf("clause appears %d times after re-render, want 1", n)
	}
}

// TestTurnLogClauseIsScopedToCrew. Polecat templates deliberately do not carry
// it: a polecat's completed work is already evidenced by its claim re-stamp,
// its pushed branch and its merge, and it is stopped minutes after finishing.
// Instrumenting them would make `pogo check-turns --all-types` permanently red
// for reasons that mean nothing, which is how a detector becomes ignorable.
func TestTurnLogClauseIsScopedToCrew(t *testing.T) {
	testsandbox.Isolate(t)
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatal(err)
	}
	out, err := SynthesizePrompt("polecat", PreviewTemplateVars())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, turnlog.ClauseMarker) {
		t.Errorf("polecat template carries the crew turn-completion clause")
	}
}
