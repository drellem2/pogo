package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
)

//go:embed prompts
var defaultPrompts embed.FS

// PromptDir returns the root directory for agent prompt files.
// Default: ~/.pogo/agents/
func PromptDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pogo", "agents")
}

// TemplateDir returns the directory for polecat prompt templates.
// Default: ~/.pogo/agents/templates/
func TemplateDir() string {
	return filepath.Join(PromptDir(), "templates")
}

// TemplateVars holds the variables available during polecat template expansion.
type TemplateVars struct {
	Task        string // Work item title
	Body        string // Work item body (markdown)
	Id          string // Work item ID
	Repo        string // Target repository path
	Branch      string // Target branch for refinery submit (default: main)
	WorktreeDir string // Polecat's isolated worktree path (its working directory)

	// RecentCommits is `git log --oneline -n` output for the source repo's
	// checked-out branch, surfaced as FYI context so a polecat picking up
	// the Nth ticket of a multi-ticket feature can see the prior N-1
	// commits (each carrying its mg-XXXX in the subject) without being
	// told to look. Empty string means "no context available" — templates
	// should gate the section behind `{{if .RecentCommits}}`.
	RecentCommits string
	// RecentFiles is the unique set of files touched by RecentCommits,
	// sorted and newline-joined. Same FYI framing as RecentCommits.
	RecentFiles string
}

// ResolveCrewPrompt returns the path to a crew agent's prompt file.
// Returns an error if the file does not exist.
func ResolveCrewPrompt(name string) (string, error) {
	path := filepath.Join(CrewPromptDir(), name+".md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("crew prompt not found: %s", path)
	} else if err != nil {
		return "", fmt.Errorf("stat crew prompt: %w", err)
	}
	return path, nil
}

// ResolveMayorPrompt returns the path to the mayor's prompt file.
// Returns an error if the file does not exist.
func ResolveMayorPrompt() (string, error) {
	path := filepath.Join(PromptDir(), "mayor.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("mayor prompt not found: %s", path)
	} else if err != nil {
		return "", fmt.Errorf("stat mayor prompt: %w", err)
	}
	return path, nil
}

// ResolveTemplate returns the path to a polecat template file.
// Returns an error if the file does not exist.
func ResolveTemplate(name string) (string, error) {
	path := filepath.Join(TemplateDir(), name+".md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("template not found: %s", path)
	} else if err != nil {
		return "", fmt.Errorf("stat template: %w", err)
	}
	return path, nil
}

// extendsDirectiveRE matches `extends <template> with config <config>` on its
// own line (multiline mode). Used by crew prompts that delegate their content
// to a shared template + per-instance TOML config (PM tier).
var extendsDirectiveRE = regexp.MustCompile(`(?m)^extends\s+(\S+)\s+with\s+config\s+(\S+)\s*$`)

// SynthesizeExtendsPrompt looks for an `extends <template> with config <config>`
// directive in the crew prompt at promptPath. If present, it reads the named
// template and config, writes a merged prompt (template body + config inlined
// as a TOML block) to outPath, and returns outPath. If absent, returns "" so
// the caller can use promptPath as-is.
//
// Path resolution: <config> is relative to PromptDir. <template> may be either
// a full path relative to PromptDir, or a bare name resolved alongside the
// config file. `.md` is appended to <template> when missing. The template's
// own frontmatter (e.g. auto_start, nudge_on_start) is preserved so it governs
// the synthesized agent's behavior.
func SynthesizeExtendsPrompt(promptPath, outPath string) (string, error) {
	extData, err := synthesizeExtendsBytes(promptPath)
	if err != nil {
		return "", err
	}

	// Drop-ins are an additive customization slot keyed by the prompt's
	// filename stem ("mayor" for mayor.md, "doctor" for crew/doctor.md, the
	// crew agent name for an `extends` redirect like crew/pm-pogo.md).
	// InstallPrompts never writes here — only the user does.
	basename := strings.TrimSuffix(filepath.Base(promptPath), ".md")
	drop, err := LoadDropIns(basename)
	if err != nil {
		return "", err
	}

	// Bail out early when neither layer adds anything — the caller falls
	// back to using promptPath as-is, avoiding a synthesized-file write for
	// the common no-customization case.
	if extData == nil && drop == "" {
		return "", nil
	}

	var body []byte
	if extData != nil {
		body = extData
	} else {
		// No extends directive but drop-ins are present: preserve the file
		// verbatim (frontmatter intact, hash stamp stripped) so the
		// synthesized output is parsable by the same downstream readers as
		// the original on-disk prompt.
		raw, err := os.ReadFile(promptPath)
		if err != nil {
			return "", fmt.Errorf("read prompt %s: %w", promptPath, err)
		}
		body = stripPromptHashStamp(raw)
	}
	merged := []byte(appendDropIns(string(body), drop))

	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return "", fmt.Errorf("create dir for synthesized prompt: %w", err)
	}
	if err := os.WriteFile(outPath, merged, 0644); err != nil {
		return "", fmt.Errorf("write synthesized prompt: %w", err)
	}
	return outPath, nil
}

// synthesizeExtendsBytes is the in-memory variant of SynthesizeExtendsPrompt.
// Returns nil bytes (with nil error) when the prompt has no extends directive,
// so callers can fall back to the original prompt body.
func synthesizeExtendsBytes(promptPath string) ([]byte, error) {
	_, body, err := ParsePromptFrontmatter(promptPath)
	if err != nil {
		return nil, err
	}
	m := extendsDirectiveRE.FindStringSubmatch(body)
	if m == nil {
		return nil, nil
	}
	tmplArg, cfgArg := m[1], m[2]
	root := PromptDir()
	cfgPath := filepath.Join(root, cfgArg)
	var tmplPath string
	if strings.Contains(tmplArg, "/") {
		tmplPath = filepath.Join(root, tmplArg)
	} else {
		tmplPath = filepath.Join(filepath.Dir(cfgPath), tmplArg)
	}
	if !strings.HasSuffix(tmplPath, ".md") {
		tmplPath += ".md"
	}
	tmplData, err := os.ReadFile(tmplPath)
	if err != nil {
		return nil, fmt.Errorf("read extends template %s: %w", tmplPath, err)
	}
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read extends config %s: %w", cfgPath, err)
	}

	// TOML drop-ins layer onto the base config later-wins (lexical order),
	// keyed off the cfg path's stem under dropins/. For `pm/pogo.toml` that's
	// `~/.pogo/agents/dropins/pm/pogo/*.toml`. Same shape as the markdown
	// drop-ins under dropins/<basename>/*.md but lives under a per-config
	// directory because a single PM tier can ship many sibling configs
	// (pogo.toml, lineara.toml, onethird.toml) that need independent
	// override slots.
	cfgRel := strings.TrimSuffix(cfgArg, ".toml")
	dropDir := filepath.Join(root, "dropins", cfgRel)
	mergedCfg, _, err := MergeTOMLDropIns(cfgData, dropDir)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Write(stripPromptHashStamp(tmplData))
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.WriteString("\n## Your configuration\n\nLoaded from `")
	buf.WriteString(cfgArg)
	buf.WriteString("`:\n\n```toml\n")
	buf.Write(stripPromptHashStamp(mergedCfg))
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.WriteString("```\n")
	return buf.Bytes(), nil
}

// DropInDir returns the directory containing user drop-in fragments for the
// named base prompt. Default: ~/.pogo/agents/dropins/<basename>/
//
// basename is the filename stem (no extension, no parent directory) of the
// shipped prompt — "mayor" for mayor.md, "polecat" for templates/polecat.md,
// "doctor" for crew/doctor.md.
func DropInDir(basename string) string {
	return filepath.Join(PromptDir(), "dropins", basename)
}

// LoadDropIns reads every *.md file under DropInDir(basename) in lexical
// order and returns their concatenated content (with a trailing newline
// inserted between fragments when needed). Subdirectories are ignored.
//
// An absent or empty drop-in directory yields "" with a nil error — drop-ins
// are an opt-in customization slot, not a required part of the prompt.
func LoadDropIns(basename string) (string, error) {
	dir := DropInDir(basename)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read drop-in dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", fmt.Errorf("read drop-in %s: %w", n, err)
		}
		buf.Write(data)
		if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	return buf.String(), nil
}

// appendDropIns concatenates a base prompt body and a drop-in fragment block,
// ensuring exactly one separating newline so fragments don't run into the
// last line of the base.
func appendDropIns(base, dropins string) string {
	if dropins == "" {
		return base
	}
	if base == "" {
		return dropins
	}
	if !strings.HasSuffix(base, "\n") {
		base += "\n"
	}
	return base + dropins
}

// SynthesizePrompt resolves the named prompt and produces the final content
// that an agent would receive — applying the same transformations as the
// spawn-time loader, but with no file writes. Used by `pogo agent prompt show`.
//
// Resolution order: mayor (when name == "mayor"), crew prompt, template. The
// first hit wins; an unknown name returns an error.
//
// Pipeline:
//   - mayor or crew without `extends`: read base, append drop-ins from
//     dropins/<name>/, return.
//   - crew with `extends`: synthesize template + config in-memory, append
//     drop-ins from dropins/<name>/, return.
//   - polecat template: read base, append drop-ins, run text/template
//     substitution with vars, return.
//
// Frontmatter is stripped from the base prompt so the output matches what
// the agent harness will see.
func SynthesizePrompt(name string, vars TemplateVars) (string, error) {
	if name == "mayor" {
		path, err := ResolveMayorPrompt()
		if err != nil {
			return "", err
		}
		return synthesizeStaticPrompt(path, name)
	}
	if path, err := ResolveCrewPrompt(name); err == nil {
		return synthesizeStaticPrompt(path, name)
	}
	if path, err := ResolveTemplate(name); err == nil {
		return synthesizePolecatTemplate(path, name, vars)
	}
	return "", fmt.Errorf("prompt %q not found (checked mayor, crew/, templates/)", name)
}

func synthesizeStaticPrompt(path, basename string) (string, error) {
	extData, err := synthesizeExtendsBytes(path)
	if err != nil {
		return "", err
	}
	var body string
	if extData != nil {
		body = string(extData)
	} else {
		_, b, err := ParsePromptFrontmatter(path)
		if err != nil {
			return "", err
		}
		body = b
	}
	drop, err := LoadDropIns(basename)
	if err != nil {
		return "", err
	}
	return appendDropIns(body, drop), nil
}

func synthesizePolecatTemplate(path, basename string, vars TemplateVars) (string, error) {
	_, body, err := ParsePromptFrontmatter(path)
	if err != nil {
		return "", err
	}
	drop, err := LoadDropIns(basename)
	if err != nil {
		return "", err
	}
	combined := appendDropIns(body, drop)
	tmpl, err := template.New(filepath.Base(path)).Parse(combined)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// PreviewTemplateVars returns stub TemplateVars used by `pogo agent prompt
// show` when rendering polecat templates. The user is verifying composition,
// not running the prompt, so vars carry obvious "preview" sentinels rather
// than realistic values. RecentCommits/RecentFiles stay empty so conditional
// sections gated by `{{if .RecentCommits}}` show their no-context shape.
func PreviewTemplateVars() TemplateVars {
	return TemplateVars{
		Task:        "(preview) example task title",
		Body:        "(preview) example body",
		Id:          "preview",
		Repo:        "/path/to/repo",
		Branch:      "main",
		WorktreeDir: "/path/to/worktree",
	}
}

// stripPromptHashStamp removes a leading pogo-prompt stamp if present, in
// either the markdown (HTML-comment) or TOML (#) flavor and in either the v1
// (embed=… body=…) or v0 (single hash) shape. Used when inlining installed
// prompt/config files so the stamp doesn't leak into output.
func stripPromptHashStamp(data []byte) []byte {
	s := string(data)
	if strings.HasPrefix(s, promptStampPrefix) || strings.HasPrefix(s, promptStampPrefixTOML) ||
		strings.HasPrefix(s, promptHashPrefix) || strings.HasPrefix(s, promptHashPrefixTOML) {
		if nl := strings.IndexByte(s, '\n'); nl != -1 {
			return data[nl+1:]
		}
	}
	return data
}

// ExpandString runs s through Go text/template using vars. Strings without
// template syntax are returned unchanged. Used for short snippets like
// nudge_on_start that should accept the same {{.Id}}/{{.Repo}} variables as
// the prompt body.
func ExpandString(s string, vars TemplateVars) (string, error) {
	tmpl, err := template.New("inline").Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute: %w", err)
	}
	return buf.String(), nil
}

// ExpandTemplate reads a template file and expands {{.Variable}} placeholders
// with the provided vars. Uses Go text/template syntax. Any TOML frontmatter
// at the top of the file (delimited by '+++' fences) is stripped before
// expansion so the metadata block does not leak into the rendered prompt.
//
// Drop-ins from ~/.pogo/agents/dropins/<basename>/*.md (where basename is the
// template's filename stem) are appended to the body before parsing, so
// fragments can also reference {{.Var}} and participate in the same
// expansion pass as the shipped template body.
func ExpandTemplate(templatePath string, vars TemplateVars) (string, error) {
	_, body, err := ParsePromptFrontmatter(templatePath)
	if err != nil {
		return "", err
	}

	basename := strings.TrimSuffix(filepath.Base(templatePath), ".md")
	drop, err := LoadDropIns(basename)
	if err != nil {
		return "", err
	}
	combined := appendDropIns(body, drop)

	tmpl, err := template.New(filepath.Base(templatePath)).Parse(combined)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}

// ExpandTemplateToFile expands a template and writes it to a temporary file.
// The caller is responsible for removing the temp file when done.
// Returns the path to the generated file.
func ExpandTemplateToFile(templatePath string, vars TemplateVars) (string, error) {
	expanded, err := ExpandTemplate(templatePath, vars)
	if err != nil {
		return "", err
	}

	// Write to a temp file in the pogo runtime directory
	tmpDir := filepath.Join(os.TempDir(), "pogo-prompts")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return "", fmt.Errorf("create prompt temp dir: %w", err)
	}

	f, err := os.CreateTemp(tmpDir, "polecat-*.md")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(expanded); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write expanded prompt: %w", err)
	}

	return f.Name(), nil
}

// PromptInfo describes a discovered prompt file.
type PromptInfo struct {
	Name     string `json:"name"`     // File stem (e.g., "arch", "polecat")
	Path     string `json:"path"`     // Full filesystem path
	Category string `json:"category"` // "crew", "templates", or "mayor"
}

// ListPrompts discovers all prompt files under ~/.pogo/agents/.
func ListPrompts() ([]PromptInfo, error) {
	root := PromptDir()
	var prompts []PromptInfo

	// Mayor prompt (top-level)
	mayorPath := filepath.Join(root, "mayor.md")
	if _, err := os.Stat(mayorPath); err == nil {
		prompts = append(prompts, PromptInfo{
			Name:     "mayor",
			Path:     mayorPath,
			Category: "mayor",
		})
	}

	// Crew prompts
	crewDir := CrewPromptDir()
	if entries, err := os.ReadDir(crewDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			prompts = append(prompts, PromptInfo{
				Name:     name,
				Path:     filepath.Join(crewDir, e.Name()),
				Category: "crew",
			})
		}
	}

	// Templates
	tmplDir := TemplateDir()
	if entries, err := os.ReadDir(tmplDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			prompts = append(prompts, PromptInfo{
				Name:     name,
				Path:     filepath.Join(tmplDir, e.Name()),
				Category: "templates",
			})
		}
	}

	return prompts, nil
}

// DefaultCrewPromptTemplate is the default content for new crew agent prompt files.
const DefaultCrewPromptTemplate = `# {{.Name}}

You are a crew agent — a long-running assistant managed by pogo. Unlike polecats (ephemeral, single-task agents), you run persistently and pogod restarts you if you crash.

## Your Role

Describe what this agent is responsible for. What domain does it own? What kinds of tasks should be routed to it?

## Your Tools

` + "```bash" + `
# Common tools available to all agents
pogo agent list                # See running agents
pogo agent status <name>       # Check agent status
mg mail list <your-name>       # Check your inbox
mg mail read <msg-id>          # Read a message
mg mail send <agent> --from={{.Name}} --subject="<subj>" --body="<body>"
` + "```" + `

## Working Principles

- **Stay in your lane.** Handle work within your domain. Route other requests to the appropriate agent.
- **Be responsive.** Check your mail regularly and reply promptly.
- **Follow conventions.** Match the existing code style and project norms.
- **Communicate.** If you're blocked or need help, mail the mayor.

## Identity

Your agent name is ` + "`{{.Name}}`" + `. You were started via ` + "`pogo agent start {{.Name}}`" + `.
`

// CreateCrewPrompt creates a new crew agent prompt file at ~/.pogo/agents/crew/<name>.md
// with a default template. Returns the path to the created file.
// Returns an error if the file already exists (unless force is true).
func CreateCrewPrompt(name string, force bool) (string, error) {
	if err := InitPromptDirs(); err != nil {
		return "", err
	}

	path := filepath.Join(CrewPromptDir(), name+".md")

	if !force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("crew prompt already exists: %s (use --force to overwrite)", path)
		}
	}

	// Expand the template with the agent name
	tmpl, err := template.New("crew").Parse(DefaultCrewPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parse crew template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ Name string }{Name: name}); err != nil {
		return "", fmt.Errorf("execute crew template: %w", err)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("write crew prompt: %w", err)
	}

	return path, nil
}

// InitPromptDirs creates the ~/.pogo/agents/ directory structure.
func InitPromptDirs() error {
	dirs := []string{
		CrewPromptDir(),
		TemplateDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// metaFieldFlag is a bitmask of recognized frontmatter keys that were
// explicitly declared. Stored on AgentMeta so callers can tell "set to the
// zero value" apart from "not declared at all" — important for boolean keys
// like restart_on_crash where false is a valid intentional setting.
type metaFieldFlag uint8

const (
	metaFieldRestartOnCrash metaFieldFlag = 1 << iota
	metaFieldAutoStart
	metaFieldNudgeOnStart
	metaFieldWorktree
	metaFieldProvider
)

// metaFieldByKey maps a TOML key name to its bitmask flag. The second return
// is false for unrecognized keys, which the parser silently tolerates for
// forward compatibility.
func metaFieldByKey(key string) (metaFieldFlag, bool) {
	switch key {
	case "restart_on_crash":
		return metaFieldRestartOnCrash, true
	case "auto_start":
		return metaFieldAutoStart, true
	case "nudge_on_start":
		return metaFieldNudgeOnStart, true
	case "worktree":
		return metaFieldWorktree, true
	case "provider":
		return metaFieldProvider, true
	}
	return 0, false
}

// AgentMeta is the structured metadata declared at the top of a prompt file
// via TOML frontmatter. Zero values mean "use defaults" — the parser returns
// a zero-value AgentMeta for prompts without frontmatter so existing prompts
// behave exactly as before.
//
// Recognized fields:
//   - restart_on_crash: pogod restarts the agent if it exits unexpectedly
//   - auto_start:       pogod starts the agent on daemon boot
//   - nudge_on_start:   message sent to the agent immediately after spawn
//   - worktree:         polecat-style isolated worktree on spawn
//   - provider:         harness provider id ("claude", "codex") for this
//     agent — tier 2 of the per-spawn provider precedence chain (mg-b31b),
//     beating per-type/global config but yielding to a --provider flag
type AgentMeta struct {
	RestartOnCrash bool   `json:"restart_on_crash,omitempty"`
	AutoStart      bool   `json:"auto_start,omitempty"`
	NudgeOnStart   string `json:"nudge_on_start,omitempty"`
	Worktree       bool   `json:"worktree,omitempty"`
	Provider       string `json:"provider,omitempty"`

	// explicit is a bitmask of recognized keys that appeared in the
	// frontmatter. Unexported so it stays out of JSON output; uint8 so
	// AgentMeta remains comparable with `==`. Zero for prompts without
	// frontmatter.
	explicit metaFieldFlag
}

// HasField reports whether key was explicitly present in the prompt's
// frontmatter. Returns false for prompts without frontmatter, prompts that
// did not declare the key, or unrecognized keys.
func (m *AgentMeta) HasField(key string) bool {
	if m == nil {
		return false
	}
	flag, ok := metaFieldByKey(key)
	if !ok {
		return false
	}
	return m.explicit&flag != 0
}

// frontmatterFence is the delimiter line that opens and closes a TOML
// frontmatter block at the top of a prompt file (Hugo-style).
const frontmatterFence = "+++"

// ParsePromptFrontmatter reads the prompt file at path and extracts optional
// TOML frontmatter delimited by '+++' fences at the very top of the file.
// It returns the parsed metadata and the remaining body (everything after
// the closing fence).
//
// Files without frontmatter return a zero-value AgentMeta and the full file
// contents as body, which preserves the behavior of existing prompts.
func ParsePromptFrontmatter(path string) (*AgentMeta, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read prompt %s: %w", path, err)
	}
	meta, body, err := parsePromptFrontmatterBytes(data)
	if err != nil {
		return nil, "", fmt.Errorf("parse frontmatter in %s: %w", path, err)
	}
	return meta, body, nil
}

// parsePromptFrontmatterBytes is the in-memory variant of ParsePromptFrontmatter.
// Split out so tests can exercise the parser without touching the filesystem.
func parsePromptFrontmatterBytes(data []byte) (*AgentMeta, string, error) {
	meta := &AgentMeta{}
	s := string(data)

	// Installed prompts carry a leading pogo-prompt stamp that InstallPrompts
	// prepends for staleness / user-edit detection (v1: "<!-- pogo-prompt:
	// embed=... body=... -->"; v0: "<!-- pogo-prompt-hash: ... -->"). Skip
	// either shape so the frontmatter fence is recognized when present.
	if strings.HasPrefix(s, promptStampPrefix) || strings.HasPrefix(s, promptHashPrefix) {
		if nl := strings.IndexByte(s, '\n'); nl != -1 {
			s = s[nl+1:]
		}
	}

	// No fence at offset 0 → no frontmatter, return defaults + full body.
	if !strings.HasPrefix(s, frontmatterFence) {
		return meta, s, nil
	}

	after := s[len(frontmatterFence):]
	eol := strings.IndexByte(after, '\n')
	if eol == -1 {
		return nil, "", fmt.Errorf("opening fence not terminated by newline")
	}
	if strings.TrimRight(after[:eol], " \t\r") != "" {
		return nil, "", fmt.Errorf("unexpected content after opening fence: %q", after[:eol])
	}
	rest := after[eol+1:]

	closeIdx := findFenceLine(rest)
	if closeIdx == -1 {
		return nil, "", fmt.Errorf("missing closing %q fence", frontmatterFence)
	}

	fmText := rest[:closeIdx]
	body := rest[closeIdx:]
	// Drop the closing fence line itself; whatever follows is the body.
	if nl := strings.IndexByte(body, '\n'); nl == -1 {
		body = ""
	} else {
		body = body[nl+1:]
	}

	if err := parseFrontmatterBody(fmText, meta); err != nil {
		return nil, "", err
	}
	return meta, body, nil
}

// findFenceLine returns the byte offset of the start of the next line whose
// content (ignoring trailing whitespace) is exactly the frontmatter fence.
// Returns -1 if no such line exists.
func findFenceLine(s string) int {
	i := 0
	for i <= len(s) {
		nl := strings.IndexByte(s[i:], '\n')
		var line string
		if nl == -1 {
			line = s[i:]
		} else {
			line = s[i : i+nl]
		}
		if strings.TrimRight(line, " \t\r") == frontmatterFence {
			return i
		}
		if nl == -1 {
			return -1
		}
		i += nl + 1
	}
	return -1
}

// parseFrontmatterBody parses the TOML key=value lines between the fences.
// The accepted grammar is intentionally tiny: blank lines, '#' comments, and
// 'key = value' lines where value is either a bool literal (true|false) or a
// double-quoted string (with \\, \", \n, \r, \t escapes). Unknown keys are
// ignored so adding new AgentMeta fields stays backwards compatible.
func parseFrontmatterBody(text string, meta *AgentMeta) error {
	scanner := bufio.NewScanner(strings.NewReader(text))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq == -1 {
			return fmt.Errorf("line %d: missing '=' in %q", lineNo, line)
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
			return fmt.Errorf("line %d: empty key in %q", lineNo, line)
		}
		if err := assignMetaField(meta, key, val); err != nil {
			return fmt.Errorf("line %d: %w", lineNo, err)
		}
	}
	return scanner.Err()
}

func assignMetaField(meta *AgentMeta, key, raw string) error {
	flag, known := metaFieldByKey(key)
	if !known {
		// Unknown keys are tolerated — keeps older binaries forward-compatible
		// with prompt files written for newer schema additions.
		return nil
	}
	switch key {
	case "restart_on_crash":
		b, err := parseFrontmatterBool(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		meta.RestartOnCrash = b
	case "auto_start":
		b, err := parseFrontmatterBool(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		meta.AutoStart = b
	case "worktree":
		b, err := parseFrontmatterBool(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		meta.Worktree = b
	case "nudge_on_start":
		s, err := parseFrontmatterString(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		meta.NudgeOnStart = s
	case "provider":
		s, err := parseFrontmatterString(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		meta.Provider = s
	}
	meta.explicit |= flag
	return nil
}

func parseFrontmatterBool(raw string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("expected bool (true|false), got %q", raw)
}

func parseFrontmatterString(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", fmt.Errorf("expected double-quoted string, got %q", raw)
	}
	return unescapeFrontmatterString(raw[1 : len(raw)-1])
}

func unescapeFrontmatterString(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("trailing backslash in string")
		}
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			return "", fmt.Errorf("unknown escape sequence: \\%c", s[i])
		}
	}
	return b.String(), nil
}

// RestartOnCrashDefault returns the default restart_on_crash value for an
// agent type when its prompt file does not declare one. Crew agents are
// long-running and default to restart=true; polecats are ephemeral and
// default to restart=false.
func RestartOnCrashDefault(t AgentType) bool {
	return t == TypeCrew
}

// ResolveRestartOnCrash decides whether an agent of the given type should be
// restarted on crash. Frontmatter wins when present; otherwise the type
// default applies. A missing or unreadable prompt file falls back to the
// default and is not treated as an error here — callers that need stricter
// handling should validate the prompt path themselves.
func ResolveRestartOnCrash(promptFile string, t AgentType) bool {
	def := RestartOnCrashDefault(t)
	if promptFile == "" {
		return def
	}
	meta, _, err := ParsePromptFrontmatter(promptFile)
	if err != nil {
		return def
	}
	if meta.HasField("restart_on_crash") {
		return meta.RestartOnCrash
	}
	return def
}

// Prompt stamp markers. Two shapes coexist:
//
//   - v1 (current): "<!-- pogo-prompt: embed=sha256:<hex> body=sha256:<hex> -->"
//     records two hashes — embed_hash (the embed payload at install time, used to
//     detect "binary embed advanced past the on-disk copy") and body_hash (the
//     file body as it was written, used by future conflict-detection to spot
//     in-place user edits). At install time the two are equal.
//   - v0 (legacy, read-only): "<!-- pogo-prompt-hash: <hex> -->" recorded only
//     the embed hash. Since post-install body == embed, a v0 stamp is read as
//     embed_hash == body_hash — files installed by older pogo binaries do not
//     spuriously appear "user-edited" on the v1 upgrade.
//
// Each shape has a TOML-comment flavor so the stamp is a valid comment in
// each file format: HTML for .md, # for .toml.
const promptStampPrefix = "<!-- pogo-prompt: "
const promptStampSuffix = " -->\n"
const promptStampPrefixTOML = "# pogo-prompt: "
const promptHashPrefix = "<!-- pogo-prompt-hash: "
const promptHashSuffix = " -->\n"
const promptHashPrefixTOML = "# pogo-prompt-hash: "

// contentHash returns the hex-encoded SHA-256 hash of data.
func contentHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// installedPromptStamp is the parsed pogo-prompt stamp from an installed
// prompt or config file's first line. Empty fields indicate "not stamped" or
// "stamp shape unrecognized."
type installedPromptStamp struct {
	// EmbedHash is the hash of the embed payload that produced the file.
	// InstallPrompts compares it against the current binary's embed to decide
	// whether the on-disk copy is stale.
	EmbedHash string
	// BodyHash is the hash of the file body (everything after the stamp line)
	// as it was written at install time. Conflict detection (separate ticket)
	// will compare it against a fresh hash of the current body to detect
	// in-place user edits.
	BodyHash string
}

// readInstalledPromptStamp reads the stamp from the first line of an installed
// prompt or config file. Recognizes the v1 "pogo-prompt: embed=… body=…" shape
// in both HTML-comment (.md) and TOML-comment (.toml) flavors, and the legacy
// v0 "pogo-prompt-hash: <hex>" shape (treated as EmbedHash == BodyHash so v0
// files don't read as "edited" after upgrade). Returns the zero value when no
// recognized stamp is present.
func readInstalledPromptStamp(path string) installedPromptStamp {
	data, err := os.ReadFile(path)
	if err != nil {
		return installedPromptStamp{}
	}
	firstLine, _, _ := strings.Cut(string(data), "\n")

	stampSuffix := strings.TrimSuffix(promptStampSuffix, "\n")
	if strings.HasPrefix(firstLine, promptStampPrefix) && strings.HasSuffix(firstLine, stampSuffix) {
		body := strings.TrimSuffix(strings.TrimPrefix(firstLine, promptStampPrefix), stampSuffix)
		return parsePromptStampBody(body)
	}
	if strings.HasPrefix(firstLine, promptStampPrefixTOML) {
		body := strings.TrimPrefix(firstLine, promptStampPrefixTOML)
		return parsePromptStampBody(body)
	}

	// v0 backwards-compat: the recorded hash is the embed payload's hash, and
	// since the file body equals the embed at install time, treating it as
	// both EmbedHash and BodyHash matches what the v1 writer would have
	// produced for the same install.
	hashSuffix := strings.TrimSuffix(promptHashSuffix, "\n")
	if strings.HasPrefix(firstLine, promptHashPrefix) && strings.HasSuffix(firstLine, hashSuffix) {
		h := strings.TrimSuffix(strings.TrimPrefix(firstLine, promptHashPrefix), hashSuffix)
		return installedPromptStamp{EmbedHash: h, BodyHash: h}
	}
	if strings.HasPrefix(firstLine, promptHashPrefixTOML) {
		h := strings.TrimPrefix(firstLine, promptHashPrefixTOML)
		return installedPromptStamp{EmbedHash: h, BodyHash: h}
	}

	return installedPromptStamp{}
}

// parsePromptStampBody parses the inner body of a v1 stamp (the part between
// the prefix and the closing "-->", e.g. "embed=sha256:abc body=sha256:def")
// into its embed_hash and body_hash components. Unknown fields are ignored so
// new fields can be added without breaking older readers. The "sha256:" prefix
// is stripped so the returned values are bare hex hashes that compare directly
// against contentHash output.
func parsePromptStampBody(body string) installedPromptStamp {
	var s installedPromptStamp
	for _, field := range strings.Fields(body) {
		key, val, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		val = strings.TrimPrefix(val, "sha256:")
		switch key {
		case "embed":
			s.EmbedHash = val
		case "body":
			s.BodyHash = val
		}
	}
	return s
}

// installedPromptHash returns the embed hash from an installed prompt or
// config file's stamp. Thin wrapper over readInstalledPromptStamp for callers
// that only need the embed-staleness check (InstallPrompts, CheckPromptDrift);
// returns empty string if no recognized stamp is present.
func installedPromptHash(path string) string {
	return readInstalledPromptStamp(path).EmbedHash
}

// currentBodyHash returns contentHash of the file at path with any leading
// pogo-prompt stamp line stripped — i.e., the hash of the body as it would
// have been recorded into the file's stamp at install time. Used by the
// install matrix to detect whether the user has edited the body in place
// after install (current_body_hash != stamp.body_hash).
//
// Returns "" if the file cannot be read so callers can fall through cleanly.
// For unstamped files stripPromptHashStamp is a no-op, so the returned hash
// is the hash of the entire file — that's fine because stamp.BodyHash will
// also be "" for those files and the matrix treats the pair as "unknown,
// safe to update."
func currentBodyHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return contentHash(stripPromptHashStamp(data))
}

// stampedContent prepends a v1 pogo-prompt stamp to a prompt file's content.
// The comment style is chosen by extension so the stamp is a valid comment in
// each file format: HTML for .md, # for .toml. Both embed_hash and body_hash
// in the emitted stamp are equal to contentHash(data) — they only diverge
// later, after a user edits the on-disk file in place.
func stampedContent(path string, data []byte) []byte {
	hash := contentHash(data)
	body := "embed=sha256:" + hash + " body=sha256:" + hash
	var stamp string
	if strings.HasSuffix(path, ".toml") {
		stamp = promptStampPrefixTOML + body + "\n"
	} else {
		stamp = promptStampPrefix + body + promptStampSuffix
	}
	return append([]byte(stamp), data...)
}

// InstallResult describes what happened during prompt installation.
type InstallResult struct {
	Installed []string         `json:"installed"`           // files written (new)
	Updated   []string         `json:"updated,omitempty"`   // files updated (stale)
	Skipped   []string         `json:"skipped"`             // files already up-to-date
	Conflicts []PromptConflict `json:"conflicts,omitempty"` // user-edited canonical + embed changed: new embed written to <Path>.dist
	Backups   []PromptBackup   `json:"backups,omitempty"`   // user-edited canonical files copied to <Path>.bak.<ts> before --force overwrite
}

// PromptConflict reports a prompt where a user-edited canonical file collided
// with a changed embed: the canonical file is preserved as-is, and the new
// embed is written alongside as <Path>.dist for the user to reconcile
// manually. See docs/prompt-customization.md for the merge workflow.
type PromptConflict struct {
	// Path is the relative path of the canonical (user-edited) file under
	// ~/.pogo/agents/. Left untouched by the install.
	Path string `json:"path"`
	// DistPath is the relative path of the .dist sidecar where the new
	// embed was written (always Path + ".dist").
	DistPath string `json:"dist_path"`
}

// PromptBackup reports a user-edited canonical file that was copied aside
// before being overwritten by `pogo install --force`. The backup preserves
// pre-overwrite content so users can recover edits that --force would
// otherwise stomp silently. Suppressed entirely by --no-backup.
type PromptBackup struct {
	// Path is the relative path of the canonical file under ~/.pogo/agents/
	// that was overwritten.
	Path string `json:"path"`
	// BackupPath is the relative path of the .bak.<ts> sidecar that holds
	// the pre-overwrite content (always Path + ".bak." + timestamp).
	BackupPath string `json:"backup_path"`
}

// InstallOpts controls InstallPrompts behavior.
type InstallOpts struct {
	// Force overwrites every embedded file unconditionally, bypassing the
	// conflict-matrix gate that otherwise preserves user-edited canonicals.
	Force bool
	// NoBackup suppresses the user-edit backup that --force normally writes
	// to <Path>.bak.<ts> before overwriting. Only meaningful when Force is
	// true; ignored otherwise.
	NoBackup bool
}

// backupTimeLayout is the deterministic timestamp suffix appended to .bak
// filenames. Compact ISO-8601 (YYYY-MM-DDThhmmssZ) keeps the suffix free of
// path-hostile characters like ':' while remaining sortable and human-readable.
const backupTimeLayout = "2006-01-02T150405Z"

// nowFn returns the wall-clock time used to format backup-file suffixes.
// Replaced in tests so they can assert against a fixed timestamp.
var nowFn = func() time.Time { return time.Now().UTC() }

// minimalMayorPrompt is the empty mayor skeleton written by `pogo init --minimal`.
// It includes frontmatter (auto_start, restart_on_crash) so pogod treats it like
// the full mayor, but leaves the role definition for the user to fill in.
const minimalMayorPrompt = `+++
auto_start = true
restart_on_crash = true
nudge_on_start = "You are now running. Begin your coordination loop."
+++

# Mayor

You are the mayor — the coordinator for this pogo workspace. You are a crew agent, which means you run persistently and pogod restarts you if you crash.

This is a minimal scaffold. Edit this file to describe how you should coordinate work for your specific workflow. ` + "`pogo init`" + ` (without ` + "`--minimal`" + `) installs a coding-oriented mayor prompt that you can use as a reference.

## Your Tools

` + "```bash" + `
# Work items
mg list --status=available     # Unassigned work ready to claim
mg show <id>                   # Full details on a work item

# Agent management
pogo agent list                # Running agents (crew + polecats)
pogo agent spawn-polecat <name> --task="<title>" --body="<details>" --id="<id>" --repo="<repo>"
pogo nudge <name> "<message>"  # Wake up an agent

# Mail
mg mail list mayor             # Check your inbox
mg mail send <agent> --from=mayor --subject="<subj>" --body="<body>"
` + "```" + `

## Identity

Your agent name is ` + "`mayor`" + `. Your prompt file lives at ` + "`~/.pogo/agents/mayor.md`" + `. Edit it to define your coordination strategy.
`

// minimalPolecatTemplate is the polecat skeleton written by `pogo init --minimal`.
// It exposes the standard template variables and a basic claim/done protocol,
// leaving the actual task workflow open for the user to customize.
const minimalPolecatTemplate = `+++
worktree = true
nudge_on_start = "Look at the system prompt and complete the steps for this work item: {{.Id}}"
+++

# Polecat

You are an ephemeral polecat agent. You exist to complete a single task. **Never exit on your own** — the mayor will stop you when your work is verified.

## Your Assignment

**Task:** {{.Task}}

**Work Item ID:** {{.Id}}

**Repository:** {{.Repo}}

**Working Directory:** {{.WorktreeDir}}

### Details

{{.Body}}

## Protocol

1. **Claim the work item:**
   ` + "```bash" + `
   mg claim {{.Id}}
   ` + "```" + `

2. **Do the work** in ` + "`{{.WorktreeDir}}`" + `. Customize this template for your workflow — the default coding profile from ` + "`pogo init`" + ` adds branching, refinery submission, and merge polling.

3. **Mark the work done:**
   ` + "```bash" + `
   mg done {{.Id}}
   ` + "```" + `

4. **Stay alive.** Wait for the mayor to stop you. If the mayor sends a message, act on it immediately.

## Identity

Your agent name is derived from the work item. Your process name follows the pattern ` + "`pogo-cat-<name>`" + `.
`

// InitResult describes what happened during prompt initialization.
type InitResult struct {
	Created []string `json:"created"`           // files newly written
	Skipped []string `json:"skipped,omitempty"` // files already present (force=false; not used when refusal is strict)
	Mode    string   `json:"mode"`              // "default" or "minimal"
}

// InitPrompts scaffolds ~/.pogo/agents/ with prompt files for a fresh workspace.
//
// Unlike InstallPrompts (which auto-updates stale files), InitPrompts is strict:
// if any target file already exists, it returns an error without writing
// anything, unless force is true.
//
// When minimal is true, only a mayor skeleton and a polecat template skeleton
// are written — suitable for non-coding workflows. Otherwise the full
// coding-profile prompts shipped with the binary (mayor + crew/doctor + polecat
// + polecat-qa) are written.
func InitPrompts(force, minimal bool) (*InitResult, error) {
	if err := InitPromptDirs(); err != nil {
		return nil, err
	}

	destRoot := PromptDir()

	// Build the file plan: rel-path -> content (raw, unstamped).
	plan, err := initPromptPlan(minimal)
	if err != nil {
		return nil, err
	}

	// Strict pre-check: if any planned file exists and !force, refuse the whole
	// operation so we never end up partially overwritten.
	if !force {
		var existing []string
		for rel := range plan {
			destPath := filepath.Join(destRoot, rel)
			if _, statErr := os.Stat(destPath); statErr == nil {
				existing = append(existing, rel)
			}
		}
		if len(existing) > 0 {
			return nil, fmt.Errorf("refusing to overwrite existing prompt(s): %s (use --force to override)", strings.Join(existing, ", "))
		}
	}

	result := &InitResult{Mode: "default"}
	if minimal {
		result.Mode = "minimal"
	}

	// Sort for stable output ordering.
	rels := make([]string, 0, len(plan))
	for rel := range plan {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		destPath := filepath.Join(destRoot, rel)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return result, fmt.Errorf("create dir for %s: %w", rel, err)
		}
		if err := os.WriteFile(destPath, stampedContent(rel, plan[rel]), 0644); err != nil {
			return result, fmt.Errorf("write %s: %w", destPath, err)
		}
		result.Created = append(result.Created, rel)
	}

	return result, nil
}

// initPromptPlan returns the rel-path -> raw content map for InitPrompts.
// In default mode this is every file under the embedded prompts/ tree; in
// minimal mode it is just the mayor and polecat template skeletons.
func initPromptPlan(minimal bool) (map[string][]byte, error) {
	if minimal {
		return map[string][]byte{
			"mayor.md":                               []byte(minimalMayorPrompt),
			filepath.Join("templates", "polecat.md"): []byte(minimalPolecatTemplate),
		}, nil
	}

	plan := map[string][]byte{}
	err := fs.WalkDir(defaultPrompts, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel("prompts", path)
		data, readErr := defaultPrompts.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read embedded %s: %w", path, readErr)
		}
		plan[rel] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// InstallPrompts copies the default prompt files embedded in the binary to
// ~/.pogo/agents/. When the canonical file already exists the install applies
// the four-cell conflict matrix (see docs/prompt-customization.md and
// docs/prompt-customization-design.md §B):
//
//	                        | embed unchanged | embed changed
//	------------------------+-----------------+-----------------------------
//	 user has not edited    | skip            | update
//	 user has edited body   | skip            | conflict — write <name>.dist
//
// On the conflict cell, the new embed is written alongside the canonical file
// at <destPath>.dist, the canonical file is left untouched, and the result's
// Conflicts slice records the pair for the caller to surface as a warning.
//
// If opts.Force is true the matrix is bypassed — every file is overwritten
// with the new embed regardless of edit state. Before each overwrite of a
// detectably user-edited canonical (stamp.BodyHash known and current body
// hash differs), the pre-overwrite content is copied to
// <destPath>.bak.<timestamp> and recorded in the result's Backups slice, so
// --force does not silently destroy user customizations. opts.NoBackup
// suppresses that backup write — the documented escape hatch for users who
// genuinely want a clean overwrite. All backups within one InstallPrompts
// call share the same timestamp suffix so a single --force run produces a
// coherent backup set users can grep for and clean up together.
func InstallPrompts(opts InstallOpts) (*InstallResult, error) {
	if err := InitPromptDirs(); err != nil {
		return nil, err
	}

	result := &InstallResult{}
	destRoot := PromptDir()

	// Precompute the .bak suffix once per install run so all files backed
	// up in this --force pass share a single timestamp — easier for users
	// to identify and clean up as a coherent set.
	backupSuffix := ".bak." + nowFn().Format(backupTimeLayout)

	err := fs.WalkDir(defaultPrompts, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Strip the "prompts" prefix to get the relative path
		rel, _ := filepath.Rel("prompts", path)
		if rel == "." {
			return nil
		}
		destPath := filepath.Join(destRoot, rel)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}

		embeddedHash := contentHash(data)

		if !opts.Force {
			if _, statErr := os.Stat(destPath); statErr == nil {
				stamp := readInstalledPromptStamp(destPath)
				if stamp.EmbedHash == embeddedHash {
					// Embed unchanged — skip regardless of whether
					// the user has edited the canonical file.
					result.Skipped = append(result.Skipped, rel)
					return nil
				}
				// Embed has changed (or the file is unstamped). Decide
				// based on whether the user has edited the body in place.
				// Empty stamp.BodyHash means the file is unstamped or
				// shape-unrecognized — we cannot tell, so fall through to
				// the update path (preserves pre-matrix behavior, covered
				// by TestInstallPromptsUpdatesUnstampedFiles).
				if stamp.BodyHash != "" && currentBodyHash(destPath) != stamp.BodyHash {
					distPath := destPath + ".dist"
					if err := os.WriteFile(distPath, stampedContent(rel, data), 0644); err != nil {
						return fmt.Errorf("write %s: %w", distPath, err)
					}
					result.Conflicts = append(result.Conflicts, PromptConflict{
						Path:     rel,
						DistPath: rel + ".dist",
					})
					return nil
				}
				if err := os.WriteFile(destPath, stampedContent(rel, data), 0644); err != nil {
					return fmt.Errorf("update %s: %w", destPath, err)
				}
				result.Updated = append(result.Updated, rel)
				return nil
			}
		} else if !opts.NoBackup {
			// Force-overwrite path: copy aside any user-edited canonical
			// before clobbering it. Strict gate (stamp.BodyHash known and
			// differs from the on-disk body) so we don't generate noise
			// backups for pristine files or for unstamped legacy files we
			// can't classify — those keep --force's pre-mg-7c35 behavior.
			if _, statErr := os.Stat(destPath); statErr == nil {
				stamp := readInstalledPromptStamp(destPath)
				if stamp.BodyHash != "" && currentBodyHash(destPath) != stamp.BodyHash {
					existing, readErr := os.ReadFile(destPath)
					if readErr != nil {
						return fmt.Errorf("read %s for backup: %w", destPath, readErr)
					}
					backupAbs := destPath + backupSuffix
					if err := os.WriteFile(backupAbs, existing, 0644); err != nil {
						return fmt.Errorf("write backup %s: %w", backupAbs, err)
					}
					result.Backups = append(result.Backups, PromptBackup{
						Path:       rel,
						BackupPath: rel + backupSuffix,
					})
				}
			}
		}

		if err := os.WriteFile(destPath, stampedContent(rel, data), 0644); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}
		result.Installed = append(result.Installed, rel)
		return nil
	})

	return result, err
}

// PromptDrift describes a single installed prompt whose content no longer
// matches the embedded source.
type PromptDrift struct {
	// Path is the relative path under ~/.pogo/agents/ (e.g. "pm/pm-template.md").
	Path string `json:"path"`
	// Reason is "missing" if the live file does not exist, "unstamped" if it
	// has no pogo-prompt stamp, or "stale" if the stamped embed hash differs
	// from the embedded content's hash.
	Reason string `json:"reason"`
}

// CheckPromptDrift compares every embedded prompt against its installed copy
// under ~/.pogo/agents/ and returns the set that is missing, unstamped, or
// stale (stamped hash != embedded hash). Returns an empty slice when every
// embedded prompt is present and up-to-date.
//
// Used by `pogo doctor --check` to fail loud when the binary's prompts have
// advanced past what running agents are reading on disk — the failure mode
// behind mg-ec77 (PMs silently skipping behavior added to the embedded
// pm-template).
func CheckPromptDrift() ([]PromptDrift, error) {
	destRoot := PromptDir()
	var drift []PromptDrift

	err := fs.WalkDir(defaultPrompts, "prompts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel("prompts", path)
		destPath := filepath.Join(destRoot, rel)

		data, err := defaultPrompts.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		embeddedHash := contentHash(data)

		if _, statErr := os.Stat(destPath); os.IsNotExist(statErr) {
			drift = append(drift, PromptDrift{Path: rel, Reason: "missing"})
			return nil
		} else if statErr != nil {
			return fmt.Errorf("stat %s: %w", destPath, statErr)
		}

		installed := installedPromptHash(destPath)
		if installed == "" {
			drift = append(drift, PromptDrift{Path: rel, Reason: "unstamped"})
			return nil
		}
		if installed != embeddedHash {
			drift = append(drift, PromptDrift{Path: rel, Reason: "stale"})
		}
		return nil
	})

	return drift, err
}
