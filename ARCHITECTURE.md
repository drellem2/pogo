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
│  │         Event Log (via macguffin)         │   │
│  │  (~/.macguffin/log/)                      │   │
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

An agent is a process with a name, a prompt file, and access to CLI tools. There is no agent framework, no agent SDK, no agent protocol. The process IS the agent. You can find it with `ps`, signal it with `kill`, monitor it with process tools.

We start with Claude Code as the agent runtime, but the architecture should not depend on it. The PTY interface, process naming, macguffin coordination, and prompt files are all runtime-agnostic — they work with any process that reads from stdin, writes to stdout, and can run CLI commands. If a better agent runtime emerges (or we want to mix runtimes — Claude Code for some agents, a lighter harness for others), nothing in the architecture should need to change. The agent contract is: you're a UNIX process, you have a prompt, you use `mg` and `pogo` CLI tools.

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

All coordination state lives in a single global macguffin tree (`~/.macguffin/`). Work items are markdown files. Mail is Maildir. Claims are atomic renames. No database, no server, no schema.

macguffin is global, not per-project. A work item references a repo path in its body; pogo resolves it. This keeps the coordination layer simple — agents check one place for work, not N project directories. Pogo already provides the project-awareness layer via `lsp` and `pose`.

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

## Project References

Projects have a canonical identity (local path) and human-friendly references for CLI and work items.

**Primary key:** The local filesystem path. Always unique, always resolvable, VCS-agnostic. This is what pogod tracks internally (`/Users/daniel/dev/pogo`).

**Human/agent references:** Nobody wants to type full paths. When a CLI command, work item, or prompt refers to a project, pogo resolves the reference using this precedence:

1. **Short name** — last path component: `pogo` → `/Users/daniel/dev/pogo`
2. **Owner/repo** — parsed from git remote origin: `drellem2/pogo` → `/Users/daniel/dev/pogo`
3. **Unique substring** — match across all known projects: `macg` → `/Users/daniel/dev/macguffin`
4. **Ambiguous** — error listing candidates: `"pogo" matches: /Users/daniel/dev/pogo, /Users/daniel/dev/pogod — be more specific`

This is the same pattern as git commit hash prefixes and kubectl resource names. Exact match wins, then unique substring, then error.

The remote-derived `owner/repo` form is a lookup alias, not the identity. Some repos don't have remotes. Some have multiple. The local path is always authoritative. If we ever need to support non-git VCS, the resolution logic just loses the `owner/repo` step — everything else is path-based.

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

The refinery maintains its own git worktrees for testing and merging — it never touches agent or user working directories. This isolates merge operations from active development and avoids dirty-tree conflicts.

```
~/.pogo/refinery/
└── worktrees/
    └── <repo-name>/       # One worktree per repo, created on demand
```

```
loop (every poll_interval):
  items = mg list --status=available --tag=merge-ready
  for each item:
    branch = item.metadata.branch
    repo = item.metadata.repo
    worktree = ensure_worktree(repo)

    cd worktree
    git fetch origin
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

**Design rationale:** Gas Town's refinery was also deterministic code (not an agent), and this was explicitly validated as the right call. Merge processing is mechanical — it should never spend tokens on judgment. It needs to work even when all agents are down. Own worktrees ensure the refinery never interferes with agent or user checkouts.

**Future:** Batch-then-bisect merging (testing N branches together, binary search on failure) is a known optimization but out of MVP scope.

## Directory Layout

### pogod state

```
~/.pogo/
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
| `/events` | GET | Query event log (proxies macguffin) |

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

### PTY complexity and the libghostty path

There are two levels of PTY usage:

1. **Dumb byte proxying** — pogod holds the master fd, pipes bytes through on attach, writes strings on nudge. No terminal emulation needed. Both the user's terminal and the agent runtime handle their own rendering. pogod is just a wire. This is sufficient for MVP.

2. **Stream-aware management** — pogod inspects the terminal stream for idle detection, output logging, scrollback capture. This requires parsing escape sequences, which means reimplementing terminal emulation — a substantial undertaking done wrong more often than right.

For level 2, [libghostty](https://ghostty.org) (Ghostty's embeddable terminal library) is the right long-term answer. It provides a correct, high-performance terminal emulator as a library, purpose-built for embedding. Rather than hand-rolling ANSI parsing, pogod would embed libghostty to get a real terminal model it can query: cursor position, screen contents, prompt detection.

**Plan:** Start with dumb byte proxying for MVP. Idle detection can use a simple heuristic (output quiescence + known prompt bytes) without full terminal emulation. If and when full terminal emulation is actually needed, libghostty's stable embeddable API would be the right foundation — but don't add it preemptively.

## Open Questions

1. **Crew restart semantics.** When pogod restarts a crashed crew agent, does it start a fresh session or attempt to restore? Current leaning: fresh session with handoff mail from the previous run's event log.

2. **Attach transport.** Unix domain socket per agent vs. single pogod socket with multiplexing? Per-agent is simpler. Single socket is cleaner for the API. Leaning per-agent for MVP.

## Resolved Decisions

These questions came up during design and have been answered. Recorded here so they don't resurface.

1. **macguffin scope: global.** One macguffin tree at `~/.macguffin/`, not per-project. Work items reference repo paths as metadata. Pogo provides project awareness via `lsp` and `pose` — macguffin doesn't need to duplicate it. Agents check one place for work.

2. **Polecat concurrency: no limit in pogod.** The daemon doesn't enforce concurrency limits. The mayor (or human) decides how many polecats to spawn. pogod is substrate, not policy.

3. **Refinery repo access: own worktrees.** The refinery maintains dedicated worktrees under `~/.pogo/refinery/worktrees/`, one per repo. It never touches agent or user working directories. Isolation prevents dirty-tree conflicts and keeps merge operations predictable.

4. **No tmux dependency.** pogod allocates PTYs directly and holds master file descriptors. Interactive access (`pogo agent attach`), input injection (`pogo nudge`), and output monitoring are all consequences of the parent-child process relationship. No terminal multiplexer in the stack.

5. **Single event log in macguffin.** All events — work item transitions, agent lifecycle, refinery merges — write to macguffin's log at `~/.macguffin/log/`. pogod does not maintain a separate event log. macguffin is the single state layer; one place to look, one timeline, one tool (`mg log`) to query.

## What This Is Not

- **Not an agent framework.** There is no "pogo agent SDK." Agents are Claude Code processes that use CLI tools.
- **Not a job scheduler.** The mayor decides when to spawn polecats. pogod just executes the spawn.
- **Not a database.** All state is files. All coordination is filesystem operations.
- **Not an IDE.** Pogo is a set of composable tools. It works with any editor, any shell, any workflow.
