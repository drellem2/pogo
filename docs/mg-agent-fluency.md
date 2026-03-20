# Teaching Pogo Agents Macguffin (mg) Fluency

Investigation: how to give pogo agents the same natural fluency with `mg` that
Gas Town agents have with `bd`.

## How Gas Town Does It

Gas Town injects beads context through a layered system:

### 1. Claude Code Hooks (`SessionStart`)

Configured in `.claude/settings.json` per-role directory:

```json
{
  "hooks": {
    "SessionStart": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "gt prime --hook && gt mail check --inject"
      }]
    }],
    "UserPromptSubmit": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "gt mail check --inject"
      }]
    }]
  }
}
```

The hook command runs at session start. Claude Code captures stdout and injects
it as `<system-reminder>` blocks into the agent's context.

### 2. `gt prime --hook` Output Chain

The prime command detects agent role from the working directory and outputs a
sequence of context blocks:

1. **Session metadata** — role, PID, session ID
2. **Role context** — rendered from Go templates (`polecat.md.tmpl`, `mayor.md.tmpl`)
3. **Context file** — optional `CONTEXT.md` from town root
4. **Handoff content** — pinned handoff beads for session continuity
5. **Attachment status** — molecule/workflow display
6. **`bd prime`** — beads workflow context (commands, statuses, conventions)
7. **Memory injection** — stored agent memories from beads KV store
8. **Mail check** — unread messages formatted as `<system-reminder>`
9. **Molecule context** — if a workflow formula is attached
10. **Startup directive** — role-specific "what to do now" instructions

### 3. CLAUDE.md (Static Context)

Per-directory `CLAUDE.md` files provide persistent context that Claude Code
automatically loads. The town root `CLAUDE.md` is minimal:

```markdown
# Gas Town
Run `gt prime` for full context after compaction, clear, or new session.
```

Project-specific CLAUDE.md files provide domain context (build commands, conventions).

### 4. Template System

Role templates live in `gastownsrc/internal/templates/roles/`. They're Go
templates that receive `RoleData` (role name, rig, work dir, etc.) and render
full role instructions including command references, protocols, and constraints.

### 5. Ongoing Injection

- **`UserPromptSubmit`** hook runs `gt mail check --inject` on every user message,
  delivering nudges and urgent mail mid-conversation
- **`PreCompact`** hook re-primes context before compaction
- **`PreToolUse`** hooks guard dangerous operations (e.g., blocking `gh pr create`)

### Key Insight

The power is in the **combination**: static CLAUDE.md for project basics, dynamic
hooks for role/work/mail context, and ongoing injection for real-time updates.
No single mechanism is sufficient alone.

---

## What mg Fluency Looks Like

An mg-fluent agent should know these patterns without being told:

### Polecat Lifecycle
```bash
mg claim <id>                              # Claim work (atomic, exactly-once)
# ... do work, commit, push ...
mg done <id> --result='{"branch":"..."}'   # Mark done with result sidecar
```

### Mayor Lifecycle
```bash
mg list --status=available                 # Find unassigned work
mg show <id>                               # Read details
pogo agent spawn-polecat <name> --task=... # Dispatch
mg reap                                    # Reclaim dead claims
mg schedule                                # Promote items with met deps
```

### Communication
```bash
mg mail send <agent> --from=<me> --subject="..." --body="..."
mg mail list <me>                          # Check inbox
mg mail read <me> <msg-id>                 # Read message
```

### Work Management
```bash
mg new --type=task "Title"                 # File new work
mg new --type=bug --depends=<id> "Title"   # With dependency
mg list                                    # All items by status
mg show <id>                               # Full details
```

---

## Delivery Mechanisms Evaluated

### Option A: Claude Code Hooks (`mg prime`)

**How it works:** Add an `mg prime` command that outputs mg workflow context.
Configure `.claude/settings.json` hooks to run it at `SessionStart`.

**Pros:**
- Mirrors exactly how Gas Town does it — proven pattern
- Dynamic: can inject current work state, mail, claimed items
- Automatic: fires on every session start, no agent action needed
- Can include role-specific context (mayor vs polecat get different output)

**Cons:**
- Requires implementing `mg prime` command
- Requires `.claude/settings.json` in each agent directory
- Adds startup latency (shell out to `mg prime`)

**Verdict: ESSENTIAL.** This is the primary injection mechanism.

### Option B: CLAUDE.md in the Repo

**How it works:** Add mg command reference and conventions to CLAUDE.md files
in repos where agents work.

**Pros:**
- Zero infrastructure: just a file
- Always loaded by Claude Code
- Good for static context (project conventions, build commands)

**Cons:**
- Static: can't show current work state or mail
- Per-repo: must be duplicated or symlinked across repos
- Pollutes project files with agent-specific content

**Verdict: SUPPLEMENTARY.** Good for project-specific conventions, but not
sufficient for mg fluency on its own.

### Option C: Prompt File Conventions (`~/.pogo/agents/*.md`)

**How it works:** Already exists. Mayor and polecat templates at
`~/.pogo/agents/` contain mg command references.

**Pros:**
- Already implemented
- Used at spawn time by `pogo agent spawn-polecat`
- Template variables (`.Task`, `.Id`, `.Repo`) personalize context

**Cons:**
- Only injected at spawn time, not on resume/compaction
- Static templates, no dynamic state
- Not integrated with Claude Code hooks

**Verdict: KEEP AND ENHANCE.** These are the spawn-time persona. They complement
hooks but don't replace them.

### Option D: pogod Spawn-Time Injection

**How it works:** When pogod spawns a polecat, it could inject mg context
directly via the `--system-prompt` or `--append-system-prompt` flag.

**Pros:**
- Controlled by the daemon
- Can include dynamic state at spawn time

**Cons:**
- Only fires once (at spawn)
- Duplicates what hooks do better (hooks re-fire on compaction)
- Couples pogod to prompt engineering

**Verdict: AVOID.** Let hooks handle context injection. pogod should focus on
process lifecycle.

### Option E: Skills (Slash Commands)

**How it works:** Claude Code skills (e.g., `/mg-status`) that agents can invoke.

**Pros:**
- On-demand, agent-initiated
- Good for complex workflows

**Cons:**
- Requires agent to know to invoke them (chicken-and-egg)
- Not automatic
- Overkill for basic fluency

**Verdict: FUTURE ENHANCEMENT.** Useful for complex workflows once basic
fluency exists, but not the foundation.

---

## Recommendation

### Architecture: Three-Layer Context Injection

```
Layer 1: CLAUDE.md (static)
├── Project conventions, build commands
├── "Run mg prime for agent context"
└── Minimal — points to dynamic context

Layer 2: mg prime (dynamic, hook-injected)
├── Role detection (mayor, polecat, crew)
├── Current work state (claimed items, available work)
├── Mail status (unread messages)
├── Command reference (role-appropriate subset)
├── Protocol reminders (claim → work → done → exit)
└── Fires on: SessionStart, PreCompact

Layer 3: Prompt files (spawn-time persona)
├── ~/.pogo/agents/mayor.md (coordinator persona)
├── ~/.pogo/agents/templates/polecat.md (worker template)
└── Injected by pogod at spawn via --prompt-file
```

### Implementation Plan

#### Phase 1: `mg prime` Command (in macguffin repo)

Add a new `mg prime` subcommand that outputs role-appropriate context.

**Inputs:**
- Role detection: check `POGO_ROLE` env var, or infer from process name
  (`pogo-crew-mayor` → mayor, `pogo-cat-*` → polecat)
- Optional `--role=mayor|polecat|crew` flag for explicit override

**Output (mayor):**
```markdown
# Macguffin Context

## Available Work
- gt-0655: Add mg event append calls to refinery loop
- gt-12de: Add --permission-mode bypassPermissions...
(output of mg list --status=available)

## Claimed Work
(output of mg list --status=claimed)

## Your Mail
(output of mg mail list mayor)

## Quick Reference
mg list [--status=STATUS]    # List work items
mg show <id>                 # Show details
mg reap                      # Reclaim dead claims
mg schedule                  # Promote ready items
mg mail send/list/read       # Communication
```

**Output (polecat):**
```markdown
# Macguffin Context

## Your Assignment
(details of claimed item, if any)

## Quick Reference
mg claim <id>                # Claim work item
mg done <id> [--result=JSON] # Mark done
mg mail send mayor ...       # Ask for help
```

**Files to create/modify:**
- `cmd/mg/prime.go` — new subcommand
- `cmd/mg/main.go` — register subcommand

**Estimated scope:** ~150 lines of Go. Pattern: read filesystem state, format
as markdown, print to stdout.

#### Phase 2: Claude Code Hooks Configuration

Create `.claude/settings.json` for pogo agent directories:

**For mayor (`~/.pogo/agents/.claude/settings.json` or equivalent):**
```json
{
  "hooks": {
    "SessionStart": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "mg prime --role=mayor"
      }]
    }],
    "UserPromptSubmit": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "mg mail list mayor --brief 2>/dev/null || true"
      }]
    }]
  }
}
```

**For polecats (injected by pogod at spawn):**
The polecat's working directory should have a `.claude/settings.json` with:
```json
{
  "hooks": {
    "SessionStart": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "mg prime --role=polecat"
      }]
    }]
  }
}
```

**Implementation:** pogod's `spawn-polecat` should create this settings file
in the polecat's working directory before launching Claude Code.

#### Phase 3: Enhance Prompt Files

Update `~/.pogo/agents/mayor.md` and `templates/polecat.md` to:
- Remove redundant command listings (now in `mg prime` output)
- Add "Run `mg prime` after compaction or context loss" reminder
- Keep role persona and behavioral instructions

#### Phase 4: Ongoing Injection (Future)

- `UserPromptSubmit` hook for mail delivery mid-conversation
- `PreCompact` hook to re-inject mg context before compaction
- `mg mail check --inject` command that wraps output in `<system-reminder>` tags
  (mirroring `gt mail check --inject`)

### What Belongs Where

| Concern | Owner | Mechanism |
|---------|-------|-----------|
| mg command reference | `mg prime` | Hook injection |
| Current work state | `mg prime` | Hook injection |
| Mail notifications | `mg mail check --inject` | Hook injection |
| Role persona/identity | Prompt files | Spawn-time injection |
| Project conventions | CLAUDE.md | Static file |
| Process lifecycle | pogod | Not context |
| Hook configuration | pogod (at spawn) | `.claude/settings.json` |

### Migration Path

1. Implement `mg prime` in the macguffin repo
2. Test manually: `mg prime --role=mayor` should output useful context
3. Add `.claude/settings.json` to mayor's agent directory
4. Verify mayor gets mg context on session start
5. Update `spawn-polecat` to write `.claude/settings.json` for polecats
6. Verify polecats get mg context on session start
7. Add `mg mail check --inject` for ongoing mail delivery
8. Iterate on content based on agent behavior

### Open Questions

1. **Where does polecat `.claude/settings.json` live?** Gas Town uses per-worktree
   directories. Pogo polecats may need a similar convention, or pogod creates
   the file at spawn time.

2. **Should `mg prime` depend on pogod?** Keeping it standalone (reads
   `~/.macguffin/` directly) is simpler and debuggable. pogod shouldn't be
   required for mg to work.

3. **Role detection heuristic:** `POGO_ROLE` env var is cleanest. Fallback to
   process name parsing works but is fragile. Explicit `--role` flag is safest.

4. **Hook configuration management:** Gas Town has `gt hooks sync` to regenerate
   settings files. Pogo may want similar tooling as the number of agents grows,
   but manual configuration is fine for now.
