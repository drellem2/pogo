package agent

import (
	"bytes"
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
	if p == nil {
		return
	}
	var missing []string
	for _, flag := range p.NonInteractiveFlags {
		if !strings.Contains(tmpl, flag) {
			missing = append(missing, flag)
		}
	}
	if len(missing) > 0 {
		log.Printf("WARNING: polecat command template does not include required "+
			"non-interactive flag(s) %v for provider %q; polecats in new worktree "+
			"directories may be blocked by permission prompts", missing, p.ID)
	}
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
	return nil
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
