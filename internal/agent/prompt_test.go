package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandTemplate(t *testing.T) {
	// Create a temp template file
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "polecat.md")
	content := `You are a polecat. Your task: {{.Task}}

Work item ID: {{.Id}}
Repository: {{.Repo}}

## Details

{{.Body}}
`
	if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	vars := TemplateVars{
		Task: "Fix the auth bug",
		Body: "The OAuth tokens expire too early.\nSee issue #42.",
		Id:   "gt-a3f",
		Repo: "/home/user/projects/myapp",
	}

	result, err := ExpandTemplate(tmplPath, vars)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "Fix the auth bug") {
		t.Errorf("expected task in output, got: %s", result)
	}
	if !strings.Contains(result, "gt-a3f") {
		t.Errorf("expected id in output, got: %s", result)
	}
	if !strings.Contains(result, "/home/user/projects/myapp") {
		t.Errorf("expected repo in output, got: %s", result)
	}
	if !strings.Contains(result, "OAuth tokens expire too early") {
		t.Errorf("expected body in output, got: %s", result)
	}
}

func TestExpandTemplateEmptyVars(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "minimal.md")
	content := `You are a polecat. Task: {{.Task}}`
	if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ExpandTemplate(tmplPath, TemplateVars{})
	if err != nil {
		t.Fatal(err)
	}

	expected := "You are a polecat. Task: "
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestExpandTemplatePlainMarkdown(t *testing.T) {
	// A prompt file with no template variables should pass through unchanged
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "crew.md")
	content := "You are arch, the co-architect.\n\nYour job is to review designs.\n"
	if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ExpandTemplate(tmplPath, TemplateVars{})
	if err != nil {
		t.Fatal(err)
	}

	if result != content {
		t.Errorf("plain markdown should pass through unchanged\ngot: %q\nwant: %q", result, content)
	}
}

func TestExpandTemplateToFile(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "polecat.md")
	content := `Task: {{.Task}} ({{.Id}})`
	if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	vars := TemplateVars{Task: "Deploy hotfix", Id: "gt-x1"}
	path, err := ExpandTemplateToFile(tmplPath, vars)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	expected := "Task: Deploy hotfix (gt-x1)"
	if string(data) != expected {
		t.Errorf("expected %q in file, got %q", expected, string(data))
	}
}

func TestExpandTemplateNotFound(t *testing.T) {
	_, err := ExpandTemplate("/nonexistent/path.md", TemplateVars{})
	if err == nil {
		t.Error("expected error for missing template")
	}
}

func TestExpandTemplateInvalidSyntax(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "bad.md")
	content := `{{.Undefined | badFunc}}`
	if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ExpandTemplate(tmplPath, TemplateVars{})
	if err == nil {
		t.Error("expected error for invalid template")
	}
}

func TestListPrompts(t *testing.T) {
	// Save and restore HOME
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create the directory structure
	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	crewDir := filepath.Join(agentsDir, "crew")
	tmplDir := filepath.Join(agentsDir, "templates")
	os.MkdirAll(crewDir, 0755)
	os.MkdirAll(tmplDir, 0755)

	// Create some prompt files
	os.WriteFile(filepath.Join(agentsDir, "mayor.md"), []byte("mayor prompt"), 0644)
	os.WriteFile(filepath.Join(crewDir, "arch.md"), []byte("arch prompt"), 0644)
	os.WriteFile(filepath.Join(crewDir, "ops.md"), []byte("ops prompt"), 0644)
	os.WriteFile(filepath.Join(tmplDir, "polecat.md"), []byte("polecat template"), 0644)

	prompts, err := ListPrompts()
	if err != nil {
		t.Fatal(err)
	}

	if len(prompts) != 4 {
		t.Fatalf("expected 4 prompts, got %d: %+v", len(prompts), prompts)
	}

	// Check categories
	categories := map[string]int{}
	for _, p := range prompts {
		categories[p.Category]++
	}
	if categories["mayor"] != 1 {
		t.Errorf("expected 1 mayor prompt, got %d", categories["mayor"])
	}
	if categories["crew"] != 2 {
		t.Errorf("expected 2 crew prompts, got %d", categories["crew"])
	}
	if categories["templates"] != 1 {
		t.Errorf("expected 1 template, got %d", categories["templates"])
	}
}

func TestResolveCrewPrompt(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	crewDir := filepath.Join(tmpHome, ".pogo", "agents", "crew")
	os.MkdirAll(crewDir, 0755)
	os.WriteFile(filepath.Join(crewDir, "arch.md"), []byte("prompt"), 0644)

	path, err := ResolveCrewPrompt("arch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "arch.md") {
		t.Errorf("unexpected path: %s", path)
	}

	_, err = ResolveCrewPrompt("nonexistent")
	if err == nil {
		t.Error("expected error for missing crew prompt")
	}
}

func TestResolveTemplate(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	tmplDir := filepath.Join(tmpHome, ".pogo", "agents", "templates")
	os.MkdirAll(tmplDir, 0755)
	os.WriteFile(filepath.Join(tmplDir, "polecat.md"), []byte("template"), 0644)

	path, err := ResolveTemplate("polecat")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "polecat.md") {
		t.Errorf("unexpected path: %s", path)
	}

	_, err = ResolveTemplate("nonexistent")
	if err == nil {
		t.Error("expected error for missing template")
	}
}

func TestInitPromptDirs(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}

	// Verify directories exist
	crewDir := filepath.Join(tmpHome, ".pogo", "agents", "crew")
	tmplDir := filepath.Join(tmpHome, ".pogo", "agents", "templates")

	if _, err := os.Stat(crewDir); os.IsNotExist(err) {
		t.Error("crew dir not created")
	}
	if _, err := os.Stat(tmplDir); os.IsNotExist(err) {
		t.Error("templates dir not created")
	}
}
