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
│  │              Event Log                    │   │
│  │  (~/.pogo/events.log — JSONL)             │   │
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
                    │  .git/           │
                    └──────────────────┘
```

## Core Principles

### Agents are UNIX processes

An agent is a process with a name, a prompt file, and access to CLI tools. There is no agent framework, no agent SDK, no agent protocol. The process IS the agent. You can find it with `ps`, signal it with `kill`, monitor it with process tools.

We start with Claude Code as the agent runtime, but the architecture should not depend on it. The PTY interface, process naming, macguffin coordination, and prompt files are all runtime-agnostic — they work with any process that reads from stdin, writes to stdout, and can run CLI commands. If a better agent runtime emerges (or we want to mix runtimes — Claude Code for some agents, a lighter harness for others), nothing in the architecture should need to change. The agent contract is: you're a UNIX process, you have a prompt, you use `mg` and `pogo` CLI tools.

The harness-specific spawn decisions — launch command, prompt-injection mechanism, PTY nudge dialect, and lifecycle hooks — are bundled behind the `agent.Provider` abstraction (`internal/agent/provider.go`); the `provider` config key under `[agents]` selects which harness to use. Claude Code is the only registered provider today (`internal/claude`), but adding another is a matter of registering a second `Provider` value, not touching the orchestration core.

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

**Frontmatter is the configuration unit.** Each prompt file may declare structured metadata in a TOML frontmatter block (`+++` fences, Hugo-style) at the top of the file. The fields control how pogod runs the agent:

```markdown
+++
auto_start = true
restart_on_crash = true
nudge_on_start = "You are now running. Begin your coordination loop."
worktree = true
+++

# Mayor

You are the mayor — the coordinator for a pogo agent workspace...
```

Recognized fields: `auto_start`, `restart_on_crash`, `nudge_on_start`, `worktree`. Prompts without frontmatter get type-based defaults (crew restart on crash, polecats don't), so existing prompts keep working unchanged. The agent's launch command is not a per-prompt field — it comes from the active `Provider` (or the `[agents] command` config key). Parser internals live in `internal/agent/prompt.go` (`ParsePromptFrontmatter`, `AgentMeta`).

**`restart_on_crash = true` is an always-on contract.** When set, pogod respawns the agent on **any** exit — clean exit (Claude finishes its loop and returns 0), crash (non-zero exit or signal), or explicit `pogo agent stop <name>`. The kill switch for an always-on agent is to remove `restart_on_crash = true` from its frontmatter (or set it to `false`) and then stop it. Registry teardown via `StopAll` (pogod shutdown) bypasses respawn unconditionally so daemon restart and test cleanup don't loop. Implementation: `internal/agent/agent.go` (`Stop`, `StopAll`, `Respawn`) and the OnExit hook in `cmd/pogod/main.go`.

Co-locating "what the agent does" (the prose) with "how it runs" (the frontmatter) keeps a single source of truth for agent identity. There is no separate roster file, no orchestration DAG, no handler-side switch on agent name — adding a new crew agent is a matter of dropping a markdown file with `auto_start = true` into `~/.pogo/agents/crew/`.

### Prompt files are the roster

There is no registry, no roster file, and no `pogo agent register` command. The set of agents that exist is exactly the set of prompt files in `~/.pogo/agents/`. The set of agents pogod boots on startup is exactly the subset whose frontmatter declares `auto_start = true`.

On daemon startup, pogod scans `~/.pogo/agents/` (excluding `templates/`) and starts every prompt with `auto_start = true`. The scan is idempotent — agents already registered (e.g. across a `pogod` restart-while-running) are skipped rather than double-started. Implementation: `internal/agent/autostart.go` (`Registry.AutoStartAgents`).

This is what "filesystem is the coordination layer" means at the configuration tier: the disk is the schema. To add an agent that boots with the daemon, drop a markdown file. To stop one from booting, set `auto_start = false` or delete the file. No daemon API is involved in roster management.

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
   (agent harness + crew/arch.md)
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

The refinery is rigless. It doesn't resolve project references or care how many local clones of a repo exist. Each merge-ready work item carries a repo path; the refinery reads the remote URL from that path and maintains exactly one worktree per remote. Multiple agents can work on different clones of the same repo — the refinery sees one remote and pushes to it.

```
~/.pogo/refinery/
└── worktrees/
    └── <repo-name>/       # One worktree per remote, created on demand
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
      events.Emit(refinery_merged)

    if fail:
      mg update item.id --status=blocked
      mg mail send item.creator --subject="merge failed" --body="..."
      events.Emit(refinery_merge_failed)
```

**Design rationale:** Gas Town's refinery was also deterministic code (not an agent), and this was explicitly validated as the right call. Merge processing is mechanical — it should never spend tokens on judgment. It needs to work even when all agents are down. Own worktrees ensure the refinery never interferes with agent or user checkouts.

**Retry behavior.** If another commit lands on the target between fetch and push (e.g. a CI auto-bump), the ff-only merge fails with a retryable error. The refinery re-runs the full fetch→rebase→gates→merge→push cycle up to `max_attempts` times (default 7). Per-repo `<repo>/.pogo/refinery.toml`:

```toml
[gates]
max_attempts  = 7      # ff-only retry budget — raise on repos that race CI
skip_on_retry = true   # bypass gates on attempts > 1 (cost-saving when
                       # the only change between attempts is a version bump
                       # fetched from main)
```

**Future:** Batch-then-bisect merging (testing N branches together, binary search on failure) is a known optimization but out of MVP scope.

## Scheduler

Pogod hosts a daemon-side scheduler so agents can register cron and one-shot
wakeups that survive host sleep, NTP steps, and pogod restarts. This is the
**canonical** mechanism for crew-agent recurring schedules — Claude's
in-process `CronCreate` is reserved for ephemeral, in-session reminders that
do not need to outlive the agent process.

```
~/.pogo/schedules.json   # versioned JSON, atomic temp+rename writes
{
  "version": 1,
  "schedules": [
    {
      "id":            "research-poll",        // unique slug
      "agent":         "crew-research",        // delivery target
      "cron":          "*/15 * * * *",         // 5-field, local time
      "next_fire":     "2026-05-03T13:30:00Z", // absolute wall-clock UTC
      "replay_policy": "once",                 // once | count | skip
      "delivery":      "nudge",                // nudge | mail
      "message":       "check the queue",      // optional payload
      "created_at":    "2026-05-03T08:32:10Z",
      "last_fire":     "2026-05-03T13:15:00Z",
      "missed_fires":  0
    }
  ]
}
```

**Tick model.** The scheduler ticks off the heartbeat goroutine
(`internal/heartbeat`). Because schedules store absolute wall-clock fire times
and the heartbeat is the same loop that detects clock jumps, a host sleep is
absorbed for free: the goroutine resumes, sees that several `next_fire` times
have passed, applies the entry's replay policy, and reschedules. There is no
separate sleep-aware code path.

**Replay policies.** The fire policy controls what happens after a long sleep:

- `once` (default) — fire exactly once, regardless of how many fire points
  passed. The delivered payload includes the original `due` time and a
  `missed` count so the agent can decide whether to catch up or skip ahead.
- `count` — same delivery as `once`, but accumulates `missed_fires` on disk
  for inspection.
- `skip` — drop the fire entirely if it is older than ~2 tick intervals;
  reschedule to the next future occurrence. Useful for "polling" schedules
  where stale fires have no value.

**Delivery.** A fire delivers either via PTY nudge (default) or macguffin
mail. Nudge falls back to mail when the recipient is not currently registered
with pogod, so a sleeping polecat picks the message up via `mg mail list`
when it next runs.

**Decision boundary.** Like the refinery, the scheduler is mechanical: it
fires, it delivers, it persists. It does not interpret the message or decide
what the agent should do. The decision lives in the agent's prompt — the
scheduler is just the wakeup substrate.

### Agent-side recipe

A crew prompt that wants a sleep-resilient wakeup registers it on startup and
reacts to nudges in its main loop:

```markdown
# crew-research startup

On first boot (or after a handoff), idempotently register your poll schedule:

  pogo schedule crew-research --cron "*/15 * * * *" --id research-poll \
      --replay once --delivery nudge \
      --message "Check the research queue and act on any new items."

Adding the same `--id` twice replaces the existing entry (id is the dedup
key), so it's safe to re-register on every startup.

When you receive a nudge that looks like:

  Check the research queue and act on any new items.
  [scheduler id=research-poll due=... fired=...]

…run your normal processing loop. The bracketed metadata tells you whether
this was an on-time fire or a recovery from a sleep — use the `due` /
`fired` gap to decide whether to skim or catch up.
```

Polecats use the same surface for one-shot wakeups (`--once --in 1h`) when
they want to be re-prompted later without spinning their own polling loop.

**Built-in prompt migration (mg-2f79).** The shipped prompt templates have
all moved their recurring schedules from Claude's in-process `CronCreate` to
`pogo schedule`:

- `internal/agent/prompts/pm/pm-template.md` — three schedules with
  agent-suffixed IDs (`mail-check-pm-<name>`, `sweep-morning-pm-<name>`,
  `sweep-evening-pm-<name>`), all with the default `once` replay policy.
  The morning/evening sweeps are documented as at-most-once on recovery: a
  single catch-up sweep covers an arbitrarily long sleep, no matter how
  many cron points were missed. The agent-name suffix matches the polecat
  `mail-check-<work-item-id>` convention and avoids the registry-purge
  failure mode seen with short / generic IDs (mg-8e5d).
- `internal/agent/prompts/templates/polecat.md` and `polecat-qa.md` — one
  per-polecat mail-check schedule with id `mail-check-<work-item-id>`. The
  mayor cleans these up in step 3 of its coordination loop when stopping a
  polecat — pogod does not auto-GC schedules whose target agent has been
  stopped.
- `internal/agent/prompts/mayor.md` — unchanged. The mayor's in-process
  coordination loop still uses `ScheduleWakeup` for dynamic self-pacing
  (it's event-driven through mail and idempotent across sleep, so missed
  ticks during a sleep just delay the next cycle by one wake).

`CronCreate` remains valid for ephemeral, in-session reminders ("nudge me
again in 5 minutes while I work through this"). It is not appropriate for
any cadence that must outlive a single sleep cycle.

## Event Log

Pogo writes a single append-only JSONL event log at `~/.pogo/events.log`. It captures agent lifecycle (spawn, stop, crash, restart), polecat-specific milestones, work item transitions mirrored from macguffin, mail and nudge activity, and refinery merge attempts. Every line is a self-describing JSON object with a versioned envelope (`schema_version`, `timestamp`, `event_type`, `agent`, optional `work_item_id` / `repo`, plus per-event `details`).

The log is the durable observability spine: it survives `pogod` restarts, makes the system inspectable with `tail -f` + `jq` (no database, no query language), and lets `pogo events` and `mg` share one timeline.

```
~/.pogo/
├── events.log            # active log (JSONL, append-only)
├── events.log.1          # most recent rotation (rotated at 100 MB)
└── events.log.N          # older rotations, oldest dropped after N=5
```

Writers:

- **pogod / agent supervisor** emits `agent_spawned`, `agent_stopped`, `agent_crashed`, `agent_restarted`, `polecat_spawned`, `polecat_completed`.
- **refinery** emits `refinery_merge_attempted`, `refinery_merged`, `refinery_merge_failed`.
- **mg** (via the `pogo events emit` CLI bridge) mirrors `work_item_claimed`, `work_item_completed`, and `mail_sent` from macguffin into the same log so a single tail shows the full system narrative.

Emission is best-effort and non-blocking. Lines under 512 bytes rely on POSIX `O_APPEND` atomicity; longer lines take an advisory `flock`. Disk-full or write errors are logged to stderr and swallowed — the event log never blocks or crashes a calling code path. The writer (`internal/events`) is the single entry point; macguffin remains the source of truth for work item state, the event log is purely observational.

The full schema, identity conventions, event catalog, and worked examples live in [`docs/event-log.md`](docs/event-log.md). That document is the contract; this section is the orientation.

## Directory Layout

### pogod state

```
~/.pogo/
├── agents/                # Prompt files = roster (auto_start frontmatter selects boot set)
│   ├── crew/
│   │   ├── arch.md        # Crew prompt files (TOML frontmatter optional)
│   │   └── ops.md
│   ├── templates/
│   │   └── polecat.md     # Polecat prompt template
│   └── mayor.md           # Mayor prompt
├── events.log             # Append-only JSONL event log (schema: docs/event-log.md)
├── events.log.{1..5}      # Rotated history (100 MB trigger, 5 generations kept)
├── schedules.json         # Daemon-side scheduler state (see Scheduler section)
├── refinery/
│   └── worktrees/         # One worktree per remote, used for merge gates
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
└── .git/                  # Audit trail (cold path)
```

Work item transitions and mail sends are mirrored into `~/.pogo/events.log` via the `pogo events emit` CLI bridge, so a single tail shows the whole system narrative without forcing macguffin to depend on pogo.

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
| `/scheduler/schedules` | GET, POST | List or register pogod-side schedules |
| `/scheduler/schedules/{id}[?agent=X]` | GET, DELETE | Inspect or remove a schedule (composite-keyed; `?agent=` disambiguates when multiple agents share an id, otherwise 409) |
| `/events` | GET | Query event log (`~/.pogo/events.log`, JSONL) |

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
                   │  Agent harness  │
                   │  (Claude Code)  │
                   └─────────────────┘
```

**Attach protocol:** `pogo agent attach <name>` opens a unix domain socket to pogod. pogod bridges the user's terminal to the agent's PTY master fd. Raw terminal mode — keystrokes flow to the agent, agent output flows to the user. Detach with an escape sequence (e.g., `~.`). The agent keeps running after detach.

**Idle detection:** pogod reads agent output from the PTY master. When the output goes quiet for the active provider's idle threshold (`Provider.Nudge.IdleThreshold` — see `internal/agent/provider.go`), it knows the agent is ready to receive nudge input. This prevents nudges from interrupting active tool calls. The threshold is per-harness because output cadence differs between TUIs.

### PTY complexity and the libghostty path

There are two levels of PTY usage:

1. **Dumb byte proxying** — pogod holds the master fd, pipes bytes through on attach, writes strings on nudge. No terminal emulation needed. Both the user's terminal and the agent runtime handle their own rendering. pogod is just a wire. This is sufficient for MVP.

2. **Stream-aware management** — pogod inspects the terminal stream for idle detection, output logging, scrollback capture. This requires parsing escape sequences, which means reimplementing terminal emulation — a substantial undertaking done wrong more often than right.

For level 2, [libghostty](https://ghostty.org) (Ghostty's embeddable terminal library) is the right long-term answer. It provides a correct, high-performance terminal emulator as a library, purpose-built for embedding. Rather than hand-rolling ANSI parsing, pogod would embed libghostty to get a real terminal model it can query: cursor position, screen contents, prompt detection.

**Plan:** Start with dumb byte proxying for MVP. Idle detection can use a simple heuristic (output quiescence + known prompt bytes) without full terminal emulation. If and when full terminal emulation is actually needed, libghostty's stable embeddable API would be the right foundation — but don't add it preemptively.

## Open Questions

1. **Attach transport.** Unix domain socket per agent vs. single pogod socket with multiplexing? Per-agent is simpler. Single socket is cleaner for the API. Leaning per-agent for MVP.

2. **Crew handoff context.** `pogo server stop` kills all agents (pogod holds the PTY master fds, so they can't outlive it). The roster question is solved — `auto_start` frontmatter brings crew back on the next boot — but a freshly restarted crew agent still loses its in-session context. Open: should crew agents mail themselves a handoff note before shutdown (via `mg mail send --self`) so the fresh session can pick up where it left off, mirroring Gas Town's handoff protocol over macguffin mail?

## Resolved Decisions

These questions came up during design and have been answered. Recorded here so they don't resurface.

1. **macguffin scope: global.** One macguffin tree at `~/.macguffin/`, not per-project. Work items reference repo paths as metadata. Pogo provides project awareness via `lsp` and `pose` — macguffin doesn't need to duplicate it. Agents check one place for work.

2. **Polecat concurrency: no limit in pogod.** The daemon doesn't enforce concurrency limits. The mayor (or human) decides how many polecats to spawn. pogod is substrate, not policy.

3. **Refinery repo access: own worktrees.** The refinery maintains dedicated worktrees under `~/.pogo/refinery/worktrees/`, one per repo. It never touches agent or user working directories. Isolation prevents dirty-tree conflicts and keeps merge operations predictable.

4. **No tmux dependency.** pogod allocates PTYs directly and holds master file descriptors. Interactive access (`pogo agent attach`), input injection (`pogo nudge`), and output monitoring are all consequences of the parent-child process relationship. No terminal multiplexer in the stack.

5. **Single event log in pogo.** All events — agent lifecycle, polecat milestones, refinery merges, plus work item transitions and mail mirrored from macguffin — write to one JSONL file at `~/.pogo/events.log`. macguffin remains the source of truth for work item state, but the durable observability spine lives in `~/.pogo/` so pogod's writers (refinery, agent supervisor) don't need a macguffin dependency. `mg` mirrors its transitions in via the `pogo events emit` CLI bridge. Schema and event catalog: [`docs/event-log.md`](docs/event-log.md).

6. **Prompt files are the agent roster.** There is no separate roster file or registry. The set of agents that exist is the set of prompt files in `~/.pogo/agents/`; the boot set is the subset whose TOML frontmatter declares `auto_start = true`. This subsumes the earlier proposal of a `~/.pogo/crew-roster` file — the prompts already on disk are the roster, and adding a new agent is a matter of dropping a markdown file with the right frontmatter. Per-agent runtime knobs (`restart_on_crash`, `nudge_on_start`, `worktree`) live in the same frontmatter block, co-located with the prose that defines the agent's role.

## What This Is Not

- **Not an agent framework.** There is no "pogo agent SDK." Agents are harness processes (Claude Code today) that use CLI tools.
- **Not a job scheduler.** The mayor decides when to spawn polecats. pogod just executes the spawn.
- **Not a database.** All state is files. All coordination is filesystem operations.
- **Not an IDE.** Pogo is a set of composable tools. It works with any editor, any shell, any workflow.
