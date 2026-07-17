package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/drellem2/pogo/internal/gitgc"
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

// TestShippedTemplatesSurfaceRecentActivity guards the embedded polecat
// templates against silent removal of the recent-activity context block —
// the lever introduced for mg-b372. If the conditional ever needs to change
// shape, update this test deliberately rather than letting the section
// disappear on a stray edit.
func TestShippedTemplatesSurfaceRecentActivity(t *testing.T) {
	for _, name := range []string{"prompts/templates/polecat.md", "prompts/templates/polecat-qa.md", "prompts/templates/polecat-build-pr.md", "prompts/templates/polecat-triage.md", "prompts/templates/polecat-review.md", "prompts/templates/polecat-architect.md"} {
		data, err := defaultPrompts.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		body := string(data)
		for _, want := range []string{
			"{{if .RecentCommits}}",
			"{{.RecentCommits}}",
			"{{if .RecentFiles}}",
			"{{.RecentFiles}}",
			"## Recent activity",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: expected %q in template body", name, want)
			}
		}
	}
}

// TestShippedTemplatesBanUnanchoredPkill guards the prohibition on unanchored
// `pkill -f` in every prompt that drives a shell (mg-8c9c). Four polecat
// sessions once ran machine-wide `pkill -f "sleep N"`; every pogo poller idles
// in `sleep $INTERVAL` under `set -euo pipefail`, so the killed sleep returned
// 143, `set -e` fired, and the pollers — the watchdog among them — killed
// themselves. A bare prohibition gets ignored under time pressure, so each
// prompt must also carry the replacement: kill by PID, or anchor the pattern.
// If the wording changes, update this test deliberately rather than letting the
// rule disappear on a stray edit.
func TestShippedTemplatesBanUnanchoredPkill(t *testing.T) {
	names := []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-qa.md",
		"prompts/templates/polecat-build-pr.md",
		"prompts/templates/polecat-triage.md",
		"prompts/templates/polecat-review.md", "prompts/templates/polecat-architect.md",
		"prompts/mayor.md",
		"prompts/crew/doctor.md",
	}
	for _, name := range names {
		data, err := defaultPrompts.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		body := string(data)
		for _, want := range []string{
			// The rule itself.
			"unanchored `pkill -f`",
			// The one-line why — agents obey rules they understand.
			"matches every process on the machine",
			// The replacements, without which the rule gets ignored.
			`kill "$PID"`,
			`pkill -f "^`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: expected %q in template body", name, want)
			}
		}
	}
}

// TestShippedTemplatesProviderGating expands the embedded polecat templates
// under each provider and asserts the Claude-Code-specific guidance
// (CronCreate naming, the rating-modal dismissal bullet) appears only when
// Provider is "claude" (mg-e310 / gh #32). The templates are executed
// directly rather than through ExpandTemplate so the test doesn't pick up
// drop-ins from the developer's real ~/.pogo/agents/.
func TestShippedTemplatesProviderGating(t *testing.T) {
	claudeIsms := []string{"Claude Code", "CronCreate", "rating dialog"}
	expand := func(t *testing.T, name, provider string) string {
		t.Helper()
		data, err := defaultPrompts.ReadFile(name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		_, body, err := parsePromptFrontmatterBytes(data)
		if err != nil {
			t.Fatalf("parse frontmatter in %s: %v", name, err)
		}
		tmpl, err := template.New(name).Parse(body)
		if err != nil {
			t.Fatalf("parse template %s: %v", name, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, withDefaults(TemplateVars{Provider: provider})); err != nil {
			t.Fatalf("execute template %s: %v", name, err)
		}
		return buf.String()
	}

	for _, name := range []string{"prompts/templates/polecat.md", "prompts/templates/polecat-qa.md", "prompts/templates/polecat-build-pr.md", "prompts/templates/polecat-triage.md", "prompts/templates/polecat-review.md", "prompts/templates/polecat-architect.md"} {
		for _, provider := range []string{"pi", "codex", "cursor"} {
			out := expand(t, name, provider)
			for _, ism := range claudeIsms {
				if strings.Contains(out, ism) {
					t.Errorf("%s under provider %q: expected no %q in expanded prompt", name, provider, ism)
				}
			}
			// The provider-neutral scheduler policy must survive the gating.
			if !strings.Contains(out, "in-process scheduler") {
				t.Errorf("%s under provider %q: neutral in-process-scheduler guidance missing", name, provider)
			}
		}
		out := expand(t, name, "claude")
		for _, ism := range claudeIsms {
			if !strings.Contains(out, ism) {
				t.Errorf("%s under provider \"claude\": expected %q in expanded prompt", name, ism)
			}
		}
	}
}

// TestShippedBuildPRTemplateProtocol pins the protocol contract of the
// issue-track build template (mg-9675, gh-issue-workflow design §3/§6): the
// builder opens a PR linking the GH issue and triage recommendation, works
// the modify↔review loop via mail + gh pr comment, and NEVER self-submits to
// the refinery — the coordinator submits after the review loop passes.
func TestShippedBuildPRTemplateProtocol(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/templates/polecat-build-pr.md")
	if err != nil {
		t.Fatalf("read embedded polecat-build-pr.md: %v", err)
	}
	_, body, err := parsePromptFrontmatterBytes(data)
	if err != nil {
		t.Fatalf("parse frontmatter: %v", err)
	}
	tmpl, err := template.New("polecat-build-pr").Parse(body)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, withDefaults(TemplateVars{Id: "mg-test", Provider: "claude"})); err != nil {
		t.Fatalf("execute template: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		// PR creation replaces refinery submission, body links issue + triage rec.
		"gh pr create",
		"Resolves <owner>/<repo>#<n>",
		"triage recommendation",
		// Review loop: PR comments plus direct mail to the reviewer.
		"gh pr comment",
		// The no-self-submit rule must be stated explicitly.
		"Never run `pogo refinery submit` yourself",
		"Refinery submission happens later, by the ringmaster",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded polecat-build-pr.md: expected %q", want)
		}
	}

	// The internal-track self-submit command must not appear as an
	// instruction (polecat.md's step-5 form is "pogo refinery submit
	// polecat-<id> ..."). Mentions of the command inside the "never run"
	// prose don't carry the branch argument, so this catches a copy-paste
	// of the submit step without false-positives on the prohibition text.
	if strings.Contains(out, "pogo refinery submit polecat-") {
		t.Errorf("expanded polecat-build-pr.md: contains internal-track self-submit command")
	}
}

// TestExpandTemplateProviderDefault pins the fail-safe: an empty Provider
// defaults to "claude" at expansion time, so Claude-gated blocks stay visible
// for callers that predate the field (never silently hidden by an
// empty-string comparison).
func TestExpandTemplateProviderDefault(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "provider-gate.md")
	content := `Rule.{{if eq .Provider "claude"}} CLAUDE-ONLY.{{end}}`
	if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := ExpandTemplate(tmplPath, TemplateVars{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CLAUDE-ONLY") {
		t.Errorf("empty Provider should default to claude and keep gated block, got: %q", out)
	}

	out, err = ExpandTemplate(tmplPath, TemplateVars{Provider: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "CLAUDE-ONLY") {
		t.Errorf("Provider=pi should drop gated block, got: %q", out)
	}

	if got := PreviewTemplateVars().Provider; got != DefaultProviderID {
		t.Errorf("PreviewTemplateVars().Provider = %q, want %q", got, DefaultProviderID)
	}
}

func TestExpandTemplateRecentActivity(t *testing.T) {
	dir := t.TempDir()
	tmplPath := filepath.Join(dir, "polecat.md")
	// Mirror the conditional surfaced in the shipped polecat.md so the test
	// pins the contract: `{{if .RecentCommits}}` must gate the section, and
	// the inner `{{if .RecentFiles}}` must gate the files block.
	content := `Task: {{.Task}}
{{if .RecentCommits}}
## Recent activity in ` + "`{{.Repo}}`" + `

` + "```" + `
{{.RecentCommits}}
` + "```" + `
{{if .RecentFiles}}
Files:

` + "```" + `
{{.RecentFiles}}
` + "```" + `
{{end}}{{end}}
done.`
	if err := os.WriteFile(tmplPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("populated includes section", func(t *testing.T) {
		vars := TemplateVars{
			Task:          "T",
			Repo:          "/r",
			RecentCommits: "abc1234 first (mg-1111)\ndef5678 second (mg-2222)",
			RecentFiles:   "internal/agent/api.go\ninternal/agent/prompt.go",
		}
		got, err := ExpandTemplate(tmplPath, vars)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Recent activity", "abc1234", "mg-1111", "internal/agent/api.go"} {
			if !strings.Contains(got, want) {
				t.Errorf("expected %q in output:\n%s", want, got)
			}
		}
	})

	t.Run("empty RecentCommits omits section entirely", func(t *testing.T) {
		got, err := ExpandTemplate(tmplPath, TemplateVars{Task: "T", Repo: "/r"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "Recent activity") {
			t.Errorf("section must be gated when RecentCommits is empty:\n%s", got)
		}
	})

	t.Run("commits without files still renders commits", func(t *testing.T) {
		vars := TemplateVars{Task: "T", Repo: "/r", RecentCommits: "abc1234 only commit"}
		got, err := ExpandTemplate(tmplPath, vars)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "abc1234 only commit") {
			t.Errorf("expected commits even without files:\n%s", got)
		}
		if strings.Contains(got, "Files:") {
			t.Errorf("files block must be gated when RecentFiles is empty:\n%s", got)
		}
	})
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
	hash := contentHash(data)
	wantStamp := "<!-- pogo-prompt: embed=sha256:" + hash + " body=sha256:" + hash + " -->\n"
	if !strings.HasPrefix(s, wantStamp) {
		t.Errorf("stamped content should start with v1 HTML stamp\ngot:  %q\nwant: %q", s[:len(wantStamp)+1], wantStamp)
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
	hash := contentHash(data)
	wantStamp := "# pogo-prompt: embed=sha256:" + hash + " body=sha256:" + hash + "\n"
	if !strings.HasPrefix(s, wantStamp) {
		t.Errorf("stamped content for .toml should start with v1 TOML stamp\ngot:  %q\nwant: %q", s[:len(wantStamp)+1], wantStamp)
	}
	if strings.HasPrefix(s, "<!--") {
		t.Error("stamped .toml file must not start with HTML comment — would break TOML parsing")
	}
	if !strings.Contains(s, "name = \"pm-foo\"") {
		t.Error("stamped content should contain original content")
	}
}

// TestStampedContentV1RoundTrip verifies that stampedContent + readInstalledPromptStamp
// round-trips both hashes, and that at install time embed_hash == body_hash ==
// contentHash(data) for both .md and .toml flavors.
func TestStampedContentV1RoundTrip(t *testing.T) {
	cases := map[string]struct {
		path string
		data []byte
	}{
		"markdown": {"crew/foo.md", []byte("# My Prompt\nDo stuff.\n")},
		"toml":     {"pm/foo.toml", []byte("name = \"pm-foo\"\n")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, filepath.Base(tc.path))
			if err := os.WriteFile(path, stampedContent(tc.path, tc.data), 0644); err != nil {
				t.Fatal(err)
			}
			stamp := readInstalledPromptStamp(path)
			want := contentHash(tc.data)
			if stamp.EmbedHash != want {
				t.Errorf("EmbedHash=%q want %q", stamp.EmbedHash, want)
			}
			if stamp.BodyHash != want {
				t.Errorf("BodyHash=%q want %q", stamp.BodyHash, want)
			}
			// At install time the two hashes are equal — the v1 stamp records
			// them separately so future installs can tell embed-changed apart
			// from user-edited.
			if stamp.EmbedHash != stamp.BodyHash {
				t.Errorf("at install time EmbedHash should equal BodyHash, got %q vs %q",
					stamp.EmbedHash, stamp.BodyHash)
			}
		})
	}
}

// TestReadInstalledPromptStampV0BackwardsCompat verifies that a v0 single-hash
// stamp is read as EmbedHash == BodyHash, so files installed by older pogo
// binaries don't all spuriously read as "user-edited" on the v1 upgrade.
func TestReadInstalledPromptStampV0BackwardsCompat(t *testing.T) {
	cases := map[string]struct {
		filename string
		content  string
	}{
		"v0 markdown": {
			"test.md",
			"<!-- pogo-prompt-hash: deadbeef -->\n# Body\n",
		},
		"v0 toml": {
			"test.toml",
			"# pogo-prompt-hash: deadbeef\nname = \"x\"\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			if err := os.WriteFile(path, []byte(tc.content), 0644); err != nil {
				t.Fatal(err)
			}
			stamp := readInstalledPromptStamp(path)
			if stamp.EmbedHash != "deadbeef" {
				t.Errorf("EmbedHash=%q want %q", stamp.EmbedHash, "deadbeef")
			}
			if stamp.BodyHash != "deadbeef" {
				t.Errorf("BodyHash=%q want %q (v0 must read as EmbedHash==BodyHash)",
					stamp.BodyHash, "deadbeef")
			}
		})
	}
}

// TestReadInstalledPromptStampUnrecognized verifies that unstamped files and
// stamps with unknown shapes return the zero value (no spurious matches).
func TestReadInstalledPromptStampUnrecognized(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"plain content":     "# No stamp here\n",
		"unrelated comment": "<!-- something else -->\n# Body\n",
		"empty":             "",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".md")
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			stamp := readInstalledPromptStamp(path)
			if stamp.EmbedHash != "" || stamp.BodyHash != "" {
				t.Errorf("expected zero stamp for %q, got %+v", content, stamp)
			}
		})
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
	result, err := InstallPrompts(InstallOpts{})
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
	result2, err := InstallPrompts(InstallOpts{})
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

	// Simulate stale file: a v0 stamp whose hash matches the on-disk body
	// (so the install matrix reads it as "not user-edited") but differs
	// from the current binary's embedded mayor.md (so the embed has
	// "changed" from the perspective of this file). Writing the stamp's
	// hash with a value that matches the body keeps the v0-compat path
	// honest — v0 stamps record only one hash and the install code treats
	// it as both EmbedHash and BodyHash.
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	oldBody := []byte("# Old mayor prompt\n")
	oldHash := contentHash(oldBody)
	stale := append([]byte("<!-- pogo-prompt-hash: "+oldHash+" -->\n"), oldBody...)
	os.WriteFile(mayorPath, stale, 0644)

	result3, err := InstallPrompts(InstallOpts{})
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
	os.WriteFile(filepath.Join(tmplDir, "polecat-build-pr.md"), []byte("# Old polecat-build-pr\n"), 0644)
	os.WriteFile(filepath.Join(tmplDir, "polecat-triage.md"), []byte("# Old polecat-triage\n"), 0644)

	result, err := InstallPrompts(InstallOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// Files without hash stamps should be treated as stale and updated.
	if len(result.Updated) == 0 {
		t.Error("expected unstamped files to be updated")
	}
	for _, rel := range []string{"mayor.md", "crew/doctor.md", "templates/polecat.md", "templates/polecat-qa.md", "templates/polecat-build-pr.md", "templates/polecat-triage.md"} {
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

	result, err := InstallPrompts(InstallOpts{})
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

// TestInstallPromptsConflictMatrixSkipsWhenEmbedUnchanged covers cell (b)
// of the matrix in docs/design/prompt-customization-design.md §B: the user has
// edited the canonical file in place, but the embedded prompt has not
// changed since install. The install must skip (the embed hasn't moved,
// so there is nothing new to write) and must not produce a .dist sidecar.
func TestInstallPromptsConflictMatrixSkipsWhenEmbedUnchanged(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("first InstallPrompts: %v", err)
	}

	// User edits mayor.md in place: preserve the stamp line, append a
	// custom rule to the body. This makes currentBodyHash != stamp.BodyHash
	// without changing the recorded embed_hash.
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	original, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	nl := strings.IndexByte(string(original), '\n')
	if nl == -1 {
		t.Fatalf("expected stamped mayor.md to contain a newline, got %q", original)
	}
	edited := append([]byte{}, original[:nl+1]...)
	edited = append(edited, original[nl+1:]...)
	edited = append(edited, []byte("\n## My house rules\nKeep PRs small.\n")...)
	if err := os.WriteFile(mayorPath, edited, 0644); err != nil {
		t.Fatalf("rewrite mayor.md: %v", err)
	}
	preBody := append([]byte{}, edited...)

	result, err := InstallPrompts(InstallOpts{})
	if err != nil {
		t.Fatalf("second InstallPrompts: %v", err)
	}
	if len(result.Conflicts) != 0 {
		t.Errorf("expected no conflicts when embed unchanged, got %+v", result.Conflicts)
	}
	for _, u := range result.Updated {
		if u == "mayor.md" {
			t.Errorf("expected mayor.md NOT to be updated when embed unchanged, got Updated=%v", result.Updated)
		}
	}
	skipped := false
	for _, s := range result.Skipped {
		if s == "mayor.md" {
			skipped = true
			break
		}
	}
	if !skipped {
		t.Errorf("expected mayor.md in Skipped when embed unchanged, got Skipped=%v Updated=%v Installed=%v",
			result.Skipped, result.Updated, result.Installed)
	}
	// Canonical file must be byte-identical to what the user wrote.
	post, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatalf("read mayor.md after install: %v", err)
	}
	if string(post) != string(preBody) {
		t.Errorf("install touched user-edited mayor.md when embed was unchanged")
	}
	// And no .dist must have been written.
	if _, err := os.Stat(mayorPath + ".dist"); err == nil {
		t.Errorf("unexpected mayor.md.dist on no-conflict path")
	}
}

// TestInstallPromptsConflictMatrixWritesDistOnUserEditAndEmbedChange covers
// cell (d) — the new behavior. Setup mimics "older pogo install + user
// edit + binary upgrade": a v1 stamp whose embed_hash is *not* the current
// binary's embed (so the embed has effectively changed) and whose
// body_hash is the hash of the body the older install wrote, paired with
// an on-disk body that differs from that hash (so the user has edited).
// Expectation: canonical file is preserved untouched, the new embed is
// written to <name>.dist, and the conflict is reported in the result.
func TestInstallPromptsConflictMatrixWritesDistOnUserEditAndEmbedChange(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Synthesize an "older install" of mayor.md that the user has since
	// edited. The body_hash slot of the stamp records what the older
	// install wrote (oldPristineHash); the actual body is something else.
	oldPristineBody := []byte("# Mayor (older shipped version)\n")
	oldPristineHash := contentHash(oldPristineBody)
	userBody := []byte("# Mayor (older shipped version)\n\n## My house rules\nNo amend commits.\n")
	stampLine := "<!-- pogo-prompt: embed=sha256:" + oldPristineHash + " body=sha256:" + oldPristineHash + " -->\n"
	mayorPath := filepath.Join(agentsDir, "mayor.md")
	canonicalContent := append([]byte(stampLine), userBody...)
	if err := os.WriteFile(mayorPath, canonicalContent, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := InstallPrompts(InstallOpts{})
	if err != nil {
		t.Fatalf("InstallPrompts: %v", err)
	}

	// Conflict must be reported for mayor.md.
	var conflict *PromptConflict
	for i, c := range result.Conflicts {
		if c.Path == "mayor.md" {
			conflict = &result.Conflicts[i]
			break
		}
	}
	if conflict == nil {
		t.Fatalf("expected mayor.md in Conflicts, got Conflicts=%+v Updated=%v Installed=%v",
			result.Conflicts, result.Updated, result.Installed)
	}
	if conflict.DistPath != "mayor.md.dist" {
		t.Errorf("expected DistPath=mayor.md.dist, got %q", conflict.DistPath)
	}

	// Canonical mayor.md must be untouched.
	post, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatalf("read mayor.md after install: %v", err)
	}
	if string(post) != string(canonicalContent) {
		t.Errorf("install modified user-edited canonical file:\n got:  %q\n want: %q", post, canonicalContent)
	}

	// .dist must exist, carry a stamp, and have an embed_hash matching
	// the current binary (so a future install reads it as up-to-date if
	// the user accepts it by renaming over the canonical).
	distPath := mayorPath + ".dist"
	distData, err := os.ReadFile(distPath)
	if err != nil {
		t.Fatalf("read mayor.md.dist: %v", err)
	}
	distStamp := readInstalledPromptStamp(distPath)
	if distStamp.EmbedHash == "" {
		t.Errorf("expected mayor.md.dist to carry a stamp, got %q", distData)
	}
	if distStamp.EmbedHash == oldPristineHash {
		t.Errorf("dist file's stamp records old embed hash; expected current binary's embed hash")
	}

	// Canonical must NOT be in Updated or Installed.
	for _, u := range result.Updated {
		if u == "mayor.md" {
			t.Errorf("mayor.md must not be Updated on conflict, got Updated=%v", result.Updated)
		}
	}
	for _, i := range result.Installed {
		if i == "mayor.md" {
			t.Errorf("mayor.md must not be Installed on conflict, got Installed=%v", result.Installed)
		}
	}
}

// withFixedNow pins nowFn to a fixed time for the duration of the test so the
// .bak.<timestamp> suffix is deterministic and the format can be asserted
// exactly. Returns the suffix the install run will use.
func withFixedNow(t *testing.T) string {
	t.Helper()
	fixed := time.Date(2026, 5, 9, 10, 30, 45, 0, time.UTC)
	orig := nowFn
	nowFn = func() time.Time { return fixed }
	t.Cleanup(func() { nowFn = orig })
	return ".bak." + fixed.Format(backupTimeLayout)
}

// installFreshThenEditMayor seeds a tmpHome, runs the matrix install once so
// mayor.md gets a v1 stamp matching the current binary's embed, then writes a
// user edit on top of it. Returns the on-disk mayor.md path and the byte
// contents the user wrote (which the test will compare against the .bak file).
func installFreshThenEditMayor(t *testing.T, tmpHome string) (string, []byte) {
	t.Helper()
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("seed InstallPrompts: %v", err)
	}
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	original, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	// Preserve the stamp line, append a user-style customization to the
	// body so currentBodyHash diverges from the recorded body_hash.
	edited := append([]byte{}, original...)
	if !strings.HasSuffix(string(edited), "\n") {
		edited = append(edited, '\n')
	}
	edited = append(edited, []byte("\n## My house rules\nKeep PRs small.\n")...)
	if err := os.WriteFile(mayorPath, edited, 0644); err != nil {
		t.Fatalf("rewrite mayor.md: %v", err)
	}
	return mayorPath, edited
}

// TestInstallPromptsForceBackupOnUserEdit verifies that --force without
// --no-backup copies a user-edited canonical to <name>.bak.<ts> *before*
// overwriting it, names the backup with the deterministic compact-ISO-8601
// suffix from backupTimeLayout, records the (Path, BackupPath) pair in
// result.Backups, and writes pre-overwrite content to the backup so users
// can recover their edits.
func TestInstallPromptsForceBackupOnUserEdit(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	suffix := withFixedNow(t)

	mayorPath, userBody := installFreshThenEditMayor(t, tmpHome)

	result, err := InstallPrompts(InstallOpts{Force: true})
	if err != nil {
		t.Fatalf("InstallPrompts force: %v", err)
	}

	// Backups slice must record mayor.md.
	var backup *PromptBackup
	for i, b := range result.Backups {
		if b.Path == "mayor.md" {
			backup = &result.Backups[i]
			break
		}
	}
	if backup == nil {
		t.Fatalf("expected mayor.md in Backups, got Backups=%+v", result.Backups)
	}
	wantBackupRel := "mayor.md" + suffix
	if backup.BackupPath != wantBackupRel {
		t.Errorf("BackupPath = %q, want %q", backup.BackupPath, wantBackupRel)
	}

	// Backup file must exist on disk and carry the user's pre-overwrite content.
	backupAbs := mayorPath + suffix
	got, err := os.ReadFile(backupAbs)
	if err != nil {
		t.Fatalf("read backup file %s: %v", backupAbs, err)
	}
	if string(got) != string(userBody) {
		t.Errorf("backup contents do not match pre-overwrite body:\n got  %q\n want %q", got, userBody)
	}

	// Canonical mayor.md must now hold the freshly stamped embed (--force
	// overwrote it). The backup is the only copy of the user's edits.
	post, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatalf("read mayor.md after force: %v", err)
	}
	if string(post) == string(userBody) {
		t.Errorf("expected canonical mayor.md to be overwritten by --force, but it still equals user body")
	}
	if readInstalledPromptStamp(mayorPath).EmbedHash == "" {
		t.Errorf("post-force mayor.md missing v1 stamp; --force should rewrite stamped content")
	}
}

// TestInstallPromptsForceNoBackupSkipsBackup verifies that --force --no-backup
// suppresses the backup write entirely: no .bak.<ts> file lands on disk and
// result.Backups is empty even though the canonical was user-edited (the same
// fixture that produces a backup in TestInstallPromptsForceBackupOnUserEdit).
func TestInstallPromptsForceNoBackupSkipsBackup(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	suffix := withFixedNow(t)

	mayorPath, userBody := installFreshThenEditMayor(t, tmpHome)

	result, err := InstallPrompts(InstallOpts{Force: true, NoBackup: true})
	if err != nil {
		t.Fatalf("InstallPrompts force --no-backup: %v", err)
	}

	if len(result.Backups) != 0 {
		t.Errorf("expected empty Backups with --no-backup, got %+v", result.Backups)
	}
	backupAbs := mayorPath + suffix
	if _, err := os.Stat(backupAbs); err == nil {
		t.Errorf("expected no backup file on --no-backup, but %s exists", backupAbs)
	}

	// Sanity: --force still overwrote — the user's body is gone from the
	// canonical, which is exactly the silent stomping --no-backup opts into.
	post, err := os.ReadFile(mayorPath)
	if err != nil {
		t.Fatalf("read mayor.md after force: %v", err)
	}
	if string(post) == string(userBody) {
		t.Errorf("expected --force to overwrite mayor.md even with --no-backup, but it kept user body")
	}
}

// TestInstallPromptsForceSkipsBackupForPristine verifies that --force does
// not generate spurious .bak files for canonical files the user has not
// touched. Backup only triggers when stamp.BodyHash and current body diverge —
// for a fresh install + immediate --force run, every file is pristine, so
// Backups must be empty.
func TestInstallPromptsForceSkipsBackupForPristine(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	withFixedNow(t)

	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("seed InstallPrompts: %v", err)
	}

	result, err := InstallPrompts(InstallOpts{Force: true})
	if err != nil {
		t.Fatalf("InstallPrompts force: %v", err)
	}

	if len(result.Backups) != 0 {
		t.Errorf("expected no backups for pristine files, got %+v", result.Backups)
	}

	// And no .bak.* file should exist anywhere under the agents tree.
	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	err = filepath.Walk(agentsDir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if strings.Contains(info.Name(), ".bak.") {
			t.Errorf("unexpected backup file on pristine --force: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk agents dir: %v", err)
	}
}

// TestCheckPromptDriftCleanInstall verifies that immediately after
// InstallPrompts, no prompt is reported as drifted.
func TestCheckPromptDriftCleanInstall(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("InstallPrompts: %v", err)
	}

	drift, err := CheckPromptDrift()
	if err != nil {
		t.Fatalf("CheckPromptDrift: %v", err)
	}
	if len(drift) != 0 {
		t.Errorf("expected no drift after fresh install, got %+v", drift)
	}
}

// TestCheckPromptDriftDetectsStale simulates the mg-ec77 failure mode:
// the live prompt file carries an out-of-date hash stamp because the
// embedded version has advanced. Drift must be reported as "stale".
func TestCheckPromptDriftDetectsStale(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("InstallPrompts: %v", err)
	}

	// Overwrite an arbitrary installed prompt with a wrong hash stamp.
	// This mirrors what would happen if the binary's embedded version
	// of pm-template advanced past the on-disk copy.
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	stale := "<!-- pogo-prompt-hash: 0000000000000000000000000000000000000000000000000000000000000000 -->\n# Old mayor prompt\n"
	if err := os.WriteFile(mayorPath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	drift, err := CheckPromptDrift()
	if err != nil {
		t.Fatalf("CheckPromptDrift: %v", err)
	}
	found := false
	for _, d := range drift {
		if d.Path == "mayor.md" {
			found = true
			if d.Reason != "stale" {
				t.Errorf("mayor.md drift reason = %q, want %q", d.Reason, "stale")
			}
		}
	}
	if !found {
		t.Errorf("expected mayor.md in drift list, got %+v", drift)
	}
}

// TestCheckPromptDriftDetectsMissingAndUnstamped covers the two non-stale
// drift reasons: the live file simply isn't there yet, or it exists but has
// no hash stamp (e.g. user hand-edited and stripped it).
func TestCheckPromptDriftDetectsMissingAndUnstamped(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("InstallPrompts: %v", err)
	}

	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	if err := os.Remove(mayorPath); err != nil {
		t.Fatalf("remove mayor.md: %v", err)
	}
	pmTmplPath := filepath.Join(tmpHome, ".pogo", "agents", "pm", "pm-template.md")
	if err := os.WriteFile(pmTmplPath, []byte("# Hand-edited, no hash stamp\n"), 0644); err != nil {
		t.Fatalf("rewrite pm-template.md: %v", err)
	}

	drift, err := CheckPromptDrift()
	if err != nil {
		t.Fatalf("CheckPromptDrift: %v", err)
	}
	reasons := map[string]string{}
	for _, d := range drift {
		reasons[d.Path] = d.Reason
	}
	if reasons["mayor.md"] != "missing" {
		t.Errorf("mayor.md reason=%q, want %q", reasons["mayor.md"], "missing")
	}
	if reasons[filepath.Join("pm", "pm-template.md")] != "unstamped" {
		t.Errorf("pm/pm-template.md reason=%q, want %q",
			reasons[filepath.Join("pm", "pm-template.md")], "unstamped")
	}
}

func TestParsePromptFrontmatterWellFormed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor.md")
	content := `+++
restart_on_crash = true
auto_start = true
nudge_on_start = "Begin your coordination loop."
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
	wantBody := "# Mayor\n\nYou are the mayor.\n"
	if body != wantBody {
		t.Errorf("body=%q want %q", body, wantBody)
	}
}

// TestParsePromptFrontmatterProvider verifies the provider: frontmatter key
// parses into AgentMeta.Provider and registers in the explicit bitmask — the
// tier-2 input to per-spawn provider resolution (mg-b31b).
func TestParsePromptFrontmatterProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polecat.md")
	content := "+++\nprovider = \"codex\"\nworktree = true\n+++\n# Polecat\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	meta, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Provider != "codex" {
		t.Errorf("Provider = %q, want codex", meta.Provider)
	}
	if !meta.HasField("provider") {
		t.Error("expected HasField(provider) = true")
	}
	if body != "# Polecat\n" {
		t.Errorf("body = %q, want %q", body, "# Polecat\n")
	}
}

// TestParsePromptFrontmatterNoProvider verifies a prompt without a provider:
// key leaves AgentMeta.Provider empty and HasField(provider) false, so
// resolution falls through to the config tiers.
func TestParsePromptFrontmatterNoProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "polecat.md")
	if err := os.WriteFile(path, []byte("+++\nworktree = true\n+++\n# Polecat\n"), 0644); err != nil {
		t.Fatal(err)
	}

	meta, _, err := ParsePromptFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Provider != "" {
		t.Errorf("Provider = %q, want empty", meta.Provider)
	}
	if meta.HasField("provider") {
		t.Error("expected HasField(provider) = false when key absent")
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

// TestEmbeddedDoctorOnDemand verifies the shipped crew/doctor.md prompt
// declares on-demand semantics (auto_start = false, restart_on_crash = false)
// so a fresh install can stop the doctor on demand instead of pogod
// auto-restarting it (gh #18). Mayor/PM keep their always-on default and are
// asserted to still opt in.
func TestEmbeddedDoctorOnDemand(t *testing.T) {
	writeEmbedded := func(embedPath string) string {
		data, err := defaultPrompts.ReadFile(embedPath)
		if err != nil {
			t.Fatalf("read embedded %s: %v", embedPath, err)
		}
		path := filepath.Join(t.TempDir(), filepath.Base(embedPath))
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	doctorPath := writeEmbedded("prompts/crew/doctor.md")
	meta, _, err := ParsePromptFrontmatter(doctorPath)
	if err != nil {
		t.Fatalf("parse embedded doctor frontmatter: %v", err)
	}
	if !meta.HasField("auto_start") || meta.AutoStart {
		t.Errorf("embedded doctor should declare auto_start = false, got HasField=%v AutoStart=%v",
			meta.HasField("auto_start"), meta.AutoStart)
	}
	if !meta.HasField("restart_on_crash") || meta.RestartOnCrash {
		t.Errorf("embedded doctor should declare restart_on_crash = false, got HasField=%v RestartOnCrash=%v",
			meta.HasField("restart_on_crash"), meta.RestartOnCrash)
	}
	// The on-demand frontmatter must override the crew always-on default.
	if ResolveRestartOnCrash(doctorPath, TypeCrew) {
		t.Error("embedded doctor should resolve restart_on_crash = false for a crew agent")
	}

	// Mayor stays always-on by default.
	mayorPath := writeEmbedded("prompts/mayor.md")
	mayorMeta, _, err := ParsePromptFrontmatter(mayorPath)
	if err != nil {
		t.Fatalf("parse embedded mayor frontmatter: %v", err)
	}
	if !mayorMeta.AutoStart || !mayorMeta.RestartOnCrash {
		t.Errorf("embedded mayor should stay always-on, got AutoStart=%v RestartOnCrash=%v",
			mayorMeta.AutoStart, mayorMeta.RestartOnCrash)
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
		filepath.Join("templates", "polecat-triage.md"),
		filepath.Join("templates", "polecat-review.md"),
		filepath.Join("templates", "polecat-architect.md"),
		filepath.Join("pm", "pm-template.md"),
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
	if !strings.HasPrefix(string(data), promptStampPrefix) {
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

// TestPolecatTemplatesIncludeMailCheckCron locks in the requirement that
// every polecat template instructs the agent to register a mail-check cron at
// startup. Without this, polecats won't proactively read mail and the mayor
// can't reach them mid-task. See work item mg-c1d3.
func TestPolecatTemplatesIncludeMailCheckCron(t *testing.T) {
	templates := []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-qa.md",
		"prompts/templates/polecat-triage.md",
		"prompts/templates/polecat-review.md", "prompts/templates/polecat-architect.md",
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

// TestPolecatTemplateIncludesNotImplementedVerification locks in the
// "verify before treating a design as not-implemented" pre-flight rule.
// Origin: mg-a374's cleanup-pass polecat marked a shipped feature
// (`mg spend`) as "not implemented" because it never ran the CLI or grepped
// for the symbol. Without this rule a future cleanup-pass polecat could
// delete the rationale doc for a shipped feature on the same false premise.
// See mg-f1de.
func TestPolecatTemplateIncludesNotImplementedVerification(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/templates/polecat.md")
	if err != nil {
		t.Fatalf("read polecat.md: %v", err)
	}
	s := string(data)

	// The sub-step header phrase — pinned so the rule can't silently move
	// out of the protocol checklist into a less-prominent location.
	if !strings.Contains(s, `Verify "not implemented" claims before acting on them`) {
		t.Error(`polecat.md: expected the "Verify 'not implemented' claims before acting on them" sub-step in step 3`)
	}
	// The three concrete verification probes from the rule. Each must be
	// nameable so a polecat reading the template knows what action to take.
	if !strings.Contains(s, "canonical CLI") {
		t.Error("polecat.md: expected the canonical-CLI verification probe")
	}
	if !strings.Contains(s, "grep") {
		t.Error("polecat.md: expected the grep-the-named-symbol verification probe")
	}
	if !strings.Contains(s, "on-disk artifact") {
		t.Error("polecat.md: expected the on-disk-artifact verification probe")
	}
	// The framing that a positive check means "archeology, not plan" —
	// this is the load-bearing conclusion of the rule, not just the probes.
	if !strings.Contains(s, "archeology") {
		t.Error("polecat.md: expected the `archeology` framing for shipped-but-documented features")
	}
}

// TestTriageTemplateInvestigateAndRecommendOnly locks in the polecat-triage
// template's contract from the gh-issue workflow design (mg-be91,
// docs/design/gh-issue-workflow-design.md §5–6): triage polecats read the
// GitHub issue named in their work item, investigate the codebase, consult
// the product PM synchronously, and report a structured recommendation —
// they never implement, push, or submit to the refinery.
func TestTriageTemplateInvestigateAndRecommendOnly(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/templates/polecat-triage.md")
	if err != nil {
		t.Fatalf("read polecat-triage.md: %v", err)
	}
	s := string(data)

	// Read-only worktree posture: the frontmatter still requests a worktree
	// (an isolated, current checkout to investigate from), but the body must
	// carry the no-code rule — the defining difference from polecat.md.
	if !strings.Contains(s, "worktree = true") {
		t.Error("polecat-triage.md: expected `worktree = true` frontmatter")
	}
	if !strings.Contains(s, "You do not write code") {
		t.Error("polecat-triage.md: expected the `You do not write code` principle")
	}
	// The template must never instruct a refinery submission; the only
	// allowed mention is the prohibition itself.
	if strings.Contains(s, "pogo refinery submit polecat-") {
		t.Error("polecat-triage.md: must not contain a refinery submit command")
	}

	// Reads the GH issue referenced in the work item body, and acks it —
	// the one permitted issue-write during triage (pm-pogo consult,
	// 2026-07-05).
	if !strings.Contains(s, "gh issue view") {
		t.Error("polecat-triage.md: expected the `gh issue view` step")
	}
	if !strings.Contains(s, "gh: <owner>/<repo>#<n>") {
		t.Error("polecat-triage.md: expected the `gh: <owner>/<repo>#<n>` issue-reference convention")
	}
	if !strings.Contains(s, "gh issue comment") {
		t.Error("polecat-triage.md: expected the claim-time ack comment step")
	}

	// Synchronous PM consult before finalizing the recommendation.
	if !strings.Contains(s, "pm-pogo") {
		t.Error("polecat-triage.md: expected the pm-pogo consult step")
	}

	// Structured recommendation keys in the mg done result, per pm-pogo's
	// authoritative format (owner of the quality bar).
	for _, key := range []string{`"workflow"`, `"issue"`, `"kind"`, `"recommendation"`, `"proposed_approach"`, `"effort"`, `"open_questions"`, `"checked"`, `"reproduced"`, `"duplicates"`, `"proposed_public_reply"`} {
		if !strings.Contains(s, key) {
			t.Errorf("polecat-triage.md: expected %s key in the structured mg done result", key)
		}
	}
	// The full verdict vocabulary, including the polite already-works close.
	if !strings.Contains(s, "implement|wontfix|needs-info|duplicate|already-works") {
		t.Error("polecat-triage.md: expected the full recommendation vocabulary")
	}
}

// TestReviewTemplateProtocol pins the load-bearing pieces of the reviewer
// polecat protocol (docs/design/gh-issue-workflow-design.md §6, mg-546c):
// the three review lenses in order, the dual-channel output (gh pr comment
// for humans, mg done verdict JSON as the record), the same-identity
// prohibition on `gh pr review`, and the 3-round modify↔review cap with
// coordinator escalation. A stray edit dropping any of these silently
// changes the gh-issue workflow's termination guarantees.
func TestReviewTemplateProtocol(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/templates/polecat-review.md")
	if err != nil {
		t.Fatalf("read polecat-review.md: %v", err)
	}
	s := string(data)

	// The three lenses, present and in order.
	qa := strings.Index(s, "QA — build and tests actually run")
	arch := strings.Index(s, "Architecture — fits the codebase")
	faith := strings.Index(s, "Design-faithfulness — the diff matches the approved recommendation")
	if qa < 0 || arch < 0 || faith < 0 {
		t.Fatalf("polecat-review.md: missing review lens heading(s): qa=%d arch=%d faith=%d", qa, arch, faith)
	}
	if !(qa < arch && arch < faith) {
		t.Errorf("polecat-review.md: lenses out of order: qa=%d arch=%d faith=%d (want QA < architecture < design-faithfulness)", qa, arch, faith)
	}

	// Design-faithfulness must name its two failure modes.
	for _, want := range []string{"scope creep", "silent omissions"} {
		if !strings.Contains(s, want) {
			t.Errorf("polecat-review.md: expected design-faithfulness lens to flag %q", want)
		}
	}

	// Dual-channel output: PR comment for visibility, mg verdict as the record.
	if !strings.Contains(s, "gh pr comment") {
		t.Error("polecat-review.md: expected `gh pr comment` as the PR-visible channel")
	}
	if !strings.Contains(s, "never `gh pr review`") {
		t.Error("polecat-review.md: expected the same-identity prohibition on `gh pr review`")
	}
	if !strings.Contains(s, `"verdict": "pass"`) || !strings.Contains(s, `"verdict": "fail"`) {
		t.Error("polecat-review.md: expected mg done verdict JSON for both pass and fail")
	}
	// Advisory findings must survive in the verdict of record, not just in
	// mail and PR comments (pm-pogo sign-off condition, mg-546c).
	if !strings.Contains(s, `"advisory":`) {
		t.Error("polecat-review.md: expected the pass verdict JSON to carry an `advisory` array")
	}

	// Loop protocol: findings mailed to the builder directly, round status and
	// verdict transitions to the coordinator, 3-round cap with escalation.
	if !strings.Contains(s, "mg mail send <build-ticket-id>") {
		t.Error("polecat-review.md: expected findings mailed directly to the builder polecat")
	}
	if !strings.Contains(s, "round 3 ends without a pass") {
		t.Error("polecat-review.md: expected the 3-round cap termination exit")
	}
	if !strings.Contains(s, `"rounds": 3`) {
		t.Error("polecat-review.md: expected the round-cap fail verdict to record rounds=3")
	}
	// mg done must be gated to terminal verdicts only.
	if !strings.Contains(s, "Do **not** call `mg done`") {
		t.Error("polecat-review.md: expected the mid-loop mg-done prohibition")
	}
}

// TestPMTemplateIncludesSweepCronEntries locks in the requirement that the
// PM template instructs each PM to register two sweep crons (09:00 and 17:00
// local) on startup. Without these, PMs have no twice-daily cadence — the
// pogod-internal cron was removed (mg-ddc1), so each PM self-schedules via
// CronCreate, mirroring the polecat mail-check pattern. See work item mg-8e32.
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

// TestPMTemplateIncludesRoadmapRegen locks in the requirement that the PM
// template instructs each PM to regenerate <product-repo>/docs/roadmap.md on
// every sweep, with the standard skeleton, and to commit + push it as the
// narrow exception to the no-direct-push rule. Without these, sweeps stop
// producing the roadmap artifact end-to-end and PM digests have nothing to
// link to. Verified live in mg-00b7; pinned here so future edits to
// pm-template.md can't silently drop the regen step (the bug mg-ec77 fixed at
// the propagation layer would re-emerge if the source itself lost the
// instruction). See work item mg-a7b8 (regen feature) and mg-00b7 (gate).
func TestPMTemplateIncludesRoadmapRegen(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/pm/pm-template.md")
	if err != nil {
		t.Fatalf("read pm-template.md: %v", err)
	}
	s := string(data)

	// Section header that flags the regen step in the synthesized prompt.
	if !strings.Contains(s, "### Regenerate roadmap.md each sweep") {
		t.Error("pm-template.md: expected `### Regenerate roadmap.md each sweep` header")
	}
	// The artifact path is referenced through a placeholder so each PM
	// resolves it against its own product repo.
	if !strings.Contains(s, "<your-product-repo>/docs/roadmap.md") {
		t.Error("pm-template.md: expected `<your-product-repo>/docs/roadmap.md` artifact reference")
	}
	// The skeleton's required buckets — these are what the digest links into.
	for _, bucket := range []string{
		"## Now (in flight)",
		"## Next (queued, available)",
		"## Later (proposed)",
		"## Backlog (open but no near-term plan)",
		"## Recently shipped (last 7d)",
		"## Trajectory",
	} {
		if !strings.Contains(s, bucket) {
			t.Errorf("pm-template.md: expected roadmap skeleton bucket %q", bucket)
		}
	}
	// The narrow push exception — the only file a PM may push directly.
	if !strings.Contains(s, "git push origin main") {
		t.Error("pm-template.md: expected the regen recipe to push to `origin main`")
	}
	if !strings.Contains(s, "git commit -m \"pm-") {
		t.Error("pm-template.md: expected the regen recipe to commit with a `pm-<name>:` message")
	}
}

// TestMayorPromptIncludesStallWatch locks in the requirement that the mayor
// prompt teaches the stall-watch loop introduced in mg-783f. Without these
// invariants, future edits could silently drop the wedged-session safety net
// that mg-60ca proved is necessary (a Claude session that hangs mid-conversation
// while its host process stays alive — restart-on-crash never fires).
func TestMayorPromptIncludesStallWatch(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	s := string(data)

	// The new section header.
	if !strings.Contains(s, "Stall-watch crew agents") {
		t.Error("mayor.md: expected `Stall-watch crew agents` step in the coordination loop")
	}
	// The heartbeat substrate — sweep.log mtime under the PM agent dir.
	if !strings.Contains(s, "~/.pogo/agents/pm/") || !strings.Contains(s, "sweep.log") {
		t.Error("mayor.md: expected sweep.log heartbeat reference (`~/.pogo/agents/pm/<name>/sweep.log`)")
	}
	// The thresholds — both nudge and restart bounds must be named so the
	// behavior is unambiguous. T_stall=90min and T_restart=120min are the
	// conservative defaults agreed in mg-783f.
	if !strings.Contains(s, "90 min") {
		t.Error("mayor.md: expected the `90 min` stall threshold")
	}
	if !strings.Contains(s, "120 min") {
		t.Error("mayor.md: expected the `120 min` restart threshold")
	}
	// Escalation path: nudge first, then stop+start.
	if !strings.Contains(s, "pogo agent stop") || !strings.Contains(s, "pogo agent start") {
		t.Error("mayor.md: expected restart escalation via `pogo agent stop` + `pogo agent start`")
	}
	// system_wake suppression — without it, every host wake triggers spurious
	// restarts before pogod's heartbeat can replay the agent's schedules.
	if !strings.Contains(s, "system_wake") {
		t.Error("mayor.md: expected `system_wake` suppression to prevent post-wake false positives")
	}
	if !strings.Contains(s, "pogo events list") {
		t.Error("mayor.md: expected `pogo events list` to query system_wake events")
	}
}

// TestMayorPromptIncludesDispatchDontImplement locks in the requirement that
// the mayor prompt carries a standalone `## Dispatch, don't implement` callout
// near the top, restating that mayor coordinates and polecats execute. Daniel's
// 2026-05-07 non-programmer onboarding feedback (mg-5c5b) flagged that mayor
// occasionally drifts into doing local file edits itself; the rule was
// implicit before and easy to lose in the surrounding coordination detail.
// Without these invariants, future edits could silently weaken the
// dispatch-only contract.
func TestMayorPromptIncludesDispatchDontImplement(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	s := string(data)

	// Section header — pinning the literal so the section can't be renamed
	// without an explicit test update.
	if !strings.Contains(s, "## Dispatch, don't implement") {
		t.Error("mayor.md: expected `## Dispatch, don't implement` standalone callout")
	}
	// The core executor/dispatcher framing — the line mayor must internalize.
	if !strings.Contains(s, "{{.Worker}} is the executor") || !strings.Contains(s, "you are the dispatcher") {
		t.Error("mayor.md: expected `{{.Worker}} is the executor; you are the dispatcher` framing")
	}
	// Carve-outs must be preserved so mayor doesn't over-correct and refuse
	// to do its actual coordination work (ticket edits, mail, read-only
	// diagnostics, polecat lifecycle). All four belong in the prompt.
	for _, marker := range []string{
		"Editing `mg` ticket bodies",
		"Mail to other agents",
		"Read-only diagnostics",
		"Spawning, nudging, stopping {{.Worker}}s",
	} {
		if !strings.Contains(s, marker) {
			t.Errorf("mayor.md: expected dispatch-rule carve-out marker %q", marker)
		}
	}
	// The "just fix" anti-pattern is the specific failure mode Daniel
	// surfaced — mayor jumping in to make a quick local edit instead of
	// dispatching. Pin the literal so the warning can't quietly drop.
	if !strings.Contains(s, "just fix") {
		t.Error("mayor.md: expected `just fix` anti-pattern callout in the dispatch rule")
	}
	// The rule must precede the Coordination Loop so it frames every
	// subsequent step. If it slips below the loop, it loses its priming role.
	dispatchIdx := strings.Index(s, "## Dispatch, don't implement")
	loopIdx := strings.Index(s, "## Coordination Loop")
	if dispatchIdx < 0 || loopIdx < 0 || dispatchIdx >= loopIdx {
		t.Errorf("mayor.md: expected `## Dispatch, don't implement` to precede `## Coordination Loop` (dispatchIdx=%d, loopIdx=%d)", dispatchIdx, loopIdx)
	}
}

// TestMayorPromptIncludesUserConfigRule locks in the requirement that the
// mayor prompt carries a standalone `## User setup is configuration, not a
// platform change` callout. Daniel's 2026-05-07 non-programmer onboarding
// feedback (mg-5c5b) flagged that mayor was misrouting user-side workflow
// setup as platform feature requests against pogo / macguffin source. The
// carve-out for genuine platform bugs must be preserved so this rule doesn't
// over-correct and silence real defect reports.
func TestMayorPromptIncludesUserConfigRule(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	s := string(data)

	// Section header — pinning the literal so the section can't be renamed
	// without an explicit test update.
	if !strings.Contains(s, "## User setup is configuration, not a platform change") {
		t.Error("mayor.md: expected `## User setup is configuration, not a platform change` standalone callout")
	}
	// The user-config locations the rule applies to. Pin each so a partial
	// rewrite can't drop one and silently reintroduce the failure mode for
	// that path.
	for _, marker := range []string{
		"~/.pogo/",
		"~/.config/pogo/",
		"~/.claude/CLAUDE.md",
	} {
		if !strings.Contains(s, marker) {
			t.Errorf("mayor.md: expected user-config path marker %q", marker)
		}
	}
	// The platform-ticket threshold — explicit user signal that the default
	// behavior is wrong, not just "could be easier." Pin both phrasings since
	// each captures a distinct part of the contract.
	if !strings.Contains(s, "broken in the pogo defaults") {
		t.Error("mayor.md: expected `broken in the pogo defaults` threshold for platform tickets")
	}
	if !strings.Contains(s, "ship for everyone") {
		t.Error("mayor.md: expected `ship for everyone` threshold for platform tickets")
	}
	// The carve-out for genuine platform bugs surfaced via user setup must
	// stay — otherwise a strict reading of the rule silences real defect
	// reports (e.g., `pogo init` producing a broken prompt).
	if !strings.Contains(s, "exposed platform bugs") && !strings.Contains(s, "exposes a real platform") && !strings.Contains(s, "uncovers a real platform defect") {
		t.Error("mayor.md: expected carve-out for platform bugs exposed by user setup")
	}
	// The rule must precede the Coordination Loop so it frames how mayor
	// triages user requests before the dispatch steps.
	cfgIdx := strings.Index(s, "## User setup is configuration")
	loopIdx := strings.Index(s, "## Coordination Loop")
	if cfgIdx < 0 || loopIdx < 0 || cfgIdx >= loopIdx {
		t.Errorf("mayor.md: expected user-config rule to precede `## Coordination Loop` (cfgIdx=%d, loopIdx=%d)", cfgIdx, loopIdx)
	}
}

// TestPMTemplateIncludesHeartbeat locks in the requirement that the PM template
// (a) instructs the mail-check schedule to refresh sweep.log on every fire and
// (b) documents mayor's stall-watch contract so PMs know they will be restarted
// if their heartbeat goes stale. Without these, mayor's stall-watch loop has
// nothing fresh to read and would constantly false-positive on every PM. See
// mg-783f.
func TestPMTemplateIncludesHeartbeat(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/pm/pm-template.md")
	if err != nil {
		t.Fatalf("read pm-template.md: %v", err)
	}
	s := string(data)

	// Mail-check nudge must include the heartbeat append. The literal token
	// `heartbeat (mail-check)` is the contract between pm-template and mayor:
	// changing it on either side without the other breaks stall-watch silently.
	if !strings.Contains(s, "heartbeat (mail-check)") {
		t.Error("pm-template.md: expected the mail-check nudge to append a `heartbeat (mail-check)` line to sweep.log")
	}
	// Section header that documents the contract for human readers and PMs.
	if !strings.Contains(s, "## {{.CoordinatorTitle}}'s stall-watch") {
		t.Error("pm-template.md: expected `## {{.CoordinatorTitle}}'s stall-watch` section documenting the contract")
	}
	// Both thresholds must be named so PMs can reason about how much slack
	// they have between mail-checks before mayor escalates.
	if !strings.Contains(s, "T_stall = 90 min") {
		t.Error("pm-template.md: expected `T_stall = 90 min` threshold")
	}
	if !strings.Contains(s, "T_restart = 120 min") {
		t.Error("pm-template.md: expected `T_restart = 120 min` threshold")
	}
	// Polecat warning — accidental clobbering of sweep.log silently breaks
	// the heartbeat contract. The acceptance criteria in mg-783f calls this
	// out explicitly.
	if !strings.Contains(s, "clobber sweep.log") {
		t.Error("pm-template.md: expected the `Don't clobber sweep.log` warning so polecats don't break the heartbeat")
	}
}

// TestPMTemplateIncludesProactivity locks in the requirement that the PM
// template carries a `## Self-pacing and proactivity` section with the five
// concrete behaviors that distinguish proactivity-driven PMs from passive,
// sweep-only PMs. Daniel's 2026-05-04 feedback ("pms need more self-drive,
// they dont want to self-pace and keep waiting, ensure they have the
// proactivity principle etc") drove mg-2f76 to encode the principle
// in-template; mg-1345 renamed it from "propulsion" to "proactivity" per gh
// #14 (CloverRoss + Daniel). Without these invariants, future edits could
// silently drop the proactivity framing and PMs would regress to the passive
// default.
func TestPMTemplateIncludesProactivity(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/pm/pm-template.md")
	if err != nil {
		t.Fatalf("read pm-template.md: %v", err)
	}
	s := string(data)

	// Section header — pinning the literal so the section can't be renamed
	// without an explicit test update.
	if !strings.Contains(s, "## Self-pacing and proactivity") {
		t.Error("pm-template.md: expected `## Self-pacing and proactivity` section")
	}
	// The canonical proactivity-principle one-liner (gh #14) must be present
	// as the floor, named so other prompts can reference it.
	if !strings.Contains(s, "proactivity-principle:") {
		t.Error("pm-template.md: expected the named `proactivity-principle:` canonical one-liner")
	}
	// The core principle, lifted from mayor's "Proactivity Principle":
	// PMs act on signal, not on cron.
	if !strings.Contains(s, "When you see signal, you act") {
		t.Error("pm-template.md: expected `When you see signal, you act` proactivity tagline")
	}
	// "Floor not ceiling" is the framing that re-positions sweeps as the
	// minimum cadence, not the gate on between-sweep work.
	if !strings.Contains(s, "floor") || !strings.Contains(s, "ceiling") {
		t.Error("pm-template.md: expected sweeps-as-floor-not-ceiling framing")
	}
	// Each of the five concrete behaviors must be pinned. Checking for a
	// distinguishing token from each behavior keeps the test resilient to
	// minor wording changes while catching accidental drops.
	for _, marker := range []string{
		"act on signal as it arrives",                // behavior 1
		"Self-paced filing during active arcs",       // behavior 2
		"Proactive backlog mining when idle",         // behavior 3
		"{{.CoordinatorTitle}} will not babysit you", // behavior 4
		"Stop-loss is proactivity too",               // behavior 5
	} {
		if !strings.Contains(s, marker) {
			t.Errorf("pm-template.md: expected proactivity behavior marker %q", marker)
		}
	}
	// The proactivity section must precede the scheduler-reaction section,
	// per mg-2f76's composition rule: PMs read "act on signal" first, then
	// the scheduler reaction is positioned as the catch-all for events that
	// don't have a more specific proactivity trigger.
	propIdx := strings.Index(s, "## Self-pacing and proactivity")
	schedIdx := strings.Index(s, "## Reacting to scheduler fires")
	if propIdx < 0 || schedIdx < 0 || propIdx >= schedIdx {
		t.Errorf("pm-template.md: expected `## Self-pacing and proactivity` to precede `## Reacting to scheduler fires` (propIdx=%d, schedIdx=%d)", propIdx, schedIdx)
	}
	// The legacy "propulsion" framing must be fully gone — mg-1345 renamed it.
	if strings.Contains(s, "propulsion") || strings.Contains(s, "Propulsion") {
		t.Error("pm-template.md: legacy `propulsion` wording should be gone after the mg-1345 rename to `proactivity`")
	}
	// The Cadence section's "Between sweeps" framing must reflect proactivity
	// (active on signal), not the prior passive "stay idle" wording. This is
	// the line Daniel's feedback most directly targeted.
	if !strings.Contains(s, "active on signal") {
		t.Error("pm-template.md: expected Cadence's `Between sweeps` line to say PMs remain `active on signal` (not `stay idle`)")
	}
	if strings.Contains(s, "Between sweeps you stay idle") {
		t.Error("pm-template.md: legacy `Between sweeps you stay idle` wording should be replaced — it contradicts the proactivity section")
	}
}

// TestDefaultPromptsUseProactivityPrinciple locks in the mg-1345 rename
// (gh #14, CloverRoss + Daniel): the canonical principle is "proactivity",
// not the legacy "propulsion" framing, and the named one-liner ships in
// mayor.md plus the crew/polecat prompts so it is referenceable everywhere.
func TestDefaultPromptsUseProactivityPrinciple(t *testing.T) {
	// mayor.md is the canonical home of the principle.
	mayor, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	ms := string(mayor)
	if !strings.Contains(ms, "## The Proactivity Principle") {
		t.Error("mayor.md: expected `## The Proactivity Principle` heading")
	}
	if !strings.Contains(ms, "proactivity-principle:") {
		t.Error("mayor.md: expected the named `proactivity-principle:` canonical one-liner")
	}

	// No default-shipped prompt may retain the legacy "propulsion" framing,
	// and each must carry the named principle so it can be referenced.
	for _, rel := range []string{
		"prompts/mayor.md",
		"prompts/pm/pm-template.md",
		"prompts/crew/doctor.md",
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-qa.md",
		"prompts/templates/polecat-triage.md",
		"prompts/templates/polecat-review.md", "prompts/templates/polecat-architect.md",
	} {
		data, err := defaultPrompts.ReadFile(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		s := string(data)
		if strings.Contains(s, "propulsion") || strings.Contains(s, "Propulsion") {
			t.Errorf("%s: legacy `propulsion` wording should be gone after the mg-1345 rename", rel)
		}
		if !strings.Contains(s, "proactivity-principle") {
			t.Errorf("%s: expected the named `proactivity-principle` so it is referenceable", rel)
		}
	}
}

// TestSynthesizeExtendsPrompt covers the PM crew-loader directive that lets a
// crew prompt redirect to a shared template plus a per-instance TOML config.
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

// TestSynthesizeExtendsPromptStripsHashStamps verifies that the pogo-prompt
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
	// Catches both v1 ("pogo-prompt: ") and legacy v0 ("pogo-prompt-hash: ")
	// shapes — the prefix below is contained in both.
	if strings.Contains(out, "pogo-prompt") {
		t.Errorf("synthesized prompt should not contain pogo-prompt stamps:\n%s", out)
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

	reg, err := NewRegistry(shortSocketDir(t))
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

// TestLoadDropInsAbsentDir confirms that a missing drop-in directory is not
// an error — drop-ins are an opt-in customization slot.
func TestLoadDropInsAbsentDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := LoadDropIns("mayor")
	if err != nil {
		t.Fatalf("LoadDropIns: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty result for missing dir, got %q", got)
	}
}

// TestLoadDropInsLexicalOrder confirms that fragments are concatenated in
// lexical filename order (the systemd / cron.d convention) so users can use
// numeric prefixes to control composition.
func TestLoadDropInsLexicalOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := DropInDir("mayor")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// Write in non-lexical order; expect lexical concatenation regardless.
	for name, body := range map[string]string{
		"50-middle.md": "## middle\n",
		"10-first.md":  "## first\n",
		"90-last.md":   "## last\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := LoadDropIns("mayor")
	if err != nil {
		t.Fatalf("LoadDropIns: %v", err)
	}
	if !strings.Contains(got, "## first") {
		t.Errorf("output missing first fragment:\n%s", got)
	}
	firstIdx := strings.Index(got, "## first")
	middleIdx := strings.Index(got, "## middle")
	lastIdx := strings.Index(got, "## last")
	if !(firstIdx < middleIdx && middleIdx < lastIdx) {
		t.Errorf("fragments not in lexical order: first=%d middle=%d last=%d\n%s",
			firstIdx, middleIdx, lastIdx, got)
	}
}

// TestLoadDropInsIgnoresNonMarkdown confirms that non-.md files and
// subdirectories are skipped — keeps the directory safe to use as a notes
// area as long as customizations end in .md.
func TestLoadDropInsIgnoresNonMarkdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := DropInDir("mayor")
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("kept\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadDropIns("mayor")
	if err != nil {
		t.Fatalf("LoadDropIns: %v", err)
	}
	if strings.Contains(got, "ignored") {
		t.Errorf("non-.md content should be skipped:\n%s", got)
	}
	if !strings.Contains(got, "kept") {
		t.Errorf("expected real.md content, got:\n%s", got)
	}
}

// TestSynthesizePromptMayorAppendsDropIns confirms that `pogo agent prompt
// show <coordinator>` (the show-side caller) renders the coordinator body plus
// any dropins/<coordinator>/*.md fragments, frontmatter stripped. The prompt
// file stays mayor.md, but it resolves under the coordinator's display name
// (default "ringmaster"), and drop-ins are keyed by that name.
func TestSynthesizePromptMayorAppendsDropIns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	mayorBody := "+++\nauto_start = true\n+++\n# Mayor\n\nBase mayor body.\n"
	if err := os.WriteFile(filepath.Join(PromptDir(), "mayor.md"), []byte(mayorBody), 0644); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("ringmaster")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "10-house.md"), []byte("## House style\n\nAlways prefer X.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := SynthesizePrompt("ringmaster", PreviewTemplateVars())
	if err != nil {
		t.Fatalf("SynthesizePrompt: %v", err)
	}
	if strings.Contains(got, "+++") {
		t.Errorf("frontmatter must be stripped from synthesized output:\n%s", got)
	}
	if !strings.Contains(got, "Base mayor body.") {
		t.Errorf("expected base body, got:\n%s", got)
	}
	if !strings.Contains(got, "House style") {
		t.Errorf("expected drop-in fragment appended, got:\n%s", got)
	}
	if strings.Index(got, "Base mayor body.") >= strings.Index(got, "House style") {
		t.Errorf("drop-in must come after base, got:\n%s", got)
	}
}

// TestSynthesizePromptCrewWithExtends covers the case where a crew prompt is
// an `extends ... with config ...` redirect. The synthesized output should
// inline the template + config, then append any drop-ins keyed by the crew
// agent name (the user-facing name, not the underlying template).
func TestSynthesizePromptCrewWithExtends(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	pmDir := filepath.Join(PromptDir(), "pm")
	if err := os.MkdirAll(pmDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pmDir, "pm-template.md"),
		[]byte("# PM Template\n\nYou are a PM.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pmDir, "pogo.toml"),
		[]byte("name = \"pm-pogo\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(CrewPromptDir(), "pm-pogo.md"),
		[]byte("extends pm-template with config pm/pogo.toml\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("pm-pogo")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "10-extra.md"),
		[]byte("## extra rule\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := SynthesizePrompt("pm-pogo", PreviewTemplateVars())
	if err != nil {
		t.Fatalf("SynthesizePrompt: %v", err)
	}
	for _, want := range []string{"PM Template", "You are a PM.", "Your configuration", "name = \"pm-pogo\"", "extra rule"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in synthesized output:\n%s", want, got)
		}
	}
}

// TestSynthesizePromptTemplateExpandsStubs confirms that polecat templates
// are run through {{.Var}} substitution with the preview stubs and that
// drop-ins for templates land before expansion (so fragment text can also
// reference template vars if it wants to).
func TestSynthesizePromptTemplateExpandsStubs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	tmplBody := "+++\nworktree = true\n+++\n" +
		"# Polecat\n\nWork item: {{.Id}}\nRepo: {{.Repo}}\n"
	if err := os.WriteFile(filepath.Join(TemplateDir(), "polecat.md"),
		[]byte(tmplBody), 0644); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("polecat")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "20-rules.md"),
		[]byte("## House polecat rules\n\nAdditional guidance for {{.Id}}.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := SynthesizePrompt("polecat", PreviewTemplateVars())
	if err != nil {
		t.Fatalf("SynthesizePrompt: %v", err)
	}
	if strings.Contains(got, "{{.Id}}") {
		t.Errorf("template vars must be expanded, got:\n%s", got)
	}
	if !strings.Contains(got, "Work item: preview") {
		t.Errorf("expected stub Id in body:\n%s", got)
	}
	if !strings.Contains(got, "Repo: /path/to/repo") {
		t.Errorf("expected stub Repo in body:\n%s", got)
	}
	if !strings.Contains(got, "House polecat rules") {
		t.Errorf("expected drop-in appended:\n%s", got)
	}
	if !strings.Contains(got, "Additional guidance for preview.") {
		t.Errorf("drop-in template vars must also expand:\n%s", got)
	}
}

// TestSynthesizePromptUnknownName confirms that an unknown prompt name
// produces an error so `pogo agent prompt show <unknown>` exits non-zero.
func TestSynthesizePromptUnknownName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	_, err := SynthesizePrompt("nope-not-here", PreviewTemplateVars())
	if err == nil {
		t.Error("expected error for unknown prompt name")
	}
}

// TestSynthesizePromptResolutionPriority confirms the documented mayor →
// crew → template precedence — a name that exists as both a crew prompt and
// a template resolves to the crew prompt first.
func TestSynthesizePromptResolutionPriority(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(CrewPromptDir(), "shared.md"),
		[]byte("# Crew shared\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(TemplateDir(), "shared.md"),
		[]byte("# Template shared\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := SynthesizePrompt("shared", PreviewTemplateVars())
	if err != nil {
		t.Fatalf("SynthesizePrompt: %v", err)
	}
	if !strings.Contains(got, "Crew shared") {
		t.Errorf("expected crew prompt to win when both exist:\n%s", got)
	}
	if strings.Contains(got, "Template shared") {
		t.Errorf("template body should not have leaked through:\n%s", got)
	}
}

// TestSynthesizeExtendsPromptDropInsOnly confirms the spawn-time crew loader
// (StartCrewAgent → SynthesizeExtendsPrompt) writes a synthesized file when
// the prompt has no `extends` directive but drop-ins exist. Without this
// wiring, mayor-side and crew-side drop-ins would only be visible via
// `pogo agent prompt show`, not at spawn.
func TestSynthesizeExtendsPromptDropInsOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	mayorBody := "+++\nauto_start = true\nnudge_on_start = \"go\"\n+++\n# Mayor\n\nBase mayor body.\n"
	mayorPath := filepath.Join(PromptDir(), "mayor.md")
	if err := os.WriteFile(mayorPath, []byte(mayorBody), 0644); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("mayor")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "10-house.md"), []byte("## House style\n\nAlways prefer X.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "synth.md")
	got, err := SynthesizeExtendsPrompt(mayorPath, outPath)
	if err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	if got != outPath {
		t.Fatalf("expected synthesized path %q, got %q", outPath, got)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "Base mayor body.") {
		t.Errorf("base body missing from synthesized output:\n%s", out)
	}
	if !strings.Contains(out, "House style") {
		t.Errorf("drop-in fragment missing from synthesized output:\n%s", out)
	}
	if strings.Index(out, "Base mayor body.") >= strings.Index(out, "House style") {
		t.Errorf("drop-in must be appended after base, got:\n%s", out)
	}
	// Frontmatter must survive the merge — StartCrewAgent re-parses the
	// synthesized file to pick up nudge_on_start, restart_on_crash, etc.
	meta, _, err := ParsePromptFrontmatter(outPath)
	if err != nil {
		t.Fatalf("ParsePromptFrontmatter on synthesized file: %v", err)
	}
	if !meta.AutoStart {
		t.Errorf("synthesized file lost auto_start frontmatter")
	}
	if meta.NudgeOnStart != "go" {
		t.Errorf("synthesized file lost nudge_on_start, got %q", meta.NudgeOnStart)
	}
}

// TestSynthesizeExtendsPromptExtendsAndDropIns confirms that an `extends`
// crew prompt picks up drop-ins keyed on the crew agent's filename stem,
// applied after the template+config inline.
func TestSynthesizeExtendsPromptExtendsAndDropIns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	pmDir := filepath.Join(PromptDir(), "pm")
	if err := os.MkdirAll(pmDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pmDir, "pm-template.md"),
		[]byte("# PM Template\n\nYou are a PM.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pmDir, "pogo.toml"),
		[]byte("name = \"pm-pogo\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	crewPath := filepath.Join(CrewPromptDir(), "pm-pogo.md")
	if err := os.WriteFile(crewPath,
		[]byte("extends pm-template with config pm/pogo.toml\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("pm-pogo")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "20-rule.md"),
		[]byte("## extra rule\n"), 0644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "synth.md")
	if _, err := SynthesizeExtendsPrompt(crewPath, outPath); err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{"PM Template", "You are a PM.", "Your configuration", "name = \"pm-pogo\"", "extra rule"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in synthesized output:\n%s", want, out)
		}
	}
	if strings.Index(out, "Your configuration") >= strings.Index(out, "extra rule") {
		t.Errorf("drop-in must be appended after extends merge, got:\n%s", out)
	}
}

// TestSynthesizeExtendsPromptDropInsLexicalOrder confirms the spawn-time
// loader honors lexical filename ordering (the systemd / cron.d convention)
// so users can sequence customizations with numeric prefixes.
func TestSynthesizeExtendsPromptDropInsLexicalOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	mayorPath := filepath.Join(PromptDir(), "mayor.md")
	if err := os.WriteFile(mayorPath, []byte("# Mayor\n\nbase\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("mayor")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"50-middle.md": "## middle\n",
		"10-first.md":  "## first\n",
		"90-last.md":   "## last\n",
	} {
		if err := os.WriteFile(filepath.Join(dropDir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	outPath := filepath.Join(t.TempDir(), "synth.md")
	if _, err := SynthesizeExtendsPrompt(mayorPath, outPath); err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	data, _ := os.ReadFile(outPath)
	out := string(data)
	first := strings.Index(out, "## first")
	middle := strings.Index(out, "## middle")
	last := strings.Index(out, "## last")
	if !(first >= 0 && middle > first && last > middle) {
		t.Errorf("drop-ins not in lexical order: first=%d middle=%d last=%d\n%s",
			first, middle, last, out)
	}
}

// TestSynthesizeExtendsPromptEmptyDropInDir confirms a created-but-empty
// drop-in directory is treated identically to an absent one — no synthesized
// file, return "" so the caller falls back to the original prompt.
func TestSynthesizeExtendsPromptEmptyDropInDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	mayorPath := filepath.Join(PromptDir(), "mayor.md")
	if err := os.WriteFile(mayorPath, []byte("# Mayor\n\nbase\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(DropInDir("mayor"), 0755); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "synth.md")
	got, err := SynthesizeExtendsPrompt(mayorPath, outPath)
	if err != nil {
		t.Fatalf("SynthesizeExtendsPrompt: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty result for empty drop-in dir, got %q", got)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("expected no synthesized file written, stat err = %v", err)
	}
}

// TestExpandTemplateAppliesDropIns confirms the spawn-time polecat loader
// (handleSpawnPolecat → ExpandTemplateToFile → ExpandTemplate) appends
// drop-ins from dropins/<basename>/*.md to the template body before
// {{.Var}} expansion, so fragment text can also reference template vars.
func TestExpandTemplateAppliesDropIns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	tmplPath := filepath.Join(TemplateDir(), "polecat.md")
	tmplBody := "+++\nworktree = true\n+++\n# Polecat\n\nWork item: {{.Id}}\n"
	if err := os.WriteFile(tmplPath, []byte(tmplBody), 0644); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("polecat")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "20-rules.md"),
		[]byte("## House polecat rules\n\nAdditional guidance for {{.Id}}.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ExpandTemplate(tmplPath, TemplateVars{Id: "mg-1234"})
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	if !strings.Contains(got, "Work item: mg-1234") {
		t.Errorf("expected base template body with var expansion:\n%s", got)
	}
	if !strings.Contains(got, "House polecat rules") {
		t.Errorf("expected drop-in fragment appended:\n%s", got)
	}
	if !strings.Contains(got, "Additional guidance for mg-1234.") {
		t.Errorf("drop-in template vars must also expand:\n%s", got)
	}
	if strings.Index(got, "Work item:") >= strings.Index(got, "House polecat rules") {
		t.Errorf("drop-in must come after base body:\n%s", got)
	}
}

// TestExpandTemplateNoDropIns confirms ExpandTemplate is a no-op for the
// drop-in pathway when the directory is absent — preserves the legacy
// behavior for templates without customizations.
func TestExpandTemplateNoDropIns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	tmplPath := filepath.Join(TemplateDir(), "polecat.md")
	if err := os.WriteFile(tmplPath, []byte("# Polecat\n\nTask: {{.Task}}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := ExpandTemplate(tmplPath, TemplateVars{Task: "do thing"})
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	want := "# Polecat\n\nTask: do thing\n"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// TestExpandTemplateMultipleDropIns confirms multiple drop-in fragments are
// concatenated in lexical order — the spawn-time mirror of
// TestLoadDropInsLexicalOrder for the polecat path.
func TestExpandTemplateMultipleDropIns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	tmplPath := filepath.Join(TemplateDir(), "polecat.md")
	if err := os.WriteFile(tmplPath, []byte("# Polecat\n\nbase\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("polecat")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"30-second.md": "## second\n",
		"10-first.md":  "## first\n",
		"50-third.md":  "## third\n",
	} {
		if err := os.WriteFile(filepath.Join(dropDir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ExpandTemplate(tmplPath, TemplateVars{})
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	first := strings.Index(got, "## first")
	second := strings.Index(got, "## second")
	third := strings.Index(got, "## third")
	if !(first >= 0 && second > first && third > second) {
		t.Errorf("drop-ins not in lexical order: first=%d second=%d third=%d\n%s",
			first, second, third, got)
	}
}

// TestInstallPromptsDoesNotTouchDropIns confirms `pogo agent prompt install`
// (and `--force`) leave the user-owned dropins/ tree alone. Locks in the
// design contract: drop-ins are wholly user-owned; install never reads or
// writes there.
func TestInstallPromptsDoesNotTouchDropIns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("mayor")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	dropFile := filepath.Join(dropDir, "10-house.md")
	original := []byte("## user-owned\n\nDo not stomp.\n")
	if err := os.WriteFile(dropFile, original, 0644); err != nil {
		t.Fatal(err)
	}
	origModTime := mustStat(t, dropFile).ModTime()
	// Sleep so a hypothetical rewrite would produce a distinguishable mtime.
	time.Sleep(10 * time.Millisecond)

	for _, force := range []bool{false, true} {
		if _, err := InstallPrompts(InstallOpts{Force: force}); err != nil {
			t.Fatalf("InstallPrompts(force=%v): %v", force, err)
		}
		got, err := os.ReadFile(dropFile)
		if err != nil {
			t.Fatalf("drop-in vanished after InstallPrompts(force=%v): %v", force, err)
		}
		if string(got) != string(original) {
			t.Errorf("drop-in modified by InstallPrompts(force=%v): got %q want %q",
				force, string(got), string(original))
		}
		if got := mustStat(t, dropFile).ModTime(); !got.Equal(origModTime) {
			t.Errorf("drop-in mtime changed by InstallPrompts(force=%v): got %v want %v",
				force, got, origModTime)
		}
	}
}

// TestInitPromptsDoesNotTouchDropIns confirms `pogo init` (with --force) is
// strict-but-narrow — it scaffolds shipped templates without disturbing
// user-authored drop-ins.
func TestInitPromptsDoesNotTouchDropIns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := InitPromptDirs(); err != nil {
		t.Fatal(err)
	}
	dropDir := DropInDir("polecat")
	if err := os.MkdirAll(dropDir, 0755); err != nil {
		t.Fatal(err)
	}
	dropFile := filepath.Join(dropDir, "20-rules.md")
	original := []byte("## drop-in\n")
	if err := os.WriteFile(dropFile, original, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := InitPrompts(true, false); err != nil {
		t.Fatalf("InitPrompts: %v", err)
	}
	got, err := os.ReadFile(dropFile)
	if err != nil {
		t.Fatalf("drop-in vanished after InitPrompts: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("drop-in modified by InitPrompts: got %q want %q",
			string(got), string(original))
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi
}

// TestShippedTemplatesNeverNameTheBranch pins the fix for mg-d39e: no shipped
// polecat template may construct a branch name out of {{.Id}}.
//
// The branch pogod actually creates is gitgc.BranchPrefix + spawnReq.Name (see
// api.go's polecat spawn path), but the template is only handed Id. Every
// coordinator dispatch spawns with name=<short> --id=mg-<short>, so a rendered
// "polecat-{{.Id}}" named polecat-mg-<short> while the real branch was
// polecat-<short> — wrong on 100% of dispatches, and it cost three merge
// cycles before anyone noticed (the polecats that merged cleanly did so by
// reading their own worktree instead of trusting the doc).
//
// Name is deliberately NOT plumbed into TemplateVars as the fix. A branch name
// in a prompt is a claim that can rot; the worktree is the observation. The
// templates tell the polecat to read the branch instead — so this test asserts
// the absence of the fabricated name, not the presence of a corrected one.
func TestShippedTemplatesNeverNameTheBranch(t *testing.T) {
	// Mirror a real coordinator dispatch: `spawn-polecat abea --id=mg-abea`.
	//
	// The branch prefix comes from gitgc.BranchPrefix, NOT from the worker's
	// display name — they are independent, and on this fleet they differ
	// (DefaultWorkerName is "pogocat"; the prefix is "polecat-"). Deriving the
	// expectation from the real constant is what makes this test catch
	// "{{.Worker}}-{{.Id}}" and not just the literal "polecat-{{.Id}}": with
	// the default worker name, "{{.Worker}}-{{.Id}}" renders "pogocat-mg-abea"
	// and would slip past a hardcoded "polecat-" string while still rendering
	// the fabricated branch on any fleet whose worker IS named "polecat"
	// (mg-564c found this hole with a template that did exactly that).
	// setWorker pins the worker name to the value that makes the bug visible.
	setWorker(t, strings.TrimSuffix(gitgc.BranchPrefix, "-"))
	const name, id = "abea", "mg-abea"
	realBranch := gitgc.BranchPrefix + name // what pogod checks out
	fabricated := gitgc.BranchPrefix + id   // what "polecat-{{.Id}}" used to render

	for _, tmplName := range []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-qa.md",
		"prompts/templates/polecat-build-pr.md",
		"prompts/templates/polecat-triage.md",
		"prompts/templates/polecat-review.md", "prompts/templates/polecat-architect.md",
	} {
		data, err := defaultPrompts.ReadFile(tmplName)
		if err != nil {
			t.Fatalf("read embedded %s: %v", tmplName, err)
		}
		_, body, err := parsePromptFrontmatterBytes(data)
		if err != nil {
			t.Fatalf("parse frontmatter %s: %v", tmplName, err)
		}
		tmpl, err := template.New(tmplName).Parse(body)
		if err != nil {
			t.Fatalf("parse template %s: %v", tmplName, err)
		}
		var buf bytes.Buffer
		vars := withDefaults(TemplateVars{
			Id:          id,
			Repo:        "/path/to/repo",
			WorktreeDir: "/path/to/worktree",
			Provider:    "claude",
		})
		if err := tmpl.Execute(&buf, vars); err != nil {
			t.Fatalf("execute template %s: %v", tmplName, err)
		}
		out := buf.String()

		if strings.Contains(out, fabricated) {
			t.Errorf("%s: renders branch %q, but pogod creates %q — "+
				"the template must not name the branch; tell the polecat to read it "+
				"with `git rev-parse --abbrev-ref HEAD`", tmplName, fabricated, realBranch)
		}
	}
}

// TestShippedPolecatTemplatesTeachBranchObservation is the other half of
// mg-d39e: the branch-using templates must hand the polecat the way to observe
// its branch, not just stay silent about the name.
func TestShippedPolecatTemplatesTeachBranchObservation(t *testing.T) {
	// Only the templates whose protocol pushes a branch need this.
	for _, tmplName := range []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-build-pr.md",
		"prompts/templates/polecat-architect.md",
	} {
		data, err := defaultPrompts.ReadFile(tmplName)
		if err != nil {
			t.Fatalf("read embedded %s: %v", tmplName, err)
		}
		_, body, err := parsePromptFrontmatterBytes(data)
		if err != nil {
			t.Fatalf("parse frontmatter %s: %v", tmplName, err)
		}
		tmpl, err := template.New(tmplName).Parse(body)
		if err != nil {
			t.Fatalf("parse template %s: %v", tmplName, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, withDefaults(TemplateVars{
			Id: "mg-abea", Repo: "/path/to/repo", WorktreeDir: "/path/to/worktree", Provider: "claude",
		})); err != nil {
			t.Fatalf("execute template %s: %v", tmplName, err)
		}
		out := buf.String()

		if !strings.Contains(out, "git rev-parse --abbrev-ref HEAD") {
			t.Errorf("%s: must teach the polecat to read its branch with "+
				"`git rev-parse --abbrev-ref HEAD`", tmplName)
		}
	}
}

// TestArchitectTemplateNoticesRatherThanRules pins the design constraint that
// is the entire reason polecat-architect.md ships in the shape it does
// (mg-564c, from the mg-945c design).
//
// The standing architect, asked to judge its own dispatchable twin, answered
// against its own interest: "a day-one architect isn't merely less useful —
// it's differently risky. It has authority without evidence. My rulings were
// good largely because I could check them against accumulated evidence, and
// the ones I got wrong were exactly the ones I ruled from priors instead of
// looking. A fresh architect has nothing BUT priors. It will be fluent,
// confident, and unable to check itself — and fluency is what makes that
// failure mode survive review."
//
// The mitigation is scope, not tone: a fresh instance's first job is NOTICING
// that a question exists, not RULING on it. A template that opens with
// confident rulings is the failure mode. These strings are load-bearing — if
// the wording changes, update this test deliberately rather than letting the
// constraint erode into generic review boilerplate.
func TestArchitectTemplateNoticesRatherThanRules(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/templates/polecat-architect.md")
	if err != nil {
		t.Fatalf("read polecat-architect.md: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		// The one sentence that defines the role.
		"A reactive architect answers questions; a standing one notices that a question exists.",
		// The named risk, not a softened paraphrase of it.
		"authority but without evidence",
		"nothing but priors",
		// The permanent-condition framing. A polecat never accumulates
		// context, so day-one risk is not a transient the template grows out
		// of — it is the operating condition of every dispatch, forever. The
		// standing architect only saw this when asked to judge its own draft:
		// "a standing architect ramps; this one is day one every single time,
		// and my draft opens by telling that fresh context it is the
		// authority. That's the worst possible line in the worst possible
		// place." Softening this to "you may lack context" loses the point.
		"you never will",
		"day one, every time",
		// Fluency is what makes the failure mode survive review.
		"Fluency is not evidence",
		// The design constraint itself.
		"NOTICING, not RULING",
		// Noticing is a legitimate terminal output, not a failure to rule.
		"is a complete and valuable answer",
		// The anchoring rule that operationalizes it.
		"file:line",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("polecat-architect.md: missing honest-limit constraint %q", want)
		}
	}

	// The advisory verdict must carry an explicit place to record what the
	// architect did NOT check. Without it, "I couldn't verify this" has no
	// home in the output and silently becomes a confident claim.
	if !strings.Contains(body, `"unchecked"`) {
		t.Error("polecat-architect.md: advisory result JSON must carry an `unchecked` field")
	}
}

// TestArchitectTemplateRequiresMeasuringReusedPredicates pins the counting rule
// (mg-d6ec): a verdict that proposes REUSING or SCOPING BY an existing
// predicate must MEASURE it against the population it would govern, report the
// count, and state whether that population is stationary.
//
// The rule is not speculative. On 2026-07-17 three agents made the identical
// error inside one hour, each holding different advantages: a polecat-architect
// read every call site and was right about all of it, then recommended a
// predicate matching 0 of the 14 queued items; the mayor caught that, then
// wrote an acceptance bar already satisfied 9x that day; the standing architect
// scoped a fix "for the whole class at once" that covered 32 of 63 nested repos
// — a count that was 67 fifteen minutes later, because dispatching polecats is
// what grows it.
//
// The architect's own conclusion, ruling on a failure it had just committed:
// "Fresh context wasn't the variable. The polecat and I failed the same way
// because reading is what produces the verdict and counting is a separate act
// that nothing forces." Hence the rule binds the VERDICT, not the author, and
// hence reading-every-call-site is named as the substitute rather than left to
// be inferred — it is the one the model reaches for.
//
// Both halves are load-bearing. "32 of 63" and "32 of 63, growing ~3 per
// dispatch" argue for DIFFERENT fixes: the second rules out scoping-by-
// enumeration entirely. A count without stationarity can still recommend the
// wrong fix confidently, so the template must ask for both.
//
// This lands on the template and not on crew/architect.md or mayor.md by
// deliberate ruling: a template binds because dispatch instantiates it, per
// verdict, fresh; a crew prompt is read once at boot and then competes with
// everything else in a multi-hour context. The polecat is also the only agent
// that rules and then ACTS on its own ruling — mayor and architect are
// structurally forbidden from implementing their own verdicts, which guarantees
// a counter downstream of every ruling they make. The polecat has none.
//
// If the wording changes, update this test deliberately. In particular do NOT
// let the rule acquire an escape hatch ("consider measuring", "where
// practical") — all three failures above were committed by agents who would
// each have said they were being careful, and a rule with an escape hatch is a
// rule that reports PASS.
func TestArchitectTemplateRequiresMeasuringReusedPredicates(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/templates/polecat-architect.md")
	if err != nil {
		t.Fatalf("read polecat-architect.md: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		// The line that names why reading doesn't produce the count.
		"Looking finds a member. Only counting finds the population.",
		// The rule's two mandatory halves.
		"MEASURE it against the live population it would govern",
		"Whether that population is stationary",
		"name what moves it",
		// The substitute the model will reach for, named so it can't be
		// reached for silently.
		"Reading every call site is not a substitute",
		"Counting is a **separate act**",
		// The out, which must be explicit rather than silent.
		"mark the recommendation provisional",
		// No escape hatch.
		"a rule with an escape hatch is a rule that reports PASS",
		// Stationarity changes the recommendation rather than refining it.
		"argue for **different fixes**",
		"can still recommend the wrong fix",
		// Provenance: the rule arrives with its own falsification attached,
		// which is what makes it hard to wave through as boilerplate.
		"an architect who had just failed it",
		"0 of the 14 items",
		"already satisfied 9×",
		"63 nested repos, the fix covering 32",
		"67, not 63",
		// Why the verdict and not the author.
		"counting is a separate act that nothing forces",
		// Why the polecat and not the crew.
		"Judging doesn't touch the population; acting does.",
		"rules and then ACTS on your own ruling",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("polecat-architect.md: missing measure-the-population rule %q", want)
		}
	}

	// The rule needs somewhere to LAND in the machine-readable verdict, or it
	// stays prose the architect can agree with and not do. `measured` is to
	// this rule what `unchecked` is to the honest-limits rule.
	if !strings.Contains(body, `"measured"`) {
		t.Error("polecat-architect.md: advisory result JSON must carry a `measured` field for the count + stationarity")
	}

	// The escape hatches the ticket explicitly refused. These are the exact
	// softenings a well-meaning edit reaches for.
	for _, hatch := range []string{"consider measuring", "where practical"} {
		if strings.Contains(strings.ToLower(body), hatch) {
			t.Errorf("polecat-architect.md: the counting rule must not be weakened to %q", hatch)
		}
	}
}

// TestArchitectTemplateDefersPRReviewToReviewTemplate pins the non-duplication
// boundary that made this template shippable at all (mg-564c; the question
// mg-abea's evidence raised).
//
// polecat-review.md already reviews PRs through an explicit architecture lens,
// against the approved recommendation as its contract. The architect draft's
// original "shape C — design-correctness review gate" duplicated exactly that,
// and duplicated it worse: review checks a diff against a stated agreement
// (evidence), where a fresh architect would check it against priors. Shape C
// was cut. The architect's domain is the design question that exists BEFORE
// there is a diff; once code exists, polecat-review owns it.
//
// If this boundary blurs, the two templates drift into competing PR reviewers
// with different contracts — so assert both the deferral and the absence of a
// resurrected shape C.
func TestArchitectTemplateDefersPRReviewToReviewTemplate(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/templates/polecat-architect.md")
	if err != nil {
		t.Fatalf("read polecat-architect.md: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		"You are not a PR reviewer",
		"polecat-review",
		"There is no shape C",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("polecat-architect.md: missing review-boundary marker %q", want)
		}
	}

	// The three surviving shapes are all pre-diff. A resurrected "shape C"
	// heading is the specific regression this guards.
	if strings.Contains(body, "**C.") {
		t.Error("polecat-architect.md: shape C was cut as duplicative of polecat-review; do not resurrect it")
	}
}

// TestMayorRoutesOnTypeMarkerNotInference pins the type->template routing rule
// (mg-7150) and, more importantly, the reason it dispatches on a marker rather
// than on meaning.
//
// mayor.md's only other template-routing rule keys on `workflow: gh-issue` — a
// structural marker the filer writes. The one place the system DOES classify
// semantically (polecat-triage's kind/recommendation/effort) feeds a HUMAN
// gate, never dispatch. The system's position is: markers route, semantics
// inform humans. An inferred design-detector would be the first crossing of
// that line.
//
// It must stay a marker because the two misroutes are asymmetric, and only one
// is silent: a design item sent to the build polecat gets implemented, PR'd,
// and MERGED — the design question answered by whatever got built. A build
// item sent to the architect wastes one loud, harmless cycle. A rule that
// guesses trades the cheap loud failure for the expensive silent one, so the
// default stays `polecat` and architect is strictly opt-in.
//
// These strings are load-bearing. If the wording changes, update this test
// deliberately — do not let the constraint erode into "detect design-shaped
// tickets", which is the exact thing it forbids.
func TestMayorRoutesOnTypeMarkerNotInference(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	body := string(data)

	// The rule itself: type selects the template for single-shot work.
	for _, want := range []string{
		"`design` | `--template=polecat-architect`",
		"`qa` | `--template=polecat-qa`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: missing type->template routing rule %q", want)
		}
	}

	// The prohibition on inferring, and the asymmetry that justifies it.
	for _, want := range []string{
		"Route on the `type` marker only — never on what the ticket looks like",
		"Silent, and it lands code",
		"Loud and harmless",
		"Markers route; semantics inform humans",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: missing marker-not-inference constraint %q", want)
		}
	}

	// The default must remain opt-in. If the architect ever becomes the
	// default for un-typed work, the silent failure mode above is live.
	if !strings.Contains(body, "anything else (default `task`)") {
		t.Error("mayor.md: type table must name the default `task` route; architect stays opt-in")
	}
}

// TestMayorDispatchesQAItemsToQATemplate guards a live bug this rule fixed
// (mg-7150): mayor.md's step-4 QA prose used to say a `--type=qa` item "will be
// dispatched to a new polecat like any other work item" — i.e. the DEFAULT
// code-writing template. `--template=polecat-qa` appeared nowhere in this
// prompt tree outside the template's own self-description, so polecat-qa was
// dispatched by nobody: QA items got the build template and the QA template
// shipped dead. The step-4 prose must keep pointing at the type table.
func TestMayorDispatchesQAItemsToQATemplate(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	body := string(data)

	if !strings.Contains(body, "mg new --type=qa") {
		t.Fatal("mayor.md: expected step 4 to create QA items with --type=qa")
	}
	if !strings.Contains(body, "never** get the default build template") {
		t.Error("mayor.md: step-4 QA prose must forbid the default build template for QA items")
	}
	// The regression: prose that hands QA off to the generic dispatch path
	// without naming the template it lands on.
	if strings.Contains(body, "dispatched to a new {{.Worker}} like any other work item") {
		t.Error("mayor.md: step-4 QA prose reverted to the generic-dispatch wording that routed QA items to the build template (mg-7150)")
	}
}
