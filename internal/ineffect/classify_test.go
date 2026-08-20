package ineffect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/staleness"
)

func TestClassifyPaths(t *testing.T) {
	cases := []struct {
		path string
		want Class
		why  string
	}{
		{"internal/agent/api.go", ClassCompiled, "shipped Go reaches a runtime only through a binary"},
		{"cmd/pogod/main.go", ClassCompiled, ""},
		{"pkg/plugin/plugin.go", ClassCompiled, ""},
		{"go.mod", ClassCompiled, "a module change alters what a rebuild produces"},
		{"go.sum", ClassCompiled, ""},

		{"internal/agent/prompts/mayor.md", ClassPrompt, "the corpus is .md but it is not documentation"},
		{"internal/agent/prompts/templates/polecat.md", ClassPrompt, ""},

		{"scripts/launchd/pogo-deploy.sh", ClassAsset, ""},
		{"scripts/launchd/com.pogo.deploy.plist", ClassAsset, ""},
		{"scripts/pogo-self-deploy", ClassAsset, "extensionless, but under scripts/ and executed"},
		{"test.sh", ClassAsset, "the gate runs it out of a checkout"},
		{"hooks/pre-commit", ClassAsset, ""},
		{".github/workflows/ci.yml", ClassAsset, ""},

		{"internal/agent/api_test.go", ClassNoCarrier, "a _test.go file is compiled into no shipped binary"},
		{"scripts/pogo-deploy_test.sh", ClassNoCarrier, ""},
		{"internal/refinery/testdata/x.json", ClassNoCarrier, ""},
		{"_testdata/repo/file.go", ClassNoCarrier, "fixture trees are not shipped Go"},
		{"ARCHITECTURE.md", ClassNoCarrier, ""},
		{"changelog.d/mg-3d0e.added.md", ClassNoCarrier, ""},
		{"docs/operations.md", ClassNoCarrier, ""},
		{"LICENSE-APACHE", ClassNoCarrier, ""},

		{"Dockerfile", ClassUnclassified, "no rule matches, and an unexamined path must not render as documentation"},
		{".goreleaser.yml", ClassUnclassified, ""},
	}
	for _, c := range cases {
		if got := Classify(c.path); got != c.want {
			t.Errorf("Classify(%q) = %q, want %q%s", c.path, got, c.want, note(c.why))
		}
	}
}

func note(why string) string {
	if why == "" {
		return ""
	}
	return " — " + why
}

// TestTestFilesNeverClassifyAsCompiled is the rule that would be easiest to
// lose by reordering the switch, and losing it is not visible in the output: a
// _test.go file classified as `compiled` produces a carrier row that says a
// binary carries the change, about a file no binary contains.
func TestTestFilesNeverClassifyAsCompiled(t *testing.T) {
	for _, p := range []string{
		"internal/ineffect/assess_test.go",
		"cmd/pogo/main_test.go",
		"internal/agent/prompts/x_test.go",
	} {
		if got := Classify(p); got == ClassCompiled || got == ClassPrompt {
			t.Errorf("Classify(%q) = %q; test material has no runtime carrier and must never be given one", p, got)
		}
	}
}

func TestPkgDir(t *testing.T) {
	cases := map[string]string{
		"internal/agent/api.go": "internal/agent",
		"cmd/pogo/main.go":      "cmd/pogo",
		"go.mod":                "",
		"main.go":               "",
		"scripts/x.sh":          "",
	}
	for in, want := range cases {
		if got := PkgDir(in); got != want {
			t.Errorf("PkgDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstalledPromptPath(t *testing.T) {
	if got := InstalledPromptPath("/root/agents", "internal/agent/prompts/templates/polecat.md"); got != "/root/agents/templates/polecat.md" {
		t.Errorf("InstalledPromptPath = %q; the installed layout mirrors the corpus layout, which is what makes the two comparable path-by-path", got)
	}
	if got := InstalledPromptPath("/root/agents", "internal/agent/api.go"); got != "" {
		t.Errorf("InstalledPromptPath(non-corpus) = %q, want \"\"", got)
	}
}

// TestPromptsSubtreeAgreesWithStaleness pins the one constant this package
// duplicates. internal/staleness owns the same fact for a different comparison;
// if the corpus moves and only one of them is updated, this package would
// silently classify prompts as documentation — a green row on the artifact
// class with the most carriers.
func TestPromptsSubtreeAgreesWithStaleness(t *testing.T) {
	want := strings.TrimSuffix(staleness.PromptsSubtree, "/") + "/"
	if PromptsSubtree != want {
		t.Errorf("ineffect.PromptsSubtree = %q, staleness.PromptsSubtree = %q — the corpus has moved and only one copy of the path was updated", PromptsSubtree, staleness.PromptsSubtree)
	}
}

// TestPromptEmbedPkgHasTheDirective checks the build fact PromptEmbedPkg
// asserts. Without this the constant is exactly the kind of claim this package
// refuses to make elsewhere: a hardcoded statement about which package compiles
// the corpus in, rotting silently the day the embed moves — and the symptom
// would be prompt findings reporting the wrong binaries as carriers.
func TestPromptEmbedPkgHasTheDirective(t *testing.T) {
	dir := filepath.Join("..", "..", PromptEmbedPkg)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("PromptEmbedPkg = %q does not resolve to a package directory: %v", PromptEmbedPkg, err)
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "//go:embed prompts") {
			return
		}
	}
	t.Errorf("no file in %s carries `//go:embed prompts`; PromptEmbedPkg names the package that compiles the corpus into a binary, and it no longer does — prompt findings are now reporting the wrong carriers", PromptEmbedPkg)
}
