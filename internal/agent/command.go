package agent

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// CommandTemplateVars are the variables available in agent command templates.
type CommandTemplateVars struct {
	PromptFile string // Path to the agent's prompt file
	AgentName  string // Agent name
	AgentType  string // "crew" or "polecat"
	WorkDir    string // Working directory for the agent process
}

// ExpandCommand expands a command template string into a command slice.
// The template uses Go text/template syntax with CommandTemplateVars fields.
// Returns the command as a string slice split on whitespace.
func ExpandCommand(tmpl string, vars CommandTemplateVars) ([]string, error) {
	t, err := template.New("cmd").Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("parse agent command template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return nil, fmt.Errorf("expand agent command template: %w", err)
	}

	parts := strings.Fields(buf.String())
	if len(parts) == 0 {
		return nil, fmt.Errorf("agent command template expanded to empty string")
	}
	return parts, nil
}

// ValidatePolecatCommand checks that a polecat command template carries every
// non-interactive flag the provider requires. Polecats run in freshly-created
// worktree directories; without the harness's permission/trust bypass flags,
// the agent blocks on an interactive prompt and cannot execute autonomously.
// Logs a warning naming any missing flags.
//
// A nil provider — or one that declares no NonInteractiveFlags — makes this a
// no-op: there is nothing to require.
func ValidatePolecatCommand(tmpl string, p *Provider) {
	missing := missingNonInteractiveFlags(tmpl, p)
	if len(missing) > 0 {
		log.Printf("WARNING: polecat command template does not include required "+
			"non-interactive flag(s) %v for provider %q; polecats in new worktree "+
			"directories may be blocked by permission prompts", missing, p.ID)
	}
}

// missingNonInteractiveFlags returns the provider's required non-interactive
// flags that the template neither contains directly nor satisfies through a
// declared alias (see Provider.NonInteractiveFlagAliases). A nil provider — or
// one that declares no NonInteractiveFlags — yields nil. Split out from
// ValidatePolecatCommand, which only logs, so the alias contract is testable.
func missingNonInteractiveFlags(tmpl string, p *Provider) []string {
	if p == nil {
		return nil
	}
	var missing []string
	for _, flag := range p.NonInteractiveFlags {
		if flagPresent(tmpl, flag, p.NonInteractiveFlagAliases[flag]) {
			continue
		}
		missing = append(missing, flag)
	}
	return missing
}

// flagPresent reports whether tmpl carries flag or any of its aliases.
func flagPresent(tmpl, flag string, aliases []string) bool {
	if strings.Contains(tmpl, flag) {
		return true
	}
	for _, alias := range aliases {
		if strings.Contains(tmpl, alias) {
			return true
		}
	}
	return false
}

// writeContextFilePrompt delivers the persona prompt to a provider that uses
// the InjectContextFile strategy — it copies the expanded prompt file into the
// agent's working directory under the provider's configured ContextFile name,
// prefixed by the provider's ContextFileHeader.
//
// Codex is the motivating case: it has no --append-system-prompt-file-style
// flag, but reads AGENTS.override.md from its working directory and prefers it
// over a checked-in AGENTS.md. Writing the persona there injects it additively
// without clobbering the repo's own AGENTS.md. Cursor is the same shape one
// level down: its persona lands in .cursor/rules/pogo-persona.mdc — a nested
// path, hence the MkdirAll — behind an `alwaysApply: true` frontmatter header,
// which is the only rule form Cursor reliably folds into the system prompt.
//
// It is a no-op for providers that inject via flag (Claude, pi) or env, for a
// nil provider, and when either the prompt file or the working directory is
// unset (the prompt is still reachable via the POGO_AGENT_PROMPT env fallback
// that Spawn always sets). Returns an error only when the copy itself fails.
func writeContextFilePrompt(p *Provider, promptFile, dir string) error {
	if p == nil || p.PromptInjection.Kind != InjectContextFile {
		return nil
	}
	if promptFile == "" || dir == "" || p.PromptInjection.ContextFile == "" {
		return nil
	}
	content, err := os.ReadFile(promptFile)
	if err != nil {
		return fmt.Errorf("read prompt file for context-file injection: %w", err)
	}
	if header := p.PromptInjection.ContextFileHeader; header != "" {
		content = append([]byte(header), content...)
	}
	dest := filepath.Join(dir, p.PromptInjection.ContextFile)
	// ContextFile may be nested (Cursor: .cursor/rules/pogo-persona.mdc). The
	// worktree carries no such directory, so create it before writing.
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("create context file directory for %q: %w", dest, err)
	}
	if err := os.WriteFile(dest, content, 0644); err != nil {
		return fmt.Errorf("write context file %q: %w", dest, err)
	}
	// Keep the injected persona out of `git add -A`. The context file lands as
	// an untracked file inside the agent's worktree; without this a stray
	// `git add -A` would stage pogo's internal prompt — a dirty polecat branch,
	// or a leaked prompt in a user's own repo. .git/info/exclude is repo-local
	// and never committed, so this touches no tracked file. Best-effort: the
	// persona is already delivered, so a repo without git (or an unwritable
	// exclude) is logged, not fatal. See mg-9de9, gh #40.
	if err := ensureWorktreeGitExcluded(dir, p.PromptInjection.ContextFile); err != nil {
		log.Printf("WARNING: could not gitignore injected persona %q in %s: %v", p.PromptInjection.ContextFile, dir, err)
	}
	return nil
}

// personaExcludeComment marks the block pogo appends to a worktree's
// .git/info/exclude for injected persona files.
const personaExcludeComment = "# pogo injected agent persona (added automatically by pogo)"

// ensureWorktreeGitExcluded appends the injected context-file path to the
// worktree's .git/info/exclude so `git add -A` never stages it. relPath is the
// provider's ContextFile, relative to the worktree root (dir). The pattern is
// anchored to the root with a leading slash so a same-named file deeper in the
// tree is unaffected. Idempotent: an already-excluded path is left untouched.
//
// info/exclude is shared across a repo's linked worktrees, but the injected
// paths (.cursor/rules/pogo-persona.mdc, AGENTS.override.md) are pogo's own and
// never tracked, so excluding them repo-wide is invisible and harmless. Returns
// an error the caller may log; it never fails the spawn.
func ensureWorktreeGitExcluded(dir, relPath string) error {
	if dir == "" || relPath == "" {
		return nil
	}
	pattern := "/" + filepath.ToSlash(filepath.Clean(relPath))

	excludePath, err := worktreeGitExcludePath(dir)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return nil // already excluded
		}
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	var b strings.Builder
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteString("\n")
	}
	// Group all pogo persona patterns under a single comment: only emit the
	// comment the first time we touch this exclude file for a persona path.
	if !strings.Contains(string(data), personaExcludeComment) {
		b.WriteString(personaExcludeComment + "\n")
	}
	b.WriteString(pattern + "\n")
	_, err = f.WriteString(b.String())
	return err
}

// worktreeGitExcludePath resolves the info/exclude file for the worktree rooted
// at dir. `git rev-parse --git-path` handles every layout — plain repo, linked
// worktree (whose info/exclude lives in the shared common dir), submodule — so
// we don't hand-parse gitdir indirection. The toplevel is cross-checked against
// dir because git skips an invalid .git entry and resolves to an enclosing
// repo, whose exclude file we must not touch.
func worktreeGitExcludePath(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel", "--git-path", "info/exclude")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git info/exclude: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("unexpected git rev-parse output: %q", string(out))
	}
	toplevel, exclude := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if toplevel == "" || exclude == "" {
		return "", fmt.Errorf("empty git rev-parse output")
	}
	if !samePath(toplevel, dir) {
		return "", fmt.Errorf("dir %s is not a repo root (toplevel %s); refusing to edit enclosing repo", dir, toplevel)
	}
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(dir, exclude)
	}
	return exclude, nil
}

// samePath reports whether two paths name the same directory, tolerating
// trailing separators and symlinks (macOS /tmp vs /private/tmp).
func samePath(a, b string) bool {
	return resolvePath(a) == resolvePath(b)
}

func resolvePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// ValidateCommandBinary checks that the first token of the command template
// (the binary) exists on PATH. Logs a warning if not found.
func ValidateCommandBinary(tmpl string) {
	// Parse and expand with dummy vars to extract the binary name
	parts := strings.Fields(tmpl)
	if len(parts) == 0 {
		return
	}
	// The first token might be a template expression; try to extract a literal
	binary := parts[0]
	if strings.Contains(binary, "{{") {
		// First token is a template var, can't validate statically
		return
	}
	if _, err := exec.LookPath(binary); err != nil {
		log.Printf("WARNING: agent command binary %q not found on PATH", binary)
	}
}
