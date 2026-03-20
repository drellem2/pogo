# Pogo Architecture

Pogo is an operating system for agent-first development. It combines project discovery, code search, and agent orchestration into a cohesive set of UNIX tools.

## System Model

```
┌─────────────────────────────────────────────────┐
│                    pogod                         │
│  ┌──────────┐  ┌──────────┐  ┌───────────────┐  │
│  │ Projects │  │  Search  │  │    Agents     │  │
│  │ Scanner  │  │  (zoekt) │  │  Supervisor   │  │
│  └──────────┘  └──────────┘  └───────────────┘  │
│  ┌──────────────────────────────────────────┐   │
│  │              Refinery                     │   │
│  │  (merge queue loop)                       │   │
│  └──────────────────────────────────────────┘   │
│  ┌──────────────────────────────────────────┐   │
│  │            Event Log                      │   │
│  │  (~/.pogo/events.jsonl)                   │   │
│  └──────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
          │                    │
          │ HTTP API           │ process mgmt
          ▼                    ▼
┌──────────────┐     ┌─────────────────┐
│  CLI tools   │     │     Agents      │
│  pogo, lsp,  │     │ ┌─────────────┐ │
│  pose, mg    │     │ │ crew-arch   │ │
│              │     │ │ crew-ops    │ │
│              │     │ │ cat-a3f     │ │
│              │     │ │ mayor       │ │
│              │     │ └─────────────┘ │
└──────────────┘     └────────┬────────┘
                              │
                              │ filesystem ops
                              ▼
                    ┌──────────────────┐
                    │    macguffin     │
                    │  ~/.macguffin/   │
                    │  work/ mail/     │
                    │  log/  .git/     │
                    └──────────────────┘
```

## Core Principles

### Agents are UNIX processes

An agent is a Claude Code process with a name, a prompt file, and access to CLI tools. There is no agent framework, no agent SDK, no agent protocol. The process IS the agent. You can find it with `ps`, signal it with `kill`, monitor it with process tools.

**pogod is the parent process.** It spawns agents, allocates a PTY for each, and holds the master file descriptor. This is the standard UNIX pattern — the parent owns the child's terminal. It's how shells, `expect`, `script(1)`, and terminal multiplexers work. We use the same primitive directly rather than going through tmux.

This gives pogod three capabilities for free:
1. **Interactive access** — `pogo agent attach` bridges a user's terminal to the agent's PTY
2. **Input injection** — `pogo nudge` writes to the agent's PTY master fd
3. **Output monitoring** — pogod can read agent output for health checks and idle detection

Two agent types, distinguished by naming convention and lifecycle:

- **Crew** (`pogo-crew-<name>`): Long-running. The daemon restarts them on crash. They handoff to fresh sessions when context fills. They push directly to main.
- **Polecat** (`pogo-cat-<id>`): Ephemeral. Spawned for a single task. Exit on completion. Submit work to the refinery merge queue.

The mayor is a crew agent. There is no special mayor code — just a prompt file that says "you coordinate work."

### The filesystem is the coordination layer

All coordination state lives in macguffin's directory tree (`~/.macguffin/`). Work items are markdown files. Mail is Maildir. Claims are atomic renames. No database, no server, no schema.

Agents interact with state through the `mg` CLI, the same way a human would. There is no internal API for "agent claims work" — the agent runs `mg claim <id>` like anyone else.

### Prompt files are configuration

Agent behavior is defined by markdown files in `~/.pogo/agents/`. Changing an agent's behavior means editing a text file. No restart required for polecats (each spawn reads the template fresh). Crew agents pick up changes on their next handoff cycle.

```
~/.pogo/agents/
├── crew/
│   ├── arch.md
│   └── ops.md
├── templates/
│   └── polecat.md
└── mayor.md
```

### pogod is the substrate

The pogo daemon provides three categories of service:

1. **Discovery** (existing): Project scanning, indexing, code search
2. **Agent supervision** (new): Starting, monitoring, restarting crew agents. Reaping dead polecats.
3. **Refinery** (new): Mechanical merge queue processing

The daemon does NOT make decisions. It does not read work items and decide what to do. It starts agents, keeps crew alive, merges tested branches, and logs events. Decision-making lives in prompt files.

## Agent Lifecycle

### Crew Agent

```
pogo agent start arch
        │
        ▼
   pogod spawns pogo-crew-arch
   (Claude Code + crew/arch.md)
        │
        ▼
   ┌─── Agent runs ◄──────────────────┐
   │    - checks mg hook               │
   │    - processes work                │
   │    - sends/reads mail              │
   │    - pushes to main                │
   │                                    │
   │    Context full?                   │
   │    ├─ yes → handoff ──────────────►│
   │    └─ no  → continue               │
   │                                    │
   │    Crash?                          │
   │    └─ pogod restarts ─────────────►│
   │                                    │
   │    pogo agent stop arch            │
   └──► Agent exits                     │
```

### Polecat

```
pogo agent spawn "fix the auth bug"
        │
        ▼
   pogod creates mg work item (if not already one)
   pogod generates prompt from template + work item
   pogod spawns pogo-cat-<id>
        │
        ▼
   Agent runs
   - claims work item (mg claim)
   - does the work
   - pushes branch
   - marks done (mg done)
   - exits
        │
        ▼
   pogod notices exit
   - logs event
   - runs mg reap (cleanup)
   Refinery picks up branch
   - runs quality gate
   - merges or rejects
```

## Coordination Model

### Work Assignment

Work flows through macguffin:

1. **Human or mayor** creates work: `mg new --type=bug "auth tokens expire early"`
2. **Mayor** (or human) decides who should do it:
   - Crew work: `mg mail send crew-arch --subject="look at gt-a3f"`
   - Polecat work: `pogo agent spawn --item=gt-a3f`
3. **Agent** claims the item: `mg claim gt-a3f`
4. **Agent** completes work: `mg done gt-a3f`

There is no "sling" command. Spawning a polecat with a work item is the assignment. Mailing a crew member is the assignment. The mechanisms are macguffin primitives, not orchestration abstractions.

### Inter-Agent Communication

Two channels:

1. **macguffin mail** — async, persistent. For task descriptions, status reports, questions. Agent checks `mg mail list <self>` periodically.
2. **pogo nudge** — sync, ephemeral. For wakeup signals. pogod writes the message to the target agent's PTY master fd — the agent sees it as typed input. Falls back to mail if the agent isn't running.

No direct RPC. No shared memory. No pub/sub. No tmux. Agents are processes that read files and run commands. pogod mediates interactive access because it owns their terminals.

### The Propulsion Principle

Carried forward from Gas Town because it is the most important operational pattern:

> When an agent finds work on its hook, it runs. No announcement, no confirmation, no waiting for human approval.

This is enforced by convention in prompt files, not by code. The crew prompt says "if you have work, execute it." The polecat prompt says "your task is X, do it now." There is no "are you sure?" step.

## The Refinery

A deterministic loop inside pogod, not an agent.

```
loop (every poll_interval):
  items = mg list --status=available --tag=merge-ready
  for each item:
    branch = item.metadata.branch
    repo = item.metadata.repo

    git fetch origin branch
    git checkout branch
    run quality_gate (build.sh / test.sh / .pogo/refinery.toml)

    if pass:
      git checkout main
      git merge --ff-only branch
      git push origin main
      mg done item.id --result='{"merged": true}'
      log event: refinery.merge

    if fail:
      mg update item.id --status=blocked
      mg mail send item.creator --subject="merge failed" --body="..."
      log event: refinery.fail
```

**Design rationale:** Gas Town's refinery was also deterministic code (not an agent), and this was explicitly validated as the right call. Merge processing is mechanical — it should never spend tokens on judgment. It needs to work even when all agents are down.

**Future:** Batch-then-bisect merging (testing N branches together, binary search on failure) is a known optimization but out of MVP scope.

## Directory Layout

### pogod state

```
~/.pogo/
├── events.jsonl           # Append-only event log
├── agents/
│   ├── crew/
│   │   ├── arch.md        # Crew prompt files
│   │   └── ops.md
│   ├── templates/
│   │   └── polecat.md     # Polecat prompt template
│   └── mayor.md           # Mayor prompt
└── (existing config, search index, etc.)
```

### macguffin state

```
~/.macguffin/
├── work/
│   ├── available/         # Ready to claim
│   ├── claimed/           # In progress (PID-suffixed)
│   ├── done/              # Completed
│   └── pending/           # Blocked on dependencies
├── mail/
│   └── <agent>/
│       ├── new/           # Unread
│       └── cur/           # Read
├── log/                   # Event log (JSONL)
└── .git/                  # Audit trail (cold path)
```

### Per-repo config

```
<repo>/
└── .pogo/
    ├── refinery.toml      # Merge queue config for this repo
    └── search/            # Zoekt index (existing)
```

## Process Naming

Process names are the agent identity system. No registry, no UUID, no database.

| Pattern | Meaning | Example |
|---------|---------|---------|
| `pogo-crew-<name>` | Long-running crew agent | `pogo-crew-arch` |
| `pogo-cat-<id>` | Ephemeral polecat | `pogo-cat-a3f` |
| `pogo-mayor` | The coordinator | `pogo-mayor` |
| `pogod` | The daemon | `pogod` |

Discovery: `pgrep -a pogo-crew` lists all crew. `pgrep -a pogo-cat` lists all polecats. `pogo agent list` wraps this with formatted output.

## API Surface

pogod exposes HTTP endpoints. Existing endpoints are unchanged; new endpoints for agent management:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/agents` | GET | List running agents |
| `/agents/:name` | GET | Agent details + status |
| `/agents` | POST | Start/spawn an agent |
| `/agents/:name` | DELETE | Stop an agent |
| `/refinery/queue` | GET | Pending merge items |
| `/refinery/history` | GET | Recent merge results |
| `/events` | GET | Query event log |

CLI commands (`pogo agent *`, `pogo nudge`) are thin wrappers around these endpoints, following the existing pogo CLI pattern.

## PTY Management

pogod allocates a PTY for each agent it spawns. This is the core mechanism that replaces tmux.

```
┌────────┐         ┌──────────────────────┐
│  User  │         │        pogod         │
│terminal│◄──attach──┤                      │
└────────┘         │  Agent Registry       │
                   │  ┌──────────────────┐ │
┌────────┐         │  │ crew-arch        │ │
│ pogo   │──nudge──►│ │  pid: 12345      │ │
│ nudge  │  (HTTP) │  │  pty: /dev/pts/3 │ │
└────────┘         │  │  master_fd: 7    │ │
                   │  │  started: ...    │ │
                   │  ├──────────────────┤ │
                   │  │ cat-a3f          │ │
                   │  │  pid: 12350      │ │
                   │  │  pty: /dev/pts/4 │ │
                   │  │  master_fd: 8    │ │
                   │  └──────────────────┘ │
                   └──────────────────────┘
                            │
                     PTY slave (stdin/stdout)
                            │
                   ┌────────▼────────┐
                   │  Claude Code    │
                   │  (agent process)│
                   └─────────────────┘
```

**Attach protocol:** `pogo agent attach <name>` opens a unix domain socket to pogod. pogod bridges the user's terminal to the agent's PTY master fd. Raw terminal mode — keystrokes flow to the agent, agent output flows to the user. Detach with an escape sequence (e.g., `~.`). The agent keeps running after detach.

**Idle detection:** pogod reads agent output from the PTY master. When it sees the Claude Code prompt marker (idle state), it knows the agent is ready to receive nudge input. This prevents nudges from interrupting active tool calls.

## Open Questions

1. **macguffin scope.** Should macguffin state be global (`~/.macguffin/`) or per-project? The current leaning is global with pogo providing the project-awareness layer. A work item references a repo path; pogo resolves it.

2. **Polecat concurrency limits.** Should pogod enforce a maximum number of concurrent polecats? Or leave this to the mayor's judgment (via prompt)?

3. **Crew restart semantics.** When pogod restarts a crashed crew agent, does it start a fresh session or attempt to restore? Current leaning: fresh session with handoff mail from the previous run's event log.

4. **Refinery repo access.** The refinery needs to clone/fetch repos to run quality gates. Should it use pogo's discovered project paths directly, or maintain its own worktrees?

5. **Attach transport.** Unix domain socket per agent vs. single pogod socket with multiplexing? Per-agent is simpler. Single socket is cleaner for the API. Leaning per-agent for MVP.

## What This Is Not

- **Not an agent framework.** There is no "pogo agent SDK." Agents are Claude Code processes that use CLI tools.
- **Not a job scheduler.** The mayor decides when to spawn polecats. pogod just executes the spawn.
- **Not a database.** All state is files. All coordination is filesystem operations.
- **Not an IDE.** Pogo is a set of composable tools. It works with any editor, any shell, any workflow.
