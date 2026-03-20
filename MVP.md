# Pogo Agent Orchestration — MVP

This document defines the minimum viable feature set to replace Gas Town's agent orchestration with pogo-native tooling. The goal is a system where agents are UNIX processes, coordination happens through macguffin, and configuration lives in prompt files.

## What We're Replacing

Gas Town proved that autonomous multi-agent development works. But it accumulated conceptual weight: Dolt-backed beads, wisps, convoys, dogs, rigs, witnesses, formulas, molecules. Many of these were workarounds for limitations that are disappearing (context windows, tool reliability, session stability). The next system should carry forward Gas Town's best ideas while drastically simplifying the machinery.

### Lessons from Gas Town Worth Keeping

1. **The Propulsion Principle.** When an agent finds work, it runs. No announcements, no confirmation, no ceremony. This is the single most important pattern — it keeps the system moving when humans are away.

2. **Hook-based work assignment.** An agent's hook is its current assignment. Empty hook = idle. Occupied hook = working. Simple, observable, unambiguous.

3. **Handoff protocol.** Agents can cycle to fresh sessions without losing work state. The hook persists; the handoff mail carries context. This handles Claude session limits gracefully.

4. **Process-name conventions.** Gas Town's naming (`polecat-alpha`, `crew-arch`) made `ps` output readable and enabled simple process management. Worth formalizing.

5. **Event logging.** An append-only event log (JSONL) makes the system observable and debuggable without adding coordination overhead.

6. **Direct-to-main for maintainers.** Crew push to main. Polecats submit to a merge queue. No feature branch sprawl.

### Gas Town Patterns We're Dropping

1. **Dolt as a backend.** Schema migrations, port management, daemon babysitting — all for what amounts to sequential state transitions on work items. macguffin's filesystem approach is simpler and sufficient.

2. **Wisps.** An intermediate abstraction between molecules and polecats that added lifecycle management overhead without clear benefit. Work items go directly to agents.

3. **Convoy orchestration.** Built but rarely used in practice. Multi-agent parallel work can be modeled as multiple macguffin items with a shared dependency.

4. **Rig declarations.** Explicit rig configuration (`gt rig add`) is replaced by pogo's automatic repo discovery.

5. **Witness as a separate agent type.** Health monitoring folds into the daemon. Stale process detection is already in macguffin (`mg reap`).

6. **The Dog taxonomy.** Boot dogs, plugin dogs, formula dogs — too many agent subtypes. Two types are enough: long-running and ephemeral.

7. **Formula/molecule dispatch for infrastructure.** Critical maintenance should be deterministic code, not token-burning agent dispatch.

## Architecture Overview

```
pogod (daemon)
├── Project discovery + indexing (existing)
├── Agent lifecycle management (new)
├── Refinery loop (new)
└── Event log (new)

macguffin (coordination)
├── Work items: available → claimed → done
├── Mail: inter-agent messaging
├── Dependencies: scheduling + promotion
└── Reaping: stale claim recovery

~/.pogo/agents/ (configuration)
├── crew/*.md          — Long-running agent prompts
├── templates/*.md     — Ephemeral agent prompt templates
└── mayor.md           — Coordinator prompt
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full model.

## Pre-MVP: Process Management with PTY

Before any agent orchestration features, pogod needs to be able to own agent processes. This is the foundation that everything else — interactive sessions, nudge, agent lifecycle — builds on.

### Why PTY, not tmux

Gas Town used tmux as its process management layer: agents ran in tmux sessions, nudge used `tmux send-keys`, attach used `tmux attach`. This works but couples the entire system to a terminal multiplexer that wasn't designed for programmatic agent management. It's a UNIX philosophy violation — we're using a user-facing UI tool as plumbing.

The UNIX-native alternative: **pogod allocates a PTY per agent and holds the master file descriptor.** This is exactly what tmux does internally, minus the window/pane/session abstractions we don't need. It's also what shells, `expect`, and `script(1)` do. The parent process owns the child's terminal — the most fundamental UNIX process relationship.

### What to build

**PTY allocation and agent registry:**
```go
// pogod spawns an agent:
// 1. Open PTY pair via github.com/creack/pty (or os/exec + syscall)
// 2. Launch Claude Code with the slave end as controlling terminal
// 3. Hold master fd in an agent registry
// 4. Read agent stdout from master (for logging/buffering)
// 5. Write to master for nudge/interactive input
```

**Attach — connect a human terminal to a running agent:**
```bash
pogo agent attach <name>
```
This connects the user's terminal to the agent's PTY master via pogod. The transport can be a unix domain socket or HTTP upgrade (websocket). The user sees the agent's Claude Code session as if they'd started it directly. Detach (`Ctrl-\` or similar) returns to the user's shell. The agent keeps running.

**Nudge — write to an agent's stdin:**
```bash
pogo nudge <name> "check your mail"
```
pogod receives this via HTTP API and writes the text to the agent's PTY master fd. The agent sees it as typed input. No tmux, no session discovery, no pane management.

### Deliverables

1. `pogod` can spawn a process with a PTY and hold the master fd
2. `pogo agent attach <name>` connects a terminal to a running agent
3. `pogo nudge <name> "message"` writes to an agent's stdin via pogod
4. Agent registry in pogod tracks: name, PID, PTY master fd, start time, type

This is Phase 0 because interactive access to agents is table stakes. Without it, crew agents are batch workers you can't talk to — which defeats the purpose of having persistent agents.

### Implementation notes

Go has good PTY support via `github.com/creack/pty` (widely used, ~3K stars, used by Docker). The core is ~50 lines:

```go
import "github.com/creack/pty"

cmd := exec.Command("claude", "--prompt-file", promptPath)
ptmx, err := pty.Start(cmd)
// ptmx is the master fd — read for output, write for input
// cmd runs with the slave as its controlling terminal
```

The attach protocol (bridging a remote terminal to the PTY) is more involved but well-trodden. `gotty`, `ttyd`, and similar tools do this over websockets. For MVP, a unix domain socket per agent is simplest — `pogo agent attach` connects to the socket and does raw terminal proxying.

---

## MVP Features

### 1. Agent Lifecycle (`pogo agent`)

Two agent types, distinguished by process name and lifecycle:

| | Crew | Polecat |
|---|------|---------|
| Process name | `pogo-crew-<name>` | `pogo-cat-<id>` |
| Lifetime | Persistent — daemon restarts on crash | Ephemeral — exits after task completion |
| Prompt | `~/.pogo/agents/crew/<name>.md` | Generated from template + work item |
| Session | Long-lived, handoff-capable | Single task, no handoff |
| Merge path | Push to main | Submit to refinery |

**Commands:**

```bash
pogo agent start <name>              # Start a crew agent (uses crew/<name>.md)
pogo agent spawn "fix the auth bug"  # Spawn an ephemeral polecat
pogo agent list                      # List running agents (ps-based)
pogo agent stop <name>               # Signal agent to stop
pogo agent status [name]             # Show agent status + current hook
```

**Implementation notes:**
- Agents are Claude Code processes spawned by pogod with PTY allocation (Phase 0)
- Crew agents: `pogod` monitors and restarts on unexpected exit
- Polecats: launched, run, exit. `pogod` notices exit and runs `mg reap` for cleanup
- Process names enable `pgrep pogo-crew` / `pgrep pogo-cat` for discovery
- Each agent gets a macguffin identity (its process name) for claiming work and sending mail

**Startup injection:**
When `pogod` launches an agent, it sets environment variables:
- `POGO_AGENT_NAME` — the agent's identity
- `POGO_AGENT_TYPE` — `crew` or `polecat`
- `POGO_AGENT_PROMPT` — path to the prompt file

The agent's Claude Code session receives its prompt file as system instructions. For crew, this is the static file. For polecats, it's generated from the template with the work item inlined.

### 2. Prompt Files

Agent identity = prompt file. No registry, no database, no schema.

```
~/.pogo/agents/
├── crew/
│   ├── arch.md          # "You are arch, the co-architect..."
│   └── ops.md           # "You are ops, the infrastructure lead..."
├── templates/
│   └── polecat.md       # "You are a polecat. Your task: {{.Task}}"
└── mayor.md             # "You are the mayor. You coordinate..."
```

**Prompt file format:** Plain markdown. The file IS the system prompt. No frontmatter, no YAML, no special syntax beyond optional `{{.Variable}}` template expansion for polecats.

**Template variables for polecats:**
- `{{.Task}}` — work item title
- `{{.Body}}` — work item body (markdown)
- `{{.Id}}` — macguffin work item ID
- `{{.Repo}}` — target repository path (from pogo)

**Why plain files:**
- Editable with any tool (vim, VS Code, another agent)
- Versionable in git
- Readable by humans and agents alike
- No import step — drop a file, it exists

### 3. Mayor

The mayor is a crew agent. Its only special property is its prompt file, which gives it conventions for:

- Periodically checking `mg list --status=available` for unassigned work
- Spawning polecats via `pogo agent spawn` for ready items
- Reading mail from agents that need routing help
- Checking `pogo agent list` for stuck or idle agents
- Running `mg schedule` to promote items whose dependencies cleared

The mayor is NOT special code. It's a prompt file that says "you are the coordinator" and has access to the same CLI tools as everyone else. If the mayor prompt is bad, you edit `~/.pogo/agents/mayor.md`. If you want a different coordination strategy, write a different prompt.

**Bootstrap:** `pogo agent start mayor` launches the mayor like any other crew agent.

### 4. Refinery

The refinery is a daemon loop inside `pogod` (not a separate agent) that processes merge requests from polecats.

**Flow:**
1. Polecat completes work, pushes to a branch, creates a merge-ready macguffin item
2. Refinery picks up the item: pulls branch, runs quality gates (`build.sh`, `test.sh`, or repo-specific config)
3. On pass: fast-forward merge to main, push, mark item done
4. On fail: mark item failed, mail the author agent

**Why a daemon loop, not an agent:**
- Merge queue processing is mechanical and deterministic
- It should never burn tokens on "thinking about whether to merge"
- It needs to be reliable even when all agents are down
- Gas Town learned this lesson: critical infrastructure should be code, not prompts

**Configuration:**
```toml
# ~/.config/pogo/config.toml
[refinery]
enabled = true
poll_interval = "30s"
quality_gate = "./build.sh"     # Default; per-repo override via .pogo/refinery.toml
```

Per-repo override:
```toml
# <repo>/.pogo/refinery.toml
quality_gate = "./build.sh"
branch_pattern = "pogo-cat-*"   # Only merge branches matching this pattern
```

**Scope for MVP:** Single-repo, sequential merge. No batch-then-bisect (Gas Town's refinery had this but it's complex and can come later).

### 5. Nudge (`pogo nudge`)

Agent-to-agent wakeup. Delivered via PTY — no tmux dependency.

```bash
pogo nudge <agent-name> "message"     # Send text to agent's session
pogo nudge mayor "new work available"
pogo nudge arch "check your mail"
```

**Implementation:**
- `pogo nudge` sends an HTTP request to pogod
- pogod writes the message text to the agent's PTY master fd (established in Phase 0)
- The agent's Claude Code session sees it as typed input
- If the agent isn't running, falls back to macguffin mail (async delivery)

**Delivery modes (from Gas Town, simplified):**
- Default: wait for idle prompt, then deliver. Avoids interrupting active tool calls. pogod can detect idle state by monitoring PTY output for the prompt marker.
- `--immediate`: direct write to PTY. For emergencies only.

Nudge is a convenience — not a requirement. Agents should also poll their macguffin mail on a reasonable interval. Nudge just reduces latency for time-sensitive coordination.

### 6. Event Log

All events — work items, agent lifecycle, refinery merges — go to macguffin's log at `~/.macguffin/log/`. macguffin is the single state layer; pogod writes its own events (agent start/stop, refinery merge/fail) there rather than maintaining a separate log.

```json
{"ts":"2026-03-20T10:00:00Z","event":"agent.start","agent":"crew-arch","type":"crew"}
{"ts":"2026-03-20T10:01:00Z","event":"work.claim","agent":"cat-a3f","item":"gt-a3f"}
{"ts":"2026-03-20T10:05:00Z","event":"work.done","agent":"cat-a3f","item":"gt-a3f"}
{"ts":"2026-03-20T10:05:01Z","event":"agent.exit","agent":"cat-a3f","code":0}
{"ts":"2026-03-20T10:06:00Z","event":"refinery.merge","item":"gt-a3f","branch":"pogo-cat-a3f"}
```

This may require an `mg log append` command or equivalent so pogod can write events without reaching into macguffin's directory structure directly.

Not a coordination mechanism — purely observability. `tail -f` for humans, `jq` for agents, `grep` for debugging.

## What's NOT in the MVP

These are things we might want eventually but are explicitly out of scope:

- **Multi-machine coordination.** Single machine first. macguffin's `rename(2)` atomicity doesn't work over NFS.
- **Batch-then-bisect merging.** Sequential merge is good enough to start. Bisect is an optimization.
- **Web UI / dashboard.** The CLI + event log is the interface. A TUI or web dashboard can come later.
- **Cross-repo molecule orchestration.** Multi-repo workflows exist but aren't the first problem to solve.
- **Agent-to-agent RPC.** Agents communicate through macguffin mail and nudge. No direct RPC protocol.
- **Automatic agent scaling.** The mayor spawns polecats based on its prompt instructions, not autoscaling rules.

## Implementation Order

Build in this order — each step is independently useful:

### Phase 0: Process Management (Pre-MVP)
Add PTY allocation, agent registry, attach, and nudge to pogod. This is the substrate that makes agents interactive UNIX processes rather than batch jobs.

**Depends on:** pogod (existing)
**Deliverable:** Can spawn a process with a PTY, attach to it interactively, and write to its stdin programmatically

### Phase 1: Agent Lifecycle
Add `pogo agent start|spawn|list|stop|status` commands. Build on Phase 0's PTY infrastructure to manage crew and polecat processes with proper naming, monitoring, and restart.

**Depends on:** Phase 0, macguffin (existing)
**Deliverable:** Can launch crew agents and ephemeral polecats from the CLI

### Phase 2: Prompt Files
Establish the `~/.pogo/agents/` convention. Template expansion for polecats. Write the initial mayor prompt.

**Depends on:** Phase 1
**Deliverable:** Agent behavior is configured entirely through markdown files

### Phase 3: Nudge
Add `pogo nudge` as a CLI command wrapping Phase 0's PTY write capability. Add idle detection and delivery modes.

**Depends on:** Phase 0 (PTY write), Phase 1 (agent registry)
**Deliverable:** Agents can wake each other up without polling

### Phase 4: Refinery
Add the merge queue loop to pogod.

**Depends on:** Phase 1 (agents need to submit work)
**Deliverable:** Polecats can submit branches; they get merged automatically after quality gates pass

### Phase 5: Mayor Prompt + Integration Testing
Write the real mayor prompt. Run the full loop: human files work → mayor dispatches → polecat executes → refinery merges.

**Depends on:** Phases 1-4
**Deliverable:** End-to-end autonomous development cycle

## Success Criteria

The MVP is done when:

1. A human can file a macguffin work item and walk away
2. The mayor notices the item and spawns a polecat
3. The polecat claims the item, does the work, pushes a branch
4. The refinery merges the branch after tests pass
5. The human comes back to merged code on main

This is the Gas Town loop, rebuilt on UNIX primitives.
