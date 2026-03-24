# Prompt Portability Guidelines

Guidelines for writing pogo agent prompts that can work across LLM providers.

Pogo currently runs all agents via Claude Code, but the architecture is designed so the agent command is pluggable (see M8.1). These guidelines help keep prompts portable so that swapping the underlying model or runtime requires minimal changes.

## Writing portable prompts

### Use standard markdown

Write prompts in plain markdown. Avoid provider-specific markup like XML tags (`<thinking>`, `<result>`, `<artifact>`), special delimiters, or model-specific formatting conventions.

**Do:**
```markdown
## Your Assignment
Complete the task described below.

### Details
Fix the failing test in auth_test.go.
```

**Don't:**
```xml
<task>
<assignment>Complete the task described below.</assignment>
<details>Fix the failing test in auth_test.go.</details>
</task>
```

### Don't reference the model by name

Prompts should describe the agent's role and behavior without naming the model. Saying "You are Claude" or "As an Anthropic model" couples the prompt to one provider.

**Do:** `You are a QA verification agent.`

**Don't:** `You are Claude, an AI assistant made by Anthropic.`

### Don't assume context window sizes

Don't write instructions like "you have a 200k token context window" or "keep responses under 4096 tokens." Context window sizes vary across providers and change over time.

If you need to express a size constraint, frame it in terms of the task:
- "Keep commit messages under 3 lines."
- "Summarize in 2-3 sentences."

### Rely on CLI tools, not model-specific function calling

Pogo agents interact with the world through shell commands (`mg`, `pogo`, `git`, etc.), not through model-native tool/function calling APIs. This is already how all pogo prompts work — keep it that way.

The agent runtime (currently Claude Code) provides the shell execution capability. Prompts should describe *what commands to run*, not how tool calling works at the API level.

### Keep prompts behavioral, not mechanical

Describe *what the agent should do*, not *how the model should think*. Avoid instructions about chain-of-thought, reasoning steps, or internal processing that assume a specific model architecture.

**Do:** `Review the diff and report any issues.`

**Don't:** `Think step by step. First, analyze each file. Then, reason about potential bugs. Finally, synthesize your findings.`

### Use Go template variables for dynamic content

Pogo expands prompts using Go's `text/template` syntax. Use `{{.Task}}`, `{{.Id}}`, `{{.Repo}}`, `{{.Branch}}`, and `{{.Body}}` for work-item-specific content. This is a pogo convention, not a model concern — it works regardless of provider.

## Current non-portable patterns

The following patterns in pogo's agent infrastructure are Claude Code-specific. They don't appear in prompt files themselves, but they would need attention if supporting a different agent runtime.

### Agent launch command

Agents are launched with:
```
claude --dangerously-skip-permissions --append-system-prompt-file <prompt>
```

A different runtime would need its own launch command. The `pogo-claude` wrapper script (`internal/agent/scripts/pogo-claude.sh`) isolates this — it's the natural place to swap runtimes.

### Interactive session mode

Pogo agents run as interactive Claude Code sessions. Pogod sends a "nudge" (typed input) after spawning to kick off execution. Other runtimes may use a different interaction model (e.g., single-shot API calls, print mode).

### CLAUDE.md auto-discovery

Claude Code automatically loads `CLAUDE.md` files from the project root and parent directories. This gives agents project context for free. Other runtimes would need their own mechanism to inject project context, or pogo would need to append it to the prompt file.

### Permission model

Claude Code's `--dangerously-skip-permissions` flag gives agents unrestricted shell access. Other runtimes may have different sandboxing or permission models.

### Auto-memory

Claude Code has a persistent memory system (`~/.claude/projects/`). Prompts don't reference this directly, but agents benefit from it implicitly. A different runtime would lose cross-session memory unless pogo provided an equivalent.

## Migration surface summary

If pogo adds a second provider, here's what changes:

| Layer | What changes | Where |
|-------|-------------|-------|
| Launch command | New command template per runtime | `internal/agent/api.go`, `pogo-claude` wrapper |
| Prompt injection | `--append-system-prompt-file` → runtime equivalent | `internal/agent/api.go` |
| Nudge mechanism | Interactive stdin → runtime equivalent | `internal/agent/api.go` |
| Project context | CLAUDE.md → append to prompt or use runtime equivalent | `internal/agent/prompt.go` |
| Prompt files | No changes needed (if these guidelines are followed) | `~/.pogo/agents/` |

The key insight: **prompt content is already portable.** The non-portable parts are all in the Go code that launches and manages agents, not in the prompts themselves.

## Audit of current prompts

All prompt files in `internal/agent/prompts/` and `~/.pogo/agents/` were audited against these guidelines.

| Prompt | File | Portable? | Notes |
|--------|------|-----------|-------|
| Mayor | `prompts/mayor.md` | Yes | Uses CLI tools, no model references, standard markdown |
| Polecat | `prompts/templates/polecat.md` | Yes | Template-driven, behavioral instructions only |
| Polecat QA | `prompts/templates/polecat-qa.md` | Yes | Same pattern as polecat, verification-focused |
| Architect (crew) | `~/.pogo/agents/crew/architect.md` | Yes | CLI-driven coordination, no model assumptions |

No current prompt files contain Claude-specific patterns. The prompts are behavioral ("claim the work item", "run the tests", "mail the mayor") and interact through shell commands. They would work with any agent runtime that can execute shell commands and follow markdown instructions.
