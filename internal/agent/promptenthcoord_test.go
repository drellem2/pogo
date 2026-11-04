package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// GH #284 (mg-d8dc9): the shipped coordinator prompt teaches a fresh install
// about enth-backed projects, and what it says agrees with the worker
// template's enth branch.

// enthCoordinatorSection is the heading the coordinator prompt's enth section
// carries; the tests below cut the section out by it.
const enthCoordinatorSection = "## Enth-backed projects"

// coordinatorEnthSection renders the coordinator prompt from a fresh,
// sandboxed POGO_HOME holding only the shipped prompts, and returns the enth
// section.
func coordinatorEnthSection(t *testing.T) (full, section string) {
	t.Helper()
	testsandbox.Isolate(t)
	home := PromptDir()
	if _, err := os.Stat(filepath.Join(home, "mayor.md")); err == nil {
		t.Fatalf("control: %s already holds a mayor.md before install, so the POGO_HOME is not fresh", home)
	}
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatal(err)
	}
	out, err := SynthesizePrompt(DefaultCoordinatorName, PreviewTemplateVars())
	if err != nil {
		t.Fatalf("SynthesizePrompt(%s): %v", DefaultCoordinatorName, err)
	}
	i := strings.Index(out, enthCoordinatorSection)
	if i < 0 {
		t.Fatalf("the rendered coordinator prompt from a fresh install has no %q section", enthCoordinatorSection)
	}
	rest := out[i+len(enthCoordinatorSection):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return out, rest
}

func TestFreshInstallCoordinatorPromptHasEnthSection(t *testing.T) {
	full, sec := coordinatorEnthSection(t)
	for _, want := range []string{
		`"vcs": "enth"`,             // how to recognise one from the record
		".enth/checkout",            // ... and on disk
		"enth repo recorded as git", // the doctor row for the auto-registration trap
		"pogo project set-vcs",
		"No git worktree and no branch",
		"self_land",
		"pogo land",
		"`pogo refinery submit` on an enth repo is refused",
		"polecat-build-pr",
		"unlanded Changes",
		"enth repo create OWNER/x",
		"pogo project add ~/dev/x --vcs=enth --enth-remote=URL --enth-repo=OWNER/x",
		"docs/enth.md",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("enth section missing %q", want)
		}
	}
	if strings.Contains(sec, "{{") {
		t.Errorf("enth section carries an unrendered template action")
	}
	// The refinery section says it lands git only, so a reader who starts
	// there is pointed back.
	if !strings.Contains(full, "It lands **git-backed** projects only") {
		t.Errorf("the refinery section does not say it lands git-backed projects only")
	}
}

// The coordinator section and the worker template must not disagree: every
// worker-side fact the section states is checked against the template's enth
// render, and the git render is the control that the fact is enth-specific.
func TestCoordinatorEnthSectionAgreesWithWorkerTemplate(t *testing.T) {
	_, sec := coordinatorEnthSection(t)
	tmpls := embeddedPolecatTemplates(t)
	v := TemplateVars{Task: "T", Body: "B", Id: "mg-1", Repo: "/r", WorktreeDir: "/w",
		Coordinator: "mayor", Worker: "polecat", Provider: "claude",
		EnthRemote: "https://e", EnthRepo: "o/r"}
	git := render(t, "polecat.md", tmpls["polecat.md"], v)
	v.VCS = TemplateVCSEnth
	enth := render(t, "polecat.md", tmpls["polecat.md"], v)

	for _, fact := range []struct {
		inSection, inEnthTemplate string
	}{
		{"self_land", "self_land()"},                         // the worker lands with self_land
		{"`enth land`", "enth land "},                        // ... via enth land
		{"`pogo land`", "pogo land --item="},                 // ... recorded by pogo land
		{"`Pogo-Item` trailers", "--trailer Pogo-Item=mg-1"}, // work is named by trailers
		{"`git` commands fail there", "`git`\ncommands do not work in this directory"},
		{"up to three times", "At most three re-makes"}, // conflicts are re-made by the worker
		{"`self_land` exiting 3", "(exit 3)"},
	} {
		if !strings.Contains(sec, fact.inSection) {
			t.Errorf("coordinator section no longer says %q", fact.inSection)
		}
		if !strings.Contains(enth, fact.inEnthTemplate) {
			t.Errorf("worker template's enth render no longer carries %q, which the coordinator section relies on (%q)", fact.inEnthTemplate, fact.inSection)
		}
		if strings.Contains(git, fact.inEnthTemplate) {
			t.Errorf("control: the git render also carries %q, so it does not pin the enth branch", fact.inEnthTemplate)
		}
	}
	// The worker never submits to the refinery on enth, and the coordinator
	// section says the refinery refuses it: the two must not disagree.
	if strings.Contains(enth, "pogo refinery submit \"$BRANCH\"") {
		t.Errorf("the worker template's enth render submits to the refinery, contradicting the coordinator section")
	}
	// The PR-flow template refuses on enth, as the section says.
	bv := v
	bpr := render(t, "polecat-build-pr.md", tmpls["polecat-build-pr.md"], bv)
	if !strings.Contains(bpr, "enth repos have no pull-request track") {
		t.Errorf("polecat-build-pr's enth render no longer refuses, but the coordinator section says the PR track is refused")
	}
}
