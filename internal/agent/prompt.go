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
	"strings"
	"text/template"
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

// ExpandTemplate reads a template file and expands {{.Variable}} placeholders
// with the provided vars. Uses Go text/template syntax.
func ExpandTemplate(templatePath string, vars TemplateVars) (string, error) {
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("read template: %w", err)
	}

	tmpl, err := template.New(filepath.Base(templatePath)).Parse(string(content))
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

// AgentMeta is the structured metadata declared at the top of a prompt file
// via TOML frontmatter. Zero values mean "use defaults" — the parser returns
// a zero-value AgentMeta for prompts without frontmatter so existing prompts
// behave exactly as before.
//
// Recognized fields:
//   - restart_on_crash: pogod restarts the agent if it exits unexpectedly
//   - auto_start:       pogod starts the agent on daemon boot
//   - nudge_on_start:   message sent to the agent immediately after spawn
//   - command:          per-agent override of the agent command template
//   - worktree:         polecat-style isolated worktree on spawn
type AgentMeta struct {
	RestartOnCrash bool   `json:"restart_on_crash,omitempty"`
	AutoStart      bool   `json:"auto_start,omitempty"`
	NudgeOnStart   string `json:"nudge_on_start,omitempty"`
	Command        string `json:"command,omitempty"`
	Worktree       bool   `json:"worktree,omitempty"`
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
	case "command":
		s, err := parseFrontmatterString(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		meta.Command = s
	default:
		// Unknown keys are tolerated — keeps older binaries forward-compatible
		// with prompt files written for newer schema additions.
	}
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

// promptHashPrefix is the marker used to embed a content hash in installed prompt files.
const promptHashPrefix = "<!-- pogo-prompt-hash: "
const promptHashSuffix = " -->\n"

// contentHash returns the hex-encoded SHA-256 hash of data.
func contentHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// installedPromptHash reads the hash comment from the first line of an installed prompt file.
// Returns empty string if the file has no hash comment.
func installedPromptHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	firstLine, _, _ := strings.Cut(string(data), "\n")
	if strings.HasPrefix(firstLine, promptHashPrefix) && strings.HasSuffix(firstLine, strings.TrimSuffix(promptHashSuffix, "\n")) {
		return strings.TrimPrefix(strings.TrimSuffix(firstLine, strings.TrimSuffix(promptHashSuffix, "\n")), promptHashPrefix)
	}
	return ""
}

// stampedContent prepends a hash comment to prompt file content.
func stampedContent(data []byte) []byte {
	hash := contentHash(data)
	stamp := promptHashPrefix + hash + strings.TrimSuffix(promptHashSuffix, "\n") + "\n"
	return append([]byte(stamp), data...)
}

// InstallResult describes what happened during prompt installation.
type InstallResult struct {
	Installed []string `json:"installed"`         // files written (new)
	Updated   []string `json:"updated,omitempty"` // files updated (stale)
	Skipped   []string `json:"skipped"`           // files already up-to-date
}

// InstallPrompts copies the default prompt files embedded in the binary to
// ~/.pogo/agents/. Stale files are auto-updated by comparing content hashes.
// If force is true, all files are overwritten regardless of hash.
func InstallPrompts(force bool) (*InstallResult, error) {
	if err := InitPromptDirs(); err != nil {
		return nil, err
	}

	result := &InstallResult{}
	destRoot := PromptDir()

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

		// Check if destination already exists
		if !force {
			if _, statErr := os.Stat(destPath); statErr == nil {
				if installedPromptHash(destPath) == embeddedHash {
					result.Skipped = append(result.Skipped, rel)
					return nil
				}
				// Hash mismatch or no hash — file is stale, update it
				if err := os.WriteFile(destPath, stampedContent(data), 0644); err != nil {
					return fmt.Errorf("update %s: %w", destPath, err)
				}
				result.Updated = append(result.Updated, rel)
				return nil
			}
		}

		if err := os.WriteFile(destPath, stampedContent(data), 0644); err != nil {
			return fmt.Errorf("write %s: %w", destPath, err)
		}
		result.Installed = append(result.Installed, rel)
		return nil
	})

	return result, err
}
