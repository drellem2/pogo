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

func TestExpandTemplateBranch(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "polecat.md")
	content := `pogo refinery submit polecat-{{.Id}} --target={{if .Branch}}{{.Branch}}{{else}}main{{end}}`
	if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// With branch specified
	vars := TemplateVars{Id: "gt-a3f", Branch: "feature/foo"}
	result, err := ExpandTemplate(tmplPath, vars)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "--target=feature/foo") {
		t.Errorf("expected --target=feature/foo, got: %s", result)
	}

	// Without branch — should default to main
	vars2 := TemplateVars{Id: "gt-a3f"}
	result2, err := ExpandTemplate(tmplPath, vars2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result2, "--target=main") {
		t.Errorf("expected --target=main, got: %s", result2)
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

func TestContentHash(t *testing.T) {
	data := []byte("hello world")
	h := contentHash(data)
	if len(h) != 64 { // SHA-256 hex = 64 chars
		t.Errorf("expected 64 char hash, got %d: %s", len(h), h)
	}
	// Same input should produce same hash
	if contentHash(data) != h {
		t.Error("hash not deterministic")
	}
	// Different input should produce different hash
	if contentHash([]byte("different")) == h {
		t.Error("different content produced same hash")
	}
}

func TestStampedContent(t *testing.T) {
	data := []byte("# My Prompt\nDo stuff.\n")
	stamped := stampedContent(data)

	s := string(stamped)
	if !strings.HasPrefix(s, "<!-- pogo-prompt-hash: ") {
		t.Errorf("stamped content should start with hash comment, got: %s", s[:60])
	}
	if !strings.Contains(s, "# My Prompt\nDo stuff.\n") {
		t.Error("stamped content should contain original content")
	}
}

func TestInstalledPromptHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")

	// File with valid hash stamp
	data := []byte("original content")
	os.WriteFile(path, stampedContent(data), 0644)

	h := installedPromptHash(path)
	if h != contentHash(data) {
		t.Errorf("expected hash %s, got %s", contentHash(data), h)
	}

	// File without hash stamp
	os.WriteFile(path, []byte("# No hash here\n"), 0644)
	if installedPromptHash(path) != "" {
		t.Error("expected empty hash for unstamped file")
	}

	// Nonexistent file
	if installedPromptHash(filepath.Join(dir, "nope.md")) != "" {
		t.Error("expected empty hash for missing file")
	}
}

func TestInstallPromptsUpdatesStaleFiles(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// First install — should install files
	result, err := InstallPrompts(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) == 0 {
		t.Fatal("expected files to be installed on first run")
	}
	if len(result.Updated) != 0 {
		t.Errorf("expected no updates on first run, got %v", result.Updated)
	}

	// Second install — same binary, should skip all
	result2, err := InstallPrompts(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result2.Installed) != 0 {
		t.Errorf("expected no new installs, got %v", result2.Installed)
	}
	if len(result2.Updated) != 0 {
		t.Errorf("expected no updates, got %v", result2.Updated)
	}
	if len(result2.Skipped) != len(result.Installed) {
		t.Errorf("expected %d skipped, got %d", len(result.Installed), len(result2.Skipped))
	}

	// Simulate stale file by writing old content to one of the installed files
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	os.WriteFile(mayorPath, []byte("<!-- pogo-prompt-hash: oldhash -->\n# Old mayor prompt\n"), 0644)

	result3, err := InstallPrompts(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result3.Updated) == 0 {
		t.Error("expected stale file to be updated")
	}
	found := false
	for _, f := range result3.Updated {
		if f == "mayor.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mayor.md in updated list, got %v", result3.Updated)
	}
}

func TestInstallPromptsUpdatesUnstampedFiles(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create pre-existing files without hash stamps (simulates old install)
	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	crewDir := filepath.Join(agentsDir, "crew")
	tmplDir := filepath.Join(agentsDir, "templates")
	os.MkdirAll(crewDir, 0755)
	os.MkdirAll(tmplDir, 0755)
	os.WriteFile(filepath.Join(agentsDir, "mayor.md"), []byte("# Old mayor\n"), 0644)
	os.WriteFile(filepath.Join(crewDir, "doctor.md"), []byte("# Old doctor\n"), 0644)
	os.WriteFile(filepath.Join(tmplDir, "polecat.md"), []byte("# Old polecat\n"), 0644)
	os.WriteFile(filepath.Join(tmplDir, "polecat-qa.md"), []byte("# Old polecat-qa\n"), 0644)

	result, err := InstallPrompts(false)
	if err != nil {
		t.Fatal(err)
	}
	// Files without hash stamps should be treated as stale and updated
	if len(result.Updated) == 0 {
		t.Error("expected unstamped files to be updated")
	}
	if len(result.Installed) != 0 {
		t.Errorf("expected no new installs (files existed), got %v", result.Installed)
	}
}

func TestInstallPromptsCrewWithExistingTemplatesDir(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Simulate user who already has templates/ dir but no crew/ dir
	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	tmplDir := filepath.Join(agentsDir, "templates")
	os.MkdirAll(tmplDir, 0755)
	os.WriteFile(filepath.Join(tmplDir, "custom.md"), []byte("# Custom template\n"), 0644)

	result, err := InstallPrompts(false)
	if err != nil {
		t.Fatal(err)
	}

	// Should install crew/doctor.md even though templates/ existed
	doctorInstalled := false
	for _, f := range result.Installed {
		if f == filepath.Join("crew", "doctor.md") {
			doctorInstalled = true
		}
	}
	if !doctorInstalled {
		t.Errorf("expected crew/doctor.md to be installed, installed=%v skipped=%v", result.Installed, result.Skipped)
	}

	// Verify file exists on disk
	doctorPath := filepath.Join(agentsDir, "crew", "doctor.md")
	if _, err := os.Stat(doctorPath); os.IsNotExist(err) {
		t.Error("crew/doctor.md not found on disk after install")
	}
}

func TestParsePromptFrontmatterWellFormed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor.md")
	content := `+++
restart_on_crash = true
auto_start = true
nudge_on_start = "Begin your coordination loop."
command = "claude --dangerously-skip-permissions"
worktree = false
+++
# Mayor

You are the mayor.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.RestartOnCrash {
		t.Error("expected RestartOnCrash=true")
	}
	if !meta.AutoStart {
		t.Error("expected AutoStart=true")
	}
	if meta.Worktree {
		t.Error("expected Worktree=false")
	}
	if meta.NudgeOnStart != "Begin your coordination loop." {
		t.Errorf("NudgeOnStart=%q", meta.NudgeOnStart)
	}
	if meta.Command != "claude --dangerously-skip-permissions" {
		t.Errorf("Command=%q", meta.Command)
	}
	wantBody := "# Mayor\n\nYou are the mayor.\n"
	if body != wantBody {
		t.Errorf("body=%q want %q", body, wantBody)
	}
}

func TestParsePromptFrontmatterNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.md")
	content := "# Plain Prompt\n\nNo frontmatter here.\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil {
		t.Fatal("meta should be non-nil zero value, not nil")
	}
	if *meta != (AgentMeta{}) {
		t.Errorf("expected zero-value meta, got %+v", *meta)
	}
	if body != content {
		t.Errorf("body should equal full file content\ngot:  %q\nwant: %q", body, content)
	}
}

func TestParsePromptFrontmatterEmptyBody(t *testing.T) {
	// Frontmatter present but no body after the closing fence.
	cases := map[string]string{
		"trailing newline":    "+++\nauto_start = true\n+++\n",
		"no trailing newline": "+++\nauto_start = true\n+++",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "x.md")
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			meta, body, err := ParsePromptFrontmatter(path)
			if err != nil {
				t.Fatal(err)
			}
			if !meta.AutoStart {
				t.Error("expected AutoStart=true")
			}
			if body != "" {
				t.Errorf("expected empty body, got %q", body)
			}
		})
	}
}

func TestParsePromptFrontmatterMalformed(t *testing.T) {
	cases := map[string]string{
		"missing closing fence":    "+++\nauto_start = true\n# no fence below\n",
		"unterminated opening":     "+++",
		"junk after opening fence": "+++ stuff\nauto_start = true\n+++\n",
		"line missing equals":      "+++\nauto_start true\n+++\n",
		"empty key":                "+++\n = true\n+++\n",
		"bad bool":                 "+++\nauto_start = yes\n+++\n",
		"unquoted string":          "+++\nnudge_on_start = hi\n+++\n",
		"single-quoted string":     "+++\nnudge_on_start = 'hi'\n+++\n",
		"unterminated escape":      "+++\nnudge_on_start = \"hi\\\"\n+++\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "bad.md")
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			_, _, err := ParsePromptFrontmatter(path)
			if err == nil {
				t.Errorf("expected error for malformed frontmatter, got nil")
			}
		})
	}
}

func TestParsePromptFrontmatterUnknownFieldIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.md")
	content := `+++
auto_start = true
future_field = "ignored"
# this is a comment

restart_on_crash = true
+++
body
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatalf("unknown fields and comments should be tolerated: %v", err)
	}
	if !meta.AutoStart || !meta.RestartOnCrash {
		t.Errorf("known fields not parsed: %+v", meta)
	}
	if body != "body\n" {
		t.Errorf("body=%q", body)
	}
}

func TestParsePromptFrontmatterEscapes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.md")
	content := "+++\nnudge_on_start = \"line1\\nline2\\t\\\"quoted\\\"\"\n+++\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	meta, _, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "line1\nline2\t\"quoted\""
	if meta.NudgeOnStart != want {
		t.Errorf("NudgeOnStart=%q want %q", meta.NudgeOnStart, want)
	}
}

func TestParsePromptFrontmatterFileNotFound(t *testing.T) {
	_, _, err := ParsePromptFrontmatter("/nonexistent/prompt.md")
	if err == nil {
		t.Error("expected error for missing file")
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
