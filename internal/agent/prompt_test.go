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
