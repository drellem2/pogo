package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// The default command template (with --dangerously-skip-permissions) now lives
// on claude.Provider — see internal/claude/provider_test.go for the guard that
// the flag is never accidentally dropped.

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
// panic and correctly identifies commands missing a provider's required
// non-interactive flags. The function logs warnings rather than returning
// errors, so we just verify it runs without panic across the cases.
func TestValidatePolecatCommand(t *testing.T) {
	p := &Provider{
		ID:                  "test",
		NonInteractiveFlags: []string{"--dangerously-skip-permissions"},
	}
	// Has the required flag — no warning.
	ValidatePolecatCommand("claude --dangerously-skip-permissions --append-system-prompt-file /tmp/p.md", p)
	// Missing the flag — warns, must not panic.
	ValidatePolecatCommand("claude --append-system-prompt-file /tmp/p.md", p)
	// Different binary, still missing the flag — warns, must not panic.
	ValidatePolecatCommand("aider --model gpt-4o --read /tmp/p.md", p)
	// Nil provider — no-op, must not panic.
	ValidatePolecatCommand("anything at all", nil)
	// Provider with no required flags — no-op, must not panic.
	ValidatePolecatCommand("anything", &Provider{ID: "noflags"})
}

// TestWriteContextFilePrompt verifies the ContextFile prompt-injection path:
// a provider that injects via a context file gets the persona copied into the
// agent's working directory under the configured filename.
func TestWriteContextFilePrompt(t *testing.T) {
	const persona = "# pogo persona\nthe whole operating prompt\n"

	newPromptFile := func(t *testing.T) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "prompt.md")
		if err := os.WriteFile(f, []byte(persona), 0644); err != nil {
			t.Fatalf("write prompt file: %v", err)
		}
		return f
	}

	t.Run("context-file provider writes the persona", func(t *testing.T) {
		dir := t.TempDir()
		p := &Provider{
			ID: "codex",
			PromptInjection: PromptInjection{
				Kind:        InjectContextFile,
				ContextFile: "AGENTS.override.md",
			},
		}
		if err := writeContextFilePrompt(p, newPromptFile(t), dir); err != nil {
			t.Fatalf("writeContextFilePrompt: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "AGENTS.override.md"))
		if err != nil {
			t.Fatalf("context file not written: %v", err)
		}
		if string(got) != persona {
			t.Errorf("context file = %q, want %q", got, persona)
		}
	})

	t.Run("append-flag provider is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		p := &Provider{
			ID:              "claude",
			PromptInjection: PromptInjection{Kind: InjectAppendFlag, Flag: "--append-system-prompt-file"},
		}
		if err := writeContextFilePrompt(p, newPromptFile(t), dir); err != nil {
			t.Fatalf("writeContextFilePrompt: %v", err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("append-flag provider wrote %d file(s); want none", len(entries))
		}
	})

	t.Run("nil provider is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		if err := writeContextFilePrompt(nil, newPromptFile(t), dir); err != nil {
			t.Fatalf("writeContextFilePrompt(nil): %v", err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("nil provider wrote %d file(s); want none", len(entries))
		}
	})

	t.Run("missing prompt file or dir is a no-op", func(t *testing.T) {
		p := &Provider{
			ID:              "codex",
			PromptInjection: PromptInjection{Kind: InjectContextFile, ContextFile: "AGENTS.override.md"},
		}
		if err := writeContextFilePrompt(p, "", t.TempDir()); err != nil {
			t.Errorf("empty promptFile should be a no-op, got %v", err)
		}
		if err := writeContextFilePrompt(p, newPromptFile(t), ""); err != nil {
			t.Errorf("empty dir should be a no-op, got %v", err)
		}
	})

	t.Run("unreadable prompt file returns an error", func(t *testing.T) {
		p := &Provider{
			ID:              "codex",
			PromptInjection: PromptInjection{Kind: InjectContextFile, ContextFile: "AGENTS.override.md"},
		}
		if err := writeContextFilePrompt(p, "/nonexistent/prompt.md", t.TempDir()); err == nil {
			t.Error("expected an error for an unreadable prompt file")
		}
	})
}
