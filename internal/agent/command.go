package agent

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
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
