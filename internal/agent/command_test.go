package agent

import (
	"testing"
)

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
