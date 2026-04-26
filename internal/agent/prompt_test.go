package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	stamped := stampedContent("crew/foo.md", data)

	s := string(stamped)
	if !strings.HasPrefix(s, "<!-- pogo-prompt-hash: ") {
		t.Errorf("stamped content should start with HTML hash comment for .md, got: %s", s[:60])
	}
	if !strings.Contains(s, "# My Prompt\nDo stuff.\n") {
		t.Error("stamped content should contain original content")
	}
}

func TestStampedContentTOML(t *testing.T) {
	// TOML files must use a TOML-style comment so the stamp doesn't break parsing.
	data := []byte("name = \"pm-foo\"\nrepos = [\"foo\"]\n")
	stamped := stampedContent("pm/foo.toml", data)

	s := string(stamped)
	if !strings.HasPrefix(s, "# pogo-prompt-hash: ") {
		t.Errorf("stamped content for .toml should start with TOML hash comment, got: %s", s[:60])
	}
	if strings.HasPrefix(s, "<!--") {
		t.Error("stamped .toml file must not start with HTML comment — would break TOML parsing")
	}
	if !strings.Contains(s, "name = \"pm-foo\"") {
		t.Error("stamped content should contain original content")
	}
}

func TestInstalledPromptHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")

	// File with valid hash stamp
	data := []byte("original content")
	os.WriteFile(path, stampedContent("test.md", data), 0644)

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

func TestInstalledPromptHashTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.toml")

	data := []byte("name = \"pm-foo\"\n")
	os.WriteFile(path, stampedContent("test.toml", data), 0644)

	h := installedPromptHash(path)
	if h != contentHash(data) {
		t.Errorf("expected hash %s, got %s", contentHash(data), h)
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

	// Create pre-existing files without hash stamps (simulates old install).
	// Pre-create one file from each shipped subdirectory so the "stale →
	// updated" path is exercised; new shipped files (e.g. pm/) will appear
	// as fresh installs and that's fine — the assertion below targets the
	// stale path specifically.
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
	// Files without hash stamps should be treated as stale and updated.
	if len(result.Updated) == 0 {
		t.Error("expected unstamped files to be updated")
	}
	for _, rel := range []string{"mayor.md", "crew/doctor.md", "templates/polecat.md", "templates/polecat-qa.md"} {
		found := false
		for _, u := range result.Updated {
			if u == rel {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %s in Updated list, got Updated=%v", rel, result.Updated)
		}
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

// TestParsePromptFrontmatterAfterHashComment verifies that the parser
// recognizes frontmatter on installed prompt files, which carry a leading
// "<!-- pogo-prompt-hash: ... -->" stamp inserted by InstallPrompts.
func TestParsePromptFrontmatterAfterHashComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor.md")
	content := "<!-- pogo-prompt-hash: deadbeef -->\n" +
		"+++\n" +
		"auto_start = true\n" +
		"nudge_on_start = \"go\"\n" +
		"+++\n" +
		"# Mayor\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.AutoStart {
		t.Error("expected AutoStart=true on installed file with hash stamp")
	}
	if meta.NudgeOnStart != "go" {
		t.Errorf("NudgeOnStart=%q want %q", meta.NudgeOnStart, "go")
	}
	if body != "# Mayor\n" {
		t.Errorf("body=%q want %q", body, "# Mayor\n")
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

// TestParsePromptFrontmatterBodyOnly covers a prompt that is pure markdown
// with no frontmatter fences anywhere — the common case for legacy prompts.
// Body must be returned verbatim and meta must be a non-nil zero value.
func TestParsePromptFrontmatterBodyOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.md")
	content := "# Legacy Agent\n\nDo work.\n\n## Section\n\n- bullet\n- bullet\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || *meta != (AgentMeta{}) {
		t.Errorf("expected zero-value meta, got %+v", meta)
	}
	if body != content {
		t.Errorf("body should be returned verbatim\ngot:  %q\nwant: %q", body, content)
	}
}

// TestParsePromptFrontmatterCRLF covers Windows-style line endings throughout
// the file. The parser must accept '\r\n' on the fences and inside the
// frontmatter body, and the returned body should be unchanged from input.
func TestParsePromptFrontmatterCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crlf.md")
	content := "+++\r\nauto_start = true\r\nrestart_on_crash = true\r\nnudge_on_start = \"hello\"\r\n+++\r\n# Body\r\n\r\nLine.\r\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatalf("CRLF input should parse: %v", err)
	}
	if !meta.AutoStart {
		t.Error("expected AutoStart=true")
	}
	if !meta.RestartOnCrash {
		t.Error("expected RestartOnCrash=true")
	}
	if meta.NudgeOnStart != "hello" {
		t.Errorf("NudgeOnStart=%q want %q", meta.NudgeOnStart, "hello")
	}
	wantBody := "# Body\r\n\r\nLine.\r\n"
	if body != wantBody {
		t.Errorf("body=%q want %q", body, wantBody)
	}
}

// TestParsePromptFrontmatterBOM documents how a UTF-8 BOM at the start of a
// file is handled. The parser only recognizes frontmatter that begins at byte
// offset 0 with the '+++' fence, so a BOM-prefixed file is treated as having
// no frontmatter and the full content (including BOM) is returned as body.
func TestParsePromptFrontmatterBOM(t *testing.T) {
	dir := t.TempDir()

	bom := "\xef\xbb\xbf"

	t.Run("BOM before frontmatter is treated as plain body", func(t *testing.T) {
		path := filepath.Join(dir, "bom-fm.md")
		content := bom + "+++\nauto_start = true\n+++\n# Body\n"
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		meta, body, err := ParsePromptFrontmatter(path)
		if err != nil {
			t.Fatalf("BOM-prefixed input should not error: %v", err)
		}
		if *meta != (AgentMeta{}) {
			t.Errorf("expected zero-value meta (BOM hides frontmatter), got %+v", *meta)
		}
		if body != content {
			t.Errorf("body should equal full content including BOM\ngot:  %q\nwant: %q", body, content)
		}
	})

	t.Run("BOM before plain body returns content verbatim", func(t *testing.T) {
		path := filepath.Join(dir, "bom-plain.md")
		content := bom + "# Plain\n\nbody\n"
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		meta, body, err := ParsePromptFrontmatter(path)
		if err != nil {
			t.Fatalf("BOM-prefixed plain markdown should not error: %v", err)
		}
		if *meta != (AgentMeta{}) {
			t.Errorf("expected zero-value meta, got %+v", *meta)
		}
		if body != content {
			t.Errorf("body should be returned verbatim\ngot:  %q\nwant: %q", body, content)
		}
	})
}

// TestParsePromptFrontmatterExtraWhitespace covers tolerated whitespace
// variants: extra spacing around '=', tabs, trailing whitespace on fence
// lines, and blank lines within the frontmatter block.
func TestParsePromptFrontmatterExtraWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.md")
	content := "+++   \n" +
		"\n" +
		"  auto_start   =   true   \n" +
		"\trestart_on_crash\t=\ttrue\t\n" +
		"   nudge_on_start =     \"go\"   \n" +
		"\n" +
		"+++  \n" +
		"body\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatalf("extra whitespace should be tolerated: %v", err)
	}
	if !meta.AutoStart {
		t.Error("expected AutoStart=true")
	}
	if !meta.RestartOnCrash {
		t.Error("expected RestartOnCrash=true")
	}
	if meta.NudgeOnStart != "go" {
		t.Errorf("NudgeOnStart=%q want %q", meta.NudgeOnStart, "go")
	}
	if body != "body\n" {
		t.Errorf("body=%q want %q", body, "body\n")
	}
}

// TestParsePromptFrontmatterEmptyFrontmatter covers a frontmatter block with
// no key=value lines at all — open fence followed immediately by close fence.
// This must produce a zero-value meta and an unmodified body.
func TestParsePromptFrontmatterEmptyFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-fm.md")
	content := "+++\n+++\nbody only\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if *meta != (AgentMeta{}) {
		t.Errorf("expected zero-value meta, got %+v", *meta)
	}
	if body != "body only\n" {
		t.Errorf("body=%q want %q", body, "body only\n")
	}
}

// TestParsePromptFrontmatterFenceInBody verifies the parser closes on the
// FIRST '+++' line after the open fence and preserves any later '+++' lines
// inside the body verbatim — important when prompts demonstrate frontmatter
// syntax in their own content.
func TestParsePromptFrontmatterFenceInBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fence-body.md")
	content := "+++\nauto_start = true\n+++\n# Example\n\n+++\nlooks like frontmatter\n+++\n"
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
	wantBody := "# Example\n\n+++\nlooks like frontmatter\n+++\n"
	if body != wantBody {
		t.Errorf("body should preserve later fences verbatim\ngot:  %q\nwant: %q", body, wantBody)
	}
}

func TestAgentMetaHasField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.md")
	content := "+++\nrestart_on_crash = false\n+++\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	meta, _, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.HasField("restart_on_crash") {
		t.Error("expected HasField(restart_on_crash) = true after explicit set to false")
	}
	if meta.HasField("auto_start") {
		t.Error("expected HasField(auto_start) = false (not declared)")
	}
	if meta.HasField("unknown") {
		t.Error("expected HasField(unknown) = false")
	}

	// Nil receiver tolerated.
	var nilMeta *AgentMeta
	if nilMeta.HasField("restart_on_crash") {
		t.Error("nil meta should report no fields set")
	}

	// File without frontmatter: nothing set.
	noFm := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(noFm, []byte("# Plain\n"), 0644); err != nil {
		t.Fatal(err)
	}
	meta2, _, err := ParsePromptFrontmatter(noFm)
	if err != nil {
		t.Fatal(err)
	}
	if meta2.HasField("restart_on_crash") {
		t.Error("file without frontmatter should report no fields set")
	}
}

func TestRestartOnCrashDefault(t *testing.T) {
	if !RestartOnCrashDefault(TypeCrew) {
		t.Error("crew default should be true")
	}
	if RestartOnCrashDefault(TypePolecat) {
		t.Error("polecat default should be false")
	}
}

func TestResolveRestartOnCrash(t *testing.T) {
	dir := t.TempDir()

	writePrompt := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	noFm := writePrompt("plain.md", "# Plain\n")
	crewOptOut := writePrompt("crew-off.md", "+++\nrestart_on_crash = false\n+++\nbody\n")
	polecatOptIn := writePrompt("polecat-on.md", "+++\nrestart_on_crash = true\n+++\nbody\n")
	otherKey := writePrompt("other.md", "+++\nauto_start = true\n+++\nbody\n")

	cases := []struct {
		name       string
		promptFile string
		typ        AgentType
		want       bool
	}{
		{"crew default with no prompt", "", TypeCrew, true},
		{"polecat default with no prompt", "", TypePolecat, false},
		{"crew default without frontmatter", noFm, TypeCrew, true},
		{"polecat default without frontmatter", noFm, TypePolecat, false},
		{"crew opt-out via frontmatter", crewOptOut, TypeCrew, false},
		{"polecat opt-in via frontmatter", polecatOptIn, TypePolecat, true},
		{"unrelated frontmatter key keeps default (crew)", otherKey, TypeCrew, true},
		{"unrelated frontmatter key keeps default (polecat)", otherKey, TypePolecat, false},
		{"missing file falls back to default (crew)", filepath.Join(dir, "missing.md"), TypeCrew, true},
		{"missing file falls back to default (polecat)", filepath.Join(dir, "missing.md"), TypePolecat, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRestartOnCrash(tc.promptFile, tc.typ)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInitPromptsDefault(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	result, err := InitPrompts(false, false)
	if err != nil {
		t.Fatalf("InitPrompts failed on clean dir: %v", err)
	}
	if result.Mode != "default" {
		t.Errorf("expected mode=default, got %q", result.Mode)
	}
	if len(result.Created) == 0 {
		t.Fatal("expected files to be created")
	}

	// Verify the shipped coding profile is present on disk.
	for _, rel := range []string{
		"mayor.md",
		filepath.Join("crew", "doctor.md"),
		filepath.Join("templates", "polecat.md"),
		filepath.Join("templates", "polecat-qa.md"),
		filepath.Join("pm", "pm-template.md"),
		filepath.Join("pm", "pogo.toml"),
		filepath.Join("pm", "onethird.toml"),
	} {
		path := filepath.Join(tmpHome, ".pogo", "agents", rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", rel, err)
		}
	}

	// Each file should be hash-stamped so it interoperates with InstallPrompts.
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	data, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), promptHashPrefix) {
		t.Errorf("expected mayor.md to be hash-stamped, got first line: %q", strings.SplitN(string(data), "\n", 2)[0])
	}

	// PM TOML configs must be stamped with a TOML-style comment so the file
	// remains valid TOML — an HTML comment at the top would break parsers.
	for _, rel := range []string{
		filepath.Join("pm", "pogo.toml"),
		filepath.Join("pm", "onethird.toml"),
	} {
		path := filepath.Join(tmpHome, ".pogo", "agents", rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.HasPrefix(string(data), promptHashPrefixTOML) {
			t.Errorf("expected %s to be TOML-stamped, got first line: %q", rel, strings.SplitN(string(data), "\n", 2)[0])
		}
		if strings.HasPrefix(string(data), "<!--") {
			t.Errorf("%s starts with HTML comment — would break TOML parsing", rel)
		}
		if !strings.Contains(string(data), "name") || !strings.Contains(string(data), "= \"pm-") {
			t.Errorf("expected %s to declare a name = \"pm-...\" key", rel)
		}
	}
}

func TestInitPromptsRefusesExistingFiles(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// First init succeeds.
	if _, err := InitPrompts(false, false); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	// Second init must refuse and not error halfway through.
	_, err := InitPrompts(false, false)
	if err == nil {
		t.Fatal("expected second init to refuse existing files")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("expected 'refusing to overwrite' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("expected error to mention --force, got: %v", err)
	}
}

func TestInitPromptsForceOverwrites(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Pre-populate with a customized mayor.
	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	os.MkdirAll(agentsDir, 0755)
	customMayor := []byte("# my customized mayor\n")
	mayorPath := filepath.Join(agentsDir, "mayor.md")
	if err := os.WriteFile(mayorPath, customMayor, 0644); err != nil {
		t.Fatal(err)
	}

	// Without --force: refuse.
	if _, err := InitPrompts(false, false); err == nil {
		t.Fatal("expected refusal when mayor.md exists")
	}

	// Verify the user file was untouched.
	got, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(customMayor) {
		t.Errorf("user mayor.md was modified despite refusal: %q", got)
	}

	// With --force: overwrite.
	result, err := InitPrompts(true, false)
	if err != nil {
		t.Fatalf("force init failed: %v", err)
	}
	if len(result.Created) == 0 {
		t.Error("expected files in Created with force=true")
	}

	got2, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) == string(customMayor) {
		t.Error("--force did not overwrite mayor.md")
	}
}

func TestInitPromptsMinimal(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	result, err := InitPrompts(false, true)
	if err != nil {
		t.Fatalf("InitPrompts(minimal) failed: %v", err)
	}
	if result.Mode != "minimal" {
		t.Errorf("expected mode=minimal, got %q", result.Mode)
	}
	if len(result.Created) != 2 {
		t.Errorf("minimal should create exactly 2 files, got %d: %v", len(result.Created), result.Created)
	}

	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")

	// Mayor and polecat must exist.
	for _, rel := range []string{"mayor.md", filepath.Join("templates", "polecat.md")} {
		if _, err := os.Stat(filepath.Join(agentsDir, rel)); err != nil {
			t.Errorf("expected minimal scaffold to include %s: %v", rel, err)
		}
	}

	// Coding-profile-only files must NOT be present.
	for _, rel := range []string{
		filepath.Join("crew", "doctor.md"),
		filepath.Join("templates", "polecat-qa.md"),
	} {
		if _, err := os.Stat(filepath.Join(agentsDir, rel)); err == nil {
			t.Errorf("minimal scaffold should NOT include %s", rel)
		}
	}

	// Minimal mayor must contain the {{.Id}} placeholder in the polecat skeleton
	// so template expansion still works.
	polecatData, err := os.ReadFile(filepath.Join(agentsDir, "templates", "polecat.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(polecatData), "{{.Id}}") {
		t.Error("minimal polecat template should expose {{.Id}} for template expansion")
	}
}

func TestInitPromptsRefusalIsAtomic(t *testing.T) {
	// If only one of the planned files exists, the whole operation should still
	// refuse — no partial writes that would create a half-installed profile.
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	os.MkdirAll(filepath.Join(agentsDir, "templates"), 0755)
	// Pre-populate one file only.
	preExisting := []byte("# user-managed polecat template\n")
	polecatPath := filepath.Join(agentsDir, "templates", "polecat.md")
	if err := os.WriteFile(polecatPath, preExisting, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := InitPrompts(false, false); err == nil {
		t.Fatal("expected refusal when any planned file exists")
	}

	// Mayor must NOT have been written, since the operation aborted.
	if _, err := os.Stat(filepath.Join(agentsDir, "mayor.md")); err == nil {
		t.Error("mayor.md should not have been written during a refused init")
	}
	// And the user's polecat template must be untouched.
	got, err := os.ReadFile(polecatPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(preExisting) {
		t.Errorf("user polecat.md was modified: %q", got)
	}
}

// TestPolecatTemplatesIncludeMailCheckCron locks in the requirement that
// every polecat template instructs the agent to register a mail-check cron at
// startup. Without this, polecats won't proactively read mail and the mayor
// can't reach them mid-task. See work item mg-c1d3.
func TestPolecatTemplatesIncludeMailCheckCron(t *testing.T) {
	templates := []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-qa.md",
	}
	for _, path := range templates {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		s := string(data)
		if !strings.Contains(s, "CronCreate") {
			t.Errorf("%s: expected CronCreate instruction in template", path)
		}
		if !strings.Contains(s, "mg mail list {{.Id}}") {
			t.Errorf("%s: expected the cron prompt to call `mg mail list {{.Id}}`", path)
		}
		if !strings.Contains(s, "*/10 * * * *") {
			t.Errorf("%s: expected the cron schedule `*/10 * * * *` (every 10 minutes)", path)
		}
	}
}

// TestPMTemplateIncludesSweepCronEntries locks in the requirement that the
// PM template instructs each PM to register two sweep crons (09:00 and 17:00
// local) on startup. Without these, PMs have no twice-daily cadence — the
// pogod-internal cron was removed (mg-ddc1), so each PM self-schedules via
// CronCreate, mirroring the polecat mail-check pattern. See work item mg-8e32
// and docs/product-manager-design.md §3.
func TestPMTemplateIncludesSweepCronEntries(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/pm/pm-template.md")
	if err != nil {
		t.Fatalf("read pm-template.md: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "CronCreate") {
		t.Error("pm-template.md: expected CronCreate instruction for sweep crons")
	}
	if !strings.Contains(s, "0 9 * * *") {
		t.Error("pm-template.md: expected morning sweep cron `0 9 * * *` (09:00 local)")
	}
	if !strings.Contains(s, "0 17 * * *") {
		t.Error("pm-template.md: expected evening sweep cron `0 17 * * *` (17:00 local)")
	}
	// Each cron's prompt body must be `sweep` so the PM recognizes the trigger.
	if !strings.Contains(s, "`sweep`") {
		t.Error("pm-template.md: expected the sweep cron prompt body to be `sweep`")
	}
}

// TestSynthesizeExtendsPrompt covers the PM crew-loader directive that lets a
// crew prompt redirect to a shared template plus a per-instance TOML config.
// See docs/product-manager-design.md §1.
func TestSynthesizeExtendsPrompt(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	pmDir := filepath.Join(PromptDir(), "pm")
	if err := os.MkdirAll(pmDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Template with frontmatter — the synthesized prompt should preserve it
	// so nudge_on_start / restart_on_crash flow through.
	tmplBody := "+++\nauto_start = true\nnudge_on_start = \"sweep ready\"\n+++\n# PM Template\n\nYou are a PM.\n"
	if err := os.WriteFile(filepath.Join(pmDir, "pm-template.md"), []byte(tmplBody), 0644); err != nil {
		t.Fatal(err)
	}
	cfgBody := "name = \"pm-pogo\"\nrepos = [\"pogo\"]\n"
	if err := os.WriteFile(filepath.Join(pmDir, "pogo.toml"), []byte(cfgBody), 0644); err != nil {
		t.Fatal(err)
	}

	crewPath := filepath.Join(CrewPromptDir(), "pm-pogo.md")
	if err := os.WriteFile(crewPath, []byte("extends pm-template with config pm/pogo.toml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "synth.md")
	got, err := SynthesizeExtendsPrompt(crewPath, outPath)
	if err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	if got != outPath {
		t.Errorf("returned path = %q, want %q", got, outPath)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	// Template body and frontmatter must be preserved.
	if !strings.Contains(out, "+++\nauto_start = true") {
		t.Errorf("merged prompt missing template frontmatter:\n%s", out)
	}
	if !strings.Contains(out, "# PM Template") {
		t.Errorf("merged prompt missing template body:\n%s", out)
	}
	// Config must be inlined as a TOML block under "Your configuration".
	if !strings.Contains(out, "## Your configuration") {
		t.Errorf("merged prompt missing config section:\n%s", out)
	}
	if !strings.Contains(out, "```toml\n"+cfgBody+"```") {
		t.Errorf("merged prompt missing inlined config:\n%s", out)
	}
	if !strings.Contains(out, "pm/pogo.toml") {
		t.Errorf("merged prompt missing config path reference:\n%s", out)
	}

	// Frontmatter on the merged prompt must be parseable by ParsePromptFrontmatter
	// — that is how StartCrewAgent finds nudge_on_start / restart_on_crash.
	meta, _, err := ParsePromptFrontmatter(outPath)
	if err != nil {
		t.Fatalf("merged prompt frontmatter unparseable: %v", err)
	}
	if !meta.AutoStart {
		t.Error("expected merged prompt to inherit auto_start=true from template")
	}
	if meta.NudgeOnStart != "sweep ready" {
		t.Errorf("merged prompt nudge_on_start = %q, want %q", meta.NudgeOnStart, "sweep ready")
	}
}

// TestSynthesizeExtendsPromptNoDirective verifies that a crew prompt without
// the directive returns "" so the caller uses the original file as-is.
func TestSynthesizeExtendsPromptNoDirective(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	crewPath := filepath.Join(CrewPromptDir(), "plain.md")
	if err := os.WriteFile(crewPath, []byte("# Plain crew agent\n\nNo directive here.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := SynthesizeExtendsPrompt(crewPath, filepath.Join(t.TempDir(), "synth.md"))
	if err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty result for prompt without directive, got %q", got)
	}
}

// TestSynthesizeExtendsPromptStripsHashStamps verifies that the pogo-prompt-hash
// stamp added by InstallPrompts to the template (HTML-comment) and config
// (TOML-comment) does not leak into the synthesized prompt.
func TestSynthesizeExtendsPromptStripsHashStamps(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	pmDir := filepath.Join(PromptDir(), "pm")
	if err := os.MkdirAll(pmDir, 0755); err != nil {
		t.Fatal(err)
	}
	tmplData := stampedContent("pm/pm-template.md", []byte("+++\nauto_start = true\n+++\n# PM Template\n"))
	if err := os.WriteFile(filepath.Join(pmDir, "pm-template.md"), tmplData, 0644); err != nil {
		t.Fatal(err)
	}
	cfgData := stampedContent("pm/pogo.toml", []byte("name = \"pm-pogo\"\n"))
	if err := os.WriteFile(filepath.Join(pmDir, "pogo.toml"), cfgData, 0644); err != nil {
		t.Fatal(err)
	}
	crewPath := filepath.Join(CrewPromptDir(), "pm-pogo.md")
	if err := os.WriteFile(crewPath, []byte("extends pm-template with config pm/pogo.toml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "synth.md")
	if _, err := SynthesizeExtendsPrompt(crewPath, outPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if strings.Contains(out, "pogo-prompt-hash") {
		t.Errorf("synthesized prompt should not contain prompt-hash stamps:\n%s", out)
	}
	// Frontmatter must still parse (i.e. starts with `+++` after stripping the stamp).
	if _, _, err := ParsePromptFrontmatter(outPath); err != nil {
		t.Errorf("synthesized prompt frontmatter unparseable: %v", err)
	}
}

// TestSynthesizeExtendsPromptMissingFiles verifies that referenced template or
// config files that don't exist surface as errors (not silent fallthrough).
func TestSynthesizeExtendsPromptMissingFiles(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	crewPath := filepath.Join(CrewPromptDir(), "pm-ghost.md")
	if err := os.WriteFile(crewPath, []byte("extends pm-template with config pm/ghost.toml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := SynthesizeExtendsPrompt(crewPath, filepath.Join(t.TempDir(), "synth.md")); err == nil {
		t.Error("expected error when referenced template/config is missing")
	}
}

// TestStartCrewAgentResolvesExtendsDirective verifies that StartCrewAgent
// honors the extends-with-config directive end-to-end: the spawned agent's
// PromptFile points at the synthesized merged prompt, the merged prompt
// contains both template + config, and the InitialNudge comes from the
// template's frontmatter (not the redirecting crew file).
func TestStartCrewAgentResolvesExtendsDirective(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	pmDir := filepath.Join(PromptDir(), "pm")
	if err := os.MkdirAll(pmDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pmDir, "pm-template.md"),
		[]byte("+++\nauto_start = true\nnudge_on_start = \"sweep ready\"\n+++\n# PM Template\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pmDir, "pogo.toml"),
		[]byte("name = \"pm-pogo\"\nrepos = [\"pogo\"]\n"),
		0644,
	); err != nil {
		t.Fatal(err)
	}
	crewPath := filepath.Join(CrewPromptDir(), "pm-pogo.md")
	if err := os.WriteFile(crewPath, []byte("extends pm-template with config pm/pogo.toml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := NewRegistry(filepath.Join(tmpHome, "sockets"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)
	reg.SetCommandConfig(catCommandConfig{})

	a := startAgentViaAPI(t, reg, "pm-pogo")

	// PromptFile must be the synthesized merged prompt under the agent dir,
	// not the original redirect file.
	wantPrefix := filepath.Join(tmpHome, ".pogo", "agents", "pm-pogo")
	if !strings.HasPrefix(a.PromptFile, wantPrefix) {
		t.Errorf("PromptFile = %q, expected synthesized prompt under %q", a.PromptFile, wantPrefix)
	}
	if a.PromptFile == crewPath {
		t.Errorf("PromptFile must not be the redirect crew file %q", crewPath)
	}

	data, err := os.ReadFile(a.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "# PM Template") {
		t.Errorf("merged prompt missing template body:\n%s", body)
	}
	if !strings.Contains(body, "name = \"pm-pogo\"") {
		t.Errorf("merged prompt missing config:\n%s", body)
	}

	if a.InitialNudge != "sweep ready" {
		t.Errorf("InitialNudge = %q, want template's nudge_on_start %q", a.InitialNudge, "sweep ready")
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
