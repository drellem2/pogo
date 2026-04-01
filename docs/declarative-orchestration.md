# Declarative Agent Roles and Orchestration

Investigation into making pogo's agent roles and orchestration configurable via declarative configuration, rather than the current imperative/prompt-file approach.

## Current Architecture

Today, agent orchestration in pogo is defined across several layers:

1. **Agent types** are hardcoded as Go constants (`TypeCrew`, `TypePolecat`) in `internal/agent/agent.go`.
2. **Roles** are defined by markdown prompt files in `~/.pogo/agents/` — `mayor.md`, `crew/<name>.md`, `templates/polecat.md`. These files contain natural language instructions and are the primary mechanism for defining agent behavior.
3. **Lifecycle rules** are embedded in Go code: pogod restarts crew agents on crash, reaps polecats on exit, the refinery runs a deterministic merge loop.
4. **Orchestration logic** lives in prompt files (the mayor prompt says "spawn polecats for available work") and in `internal/agent/api.go` (the `handleStart` and `handleSpawnPolecat` handlers wire up prompts, worktrees, and nudges).
5. **Configuration** uses a minimal hand-rolled TOML parser in `internal/config/config.go` for server settings, agent command templates, and refinery options.

Key observation: pogo deliberately avoids framework abstractions. Agents are UNIX processes. Coordination is filesystem ops via macguffin. The mayor is "just a prompt." This philosophy shapes which declarative approaches fit and which don't.

## Options Evaluated

### Option 1: Terraform-like HCL Config

Define agents, roles, and orchestration rules in HCL (HashiCorp Configuration Language).

**Example config:**

```hcl
agent "mayor" {
  type = "crew"
  prompt = "agents/mayor.md"
  restart_on_crash = true
  
  nudge {
    on_start = "You are now running. Begin your coordination loop."
  }
}

agent "doctor" {
  type = "crew"
  prompt = "agents/crew/doctor.md"
  restart_on_crash = true
}

template "polecat" {
  type = "polecat"
  prompt = "agents/templates/polecat.md"
  worktree = true
  
  nudge {
    on_start = "Look at the system prompt and complete the steps for this work item: {{.Id}}"
  }
}

refinery {
  enabled = true
  poll_interval = "30s"
}
```

| Dimension | Assessment |
|-----------|-----------|
| Difficulty | Medium. Requires adding `hashicorp/hcl` dependency. HCL parsing is well-supported in Go. |
| Architectural fit | **Poor.** HCL is designed for infrastructure-as-code with plan/apply semantics, state files, and dependency graphs. Pogo agents don't have infrastructure lifecycle — they're processes with prompts. HCL's power (interpolation, modules, providers) would go unused while adding conceptual overhead. |
| Migration path | Replace `config.toml` + prompt discovery with `.hcl` files. Prompt files remain as-is (referenced by path). |
| Pros | Familiar to DevOps users. Rich type system. Good tooling (fmt, validate). |
| Cons | Heavy dependency for what's essentially "list agents and their properties." Philosophical mismatch — pogo is UNIX tools, not infrastructure-as-code. The plan/apply model doesn't map to agent lifecycle. |

### Option 2: YAML Config

Define agent roster and orchestration in YAML.

**Example config (`~/.config/pogo/agents.yaml`):**

```yaml
agents:
  mayor:
    type: crew
    prompt: mayor.md
    restart_on_crash: true
    nudge_on_start: "You are now running. Begin your coordination loop."

  doctor:
    type: crew
    prompt: crew/doctor.md
    restart_on_crash: true

templates:
  polecat:
    prompt: templates/polecat.md
    worktree: true
    nudge_on_start: "Look at the system prompt and complete the steps for this work item: {{.Id}}"

  polecat-qa:
    prompt: templates/polecat-qa.md
    worktree: true

refinery:
  enabled: true
  poll_interval: 30s
```

| Dimension | Assessment |
|-----------|-----------|
| Difficulty | Low. Go has `gopkg.in/yaml.v3` or the stdlib-adjacent `encoding/json` (YAML is a superset). |
| Architectural fit | **Moderate.** YAML is a natural fit for declaring "what agents exist and their properties." But it introduces a second config format alongside the existing TOML. |
| Migration path | Add `agents.yaml` alongside `config.toml`, or consolidate into one format. Prompt files stay as-is. |
| Pros | Human-readable. Widely understood. Low ceremony. Good for declaring agent rosters. |
| Cons | YAML's implicit typing is error-prone. Adds a second config format (TOML already exists). No schema validation without extra tooling. Indentation-sensitive parsing surprises. |

### Option 3: Extended TOML (Expand Current Config)

Extend the existing `config.toml` to include agent roster declarations.

**Example config (`~/.config/pogo/config.toml`):**

```toml
[server]
port = 10000

[refinery]
enabled = true
poll_interval = "30s"

[agents]
command = "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}"

[agents.crew]
command = "claude --dangerously-skip-permissions --append-system-prompt-file {{.PromptFile}}"

# Declare which crew agents exist and should auto-start
[agents.roster.mayor]
type = "crew"
prompt = "mayor.md"
restart_on_crash = true
nudge_on_start = "You are now running. Begin your coordination loop."

[agents.roster.doctor]
type = "crew"
prompt = "crew/doctor.md"
restart_on_crash = true

# Polecat templates (already discovered from filesystem, but can override settings)
[agents.templates.polecat]
worktree = true
nudge_on_start = "Look at the system prompt and complete the steps for this work item: {{.Id}}"
```

| Dimension | Assessment |
|-----------|-----------|
| Difficulty | **Low.** The TOML parser already exists. Just extend the section parsing in `loadConfigFile()`. |
| Architectural fit | **Good.** No new dependencies. No new file formats. Extends the existing pattern. TOML's explicit typing avoids YAML pitfalls. |
| Migration path | Purely additive — new `[agents.roster.*]` sections. Existing `config.toml` files continue to work. Prompt files stay as-is. |
| Pros | Single config file. No new dependencies. Consistent with current approach. TOML is explicit about types. Already familiar to pogo users. |
| Cons | Hand-rolled TOML parser is limited (no arrays of tables, no inline tables). Would need a real TOML library (`pelletier/go-toml`) for nested config, or significant parser expansion. Deep nesting like `[agents.roster.mayor]` gets verbose. |

### Option 4: Custom DSL

A pogo-specific configuration language.

**Example config (`~/.config/pogo/agents.pogo`):**

```
crew mayor {
  prompt "mayor.md"
  restart on-crash
  nudge on-start "You are now running. Begin your coordination loop."
}

crew doctor {
  prompt "crew/doctor.md"
  restart on-crash
}

template polecat {
  prompt "templates/polecat.md"
  worktree on
  nudge on-start "Look at the system prompt and complete the steps for this work item: {{.Id}}"
}

refinery {
  poll every 30s
}
```

| Dimension | Assessment |
|-----------|-----------|
| Difficulty | **High.** Requires writing a parser, defining grammar, handling errors gracefully, documenting the language. |
| Architectural fit | **Mixed.** Reads well for the domain. But inventing a language is a maintenance burden and violates the UNIX philosophy of using standard formats. |
| Migration path | New format entirely. Users must learn it. |
| Pros | Domain-specific expressiveness. Clean syntax for agent declarations. |
| Cons | Maintenance burden of a custom parser. No editor support. No validation tooling. Violates "use boring technology" principle. Only justified if the domain is complex enough — pogo's config is not. |

### Option 5: Go Structs + Prompt File Convention (Status Quo, Refined)

Keep the current approach but make it more explicit and discoverable. Rather than adding a new config format, codify the existing conventions:

1. Prompt files in `~/.pogo/agents/` ARE the agent roster — their presence declares that an agent exists.
2. Add optional TOML frontmatter to prompt files for per-agent settings.
3. The existing `config.toml` handles global settings.

**Example prompt file with frontmatter (`~/.pogo/agents/crew/doctor.md`):**

```markdown
+++
restart_on_crash = true
nudge_on_start = "You are now running. Check your mail."
auto_start = false
+++

# Doctor

You are the doctor — a diagnostic crew agent...
```

**Example (`~/.pogo/agents/mayor.md`):**

```markdown
+++
restart_on_crash = true
auto_start = true
nudge_on_start = "You are now running. Begin your coordination loop."
+++

# Mayor

You are the mayor — the coordinator...
```

| Dimension | Assessment |
|-----------|-----------|
| Difficulty | **Very low.** Parse a small TOML block from the top of existing markdown files. The prompt files already exist. |
| Architectural fit | **Excellent.** Prompt files are already "the config" for agent behavior. Adding structured metadata to them keeps the single-source-of-truth principle. No new files, no new formats, no split between "what the agent does" (prompt) and "how it runs" (separate config). |
| Migration path | Purely additive. Existing prompt files without frontmatter work exactly as before (defaults apply). |
| Pros | Zero new files. Agent definition stays co-located with agent behavior. Filesystem discovery still works. Matches the "prompt files are configuration" principle from ARCHITECTURE.md. Trivial to implement. |
| Cons | Limited expressiveness for cross-agent orchestration rules (e.g., "start doctor only after mayor is running"). But those rules arguably belong in the mayor's prompt, not in config. |

### Option 6: Starlark (Python-like Config Language)

Use Starlark (the language used by Bazel, Buck, and others) for programmatic configuration.

**Example (`~/.config/pogo/agents.star`):**

```python
crew("mayor",
    prompt = "mayor.md",
    restart_on_crash = True,
    nudge_on_start = "You are now running. Begin your coordination loop.",
)

crew("doctor",
    prompt = "crew/doctor.md",
    restart_on_crash = True,
)

template("polecat",
    prompt = "templates/polecat.md",
    worktree = True,
)
```

| Dimension | Assessment |
|-----------|-----------|
| Difficulty | Medium. Good Go library exists (`go.starlark.net`). |
| Architectural fit | **Poor.** Programmable config is justified when configs need conditionals, loops, and abstractions. Pogo's agent config is a flat roster — it doesn't need Turing-completeness. |
| Migration path | New dependency, new format, new semantics to learn. |
| Pros | Familiar Python-like syntax. Programmable if complexity grows. Used by serious build systems. |
| Cons | Massive overkill. Introduces a scripting runtime into what should be a static declaration. The power invites complexity that fights pogo's simplicity principle. |

## Comparison Matrix

| Option | Difficulty | Arch Fit | New Dependencies | New Files | Migration Effort |
|--------|-----------|----------|-----------------|-----------|-----------------|
| HCL | Medium | Poor | hashicorp/hcl | Yes | Medium |
| YAML | Low | Moderate | yaml.v3 | Yes | Low |
| Extended TOML | Low | Good | go-toml (or expand parser) | No | Very low |
| Custom DSL | High | Mixed | None (custom) | Yes | High |
| **Frontmatter in Prompts** | **Very low** | **Excellent** | **None** | **No** | **Very low** |
| Starlark | Medium | Poor | go.starlark.net | Yes | Medium |

## Recommendation

**Option 5: TOML Frontmatter in Prompt Files**, with Option 3 (Extended TOML) as a complement for global settings.

### Rationale

1. **"Prompt files are configuration" is already the architecture.** ARCHITECTURE.md says it explicitly. Adding frontmatter makes implicit properties (restart behavior, nudge messages, auto-start) explicit without splitting them into a separate file.

2. **No new concepts.** Users already know where prompt files live and how to edit them. Frontmatter is a well-understood pattern (Hugo, Jekyll, Obsidian). The cognitive overhead is near zero.

3. **The current hardcoded behaviors become configurable without a new system.** Today, the nudge message for the mayor is hardcoded in `handleStart` (`api.go:319-323`). With frontmatter, it moves to `mayor.md` — where it belongs, since it's part of the agent's role definition.

4. **Global orchestration settings stay in `config.toml`.** The existing config file handles refinery settings, server port, agent command templates. These are infrastructure concerns, not role definitions. The current split is correct.

5. **Cross-agent orchestration doesn't need config.** The question "should the doctor start only after the mayor?" is a coordination decision. In pogo's architecture, coordination decisions live in prompts (the mayor decides what to start) and macguffin (work items gate dependencies). Putting orchestration graphs in config would fight the architecture.

### What This Looks Like Concretely

**Phase 1: Frontmatter parsing (small, immediate)**
- Add a `ParsePromptFrontmatter(path string) (*AgentMeta, string, error)` function to `internal/agent/prompt.go`
- Supported fields: `restart_on_crash`, `auto_start`, `nudge_on_start`, `command` (per-agent override)
- `handleStart` reads frontmatter instead of hardcoding nudge messages and behaviors
- Prompt files without frontmatter get defaults (current behavior)

**Phase 2: Auto-start roster (small, follows Phase 1)**
- On `pogo server start`, pogod scans prompt files for `auto_start = true` and starts those agents
- Replaces the need for manual `pogo agent start mayor` after daemon start
- The crew roster file (`~/.pogo/crew-roster`) mentioned in ARCHITECTURE.md's open questions becomes unnecessary — the prompt files with `auto_start = true` ARE the roster

**Phase 3: Extended config.toml (optional, if needed)**
- If global orchestration settings grow beyond refinery and agent commands, expand the TOML parser or adopt `pelletier/go-toml`
- This is a "cross that bridge when we reach it" item — the hand-rolled parser handles current needs

### What NOT to Build

- **No orchestration DAG.** Agent start ordering, dependency graphs, or "start X before Y" declarations. This is the mayor's job.
- **No runtime re-reading.** Frontmatter is read at agent start time. Changing it requires restarting the agent. This matches the current behavior and avoids complexity.
- **No schema validation framework.** A few `if` statements in Go are fine. Don't add a JSON Schema or validation library.
- **No second config format.** Don't introduce YAML alongside TOML. One format for structured data (TOML), one for agent behavior (markdown with optional TOML frontmatter).

## Appendix: Orchestration vs. Configuration

A useful distinction for this design space:

| Concern | Where it lives today | Where it should live |
|---------|---------------------|---------------------|
| What agents exist | Prompt files on disk | Same (filesystem discovery) |
| How an agent behaves | Prompt file content | Same (natural language) |
| How an agent starts | Hardcoded in `api.go` | **Frontmatter in prompt file** |
| What to restart | Hardcoded (all crew) | **Frontmatter: `restart_on_crash`** |
| What to auto-start | Nothing (manual) | **Frontmatter: `auto_start`** |
| Agent command template | `config.toml` | Same (global default) |
| When to spawn polecats | Mayor prompt | Same (coordination logic) |
| Merge queue behavior | `config.toml` + refinery code | Same |
| Work routing | Mayor prompt + macguffin | Same |

The pattern: **role definition** goes in prompt files (with frontmatter for structured properties). **Infrastructure** goes in `config.toml`. **Coordination** stays in prompts and macguffin. No new layer needed.
