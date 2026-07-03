package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
)

// TestPromptDirsHonorPogoHome locks the mg-3dc3 contract: prompt discovery
// (and therefore the auto-start set) follows $POGO_HOME, so an isolated
// daemon can never enumerate — and duplicate-spawn — the real user's crew.
func TestPromptDirsHonorPogoHome(t *testing.T) {
	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)

	if got, want := PromptDir(), filepath.Join(pogoHome, "agents"); got != want {
		t.Errorf("PromptDir() = %q, want %q", got, want)
	}
	if got, want := CrewPromptDir(), filepath.Join(pogoHome, "agents", "crew"); got != want {
		t.Errorf("CrewPromptDir() = %q, want %q", got, want)
	}
	if got, want := TemplateDir(), filepath.Join(pogoHome, "agents", "templates"); got != want {
		t.Errorf("TemplateDir() = %q, want %q", got, want)
	}
	polecats, err := gitgc.DefaultPolecatsDir()
	if err != nil {
		t.Fatalf("DefaultPolecatsDir: %v", err)
	}
	if want := filepath.Join(pogoHome, "polecats"); polecats != want {
		t.Errorf("DefaultPolecatsDir() = %q, want %q", polecats, want)
	}
}

// TestListPromptsScopedToPogoHome verifies prompt discovery reads only the
// override dir: a crew prompt planted in $POGO_HOME/agents is found, and
// nothing outside it is.
func TestListPromptsScopedToPogoHome(t *testing.T) {
	pogoHome := t.TempDir()
	t.Setenv("POGO_HOME", pogoHome)

	crewDir := filepath.Join(pogoHome, "agents", "crew")
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewDir, "isolated-crew.md"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	prompts, err := ListPrompts()
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	var names []string
	for _, p := range prompts {
		names = append(names, p.Name)
		if !filepath.IsAbs(p.Path) || !strings.HasPrefix(p.Path, pogoHome) {
			t.Errorf("prompt %q resolved outside POGO_HOME: %s", p.Name, p.Path)
		}
	}
	if len(prompts) != 1 || prompts[0].Name != "isolated-crew" {
		t.Errorf("ListPrompts() = %v, want exactly [isolated-crew]", names)
	}
}
