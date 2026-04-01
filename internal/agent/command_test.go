package agent

import (
	"strings"
	"testing"
)

// TestDefaultCommandHasPermissionsSkip verifies that DefaultAgentCommand includes
// --dangerously-skip-permissions. Polecats run in freshly-created worktree directories
// that Claude Code has never seen. Without this flag, Claude would prompt for directory
// trust and block autonomous execution.
func TestDefaultCommandHasPermissionsSkip(t *testing.T) {
	if !strings.Contains(DefaultAgentCommand, "--dangerously-skip-permissions") {
		t.Fatal("DefaultAgentCommand must include --dangerously-skip-permissions for autonomous polecat execution in new worktree directories")
	}
}

func TestExpandCommand(t *testing.T) {
	tests := []struct {
		name     string
		tmpl     string
		vars     CommandTemplateVars
		wantArgs []string
		wantErr  bool
	}{
		{
			name: "default claude command",
			tmpl: "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}",
			vars: CommandTemplateVars{PromptFile: "/tmp/prompt.md"},
			wantArgs: []string{
				"claude", "--dangerously-skip-permissions",
				"--append-system-prompt-file", "/tmp/prompt.md",
			},
		},
		{
			name: "all template vars",
			tmpl: "myagent --prompt {{.PromptFile}} --name {{.AgentName}} --type {{.AgentType}} --dir {{.WorkDir}}",
			vars: CommandTemplateVars{
				PromptFile: "/tmp/p.md",
				AgentName:  "test-agent",
				AgentType:  "polecat",
				WorkDir:    "/work/dir",
			},
			wantArgs: []string{
				"myagent", "--prompt", "/tmp/p.md",
				"--name", "test-agent", "--type", "polecat",
				"--dir", "/work/dir",
			},
		},
		{
			name: "aider command",
			tmpl: "aider --model gpt-4o --read {{.PromptFile}}",
			vars: CommandTemplateVars{PromptFile: "/tmp/prompt.md"},
			wantArgs: []string{
				"aider", "--model", "gpt-4o", "--read", "/tmp/prompt.md",
			},
		},
		{
			name:    "empty template",
			tmpl:    "",
			vars:    CommandTemplateVars{},
			wantErr: true,
		},
		{
			name:    "bad template syntax",
			tmpl:    "claude {{.Invalid",
			vars:    CommandTemplateVars{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandCommand(tt.tmpl, tt.vars)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExpandCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.wantArgs) {
				t.Fatalf("got %d args %v, want %d args %v", len(got), got, len(tt.wantArgs), tt.wantArgs)
			}
			for i := range got {
				if got[i] != tt.wantArgs[i] {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], tt.wantArgs[i])
				}
			}
		})
	}
}

// TestValidatePolecatCommand verifies that ValidatePolecatCommand does not
// panic and correctly identifies commands missing the permissions flag.
// The function logs warnings rather than returning errors, so we just
// verify it runs without panic for both valid and invalid templates.
func TestValidatePolecatCommand(t *testing.T) {
	// Should not warn (has the flag)
	ValidatePolecatCommand("claude --dangerously-skip-permissions --append-system-prompt-file /tmp/p.md")
	// Should warn but not panic (missing the flag)
	ValidatePolecatCommand("claude --append-system-prompt-file /tmp/p.md")
	// Non-claude binary (no flag expected but still warns)
	ValidatePolecatCommand("aider --model gpt-4o --read /tmp/p.md")
}
