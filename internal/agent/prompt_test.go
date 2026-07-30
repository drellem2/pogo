package agent

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/testsandbox"
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
		"Refinery submission happens later, by the " + DefaultCoordinatorName,
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

// TestMayorGHIssueTriageRetirement pins the retirement step for the gh-issue
// triage ticket (mg-7c95). The playbook used to close its GO branch with "The
// triage ticket is complete — archive it on your normal sweep", which is not a
// followable instruction: a body leading with `stage: triage` is filed carrying
// a `declares-remainder` tag on ANY type, `mg archive` refuses an item that is
// not done, and `mg done` refuses a declared item that names no successor. So
// both halves refuse and the sweep never retires the ticket. The `mg done
// --successor` line is the only form that works, and it must keep naming the
// build ticket — which is also what promotes that ticket out of pending/.
func TestMayorGHIssueTriageRetirement(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read embedded mayor.md: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		// The command, with the successor named.
		"mg done <triage ticket id> --successor=<build ticket id>",
		// The why, without which the flag reads as optional ceremony.
		"declares-remainder",
		// Which gate actually bites, so nobody re-derives it from `mg archive`.
		"`mg done` is where it bites",
		// The dependency mechanism the build ticket relies on.
		"done/ OR\n   # archive/ (both are scanned)",
		// The observable confirmation that the gate opened.
		"(pending → available)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: expected %q", want)
		}
	}

	// The bare-archive instruction must not come back. It is not merely
	// incomplete — followed literally it refuses twice and retires nothing.
	if strings.Contains(body, "The triage ticket is complete — archive it on your normal sweep") {
		t.Errorf("mayor.md: the bare-archive triage retirement instruction is back; " +
			"`mg archive` refuses an item that is not done, and `mg done` refuses " +
			"a declares-remainder item with no --successor")
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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	testsandbox.Isolate(t)

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
	tmpHome := testsandbox.Isolate(t).Home

	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("InstallPrompts: %v", err)
	}

	// Overwrite an arbitrary installed prompt with an out-of-date but
	// otherwise well-formed stamp: the recorded hash matches the on-disk
	// body (the file was NOT hand-edited) and simply predates the current
	// embed. This is the mg-ec77 shape — a stale shipped template — and it
	// must classify as "stale" (install-fixable), not "edited". A v0 stamp
	// records one hash for both embed and body, so the recorded hash must
	// equal contentHash(body) for the body to read as untouched; an
	// all-zeros hash would instead look hand-edited (mg-04ab).
	mayorPath := filepath.Join(tmpHome, ".pogo", "agents", "mayor.md")
	oldBody := "# Old mayor prompt\n"
	oldHash := contentHash([]byte(oldBody))
	stale := "<!-- pogo-prompt-hash: " + oldHash + " -->\n" + oldBody
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
	tmpHome := testsandbox.Isolate(t).Home

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

// driftReasonFor returns the Reason CheckPromptDrift reports for rel, or ""
// if rel is not in the drift set.
func driftReasonFor(t *testing.T, rel string) string {
	t.Helper()
	drift, err := CheckPromptDrift()
	if err != nil {
		t.Fatalf("CheckPromptDrift: %v", err)
	}
	for _, d := range drift {
		if d.Path == rel {
			return d.Reason
		}
	}
	return ""
}

// TestCheckPromptDriftEditedVsStaleRemediesWork is the mg-04ab acceptance test.
// It constructs BOTH drift states an advanced embed can produce, asserts they
// classify apart, and — the actual bar — RUNS the advised remedy for each and
// proves the file is no longer stale afterward. The pre-fix code labelled both
// "stale" and advised 'pogo agent prompt install' for both; for the hand-edited
// canonical that advice is a silent no-op (install declines, writes .dist), so a
// label-only test would reproduce the bug exactly.
func TestCheckPromptDriftEditedVsStaleRemediesWork(t *testing.T) {
	tmpHome := testsandbox.Isolate(t).Home

	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("InstallPrompts: %v", err)
	}
	// A healthy install must not report stale — the check must be able to
	// fail, not just always pass.
	if drift := driftReasonFor(t, "mayor.md"); drift != "" {
		t.Fatalf("healthy install reported mayor.md as %q, want no drift", drift)
	}

	agentsDir := filepath.Join(tmpHome, ".pogo", "agents")
	wrongEmbed := strings.Repeat("0", 64)

	// --- State (a): shipped template moved, canonical NOT hand-edited. ---
	// On-disk stamp records an old embed hash but a body hash that still
	// matches the on-disk body, i.e. the file was not touched by a human.
	stalePath := filepath.Join(agentsDir, "mayor.md")
	staleBody := "# Old shipped mayor prompt\n"
	staleBodyHash := contentHash([]byte(staleBody))
	staleStamp := "<!-- pogo-prompt: embed=sha256:" + wrongEmbed + " body=sha256:" + staleBodyHash + " -->\n"
	if err := os.WriteFile(stalePath, []byte(staleStamp+staleBody), 0644); err != nil {
		t.Fatal(err)
	}

	// --- State (b): canonical hand-edited AND embed moved. ---
	// Old embed hash, recorded body hash that no longer matches the (edited)
	// on-disk body. This is the state where 'install' declines.
	editedRel := filepath.Join("pm", "pm-template.md")
	editedPath := filepath.Join(agentsDir, editedRel)
	recordedBody := "# original shipped pm template\n"
	recordedBodyHash := contentHash([]byte(recordedBody))
	editedBody := "# original shipped pm template\nDaniel's local customization line.\n"
	editedStamp := "<!-- pogo-prompt: embed=sha256:" + wrongEmbed + " body=sha256:" + recordedBodyHash + " -->\n"
	if err := os.WriteFile(editedPath, []byte(editedStamp+editedBody), 0644); err != nil {
		t.Fatal(err)
	}

	// Both classify apart under the same check.
	if got := driftReasonFor(t, "mayor.md"); got != "stale" {
		t.Errorf("template-moved canonical: reason=%q, want %q", got, "stale")
	}
	if got := driftReasonFor(t, editedRel); got != "edited" {
		t.Errorf("hand-edited canonical: reason=%q, want %q", got, "edited")
	}
	if DriftInstallFixable("stale") != true || DriftInstallFixable("edited") != false {
		t.Fatalf("DriftInstallFixable mapping wrong: stale=%v edited=%v",
			DriftInstallFixable("stale"), DriftInstallFixable("edited"))
	}

	// --- Remedy for (a): 'pogo agent prompt install'. Must actually fix it. ---
	if _, err := InstallPrompts(InstallOpts{}); err != nil {
		t.Fatalf("remedy install: %v", err)
	}
	if got := driftReasonFor(t, "mayor.md"); got != "" {
		t.Errorf("after 'pogo agent prompt install', mayor.md still drifted as %q; the remedy must clear it", got)
	}

	// The SAME install run must NOT have cleared the edited canonical — it
	// declines and writes the sidecar instead. This is the trap the old code
	// walked into: install exits 0, and the edited file is still stale.
	if got := driftReasonFor(t, editedRel); got != "edited" {
		t.Fatalf("install unexpectedly changed edited canonical drift to %q; expected it to still be %q", got, "edited")
	}
	distPath := editedPath + ".dist"
	if _, err := os.Stat(distPath); err != nil {
		t.Fatalf("install should have written the .dist sidecar for the edited canonical: %v", err)
	}

	// --- Remedy for (b): reconcile <name> against <name>.dist. ---
	// The doctor advice names the .dist sidecar; adopting it (one valid
	// reconciliation) is a runnable stand-in for the human merge. After it,
	// the canonical carries the current embed stamp and is no longer stale.
	distData, err := os.ReadFile(distPath)
	if err != nil {
		t.Fatalf("read .dist: %v", err)
	}
	if err := os.WriteFile(editedPath, distData, 0644); err != nil {
		t.Fatalf("reconcile (adopt .dist): %v", err)
	}
	_ = os.Remove(distPath)
	if got := driftReasonFor(t, editedRel); got != "" {
		t.Errorf("after reconciling %s against its .dist, it still drifted as %q; the advised remedy must clear it", editedRel, got)
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
	tmpHome := testsandbox.Isolate(t).Home

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
	testsandbox.Isolate(t)

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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

	// Structured recommendation keys in the packet, per pm-pogo's authoritative
	// format (owner of the quality bar). `remainder` is mg-1912's addition: it
	// is what the coordinator turns into the successor at the gate, so a packet
	// without it leaves the coordinator inventing one.
	for _, key := range []string{`"workflow"`, `"issue"`, `"kind"`, `"recommendation"`, `"proposed_approach"`, `"effort"`, `"open_questions"`, `"checked"`, `"reproduced"`, `"duplicates"`, `"remainder"`, `"proposed_public_reply"`} {
		if !strings.Contains(s, key) {
			t.Errorf("polecat-triage.md: expected %s key in the structured recommendation packet", key)
		}
	}
	// The full verdict vocabulary, including the polite already-works close.
	if !strings.Contains(s, "implement|wontfix|needs-info|duplicate|already-works") {
		t.Error("polecat-triage.md: expected the full recommendation vocabulary")
	}

	// mg-1912: the packet must NOT be recorded with `mg done --result` on the
	// triage ticket. That ticket carries `declares-remainder` (mg emits the tag
	// from a body leading `stage: triage`), and `mg done` refuses a declared
	// item that names no successor — the successor being the build ticket, filed
	// after the human gate. The guard runs before the sidecar is written, so the
	// refusal discarded the packet outright. The packet goes on the ticket body,
	// where there is no such precondition; the coordinator lifts it into
	// `--result` at transition 3, when a successor finally exists.
	//
	// Asserted end to end against a real store by
	// TestTriagePacketIsWrittenBeforeAnySuccessorExists; asserted here as text
	// so a reworded step 8 that drops the mechanism is caught by the fast suite
	// too.
	if strings.Contains(s, "mg done {{.Id}} --result") {
		t.Error("polecat-triage.md: step 8 must not record the packet with " +
			"`mg done {{.Id}} --result` — the triage ticket declares a remainder and " +
			"no successor exists before the gate, so that call is refused (exit 4) " +
			"BEFORE any sidecar is written and the packet is lost (mg-1912)")
	}
	if !strings.Contains(s, "```json triage-packet") {
		t.Error("polecat-triage.md: expected the packet to be appended to the work " +
			"item body in a fenced ```json triage-packet block — that is the marker " +
			"mayor.md's transition-3 extractor keys on (mg-1912)")
	}
	if !strings.Contains(s, "--append-body-file") {
		t.Error("polecat-triage.md: expected `mg edit --append-body-file` to write the " +
			"packet; it composes against the body on disk, so it cannot clobber the " +
			"coordinator's `stage:` edits (mg-1912)")
	}
	if !strings.Contains(s, "Never invent a successor id") {
		t.Error("polecat-triage.md: expected the anti-fabrication rule — mg refuses a " +
			"successor naming no item, but a real-but-wrong id is accepted silently " +
			"and gates a live item on a ticket that can never complete (mg-1912)")
	}
}

// TestMayorRetiresTheTriageTicketWithTheWorkersOwnPacket is the consumer half of
// mg-1912. The triage worker's packet now lands on the triage ticket body, so
// mayor.md must (a) point the build worker at that block rather than at a result
// sidecar that did not exist, and (b) lift the block into `--result` when it
// retires the ticket with `--successor` at transition 3 — the first moment a
// successor exists. Without (b) the relocation would cost the control-plane
// record; without (a) the packet is written and unfindable, which is the same
// defect wearing a different hat.
func TestMayorRetiresTheTriageTicketWithTheWorkersOwnPacket(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	s := string(data)

	if strings.Contains(s, "(see its result packet)") {
		t.Error("mayor.md: the build ticket body still points at the triage ticket's " +
			"`result packet`, which nothing wrote before the gate; point it at the " +
			"fenced json triage-packet block on the triage ticket body (mg-1912)")
	}
	if !strings.Contains(s, "json triage-packet") {
		t.Error("mayor.md: expected the json triage-packet marker — both the build " +
			"ticket's pointer and transition 3's extractor depend on it (mg-1912)")
	}
	if !strings.Contains(s, `--successor=<build ticket id> --result="$PACKET"`) {
		t.Error("mayor.md: transition 3 must retire the triage ticket with the " +
			"worker's own extracted packet (`--result=\"$PACKET\"`), so the sidecar " +
			"records what the worker wrote rather than a coordinator paraphrase — the " +
			"coordinator does not have the packet as JSON any other way (mg-1912)")
	}
	// The worker no longer attempts `mg done` on the triage ticket, so mayor.md
	// must not tell the coordinator it merely *failed*: it is never tried.
	if strings.Contains(s, "cannot have succeeded on a ticket carrying the tag") {
		t.Error("mayor.md: the retirement note still describes the triage worker's " +
			"own `mg done` as having been attempted and refused; it is no longer " +
			"attempted at all (mg-1912)")
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

// TestMayorPromptHandlesAckWatchMail locks in the mayor's half of the
// completion-deficit detector (mg-1935). pogod mails the finding; if the mayor
// prompt does not say what to do with it, the alert reaches an inbox and stops
// there — which is the bug the detector was built to fix, reproduced one level
// up. The routing is only as good as the reader.
//
// Two specifics are load-bearing and easy to lose in an edit:
//
//   - `--immediate`. A default nudge waits for 2s of PTY silence, and the agent
//     this detector finds is spinning, so the silence never arrives. The default
//     nudge cannot reach exactly the agent that needs reaching.
//   - "health=healthy proves nothing here". The heartbeat check in §3a and
//     `pogo agent diagnose` both reported healthy throughout the observed
//     incident, because a working spinner is itself PTY output. A mayor that
//     closes the finding on a healthy diagnose has learnt the wrong lesson.
func TestMayorPromptHandlesAckWatchMail(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	s := string(data)

	if !strings.Contains(s, "ack-watch") {
		t.Error("mayor.md: expected guidance for `ack-watch` mail — an alert nobody reads is the bug one level up")
	}
	if !strings.Contains(s, "pogo check-acks") {
		t.Error("mayor.md: expected `pogo check-acks` to re-confirm a finding")
	}
	if !strings.Contains(s, "--immediate") {
		t.Error("mayor.md: expected `pogo nudge --immediate` — the default nudge cannot reach a spinning agent")
	}
	if !strings.Contains(s, "health=healthy") {
		t.Error("mayor.md: expected the warning that health=healthy proves nothing for this class")
	}
	if !strings.Contains(s, "FLEET DEFICIT") {
		t.Error("mayor.md: expected the cohort-wide case to be distinguished from a per-agent fault")
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
	testsandbox.Isolate(t)

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
	testsandbox.Isolate(t)

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
	testsandbox.Isolate(t)

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
	testsandbox.Isolate(t)

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
	tmpHome := testsandbox.Isolate(t).Home

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
	tmpHome := testsandbox.Isolate(t).Home

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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
// file stays mayor.md, but it resolves under the coordinator's display name,
// and drop-ins are keyed by that name. The coordinator is pinned to a name
// that differs from the file stem so the test proves that, rather than passing
// on a coincidence when the shipped default happens to be "mayor".
func TestSynthesizePromptMayorAppendsDropIns(t *testing.T) {
	testsandbox.Isolate(t)
	setCoordinator(t, "ringmaster")
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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
	testsandbox.Isolate(t)
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

// renderShippedTemplate expands an embedded shipped template with the given
// vars, defaults applied, and returns the rendered prompt.
func renderShippedTemplate(t *testing.T, tmplName string, vars TemplateVars) string {
	t.Helper()
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
	if err := tmpl.Execute(&buf, withDefaults(vars)); err != nil {
		t.Fatalf("execute template %s: %v", tmplName, err)
	}
	return buf.String()
}

// TestBranchPolecatTemplatesInstructPRCreate closes the gap mg-7746 opened
// (mg-78d2).
//
// mg-7746 taught the refinery that a merge onto an integration branch is a PR
// -flow STEP, not completion: pogod stops marking the work item done and stops
// stopping the polecat, because the deliverable is the pull request from that
// branch to the repo default and the polecat has not opened it. That made ROOM
// for the PR — it did not fill it. No shipped template told a `--branch`
// polecat to run `gh pr create`, and the base template's step-6 note still
// promised it would be stopped and completed at the merge, which is exactly
// false on that path. The observable outcome was a polecat that deferred,
// waited to be stopped, was reaped by the 15-minute backstop, and escalated to
// the mayor as a manual-recovery alert — the deferral alerted on rather than
// filled.
//
// `.Branch` is the only signal a template gets, and it is the right one: it is
// non-empty exactly when the spawner passed `--branch`, which is what makes the
// submit target something other than `main`. The assertion runs both ways
// deliberately — an unset `.Branch` must NOT grow PR instructions, because the
// overwhelming majority of dispatches merge to the default branch, where the
// merge IS completion and a PR step would be a fabricated obligation.
func TestBranchPolecatTemplatesInstructPRCreate(t *testing.T) {
	// Every shipped template whose protocol both submits to the refinery and
	// honours `--target={{if .Branch}}...`. polecat-build-pr.md is excluded on
	// purpose: that track never submits to the refinery, so its merge is never
	// the PR-flow merge — it opens its PR before the merge, not after.
	for _, tmplName := range []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-architect.md",
	} {
		base := TemplateVars{Id: "mg-abea", Repo: "/path/to/repo", WorktreeDir: "/path/to/worktree", Provider: "claude"}

		withBranch := base
		withBranch.Branch = "daed-101-integration"
		got := renderShippedTemplate(t, tmplName, withBranch)
		if !strings.Contains(got, "gh pr create") {
			t.Errorf("%s: rendered with --branch=%q but never says `gh pr create` — "+
				"a PR-flow polecat's completion is deferred for a PR nothing tells it to open (mg-78d2)",
				tmplName, withBranch.Branch)
		}
		// The head must be the integration branch the refinery merged into, not
		// the polecat's own already-merged feature branch.
		if !strings.Contains(got, "--head "+withBranch.Branch) {
			t.Errorf("%s: PR must be opened from the integration branch %q — "+
				"the polecat's own branch is already merged into it", tmplName, withBranch.Branch)
		}
		// The base must be OBSERVED. Naming it would be a claim that can rot,
		// the same defect mg-d39e fixed for the branch name.
		if !strings.Contains(got, "gh repo view --json defaultBranchRef") {
			t.Errorf("%s: must read the default branch with "+
				"`gh repo view --json defaultBranchRef`, not assume it", tmplName)
		}

		noBranch := renderShippedTemplate(t, tmplName, base)
		if strings.Contains(noBranch, "gh pr create") {
			t.Errorf("%s: rendered with no --branch (target is the default branch, so the "+
				"merge IS completion) but still instructs `gh pr create` — that is an "+
				"invented obligation on the common path", tmplName)
		}
	}
}

// TestBranchPolecatTemplatesAssignEveryShellVarTheyInterpolate is pm-pogo's
// blocking SME finding on mg-78d2, turned into a control.
//
// polecat-architect.md shipped its PR step as:
//
//	PR=$(gh pr list ... -q '.[0].url')     # set only when one already exists
//	[ -n "$PR" ] || gh pr create ...       # the CREATE path assigns nothing
//
// and then interpolated `$PR` into the result sidecar. So the sidecar recorded
// `"pr": ""` precisely when a PR had just been opened — the first polecat on any
// integration branch, i.e. the common case. That is the mg-c8d5 defect verbatim
// (a record claiming a field it never carries) reintroduced one file over, in the
// same diff that documents mg-c8d5.
//
// The generalized invariant is cheap and catches the whole class: a shipped
// template may not interpolate a shell variable it never assigns on any path.
// It is deliberately not scoped to `$PR` — the next one will have a different
// name, and the defect is the missing assignment, not the identifier.
func TestBranchPolecatTemplatesAssignEveryShellVarTheyInterpolate(t *testing.T) {
	// Vars whose value legitimately arrives from outside the template:
	// $POGO_AGENT_NAME is exported by pogod at spawn; $BRANCH is read via
	// `git rev-parse` in an earlier step (the mg-d39e observe-don't-name rule);
	// $PID is a placeholder the reader fills from `pogo agent list` in a
	// pkill-safety example that is illustrative, not a runnable recipe.
	//
	// The exemptions are named one at a time on purpose. The moment this list
	// grows a wildcard it stops being able to fail, and $PR — the var the SME
	// caught — has to keep failing when it is unassigned.
	suppliedByTheReader := map[string]bool{"BRANCH": true, "POGO_AGENT_NAME": true, "PID": true}

	// Bare $NAME and ${NAME}, uppercase only: lowercase would sweep in prose and
	// jq/shell idioms, and every var these templates teach is uppercase.
	ref := regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]*)\}?`)

	for _, tmplName := range []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-architect.md",
	} {
		got := renderShippedTemplate(t, tmplName, TemplateVars{
			Id: "mg-abea", Repo: "/path/to/repo", WorktreeDir: "/path/to/worktree",
			Provider: "claude", Branch: "daed-101-integration",
		})
		for _, m := range ref.FindAllStringSubmatch(got, -1) {
			name := m[1]
			if suppliedByTheReader[name] || name == "EOF" {
				continue
			}
			// An assignment is `NAME=` at a word boundary — covers `PR=$(...)`,
			// `PR=<the url ...>` and `BASE=$(...)` alike. The backtick is in the
			// class because an assignment the template teaches in PROSE (``set
			// `PR=<url>` before the mg done``) is still the template telling the
			// reader to assign it, which is what the check is asking about.
			if !regexp.MustCompile("(^|[\\s;(`])" + name + "=").MatchString(got) {
				t.Errorf("%s: interpolates $%s but never assigns it — an unset var "+
					"expands to the empty string, so whatever reads it records a value "+
					"it does not have (mg-78d2, the mg-c8d5 class)", tmplName, name)
			}
		}
	}
}

// TestBranchPolecatTemplateDropsTheAutoStopPromise is the other half of
// mg-78d2: it is not enough to ADD the PR step, the contradicting sentence has
// to go.
//
// polecat.md's step-6 note tells the polecat that pogod marks its item done and
// stops it the instant the merge lands. On the PR-flow path pogod does neither,
// by design (mg-7746) — so a `--branch` polecat that believed the note would
// read its own survival as a pogod malfunction and wait to be stopped, which is
// the precise behaviour that ends in the backstop escalation.
func TestBranchPolecatTemplateDropsTheAutoStopPromise(t *testing.T) {
	const tmplName = "prompts/templates/polecat.md"
	base := TemplateVars{Id: "mg-abea", Repo: "/path/to/repo", WorktreeDir: "/path/to/worktree", Provider: "claude"}
	// The load-bearing clause of the note, quoted from the default rendering so
	// this test fails if the note is reworded rather than silently passing.
	const autoStopPromise = "it marks your work item done on your behalf"

	if got := renderShippedTemplate(t, tmplName, base); !strings.Contains(got, autoStopPromise) {
		t.Fatalf("%s: default rendering no longer contains %q — this test is pinned to that "+
			"wording; update both halves together", tmplName, autoStopPromise)
	}

	withBranch := base
	withBranch.Branch = "daed-101-integration"
	got := renderShippedTemplate(t, tmplName, withBranch)
	if strings.Contains(got, autoStopPromise) {
		t.Errorf("%s: rendered with --branch=%q still promises pogod completes and stops it at "+
			"the merge — on the PR-flow path it does neither (mg-7746/mg-78d2)",
			tmplName, withBranch.Branch)
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
// The rule is not speculative. On 2026-07-17 four agents made the identical
// error inside one hour, each holding different advantages: a polecat-architect
// read every call site and was right about all of it, then recommended a
// predicate matching 0 of the 14 queued items; the mayor caught that, then
// wrote an acceptance bar already satisfied 9x that day; the standing architect
// scoped a fix "for the whole class at once" that covered 32 of 63 nested repos
// — a count that was 67 fifteen minutes later, because dispatching polecats is
// what grows it; and the PM who caught that miscount then scoped their own
// follow-up ticket to 35 of 67, in the correction to it, and caught it
// themselves. Two of the four were caught by their own authors, and three of
// the four counts happened only because someone had just been shown one.
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
// everything else in a multi-hour context. The polecat is the only one the
// template can reach — not the only one that NEEDS the rule. An earlier draft
// claimed a structural forcing function (mayor and architect "structurally
// forbidden from implementing their own verdicts, so a counter always stands
// downstream"); its author retracted that before merge as an uncounted claim
// about a control, the exact sin this template exists to stop. The four counts
// were a cascade, not a control, and no forcing function has been identified
// for anyone. What the polecat's position adds is real but narrower: it acts on
// its own ruling, and an act touches the population the verdict NAMED, never the
// one it should have — so its own act cannot audit its own scope (mg-b42f).
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
		// Why the polecat and not the crew — the HONEST version (mg-b42f).
		// The earlier "judging doesn't touch the population; acting does" /
		// "a counter always stands downstream" mechanism was retracted by its
		// own author before merge: the four counts were a cascade, not a
		// control, and no forcing function stands downstream of anyone. The
		// polecat gets the rule because it is the only one the template can
		// reach, not the only one that needs it.
		"cascade, not a control",
		"No forcing function has been identified for anyone here",
		"you're the only one of us it can reach",
		"act on your own ruling",
		"Your own act cannot audit your own scope",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("polecat-architect.md: missing measure-the-population rule %q", want)
		}
	}

	// The retracted counter-downstream mechanism (mg-b42f). These phrases
	// asserted an uncounted claim about a control, in the very file that exists
	// to stop exactly that; the architect retracted them ~20 minutes after
	// writing them, but the retraction lost a race to a polecat's read and they
	// shipped anyway. A grep cannot tell the claim from a note retracting it —
	// so this guard pins their ABSENCE, not merely the presence of the fix.
	for _, retracted := range []string{
		"Judging doesn't touch the population; acting does.",
		"a counter always stands downstream",
		"none by its author",
		"you're the only one who needs it",
		"the only one who needs it",
	} {
		if strings.Contains(body, retracted) {
			t.Errorf("polecat-architect.md: retracted counter-downstream mechanism must not reappear: %q", retracted)
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
// guesses trades the cheap loud failure for the expensive silent one, so an
// unmapped type is REFUSED rather than defaulted and architect is strictly
// opt-in.
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

	// There is no default, and the table must say so. mg-9a04 closed the map
	// in Go — an unmapped type selects NO template and the spawn is refused
	// with a 409 — but deliberately left this table promising the build
	// worker, on a premise (a Daniel gate on prompt edits) that turned out not
	// to exist. mg-159a is that repair. `task` is the default type and the
	// most common one, so the row this pins covers the ordinary dispatch, not
	// an edge case: a coordinator that follows the old prose gets a 409 on
	// nearly everything it sends.
	for _, want := range []string{
		"the spawn is refused with a 409 naming the type",
		"Pass `--template=polecat` explicitly",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: type table must state the closed-map behaviour, missing %q", want)
		}
	}
	if strings.Contains(body, "anything else (default `task`)") {
		t.Error("mayor.md: type table reverted to the pre-mg-9a04 claim that an unmapped type falls back to the build worker; the Go map refuses it (mg-159a)")
	}
}

// TestMayorReviewTicketHasNoBuildDependency pins a deadlock out of the gh-issue
// workflow (mg-4999): the review ticket must NOT be filed with
// --depends=<build ticket id>.
//
// The dependency cannot clear on this track. macguffin files an item whose
// dependencies are unmet into pending/, and Claim refuses any item whose status
// is not "available" — so a dependent review ticket is unclaimable until the
// build ticket is done. But the build ticket is not done until after review
// passes: transition 5 has the coordinator submit the builder's branch to the
// refinery itself, and the refinery archives the item on merge. So review waits
// on build, build waits on review, and the review worker can never claim its
// ticket.
//
// Nothing was relying on the flag for ordering. Step 3 holds the review ticket
// by hand and transition 4 dispatches it only once the PR exists — the ordering
// is explicit in the prose, not derived from the dependency graph.
//
// The --depends on the *build* ticket is a different case and must stay: triage
// genuinely completes and is marked done before build begins, so that one
// clears normally. This test pins the asymmetry, which is the part that looks
// like an inconsistency to a later reader and invites a "fix" that restores the
// deadlock.
func TestMayorReviewTicketHasNoBuildDependency(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	body := string(data)

	if strings.Contains(body, "--depends=<build ticket id>") {
		t.Error("mayor.md: gh-issue review ticket is filed with --depends=<build ticket id>; the build ticket stays claimed through review, so the dependency never clears and the review worker can never claim the ticket (mg-4999)")
	}

	// The build ticket's dependency on triage is correct and must not be
	// collaterally removed by a future sweep reading this test as "the
	// gh-issue track uses no dependencies".
	if !strings.Contains(body, "--depends=<triage ticket id>") {
		t.Error("mayor.md: gh-issue build ticket must keep --depends=<triage ticket id>; triage completes before build starts, so that dependency does clear")
	}

	// The reasoning travels with the prompt. Without it the flag reads as an
	// oversight and gets restored — which is how it survived only as an
	// out-of-tree edit to ~/.pogo/agents/mayor.md until mg-4999.
	for _, want := range []string{
		"No --depends on the build ticket",
		"the review ticket would sit in pending/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: missing the rationale for omitting --depends on the review ticket, %q", want)
		}
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
	// "default" is deliberately absent: since mg-9a04 closed the routing map
	// there is no default template at all, and mg-159a struck the word from
	// mayor.md. The prohibition it guarded — a QA item must not land on the
	// build worker — is unchanged, so this pin stays, narrowed to the wording
	// that is actually true.
	if !strings.Contains(body, "never** get the build template") {
		t.Error("mayor.md: step-4 QA prose must forbid the build template for QA items")
	}
	// The regression: prose that hands QA off to the generic dispatch path
	// without naming the template it lands on.
	if strings.Contains(body, "dispatched to a new {{.Worker}} like any other work item") {
		t.Error("mayor.md: step-4 QA prose reverted to the generic-dispatch wording that routed QA items to the build template (mg-7150)")
	}
}

// The hold-instrument table (mg-61f4, from architect's mg-3ebe design).
//
// Three items (mg-78c0, mg-78d2, mg-a3d4) were held for a 03:00 restart with
// `--assignee=parked` plus an "unpark immediately after" note in the title.
// `parked` is `config.IsDispatchGated`, one predicate with two enforcement
// points, so it blocks WATCHING as well as dispatch: pogod cannot see a parked
// item at all and nothing scheduled can ever release a park. Two of the three
// were high priority. They were released only because crew agents
// independently boot-scanned `mg list` after the restart and one acted.
//
// The reframe that makes this a prompt change rather than a process reminder is
// a measurement: `snooze` appeared in ZERO shipped prompts, while `parked`
// appeared 7 times in mayor.md. So three agents did not weigh `snooze` against
// `parked` and choose wrong — nothing had ever told any of them `snooze`
// exists. The mechanism had shipped (`mg snooze`) and its driver was running
// (`mg-schedule-sweep`, */15); only the guidance was missing.
//
// These strings are load-bearing. In particular:
//   - `human` and `parked` must stay SEPARATE rows. Collapsing them is the
//     mg-4ad1 defect: an operational hold wearing `human` promotes itself into
//     a decision Daniel was never asked to make.
//   - "no driver" for the bottom three rows must read as CORRECT, not as a gap.
//     Giving pogod sight of parked items in order to release them would also
//     let it DISPATCH them — it is the same predicate.
//   - the rejected mechanisms (a park-sweeper, a keyword sniff on
//     "until"/"after" in a title) are named as rejected so they are not
//     re-proposed as improvements.
func TestPromptsTeachHoldInstrumentByReleaseCondition(t *testing.T) {
	// Every prompt whose agent can SET a hold. Measured, not assumed: the six
	// templates/ worker prompts mention neither `mg new` nor `mg edit` nor
	// `--assignee` as an affordance — a worker's protocol is claim -> work ->
	// `mg done`, so it has no hold to file and a table there would be noise
	// (the opposite error to mg-710c's under-scoping, which missed three
	// prompts that DID assert the thing).
	holdFilers := []string{
		// the coordinator files holds; all 7 `parked` mentions live here
		"prompts/mayor.md",
		// PMs park their own product's tickets, and this file previously had
		// zero mentions of `parked`, `snooze`, `blocked:` or `depends:` — no
		// hold guidance at all. Which AGENT parked a given item is deliberately
		// not claimed anywhere in this change: `creator` is the unix user and no
		// field records who set an assignee (mg-ddf4), so the justification is
		// the guidance gap plus the existence of parked PM-owned tickets.
		"prompts/pm/pm-template.md",
		// files sub-tickets and edits assignees, so it holds the affordance that
		// produced the failure; and a diagnostic hold is characteristically
		// temporal ("recheck after the next boot"), which is the misuse itself.
		"prompts/crew/doctor.md",
	}

	// The table itself, plus the sentence that decides which row to read.
	for _, path := range holdFilers {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)

		for _, want := range []string{
			// Choose by release condition, not by remembered flag.
			"pick the instrument from the RELEASE CONDITION",
			// All five rows. `human` and `parked` are distinct (mg-4ad1).
			"| a timestamp, or a duration from now | `mg snooze <id> --until <time>` / `--for <dur>` |",
			"| another work item completing | `mg edit <id> --add-depends=<id>`",
			"| a named agent must act, no deadline | `mg edit <id> --assignee=blocked:<agent>` |",
			"| a person must decide, no deadline | `mg edit <id> --assignee=human` |",
			"| not currently work, no deadline | `mg edit <id> --assignee=parked` |",
			// The driver, named — a snooze with nothing driving the sweep is
			// the failure the instrument itself refuses.
			"mg-schedule-sweep",
			// The load-bearing distinction between the top two rows and the
			// bottom three.
			"The top two rows are the only holds that anything will ever open for you.",
			// No driver is correct, not a gap.
			"and that is correct",
			// The three items, so the rule arrives with its own evidence.
			"03:00 restart",
			"unpark immediately after",
			// mg-3844: the third row's "what opens it" cell was
			// "nothing scheduled — but the field names who to chase", which stopped
			// being true when the blocked-reminder shipped. A table that describes a
			// mechanism it no longer has is the archeology trap — the reader stops
			// looking because the file already answered.
			"pogod reminds that agent",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing hold-instrument guidance %q", path, want)
			}
		}

		// The regression that produced the failure: a prompt that offers
		// `--assignee=parked` and never names the timed instrument.
		if !strings.Contains(body, "mg snooze") {
			t.Errorf("%s: names no timed hold instrument; `snooze` in zero shipped prompts is what mg-61f4 fixed", path)
		}
	}

	// mayor.md owns the doctrine half: why the bottom rows CANNOT have a
	// driver, and what happened to the undesigned boot-scan redundancy.
	mayor, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	body := string(mayor)

	for _, want := range []string{
		// The sentence that was already shipped and unread — the whole lesson
		// in one line. It predates the failure it describes.
		"parking buys silence, not disappearance",
		"nothing ages a parked item back into the alert channel",
		// Why pogod's blindness must stay: one predicate, two enforcement
		// points. This is the reason a park-sweeper is the wrong answer.
		"`config.IsDispatchGated` is one predicate with two enforcement points",
		"would also let it **dispatch** them",
		// Both rejected mechanisms, named as rejected.
		"do not file a park-sweeper",
		"rots on the next phrasing",
		// The row nearest the misuse.
		"This is the row nearest the misuse described above.",
		"mg-78d2",
		// The boot-scan call: ACCEPTED, scope narrowed to indefinite holds,
		// and the fix is removing the dependency rather than watching it.
		"it is ACCEPTED, with its scope narrowed to indefinite holds only",
		"nothing with a deadline depends on a boot-scan at all",
		"cannot make them late",
		"Do not build a better watcher for the boot-scan",
		"The fix was removing the dependency on it",
		// Why `snooze` beats a park for a timed hold, all three properties.
		"typed field, not prose",
		"does not discriminate",
		"refuses a hold that nothing will open",
		// mg-3844. These four sit immediately next to "do not file a
		// park-sweeper", and they are here because that sentence without them
		// reads as forbidding the reminder too. The boundary has to travel with
		// the rule or the next reader re-derives it — which is what mg-3844 was.
		"is NOT that sweeper",
		// The reminder does not release: it prompts the party the field already
		// names, which is the DESIGNED release path, not a bypass.
		"prompts the designed release path rather than bypassing it",
		// The property that makes it buildable where a sweeper is not.
		"carries a recipient",
		// The exclusion mayor asked for: an intentional silence must not become
		// noise. Without this a later reader "fixes" the asymmetry.
		"deliberately excluded from the reminder",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: missing hold doctrine %q", want)
		}
	}

	// The reminder's operator-facing facts, which mayor.md owns because the
	// coordinator is who receives the unreachable-blocker notice. Each is a
	// thing an agent would otherwise get wrong: reading the notice as a dispatch
	// request, expecting the nag to continue forever, or front-loading the block
	// reason into a title that never reaches the blocker.
	for _, want := range []string{
		"Since mg-3844 the field also TELLS them.",
		"not as \"dispatch this\"",
		"stops after 4 notices whether or not the block clears",
		"Put the reason in the BODY, not the title.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: missing blocked-reminder guidance %q (mg-3844)", want)
		}
	}

	// The claim the table's third row USED to make. It is false since the
	// reminder shipped, and a stale cell here is exactly the archeology trap
	// mg-3844 itself had to dig through — a confident wrong answer stops the
	// reader looking.
	for _, path := range holdFilers {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(data), "nothing scheduled — but the field names who to chase") {
			t.Errorf("%s: still claims the `blocked:` row notifies nobody; mg-3844 shipped the reminder", path)
		}
	}

	// The scope pin. A worker template gaining this table means someone
	// blanket-edited the corpus; a worker cannot file a hold, so the table
	// there is noise. If a worker prompt ever GAINS an `mg edit --assignee`
	// affordance, this assertion is the thing to revisit deliberately.
	for _, path := range []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-architect.md",
		"prompts/templates/polecat-build-pr.md",
		"prompts/templates/polecat-qa.md",
		"prompts/templates/polecat-review.md",
		"prompts/templates/polecat-triage.md",
	} {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		w := string(data)
		if strings.Contains(w, "pick the instrument from the RELEASE CONDITION") {
			t.Errorf("%s: gained the hold-instrument table, but a worker files no holds — scope was mayor + PM + doctor (mg-61f4)", path)
		}
		if strings.Contains(w, "mg edit <id> --assignee=") {
			t.Errorf("%s: gained an assignee-editing affordance, which makes it a hold filer — the mg-61f4 table's scope needs revisiting", path)
		}
	}
}

// The `mg edit` body-write affordance (mg-4bb9).
//
// mayor.md, pm/pm-template.md and crew/doctor.md all carried, verbatim:
//
//	`mg edit <id> --body="<new body>"` replaces the body wholesale — there is
//	no append/comment subcommand. To leave a note for a future actor without
//	rewriting the body, mail them.
//
// Both halves were wrong, and each was wrong in a way that caused damage:
//
//   - `--append-body` and `--append-body-file` exist. `mg edit --help` opens
//     with a banner naming `--append-body-file` as the right instrument for
//     adding to a body. Because this file answered the question first,
//     nothing sent anyone to that banner — a confident false claim forecloses
//     the `--help` that would have corrected it, which silence would not.
//   - The recommended alternative — mail — IS the mail-vs-body failure.
//     mg-8a12's scope and mg-ddf4's strongest evidence both went by mail and
//     had to be appended to the bodies afterwards; mg-ddf4's own body carries
//     the diagnosis ("This was in mail and not in the ticket, which is the
//     same defect the ticket is about").
//
// So the prompts taught the more dangerous of two available operations on the
// grounds that the safer one did not exist. What makes the append safer is
// three properties, and the test pins all three because each is a distinct
// rewrite failure it does not have: it composes against the body on disk
// (mg-f326's lost update), it cannot author the leading `# ` heading that IS
// the title (mg-bac6's rename), and it is exempt from the workflow-tag
// refusal a full rewrite hits.
//
// The absence assertions are written against the ASSERTION form ("there is no
// append/comment subcommand"), not the phrase, because the fixed text quotes
// the old claim when explaining why it was replaced.
func TestPromptsTeachAppendBodyRatherThanWholesaleRewrite(t *testing.T) {
	// The three prompts whose agents edit a body they did not just author.
	// The six templates/ worker prompts are deliberately out of scope and
	// pinned as such below: none of them mentions `mg edit` at all.
	bodyEditors := []string{
		"prompts/mayor.md",
		"prompts/pm/pm-template.md",
		"prompts/crew/doctor.md",
	}

	for _, path := range bodyEditors {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)

		for _, want := range []string{
			// The affordance, in the form that is actually safe to run: a
			// quoted heredoc into `--append-body-file -`. The quoting is not
			// decoration — an unquoted <<EOF expands $VAR and backticks
			// before mg sees them, which is the `--body="..."` bug reintroduced.
			"mg edit <id> --append-body-file - <<'EOF'",
			"Quote the heredoc.",
			// Property 1: composes against the body on disk, so it cannot
			// lose a concurrent write. mg-f326 is the incident.
			"composes against the body on disk at write time",
			"mg-f326",
			// Property 2: an append lands below the prose, so it cannot
			// author the leading heading — and that heading is the title.
			"it can never author the body's leading `# ` heading",
			"that heading *is* the title",
			// Property 3: exempt from the workflow-tag refusal a full
			// rewrite hits on an item already carrying the tag.
			"exempt from the workflow-tag refusal",
			// The rewrite is reserved, not forbidden — and when it is
			// genuinely the shape, it names the version it read.
			"Reserve `--body-file` for a genuine full rewrite",
			"mg show <id> --body-hash",
			"--if-unchanged=",
			// `mg show <id> --body` does not exist; mg-9fc8 is the incident
			// where its usage error became the stored body. A prompt that
			// sends agents to read a body owes them the flag that works.
			"mg show <id> --json | jq -r .body",
			"mg-9fc8",
			// Mail keeps its real job and loses the one it could not do.
			"a note the next actor must act on belongs in the ticket",
			"mg-8a12",
			"mg-ddf4",
			// Why the fix matters more than the flag list.
			"a confident false claim in a prompt is worse than silence",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing `mg edit` body-write guidance %q (mg-4bb9)", path, want)
			}
		}

		// The regression itself, in both halves. Either one returning means
		// an agent is being told the append does not exist.
		if strings.Contains(body, "there is no append/comment subcommand") {
			t.Errorf("%s: re-asserts that `mg edit` has no append subcommand; `--append-body-file` exists and is the documented instrument (mg-4bb9)", path)
		}
		if strings.Contains(body, "replaces the body wholesale") {
			t.Errorf("%s: describes the wholesale replace as the only body write (mg-4bb9)", path)
		}
		// Mail must not be offered as the way to avoid rewriting a body:
		// that is the mg-8a12 / mg-ddf4 failure, recommended.
		if strings.Contains(body, "without rewriting the body, mail them") {
			t.Errorf("%s: recommends mail as the alternative to rewriting a body — that is the mail-vs-body failure the append flag avoids (mg-4bb9)", path)
		}
	}

	// Scope pin, on the same boundary mg-61f4 measured: a worker's protocol is
	// claim -> work -> `mg done`, and no templates/ prompt mentions `mg edit`.
	// A worker prompt gaining this block means someone blanket-edited the
	// corpus; a worker prompt gaining `mg edit` at all means the scope of both
	// this change and mg-61f4's needs revisiting deliberately.
	//
	// polecat-triage.md is the one deliberate revision, made under mg-1912 and
	// listed separately below rather than dropped from the pin. Its protocol is
	// NOT claim -> work -> `mg done`: `mg done` is refused on a triage ticket
	// (it declares a remainder and the successor is filed after the human gate),
	// so the recommendation packet has nowhere to go but the item's own body.
	// One append, on its own item, is the whole affordance.
	for _, path := range []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-architect.md",
		"prompts/templates/polecat-build-pr.md",
		"prompts/templates/polecat-qa.md",
		"prompts/templates/polecat-review.md",
	} {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		w := string(data)
		if strings.Contains(w, "mg edit <id> --append-body-file") {
			t.Errorf("%s: gained the body-write block, but a worker edits no bodies — scope was mayor + PM + doctor (mg-4bb9)", path)
		}
		if strings.Contains(w, "mg edit") {
			t.Errorf("%s: gained an `mg edit` affordance, so it now edits bodies — the mg-4bb9 scope needs revisiting", path)
		}
	}

	// The triage carve-out, pinned as narrowly as it was granted. The ratchet's
	// actual protection is what survives here: an append cannot destroy a
	// section it never saw, and `--body-file` on a live ticket can and has
	// (mg-f326). A triage worker that gained the wholesale rewrite would be
	// racing the coordinator's `stage:` edits on the same body.
	triage, err := defaultPrompts.ReadFile("prompts/templates/polecat-triage.md")
	if err != nil {
		t.Fatalf("read polecat-triage.md: %v", err)
	}
	tw := string(triage)
	if !strings.Contains(tw, "mg edit {{.Id}} --append-body-file") {
		t.Error("polecat-triage.md: lost the `mg edit {{.Id}} --append-body-file` packet " +
			"write; it is the only durable home for a triage recommendation, since " +
			"`mg done` is refused on the ticket before the gate (mg-1912)")
	}
	for _, banned := range []string{"mg edit {{.Id}} --body-file", "mg edit {{.Id}} --body="} {
		if strings.Contains(tw, banned) {
			t.Errorf("polecat-triage.md: gained %q — a wholesale body rewrite on a ticket "+
				"the coordinator is also editing (mg-f326, mg-4bb9). The append is the "+
				"only body write in scope for this worker", banned)
		}
	}
}

// Log paths in a diagnostic section (mg-f766).
//
// mayor.md's "Refinery logs" section sent readers to
// `~/.local/share/pogo/logs/pogo.err.log`, and doctor.md's quick reference
// carried the same string. That directory does not exist on macOS: pogod's log
// dir is `logDir()` in internal/service, unconditionally
// `$HOME/Library/Logs/pogo`, with no GOOS switch. mg-9aa0 moved it and this
// paragraph was not moved with it — while `.dist:751` in the SAME file already
// referred to `~/Library/Logs/pogo/pogo-deploy.log`, so one file shipped two
// log roots, one of which resolves to nothing.
//
// What makes it worth a pin rather than a one-line correction is the failure
// SHAPE. The same section tells you to grep that file for an MR ID, so the
// symptom is not "no such file" — it is an empty grep, which is
// indistinguishable from "the refinery logged nothing about this MR". That is a
// false negative manufactured by the instrument, at the one moment it costs
// most: mid-diagnosis of a merge that appears stuck. It fired live on
// 2026-07-30 against mr-d9lc2natjv1tur4p9bb0 (two empty results; the real log
// was found by accident), and the next step from "the refinery is not logging"
// is plausibly a redundant re-submit — which has previously reopened an
// already-merged item.
//
// So the pins are:
//   - the dead path is absent from EVERY shipped prompt, not just the two that
//     carried it, so a copy-paste cannot reintroduce it;
//   - the replacement is DISCOVERABLE rather than asserted. A hard-coded path
//     in a prompt is the class of claim that rots silently, and this one
//     already did. `pogo service status` prints the plist/unit path on both
//     platforms (cmd/pogo/main.go and internal/service both print
//     "Service installed: %s"), so the plist can be read for the real value;
//   - Linux is answered too. The line was labelled "(launchd/systemd)", and
//     substituting the macOS path would be the same defect aimed at Linux
//     users. systemdUnitTemplate sets no StandardOutput/StandardError at all,
//     so on Linux there is no log FILE — output goes to the journal. A prompt
//     naming any file path for systemd would be wrong;
//   - the false-negative reasoning travels with the text. Without it the
//     correction is just a newer literal, and the next reader has no reason to
//     distrust an empty grep.
func TestPromptsDoNotAssertADeadLogPath(t *testing.T) {
	var entries []string
	if err := fs.WalkDir(defaultPrompts, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			entries = append(entries, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk prompts: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no shipped prompts found; the sweep below would pass vacuously")
	}

	// The dead path, struck from every shipped prompt. Both filenames it was
	// asserted with are included: the section named pogo.err.log for stderr and
	// pogo.log for stdout, and neither exists under any root — the launchd
	// plist points BOTH streams at one pogod.log.
	for _, path := range entries {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)
		for _, dead := range []string{
			".local/share/pogo/logs",
			"pogo.err.log",
		} {
			if strings.Contains(body, dead) {
				t.Errorf("%s: asserts the nonexistent log path %q; pogod's log dir is service.logDir() = $HOME/Library/Logs/pogo, and an empty grep against a missing file is indistinguishable from \"the refinery logged nothing\" (mg-f766)", path, dead)
			}
		}
	}

	// The replacement, in the two prompts that route an agent to pogod's log.
	for _, path := range []string{"prompts/mayor.md", "prompts/crew/doctor.md"} {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)
		for _, want := range []string{
			// Discoverable, not asserted: ask the service manager.
			`pogo service status | sed -n 's/^Service installed: //p'`,
			"StandardOutPath",
			// Linux is a different instrument, not a different path.
			"journalctl --user -u pogo.service",
			// The macOS value is still named, as the "today" reading rather
			// than as the authority — an agent mid-diagnosis should not have
			// to run two commands before its first grep.
			"~/Library/Logs/pogo/pogod.log",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing log-location guidance %q (mg-f766)", path, want)
			}
		}
		// The reasoning, without which the fix is just a fresher literal.
		if !strings.Contains(body, "empty grep") {
			t.Errorf("%s: must say an empty grep is not evidence until the file is known to exist — that false negative is the whole defect (mg-f766)", path)
		}
	}

	mayor, err := defaultPrompts.ReadFile("prompts/mayor.md")
	if err != nil {
		t.Fatalf("read mayor.md: %v", err)
	}
	body := string(mayor)
	for _, want := range []string{
		// pogod rotates its log at startup past 10 MiB (internal/service/
		// logrotate.go), so "grepped pogod.log, found nothing" has a second
		// innocent explanation besides a wrong path.
		"pogod.log.1",
		// No CLI reports the log location — PogodLogPath() has no command
		// surface. Saying so is what stops the next reader hunting for one and
		// settling for a hard-coded path again.
		"No `pogo` subcommand prints the log location",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mayor.md: missing %q from the refinery-logs section (mg-f766)", want)
		}
	}
}

// The roadmap-regeneration inputs must be invocations the CLI accepts (mg-d8ea).
//
// pm-template.md's "Regenerate roadmap.md each sweep" block is the shared role
// definition for EVERY PM — per-product config only supplies repos and tags —
// and it feeds the roadmap's Trajectory and Recently-shipped sections. Two of
// the four commands in it were refused by the CLI outright:
//
//	mg spend --by item --tag=<x> --since 7d --json   # exit 2: unknown flag: --tag
//	mg list --tag=<x> --status=closed --since 7d     # exit 1 / exit 2
//
// `mg spend` has no `--tag` flag at all; the one-tag-with-item-breakdown view
// is a SELECTOR on --by (`--by tag:<x>`), which is why a flag-shaped guess
// fails. `mg list` has neither a `--since` nor a `closed` status — the closed
// status is `done`, and there is no closed-at field, so the 7-day window has
// to be applied client-side.
//
// What makes this worth pinning rather than fixing once: the failure mode that
// actually occurred was the silent one. roadmap.md@7d07714 reported throughput
// ("28 merges, one release, five polecats still working") and carried no token
// totals and no tag-level bottleneck figures — the two things the skeleton at
// the bottom of this same section specifies. The section was produced without
// the per-item data and said nothing about the omission, and it read as an
// editorial choice because the prose that filled the space was good. Auditing
// this by reading roadmaps would conclude the section was fine.
//
// This is the third shipped prompt in one night instructing an invocation the
// CLI rejects (mg-159a: `--template` omitted where the daemon 409s; mg-4bb9:
// `mg edit` has no append subcommand). All three were found by someone running
// the command rather than reading the prompt. This test is the narrow control
// for one of them; the general one — extracting fenced `mg …` invocations from
// internal/agent/prompts/** and asserting each parses against the current CLI —
// is not filed and belongs to whoever owns prompt tooling.
func TestPMTemplateRoadmapInputsAreAcceptedInvocations(t *testing.T) {
	data, err := defaultPrompts.ReadFile("prompts/pm/pm-template.md")
	if err != nil {
		t.Fatalf("read pm-template.md: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		// The two spend views, in the accepted form. Both are wanted: the
		// bare --by tag places the product against its siblings, --by tag:<x>
		// says which items inside it spent the budget.
		"mg spend --by tag            --since 7d --json",
		"mg spend --by tag:<your-tag> --since 7d --json",
		// Why the wrong shape is reachable from memory: it looks like a
		// filter flag, and it is a selector on --by.
		"selector on `--by`, not a filter flag",
		"There is no `--tag` flag on `mg spend` at all",
		"unknown flag: --tag",
		// The closed-work read, and both halves of what was wrong with it.
		"mg list --tag=<your-tag> --status=done --json",
		"has no `--since`, and the closed status is `done`, not `closed`",
		// The client-side window, with the proxy named honestly rather than
		// presented as a closed-at timestamp.
		"select(.mtime[:10] >= $cutoff)",
		"`mtime` is normally the close, but it moves if anyone edits the item afterwards",
		// The instruction that closes the silent-degradation path. A PM that
		// hits a refusal must say so in the section, not paper over it.
		"do not improvise an invocation and do not drop the section silently",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pm-template.md: missing roadmap-input guidance %q (mg-d8ea)", want)
		}
	}

	// The regressions themselves — scoped to the COMMANDS in the fenced block,
	// not the whole document and not the fence's comments. Correct text has to
	// QUOTE each refused invocation in order to explain why it is refused, and
	// it does so at both levels: the prose beneath the fence says
	// "`--status=closed` exits 1", and mg-21b1's comment inside the fence says
	// "`--by item --tag=…` exits non-zero". A substring check against either
	// scores the correction as the defect — the same trap mg-4bb9 hit and
	// solved by asserting against the assertion form.
	//
	// The discriminator here is structural rather than phrasal, because
	// prescription and quotation live in syntactically different places: a
	// command a PM is told to RUN is a non-comment line inside the ```bash
	// fence. Everything else — prose, and `#` lines within the fence — is
	// commentary about commands. This is the same line `pogo check-prompts`
	// draws when it extracts invocations, which is why that gate and this test
	// agree on the fixed block instead of fighting over it.
	inputs := roadmapInputCommands(t, body)
	for _, bad := range []struct{ frag, why string }{
		{"--by item", "`mg spend --by item` cannot be narrowed to one tag; the per-item view within a tag is the `--by tag:<x>` selector"},
		{"--tag=<your-tag> --since", "`mg spend` has no --tag flag at all (exit 2: unknown flag: --tag)"},
		{"--status=closed", "`mg list` has no `closed` status; it is `done` (exit 1: invalid status)"},
	} {
		if strings.Contains(inputs, bad.frag) {
			t.Errorf("pm-template.md: roadmap input block runs a refused invocation containing %q — %s (mg-d8ea)", bad.frag, bad.why)
		}
	}

	// `mg list --since` in any spacing, on any line of the block. The flag does
	// not exist on that command (exit 2), and the 7-day window is the caller's
	// job — which is why the block must reach for `date`/`jq` instead.
	if regexp.MustCompile(`mg list[^\n]*--since`).MatchString(inputs) {
		t.Error("pm-template.md: roadmap input block passes --since to `mg list`, which has no such flag — window client-side on mtime (mg-d8ea)")
	}
	if !strings.Contains(inputs, "jq -c --arg cutoff") {
		t.Error("pm-template.md: roadmap input block no longer windows recently-shipped client-side; `mg list` cannot do it server-side (mg-d8ea)")
	}
}

// roadmapInputCommands returns the non-comment lines of the first ```bash fence
// inside pm-template.md's "Regenerate roadmap.md each sweep" section — the
// commands a PM is instructed to RUN, with the `#` lines that merely discuss
// commands stripped out.
//
// Both exclusions are load-bearing. Dropping the prose keeps the paragraph that
// explains a refusal from reading as the refusal; dropping in-fence comments
// does the same for mg-21b1's note naming `--by item --tag=…` as non-zero,
// three lines above the corrected invocation.
//
// It fails the test rather than returning empty if the section, the fence, or
// the expected commands cannot be found: an absence assertion that passes
// because it was handed an empty string is worse than no assertion, since it
// reports the prompt clean having examined nothing.
func roadmapInputCommands(t *testing.T, body string) string {
	t.Helper()

	const heading = "### Regenerate roadmap.md each sweep"
	start := strings.Index(body, heading)
	if start < 0 {
		t.Fatalf("pm-template.md: section %q not found — the roadmap-input assertions examined nothing (mg-d8ea)", heading)
	}
	m := regexp.MustCompile("(?s)```bash\n(.*?)\n```").FindStringSubmatch(body[start:])
	if m == nil {
		t.Fatalf("pm-template.md: no ```bash fence under %q — the roadmap-input assertions examined nothing (mg-d8ea)", heading)
	}

	var cmds []string
	for _, line := range strings.Split(m[1], "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			cmds = append(cmds, line)
		}
	}
	out := strings.Join(cmds, "\n")
	if !strings.Contains(out, "mg spend") || !strings.Contains(out, "mg list") {
		t.Fatalf("pm-template.md: the fence under %q is not the input block (no mg spend / mg list command lines):\n%s", heading, m[1])
	}
	return out
}

// The anchored-`pkill` example named a binary nothing was running (mg-ce2c).
//
// mayor.md and crew/doctor.md both closed their unanchored-`pkill` warning with
// `pkill -f "^/usr/local/bin/pogod"` as the how-to-anchor-it example. Measured
// on 2026-07-30, that pattern matched nothing and exited 1. Two independent
// reasons, and the second is the one that makes a fresher literal useless:
//
//  1. The path was stale. `/usr/local/bin/pogod` EXISTS — a Mar-20 build — so an
//     `ls` check confirms it, while `which pogod` and the running daemon
//     (pid 57196) were both `~/go/bin/pogod`. That is worse than an ordinary
//     dead path: the obvious verification agrees with the wrong answer.
//
//  2. pogod is unmatchable by `pkill` from any agent, at any path. `man pgrep`:
//     "-a  Include process ancestors in the match list. By default, the current
//     pgrep or pkill process and all of its ancestors are excluded." pogod
//     spawns every crew agent and polecat, so it is always an ancestor of the
//     shell running the `pkill`. Measured: `pgrep -f .` enumerated 889 of 907
//     processes and omitted pogod, this shell, its claude process, and launchd —
//     the whole ancestor chain — while `pgrep -a -x pogod` returned 57196.
//     So correcting the literal alone would have shipped an example that still
//     silently does nothing.
//
// The failure shape is why this is pinned rather than patched: `pkill` exits 1
// on no match, and "matched nothing" is indistinguishable from "was already
// dead". An agent mid-incident reads the anchored kill as having worked.
//
// The replacement's own hazard is pinned too, because it is worse than what it
// replaces. `pkill -f "^$(ps -o comm= -p "$PID")"` looks like the obvious
// de-hardcoding fix, but `ps` prints nothing for a pid that has already exited,
// the pattern collapses to `"^"`, and that matches everything: measured at 894
// of 907 processes, i.e. the entire fleet. A stale literal fails safe; the naive
// derivation fails catastrophically, and precisely in the common case — chasing
// a process that is already gone. So the guard is part of the pin, not a
// stylistic nicety.
func TestPromptsDoNotAnchorAKillToAHardcodedBinaryPath(t *testing.T) {
	var entries []string
	if err := fs.WalkDir(defaultPrompts, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			entries = append(entries, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk prompts: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no shipped prompts found; the sweep below would pass vacuously")
	}

	for _, path := range entries {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)

		// The stale binary, struck from every shipped prompt so a copy-paste
		// cannot reintroduce it.
		if strings.Contains(body, "/usr/local/bin/pogod") {
			t.Errorf("%s: anchors to /usr/local/bin/pogod, a stale build that still exists (so `ls` confirms it) while the daemon runs from ~/go/bin/pogod — the anchored kill matches nothing and reads as success (mg-ce2c)", path)
		}

		// The defect CLASS, not just the one literal: an anchor opening with a
		// hardcoded absolute path. The legitimate forms expand a variable
		// first — `^{{.WorktreeDir}}/...` in the polecat templates, or `^$BIN`
		// derived at run time — so neither trips this.
		if strings.Contains(body, `pkill -f "^/`) {
			t.Errorf("%s: anchors a pkill to a hardcoded absolute path; derive it from a running instance instead, because a path written into a prompt is the claim that rots and this one did (mg-ce2c)", path)
		}

		// Any prompt shipping the derived form must ship the empty-guard with
		// it. Unguarded, an exited $PID empties $BIN and "^$BIN" becomes "^",
		// which matched 894 of 907 processes when measured.
		if strings.Contains(body, `pkill -f "^$BIN"`) && !strings.Contains(body, `[ -n "$BIN" ]`) {
			t.Errorf("%s: uses pkill -f \"^$BIN\" without a [ -n \"$BIN\" ] guard; a dead $PID makes $BIN empty and \"^\" matches every process on the machine (mg-ce2c)", path)
		}
	}

	// The two prompts that carried the defect now carry the replacement.
	for _, path := range []string{"prompts/mayor.md", "prompts/crew/doctor.md"} {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)

		for _, want := range []string{
			// Derived, not asserted.
			`ps -o comm= -p "$PID"`,
			// The guard, and the reason it exists.
			`[ -n "$BIN" ]`,
			`"^"`,
			// The ancestor exclusion — the half that makes any literal useless.
			"ancestors",
			"`-a`",
			// A no-match is not evidence of a kill.
			"returns 1 when it matched nothing",
			// Linux is answered rather than assumed: `ps -o comm=` there is the
			// short name, not a path, so the recipe would be wrong aimed at it.
			`readlink /proc/"$PID"/exe`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing kill-anchor guidance %q (mg-ce2c)", path, want)
			}
		}

		// The surrounding warning is load-bearing and must survive the edit: an
		// unanchored `pkill -f "sleep 600"` has previously killed the fleet's
		// mail pollers, which idle in exactly that command.
		for _, keep := range []string{
			`pkill -f "sleep 600"`,
			`kill "$PID"`,
			"pogo agent stop <name>",
		} {
			if !strings.Contains(body, keep) {
				t.Errorf("%s: dropped %q from the unanchored-pkill warning; that paragraph is why the fleet's pollers are still alive (mg-ce2c)", path, keep)
			}
		}
	}
}

// THE EVIDENCE DISCIPLINE — one section in the two templates whose worker
// verdicts on work someone else did (mg-0d85, folding mg-04c3 and mg-c742 in
// with two additions). Four habits, one idea: a claim about your own work is
// worth what it cost to make.
//
// 1. PREDICT BEFORE THE RUN (mg-04c3). mg-218d ran sixteen mutations against a
// documentation control with each row's exit code predicted before the run;
// sixteen of sixteen matched, so no row was a post-hoc expectation. That is the
// missing half of "a control must be able to fail" — a positive control proves
// the instrument can speak, and a prediction made first cannot be fitted to the
// result afterwards. The failure mode is not fraud but a test set quietly drawn
// around the answer its author already had, invisible afterwards because every
// row passes and the write-up reads as thorough.
//
// 2. MAKE THE CONTROL FAIL, THEN TRY TO DISARM IT. The second half is the
// addition: mg-16eb fired its control, regenerated both baseline tables with the
// instrument's own --emit-baseline, spliced them back verbatim, and fired the
// control again — exit 1 before, exit 1 after. That answers "is this guard
// defeated by a legitimate refresh?", the question that retires guards quietly
// when nobody asks it: a guard a sanctioned regeneration disarms passes every
// test it has and protects nothing thereafter, and the disarming looks like
// maintenance.
//
// 3. MEASURE EVERY "do not X" (mg-c742). An instruction reliably produces the
// SENTENCE about the risk, not the avoidance of it — the more precisely a brief
// names a failure mode, the more precisely a deliverable can assert it was
// avoided, with no more evidence behind the assertion. mg-a893's acceptance said
// in terms "do not over-correct"; its commit asserts "AND NOT OVER-CORRECTED",
// sitting next to the over-correction mg-c6bc then found. The author did not
// volunteer that cover; the instruction supplied the words.
//
// 4. WEIGH A SELF-ACCUSATION, DISCOUNT A COMPLIANCE CLAIM. The asymmetry is why
// this is not symmetric advice: a compliance claim is free to produce and
// satisfies whoever asked for not-X, while an admission of X invites scrutiny of
// the admitter's own work, so nobody makes it unless it happened. An auditor
// recording that its own reproduce16eb.py printed "0 figures ... unreproduced"
// unconditionally — on a run where one of seven had not reproduced — was the day
// that made the point. Hence: look where the self-assessment does NOT point
// (mg-7d75 pre-filed an attack naming two sections, neither over the line, and
// the one broken claim was the row its list omitted), and ask for near-misses
// rather than compliance.
//
// Why the templates rather than a brief: the same argument as mg-2530. A rule in
// one author's brief is bypassed by whoever is moving fastest — and a "do not X"
// is worse than bypassed, it is *answered*. The templates are the one place every
// worker of that kind passes.
func TestQAAndReviewTemplatesCarryTheEvidenceDiscipline(t *testing.T) {
	// Every literal below is written IDENTICALLY in the two files on purpose:
	// mg-04c3's own first run caught a defect in itself because the two
	// templates opened a shared clause differently, so a shared assertion
	// silently checked only one of them.
	for _, path := range []string{
		"prompts/templates/polecat-qa.md",
		"prompts/templates/polecat-review.md",
	} {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(data)

		for _, want := range []string{
			// The four are one section, and the section says what it is for.
			"Evidence discipline — four habits, one idea",

			// 1. Predict, then run, then record both. The exit code is named
			// specifically: "what do you expect" invites a mood, "pass or
			// fail, and the exit code" invites a record.
			"Predict the outcome before the run",
			"pass or fail, and the exit code if there is one",
			// The verdict on a mismatch — otherwise the reader silently
			// corrects the prediction and the discipline evaporates.
			"A mismatch is a finding about the instrument",
			// Why the ORDER is the whole mechanism.
			"cannot be fitted to the result afterwards",

			// 2. A control must be exhibited failing. Matched on the tail the
			// two templates share — they open the clause differently ("the
			// deliverable is" / "the PR's claim is"), and asserting either
			// literal silently checks only one of the two files.
			`now catches X", exhibit the failing case`,
			// The recovery demonstration: regenerate the baseline the way
			// anyone legitimately would, and show the guard still fires.
			"regenerate it and show the check still fires",
			// Why a defeated guard is invisible rather than noisy.
			"the disarming looks like maintenance",
			// A fitted battery must be declared and extended past the
			// author's known answers.
			"never saw",
			"reads as thorough",

			// 3. Enumerate the "do not X" constraints, and check by measuring.
			`do not X" constraints and check each BY MEASUREMENT`,
			// The deliverable's own claim of not-X is unevidenced until then —
			// without this the rule is satisfied by reading the commit
			// message, which is exactly what failed.
			"carries no evidential weight",
			"quote what you measured, not what it claimed",

			// 4. The asymmetry that makes self-accusation informative and
			// self-praise worthless — stated, not assumed, because it is the
			// reason the advice is one-sided.
			"we caught ourselves doing X",
			"including about the rest of the same document",
			// Where to look. An incomplete self-attack list is the observed
			// failure mode, so the self-assessment directs attention AWAY.
			"self-assessment does NOT point",
			// Matched from "self-attack" rather than from "An incomplete",
			// which wraps mid-phrase in polecat-qa.md — the same defect
			// mg-04c3's own first run caught in itself.
			"self-attack list is the observed failure mode, not a false one",
			// And what the worker owes about its own process.
			"record your own near-misses",
			"carries information; one saying everything went to plan carries none",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing evidence discipline %q (mg-0d85)", path, want)
			}
		}

		// One section, not four paragraphs bolted on in sequence. The whole
		// point of the fold is that a template every worker of its type reads
		// gets skimmed when it accretes; a second "Evidence discipline" heading
		// means the next addition appended instead of merging.
		if got := strings.Count(body, "Evidence discipline"); got != 1 {
			t.Errorf("%s: %d evidence-discipline sections, want exactly 1 — the four habits are one idea and were folded into one section on purpose (mg-0d85)", path, got)
		}
		if got := strings.Count(body, "Predict the outcome before the run"); got != 1 {
			t.Errorf("%s: %d copies of the predict-before-run rule, want exactly 1 — it lives in the evidence-discipline section, not also inline in a step (mg-0d85)", path, got)
		}
	}

	// polecat-qa.md owns the fuller statement: it is the template whose entire
	// deliverable is a verdict on someone else's work, so the worked instances
	// land there rather than compressed into a review lens — the claim that sat
	// next to its own violation, and the one clean verdict of that day, which
	// got there by closing its weakest link by measurement rather than defending
	// it in prose.
	qa, err := defaultPrompts.ReadFile("prompts/templates/polecat-qa.md")
	if err != nil {
		t.Fatalf("read polecat-qa.md: %v", err)
	}
	qaBody := string(qa)
	for _, want := range []string{
		`asserted "AND NOT OVER-CORRECTED"`,
		"weakest link by measurement instead of defending it in prose",
	} {
		if !strings.Contains(qaBody, want) {
			t.Errorf("polecat-qa.md: missing evidence discipline %q (mg-0d85)", want)
		}
	}

	// None of the four is a gate. Nothing can verify that a prediction preceded
	// a run, that a measurement was taken, or that a near-miss was disclosed, so
	// a template refusing on any of them would be enforcing an unobservable —
	// and a refusal a worker cannot satisfy gets routed around, taking the cheap
	// useful part with it. Their value is that they change what the worker
	// writes down before they look.
	for _, forbid := range []string{
		"do not proceed until you have predicted",
		"refuse to report a verdict unless",
		"do not report a verdict until you have measured",
		"refuse to verdict unless",
		"do not report a verdict until you have disclosed",
	} {
		if strings.Contains(strings.ToLower(qaBody), forbid) {
			t.Errorf("polecat-qa.md: turned the evidence discipline into a gate (%q); nothing can verify a prediction preceded a run, a measurement was taken, or a near-miss was disclosed (mg-0d85)", forbid)
		}
	}

	// The scope pin, inherited from mg-04c3 and mg-c742 and unchanged by the
	// fold. The default, PR-build, triage and architect templates were
	// deliberately left alone: this is an auditor's section, aimed at a reader
	// checking someone else's compliance claim, and most build work has no
	// battery to fit at all. A template that talks past its reader gets skimmed,
	// which would cost the rules that ARE aimed at that reader. Widening this
	// into polecat.md needs its own argument, not a blanket edit.
	for _, path := range []string{
		"prompts/templates/polecat.md",
		"prompts/templates/polecat-build-pr.md",
		"prompts/templates/polecat-triage.md",
		"prompts/templates/polecat-architect.md",
	} {
		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, forbid := range []string{
			"Evidence discipline",
			"Predict the outcome before the run",
			"constraints and check each BY MEASUREMENT",
		} {
			if strings.Contains(string(data), forbid) {
				t.Errorf("%s: gained %q, but the evidence-discipline scope is QA + review; widening it to a build worker's template needs a separate argument (mg-0d85)", path, forbid)
			}
		}
	}
}
