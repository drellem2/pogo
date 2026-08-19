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

The coordinator (default: mayor; configurable via `[agents] coordinator`, as workers are via `[agents] worker`) is a crew agent. There is no special coordinator code — just a prompt file that says "you coordinate work."

### The filesystem is the coordination layer

All coordination state lives in a single global macguffin tree (`~/.macguffin/`). Work items are markdown files. Mail is Maildir. Claims are atomic renames. No database, no server, no schema.

macguffin is global, not per-project. A work item references a repo path in its body; pogo resolves it. This keeps the coordination layer simple — agents check one place for work, not N project directories. Pogo already provides the project-awareness layer via `lsp` and `pose`.

Agents interact with state through the `mg` CLI, the same way a human would. There is no internal API for state changes — an agent runs `mg claim <id>`, `mg done <id>` and the rest like anyone else.

One exception, and it is a deliberate one: **pogod claims a polecat's work item on its behalf at spawn** (mg-7254). It still does so by shelling out to `mg claim` — the CLI remains the only writer — but the *actor* is the daemon rather than the worker. Leaving the claim to the polecat made ownership depend on the polecat completing a model-API turn, so a worker wedged on a 529 ran for half an hour with its item still in `available/`: invisible to stall-watch, unprotected against a second dispatch, and destined to fail at `mg done`, which refuses an item that was never claimed. Ownership cannot depend on the owner being able to act.

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

**Prompt files have two provenances, and only one of them has an upstream.** `agents/mayor.md`, `agents/crew/doctor.md`, `agents/templates/*.md` and `agents/pm/pm-template.md` are *install output*: `InstallPrompts` writes them from the binary's embedded copy of `internal/agent/prompts`, so the pogo repo is their source of truth and `pogo check-staleness` is how you ask whether the installed copy is current. Every other prompt in `~/.pogo/agents/` — the crew prompts, the PM stubs — exists only there. Backing up `~/.pogo` with a git repo is reasonable and this host does it, but such a repo must track only the second group: tracking install output makes the working tree permanently dirty and puts a merge in conflict with the installer, in the live files the fleet is reading. `pogo doctor --check` reports the condition (`$POGO_HOME version control`, `internal/homevcs`); the decision and the reconciliation sequence are in [docs/pogo-home-version-control.md](docs/pogo-home-version-control.md).

**Frontmatter is the configuration unit.** Each prompt file may declare structured metadata in a TOML frontmatter block (`+++` fences, Hugo-style) at the top of the file. The fields control how pogod runs the agent:

```markdown
+++
auto_start = true
restart_on_crash = true
nudge_on_start = "You are now running. Begin your coordination loop."
worktree = true
+++

# Coordinator

You are the coordinator for a pogo agent workspace...
```

Recognized fields: `auto_start`, `restart_on_crash`, `nudge_on_start`, `worktree`. Prompts without frontmatter get type-based defaults (crew restart on crash, polecats don't), so existing prompts keep working unchanged. The agent's launch command is not a per-prompt field — it comes from the active `Provider` (or the `[agents] command` config key). Parser internals live in `internal/agent/prompt.go` (`ParsePromptFrontmatter`, `AgentMeta`).

**`restart_on_crash = true` is an always-on contract.** When set, pogod respawns the agent on **any** exit — clean exit (Claude finishes its loop and returns 0), crash (non-zero exit or signal), or explicit `pogo agent stop <name>`. The kill switch for an always-on agent is `pogo agent park <name>`: it persists a park flag at `~/.pogo/agents/<name>/.parked` (written *before* the stop, so the respawn can't win the race), removes the agent's schedules (recorded for restore), and stops the process. Parked agents are skipped by boot-time auto-start regardless of `auto_start`, show as `status=parked` in `pogo agent list`, and come back — schedules included — with `pogo agent wake <name>`. For PM-tier `extends` stubs, a stub-level `restart_on_crash` override also wins over the synthesized template's frontmatter. Registry teardown via `StopAll` bypasses respawn unconditionally so the full → index-only transition and test cleanup don't loop. Implementation: `internal/agent/agent.go` (`Stop`, `StopAll`, `Respawn`), `internal/agent/park.go` (`Park`, `Wake`), and the OnExit hook in `cmd/pogod/main.go`.

**The operator-facing consequence: pick the lever that matches the intent.** The always-on contract above is easy to read as a crash-recovery detail and miss as a fact about `stop`, so state it as a rule: for an agent with `restart_on_crash = true`, **`pogo agent stop` is a restart, and `pogo agent park` is the stop**. Park is the supported stopped-by-intent flag, and it is also the supported way to *cycle* an always-on agent — park then wake returns a fresh process with fresh context, and restores the schedules park recorded. It is race-free by construction where a scripted stop→start is not: `ShouldRespawn() == RestartOnCrash && !IsParked(name)` reads the flag on disk at exit time, and `Park` writes it *before* issuing the stop, so the respawn goroutine cannot win. Park is crew-only — it rejects polecats, which are ephemeral and not respawned. Anyone scripting stop→start against an always-on agent is racing the respawn and will see `"already running"` reported as an error when it is really the fresh instance. This contract is documented where the choice is made — the long help for `pogo agent stop`, `park`, and `list` — because an operator reaching for `stop` has no reason to go looking for `park` (drellem2/pogo#89: park's discoverability, not user error).

**Registry presence is not liveness; `diagnose`'s `process_alive` is the teardown signal.** `pogo agent list` is a view of pogod's registry, and neither of its directions is proof: absence is not evidence of exit (an agent pogod never knew about, or one dropped across a restart, is absent while its process runs), and presence is not evidence of life (a listed pid can be stale, and stays stale through the respawn window). The probe is `pogo agent diagnose <name> --json` → `process_alive`, a `kill(pid, 0)` against the agent's pid (`pidAlive`), with `health=exited|dead` alongside it. This is the same "absent and dead are different facts" principle the scheduler's reap path enforces internally with its four-state `AgentUnknown`/`AgentAlive`/`AgentExpected`/`AgentGone` model (`AgentUnknown` as the fail-safe zero value, mg-de08) and that the polecat witness store hardens with a persisted `(pid, start_time)` pair so a recycled pid cannot forge a liveness answer (mg-13a3). Those live inside pogod; `process_alive` is the external surface of the same rule.

**pogod does not stop its agents when it shuts down — it hangs them up.** There is no `signal.Notify` in `cmd/pogod`: SIGTERM (the routine stop — `pogo server stop`, launchd, the nightly restart) kills at the default disposition, and the one other exit is `log.Fatal(Serve(...))` at the bottom of `main`. Both skip deferred functions, as do SIGKILL, panic and host crash — so no cleanup runs on any path out. Agents die because pogod owns each one's PTY master: its death force-closes that fd, revoking the controlling terminal of an agent that is a session leader (gh #22), which takes SIGHUP and dies at the default disposition.

That accident is **load-bearing**, not incidental: the mail-check GC reaps any polecat absent from the in-memory registry, which is only sound because a polecat cannot outlive pogod. It holds only while the harness binary leaves SIGHUP at its default disposition — a provider that traps SIGHUP re-opens the dark-polecat path silently (mg-13a3 adds the pid+start_time witness for exactly that). Pinned by `TestPolecatDoesNotOutlivePogod` (`internal/agent/polecat_pty_hangup_test.go`, mg-61a0); see `docs/investigations/pogod-shutdown-stops-nothing-2026-07-17.md`.

Co-locating "what the agent does" (the prose) with "how it runs" (the frontmatter) keeps a single source of truth for agent identity. There is no separate roster file, no orchestration DAG, no handler-side switch on agent name — adding a new crew agent is a matter of dropping a markdown file with `auto_start = true` into `~/.pogo/agents/crew/`.

### Prompt files are the roster

There is no registry, no roster file, and no `pogo agent register` command. The set of agents that exist is exactly the set of prompt files in `~/.pogo/agents/`. The set of agents pogod boots on startup is exactly the subset whose frontmatter declares `auto_start = true`.

On daemon startup, pogod scans `$POGO_HOME/agents/` (default `~/.pogo/agents/`, excluding `templates/`) and starts every prompt with `auto_start = true`. The scan is idempotent — agents already registered (e.g. across a `pogod` restart-while-running) are skipped rather than double-started. Both the boot-time prompt refresh and the auto-start scan are gated on a `config.toml` existing: a daemon with no config file (a fresh install, or an isolated `POGO_HOME` sandbox) never installs prompts or spawns agents (mg-3dc3). Implementation: `internal/agent/autostart.go` (`Registry.AutoStartAgents`).

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
   pogod claims the work item (mg claim, mg-7254)
   - before the process starts, so the item never
     sits in available/ under a working polecat
   - a claim a DEAD dispatch left under pogod's own
     pid, with nothing in flight and no live agent,
     is adopted in place rather than refused
     forever (mg-790f)
        │
        ▼
   Agent runs
   - confirms it owns the item (mg show)
   - re-stamps the claim to its own pid
     (mg reclaim, mg-7d6d) — a rename inside
     claimed/, and pogod's hard evidence that
     this agent executed a turn
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

1. **Human or coordinator** creates work: `mg new --type=bug "auth tokens expire early"`
2. **Coordinator** (or human) decides who should do it:
   - Crew work: `mg mail send crew-arch --subject="look at gt-a3f"`
   - Polecat work: `pogo agent spawn --item=gt-a3f`
3. **pogod** claims the item at spawn, before the polecat's process starts: `mg claim gt-a3f` (mg-7254). A second dispatch onto an already-claimed item is refused — the claim is an atomic rename, so the store enforces single ownership rather than the dispatcher remembering to check.
4. **Agent** re-stamps the claim to its own pid as its first act: `mg reclaim gt-a3f` (mg-7d6d). Ownership does not change hands — the re-stamp is a rename *within* `claimed/`, so the item is never back in `available/` — but the pid does, and that is pogod's only hard evidence that the agent executed a turn rather than being wedged with an unsubmitted kickoff. It engages only where `mg` supports it; pogod probes at startup and falls back to a weaker screen-based signal otherwise.
5. **Agent** completes work: `mg done gt-a3f`

**When a dispatch dies holding the claim.** The claim is taken at step 3 and the
polecat's re-stamp is step 4, so between them the claim names pogod's pid and no
agent answers for it. A dispatch that dies in that window — killed, wedged inside
`Spawn`, or failing before any cleanup runs — leaves the item in `claimed/` with
nothing working it, where dispatch skips it and stall-watch does not look. A
failed spawn does hand the claim back, but only if it lives long enough to
(mg-6f5e produced no output at all), so the durable guard is in the **claim
check**: a claim under **pogod's own pid**, with **no dispatch in flight** and
**no live agent** on the item, is owned by nothing and the next dispatch adopts
it (mg-790f). Adoption writes nothing — the claim file is already what step 3
produces — so the item never passes back through `available/` and the guarantee
above is preserved. Holder liveness is *not* consulted: a re-stamp names the
short-lived `mg reclaim` subprocess, so a dead claim pid is the normal state of a
properly owned item. A claim under any *other* pid, including an earlier pogod's,
is refused rather than adopted — it is indistinguishable from a human's — and the
refusal names the holder, the in-flight dispatch, or the live polecat, because
the half-hour cost of this class of failure was never the state itself but the
absence of any command that could see it.

There is no "sling" command. Spawning a polecat with a work item is the assignment. Mailing a crew member is the assignment. The mechanisms are macguffin primitives, not orchestration abstractions.

### Dispatch gates

`pogo agent spawn-polecat` is the one chokepoint every item passes through
before any worker touches it, whoever filed it. The gates sit there, above
every side effect, so a refused dispatch leaves no worktree, agent dir, or
prompt file behind. They ask different questions and fail in different
directions:

| Gate | Question | Answer | Default |
|---|---|---|---|
| **Assignee** | May this item be executed automatically at all? | **409** — permanent; retrying unchanged is refused forever | enforces `human` / `parked` / `blocked:<agent>` |
| **Pairing** | Has the obligation this item's repo puts on it been discharged? | **409** — permanent until the pair is filed | inert; one deployment's config |
| **Stranded work** | Does this item already have pushed work a worker would ignore? | **409** — permanent until the branch is merged or abandoned | enforces, scanning |
| **Preserved worktree** | Is this item's work already written and *uncommitted* in a retained worktree? | **409** — permanent until the tree is committed, rescued or ruled spent | enforces, scanning |
| **Merged work** | Has this item's work **already landed** on the target? | **409** — permanent until the item is closed | enforces when a refinery exists |
| **Host load** | Can this **host** take another worker right now? | **503** — a *later*; the same request succeeds once the host clears | enforces, measuring |

The first five are about the item. The last is about the machine, and nothing
before it measured that.

**Why the third exists.** Stopping a wedged polecat releases its claim and
returns the item to `available/` without consulting its branch, so an item whose
worker finished and pushed re-enters the pool describing itself as unstarted
(mg-b468). On 2026-08-05 a re-dispatch went out three minutes after such a stop
and spent its life duplicating 1026 lines; five more items carried pushed
**pre-registration** commits, where a worker starting from the target writes its
predictions after seeing the results and the artifact is indistinguishable from a
valid one. The stop side now reports (`work_item_stranded_push`) rather than
refusing — refusing the claim release would strand the item in `claimed/` under a
dead pid, which is worse — so the refusal lives here, at the harm moment.

The check (`internal/strandedwork`) answers with **patch identity** (`git
cherry`), never ancestry: the refinery merges by rebase, so `git log
main..branch` reports every successfully merged branch as unmerged. It
distinguishes `resubmit` from `pre_registration` because the two need opposite
handling, and from `carried` — a branch whose commits another branch already
carries **and owns**, which is what a reviewer's worktree branch is after it
checks the branch under review out (mg-1af2). The discriminator is *ownership*,
not containment: containment is symmetric, so "some other branch has these
commits" would silence the builder's genuine strand as readily as the reviewer's
false one. It consults **no notion of liveness** — a running polecat is the
precondition for stranded work, not evidence against it, because the re-dispatch
*is* the running polecat. Attribution of a branch to an item is heuristic (a
commit-subject id, or the item's id-suffix in the branch name), so the refusal is
overridable with `--stranded-override="<why>"`, recorded as an event.

**Why the fourth exists.** It is the third one's mid-flight twin, and the gap
between them was the *normal* state of every worker. The stranded-work gate is
defined over **commits**, and a polecat commits at the END of its life — so a
crash, a stop or an outage leaves behind exactly the state neither gate covered:
uncommitted files in a worktree, zero commits ahead of the target, invisible to
`git cherry`, to `pogo check-stranded` and to both stranded-push reporters, by
construction.

Everything *except* a gate already existed for this state (mg-836c). pogod
detects the dirty tree on exit, **preserves** it rather than reaping it, emits
`worktree_preserved`, mails the coordinator a per-tree notice naming the tree,
the files and the prohibition, and `pogo gc --list-preserved` reports the whole
retained population. None of it reached dispatch: the item stayed `available`,
stall-watch and priority-wake advertised it as ready, and `spawn-polecat` did not
refuse. After the 2026-08-14→19 outage five open items were in that state; the
one dispatch attempted against them was stopped only by accident, because `git
worktree add` failed on a branch still checked out — an error naming a different
reason, whose obvious remedy (remove the stale worktree, re-dispatch) destroys
the only copy of the files.

The notice had said all of this correctly six hours earlier. It fires **once**,
says so in its own text, and went to **one** addressee, who was itself among the
agents down in the outage it was reporting on. *A one-shot notice to a single
addressee is exactly as reliable as that addressee.* This gate is the same
knowledge held as a standing property of the item, which is the difference
between a notice and a guard.

It **fails open** on a polecats directory it cannot reach and **closed** on a
tree it found and could not read — gc already refuses to reclaim such a tree, and
a gate that dispatched over one would be less careful than the reaper it covers
for. Its remedy is deliberately *not* `refinery submit`: nothing is committed, so
there is nothing to submit, and the disposition is a decision (commit the tree
and land it, rescue what is worth keeping, or rule the work spent). Overridable
with `--preserved-override="<why>"`, recorded as an event — and that is the one
override whose consequence is not recoverable, since the tree is reaped once its
item concludes.

The same probe feeds stall-watch, so the surfaces that *advertise* an item stop
advertising it: `internal/stallwatch`'s preserved-worktree check reports it with
the opposite remedy instead of dropping it silently.

**Why the fifth exists**, given the third. They are complements, not degrees of
the same check: the stranded-work gate covers a branch that is pushed and
**unmerged**, and the case it stops applying to is the one that opens the moment
that branch lands. pogod closes a work item at merge, but the close can be
**refused, correctly** — an item tagged `declares-remainder` names work that must
outlive it, and `mg done` turns such an item away until a successor is named. So
the merge lands, the close is refused, the polecat is stopped, its claim is
released, and the item is back in `available/`: genuinely unclaimed, genuinely
open, and completely done. That happened to mg-0e8c and mg-ac0c 66 minutes apart
on 2026-08-12, and priority-wake advertised each as "high priority, ready and
unclaimed" within minutes of its branch merging (mg-9d4e).

The guard that produces that state is **not** the bug and is not weakened here.
Its alternative failure is strictly worse and happened the same night: mg-69f1
was untagged, closed cleanly, and silently dropped its remainder. A completed
item re-offered to the queue is loud and recoverable; a lost remainder is
neither.

The gate answers from the **refinery's own record** of the merge, keyed on the
`--author` the branch was submitted under, rather than from git: a heuristic
refusal gets overridden on reflex, and the git-side attribution routes are the
ones `internal/strandedwork` already documents as incomplete. It excludes
**PR-flow** merges, which land on an integration branch whose deliverable — a PR
against the default branch — does not exist yet, so the item is legitimately
unfinished. It fails **open** on an absent id, an absent refinery, or a merge the
bounded in-memory history has forgotten, so a quiet answer is never proof that
nothing merged. `--merged-override="<why>"` dispatches anyway; unlike the other
gates' overrides this has a use that is not a false positive, since an item can
genuinely owe work after its merge.

The same moment is reported as well as refused. When the post-merge `mg done` is
turned away for any reason other than the worker having already closed the item,
pogod emits `work_item_merged_not_closed` and mails the coordinator. The
detection has to live in the daemon: pogod stops the polecat about half a second
after the merge whether or not the close applied, so the worker is not slow to
notice, it is gone.

**Why the last one exists.** The concurrency rule is "a reasonable limit is 3-5
concurrent workers", and a count of slots cannot see what is in them. Measured
(mg-1b8c): identical gate work took **11.5s** on a host with capacity and
**78.5s** on a full one, enough to push a gate through a fixed timeout and
produce a merge failure attributed to a branch that was fine. The live instance
was **one** worker that had self-parallelised into three compute processes
holding ~5.7 of 10 cores — which any count of agents reads as an idle box. So
the gate counts the **resource**, via `internal/hostload`'s process-subtree
attribution, and refuses above `FleetHeavyAt` (half the cores held by the fleet).

**What it deliberately does not read.** Not the load average — measured at 214
against ~7.5 of 10 cores actually in use, because Darwin counts I/O waiters, so
a guard keyed on it refuses to dispatch while cores sit idle. Not total host
CPU — a VPN client and the system indexer held cores that pausing fleet work
would not give back, and gating on them hands an unrelated process a veto over
the fleet's dispatch. Not a count of agents, for the reason above.

**It does not predict cost.** Nothing on a work item declares one; per-repo
history is wrong in exactly the expensive cases; and a filer-set marker depends
on somebody remembering, where an observation of the host does not. The gate
makes the weaker claim the evidence supports: what the fleet holds *now*.

**It fails open**, like the stranded-work and preserved-worktree gates and unlike
the assignee and pairing gates — an unreadable or unattributable
sample proceeds. Refusing work on missing information stalls the queue for a
reason nobody downstream can check or clear.

`pogo host load` reports the same numbers from the same gate, so a
coordinator's plan and pogod's enforcement cannot disagree.

### The `declares-remainder` warning is injected, not composed

One thing at the dispatch point is **not** a gate and must not become one. An
item tagged `declares-remainder` says its own output is a recommendation, so
`mg done` refuses it until a successor is named — and the worker is stopped
within seconds of its branch merging, which means a close it did not prepare for
lands on nobody: the item returns to `available/` where the priority wake and the
stall watch advertise finished work as unclaimed, clearable only by the
coordinator.

The control for that was a line in the coordinator's operating instructions —
*when you dispatch such an item, say so in the body you pass*, the body being the
snapshot the worker reads and the only channel that reaches it in time. That
line is in the right file, addressed to the right actor, and it failed three
times: mg-6e4f and mg-4020 on 2026-08-13, then mg-9d4e, **the ticket about this
failure**, dispatched under a repeated stall-watch notice about that same item.
A control that degrades under load is a poor control for a coordinator whose load
is the thing being managed, and there were 21 tagged items open when it was
filed.

So `spawn-polecat` reads the tag itself and **prepends** a fixed block to the
rendered prompt naming the refusal and both ways out. **What it tells the worker
is not "pass `--successor`", and that distinction is the whole of mg-27c0**: on
the merge path the worker submits and is stopped, pogod performs the close, and
pogod finds the successor only by asking the store which item names the closing
item as its `predecessor`. `mg new` does **not** write that edge (measured
2026-08-19: a freshly filed item's `predecessor` field is `[]`), so a block that
said merely "file the successor" would reproduce the state that cost 5 of 5
declared items on 2026-08-13 — every one had a successor filed and none of them
closed. The block therefore names **both** commands, `mg new` and `mg edit <new
id> --add-tags=predecessor:<this id>`, offers `mg done <id> --successor=<id>` as
the one-step form for a worker that does reach its own close, and gives the
second way out: if nothing is genuinely owed, retract the declaration with
`mg edit <id> --rm-tags=declares-remainder` and say why. Two properties are
load-bearing (mg-a367):

- **Injected by the daemon, not composed by the dispatcher.** Nothing about it
  depends on anyone remembering. It keys on the tag, read from the store.
- **Additive.** It goes above the template's own output and touches nothing
  else; a dispatcher's brief frequently carries load-bearing rescue
  instructions, so replacing or reordering a word of it would be a worse bug
  than the one being fixed. That is also why it is not a `{{if}}` block in the
  worker templates: a template is an on-disk, user-editable file, and making the
  warning conditional on one carrying it would rebuild the defect one layer
  down.

**It refuses nothing.** The tag means "this work has a known remainder", not
"this is unsafe to start". It also does not suppress itself for an item that
already carries a `successor:` tag: such a tag does satisfy `mg done`, but only
while the id it names still resolves, and this process cannot tell a rotted link
from a live one — `internal/workitem` does not read the archive. A redundant
paragraph costs a worker seconds; a missing one costs the coordinator an item
stuck in `available/`.

### Inter-Agent Communication

Two channels:

1. **macguffin mail** — async, persistent. For task descriptions, status reports, questions. Agent checks `mg mail list <self>` periodically.
2. **pogo nudge** — sync, ephemeral. For wakeup signals. pogod writes the message to the target agent's PTY master fd — the agent sees it as typed input — and then **confirms** delivery from the harness's own submission receipts rather than assuming it (see "Confirmed nudge delivery" below). Falls back to mail if the agent isn't running.

No direct RPC. No shared memory. No pub/sub. No tmux. Agents are processes that read files and run commands. pogod mediates interactive access because it owns their terminals.

### Mail to the human, and the optional representative relay

`human` is the box the whole fleet writes when something needs a person. It is
addressed from 21 located sites across three repos — Go watchers, prompt files,
loose shell scripts — plus senders nobody has located, and the population grows
every time a watcher is added. **Treat the write side as fixed.**

What is *not* fixed is who reads it. A deployment may install a
**representative**: a crew agent that owns `human` as its inbox, and rewrites
what matters into a separate terminal box that the desktop notifier polls
(mg-b17b design, mg-65d2 build). It strips internal identifiers, goes back to a
sending agent when a mail says what happened but not what it means, coalesces a
burst about one incident into one message, and may drop what is not worth a
person's attention.

The topology is deliberately inverted: **move the two readers, not the
twenty-one writers.** Every `mg mail send human` in this repo stays correct, and
so does every one that does not exist yet. Nothing in pogo needs to know whether
a representative is running.

Three properties make it safe, and none of them is optional:

1. **The failure mode is raw delivery, not silence.** A second poller watches
   `human/new` directly and delivers anything still sitting there after ~15
   minutes, marked unprocessed. Crew respawn is one-shot (see "Crew Agent"), so
   without a bypass every representative outage becomes total silence toward the
   only person who could notice it. A relay whose failure mode is silence is
   worse than no relay.
2. **`new/` vs `cur/` is a liveness boundary, not a delivery boundary.** The
   representative moves a message out of `human/new` the moment it takes
   ownership — whatever it then decides: forward, hold pending a takeaway, or
   drop. So "still in `new/`" means exactly one thing: *nobody looked at this*.
   A deliberate drop is not resurrected by the bypass, and a hold that outlasts
   the window is not forwarded in its unimproved form.
3. **Escalations bypass by construction.** See `[agents] escalation_box` in
   [docs/CONFIGURATION.md](docs/CONFIGURATION.md). Addressed to `human`, a
   notice reading "the representative is deaf" is filed in the inbox of the
   agent it is reporting as unable to read its inbox. That bypass cannot be a
   timeout, because a wedged relay noticing anything is what cannot be relied on.

Property 1 catches a representative that is dead or deaf. It does **not** catch
one that reads everything, moves it all to `cur/`, and then wedges — that agent
silences the channel while looking busy, which is the failure `internal/ackwatch`
exists for. Point `ackwatch` and `deafwatch` at the representative and property 3
carries their escalations past it.

The relay itself lives outside this repo: the agent is an operator prompt in
`~/.pogo/agents/crew/`, and the two readers are `pogo-reminders` and `bridget`.
pogo's only stake in it is `escalation_box`.

### The Proactivity Principle

Carried forward from Gas Town because it is the most important operational pattern:

> When you have work assigned to you, find it and ensure it gets done. If you are waiting on work, proactively check to ensure it gets done — nudge the other agent, work on something else meanwhile, unblock or support them — rather than assuming. Never assume work is happening if it isn't being reported.

This is enforced by convention in prompt files, not by code. The crew prompt says "if you have work, execute it." The polecat prompt says "your task is X, do it now." There is no "are you sure?" step.

## The Refinery

A deterministic loop inside pogod, not an agent.

The refinery maintains its own git worktrees for testing and merging — it never tests or merges in agent or user working directories. This isolates merge operations from active development and avoids dirty-tree conflicts. The one deliberate exception: after a successful merge advances `origin/<target>`, the refinery fast-forwards the source checkout the MR was submitted from — but only if that checkout is clean and sitting on the target branch (ff-only; never a merge, rebase, or reset; dirty trees are logged and skipped). Without this, the local checkout reads "merged" while still showing pre-merge code, and the next polecat branches from stale state (gh #30).

The refinery is rigless. It doesn't resolve project references or care how many local clones of a repo exist. Each merge-ready work item carries a repo path; the refinery reads the remote URL from that path and maintains exactly one worktree per remote. Multiple agents can work on different clones of the same repo — the refinery sees one remote and pushes to it.

```
~/.pogo/refinery/
└── worktrees/
    └── <repo-name>/       # One worktree per remote, created on demand
```

Merges run in **lanes, one per repo**. The worktree layout above is why: there
is exactly one clone per repo, so two merges for the same repo would share a
working tree and would each rebase onto a target ref the other is about to move.
Two merges for *different* repos share neither, and serialising them was pure
cost — gate cost in the slowest repo set merge latency for every repo. A cap
(`[refinery] max_concurrent_merges`, default 2) bounds how many repos merge at
once; setting it to 1 is the historic single-slot loop.
See [docs/design/refinery-concurrency-design.md](docs/design/refinery-concurrency-design.md).

```
loop (woken by submit, by a lane finishing, or every poll_interval as backstop):
  start every queued item whose repo has no merge in flight, up to the cap;
  each runs on its own goroutine and the loop does NOT wait for it.

  per item:
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

**Every argument is checked while the submitter is still running — including the branch (mg-586d).** `Submit` refuses a malformed post-merge tag, an unparseable verdict and a target ref that is not on origin, all for one reason: the last moment the AUTHOR can be told is before the MR id is handed back. The branch — the subject of the operation — was checked for non-emptiness and nothing else, so a branch that existed nowhere was accepted, returned an MR id, and failed later at `git checkout -B <branch> origin/<branch>` (exit 128) in the merge worker. The submitter got a success-shaped answer for an operation that could not succeed, and the correction surfaced in a component they never ran, read by whoever monitors the refinery rather than by the person who could fix it with one push. The check mirrors what the merge worker resolves: `ls-remote --heads origin <branch>` against the submitting repo, falling back to its local refs when it has no origin (the bare-repo shape, where the refinery's clone takes those refs as `origin/*`). It runs **before** target-ref auto-creation, so a doomed submit cannot leave a stray branch on origin on its way out. The predicate is `origin/<branch>` **by name**, deliberately *not* gh#134's "does any origin ref hold this head" — that one answers durability (would stopping this worker lose work), and it is measurably not the same question: with `origin/other` holding the exact head, `git checkout -B b origin/b` still exits 128. Containment is used for the **diagnostic** instead: the refusal names the origin ref that does hold the head, because "you pushed under another name" and "you never pushed at all" need different fixes. Submit does **not** push a local-only branch for you; that would be a product change, and the minimum this owes the caller is that they learn at the prompt.

**Design rationale:** Gas Town's refinery was also deterministic code (not an agent), and this was explicitly validated as the right call. Merge processing is mechanical — it should never spend tokens on judgment. It needs to work even when all agents are down. Own worktrees ensure the refinery never interferes with agent or user checkouts.

**Merged-polecat reap.** On a successful merge, the refinery's `OnMerged` hook has pogod reap the authoring polecat immediately: mark its work item done on its behalf, stop the process, and (via the agent exit hook) remove its worktree and mail-check schedule. This is event-driven rather than waiting for the coordinator's next coordination cycle, closing the window where a lingering completed polecat holds a slot or re-submits its branch (gh #35). The coordinator's reap loop remains as backstop for merges that resolve while pogod is down.

**The author's verdict rides in on the merge request (mg-dfea).** The reap above used to be the *sole* author of the result sidecar, and that is what destroyed the worker's own verdict — not by overwriting one, because `mg done` refuses a second done rather than clobbering the first, but by **preemption**: pogod closes the item at merge and stops the polecat ~0.5s later, so the polecat's own `mg done --result` arrives at a closed item and is turned away. Nothing complained, and the prompt told the worker that refusal was success. Measured over the live store on 2026-08-06: 139 of 149 landed items carried a refinery-authored sidecar and 10 of 149 carried any field beyond `branch`/`mr`/`target`. The fix gives the author a moment it can actually reach — `pogo refinery submit --verdict-file=...` carries a JSON object on the `MergeRequest`, and the reap merges it into the sidecar under a `verdict` key. It is **nested rather than flattened** so an author's claims can never shadow the refinery's measurements of what actually merged; and an author that records nothing still produces a verdict-free sidecar, so a dropped verdict stays detectable rather than being papered over.

This is correct **when the merge is the deliverable**, which is most of the time and not always. The four subsections below are the exceptions, and they split into two kinds: three that leave the post-merge work with the polecat and stop the reap from killing it (PR flow, `--defer-done`, `post-merge-work`), and one that moves the work off the polecat entirely (`--post-merge-tag`). Reach for the second kind when the step only needs the merged commit; the first kind is for work only the author can do.

**When a merge is *not* completion (PR flow).** The reap above is only correct when the merge lands on the repo's **default branch** — then there is nothing left to deliver. A polecat dispatched with `--branch` merges into an **integration branch** instead, and its deliverable is the pull request from that branch to the default branch, which it has not opened yet. `Submit` therefore classifies every MR: it resolves the repo's default branch and sets `pr_flow` on the merge request when `target_ref` is anything else. A `pr_flow` merge is treated exactly like `--defer-done` — merge, mail, leave the work item **claimed**, keep the polecat running — and the polecat calls `mg done` itself once the PR is open. **What tells it to open one is the prompt:** `polecat.md` and `polecat-architect.md` gate a post-merge `gh pr create` step on `{{if .Branch}}`, reading the base with `gh repo view --json defaultBranchRef` rather than naming it, and reusing an already-open PR because several polecats can land on one integration branch. Deferring completion without that step only bought the polecat time to wait for a stop that was never coming, so the deferral was alerted on instead of filled (mg-78d2). A bounded backstop (15 min) still reaps a deferred polecat that never ends its lifecycle, but escalates only when the work item has not actually reached a terminal state, so a polecat that finished and is merely waiting to be stopped is reaped quietly.

This is **derived, never requested**. Three work items (mg-74ee, mg-6579, mg-7746) merged, were marked done, and left no PR because the deferral depended on a caller passing `--defer-done`; the classification now lives where the target ref is known. `--defer-done` remains for the other direction — forcing deferral on a default-branch merge.

**When a merge is *not* completion (the item says so).** The two mechanisms above both depend on the **submitter** knowing the merge is not the end: the flag is passed at submit time, the target ref is chosen at submit time. A release ticket defeats both. It merges a version bump to the default branch — correctly, with no flag — and everything it actually promises (tag, artifacts, verification) happens *after*. On 2026-07-29 mg-ca3c (pogo v0.7.0) and mg-9f17 (macguffin v0.3.0) each merged, were marked done, and had their polecats stopped before the tag step. Both releases read as complete from `mg show`, the result sidecar, the `MERGED` mail, and CI; `git describe` still said `v0.6.0`.

The refinery cannot know whether a merge completes a ticket — **the ticket knows**. A work item tagged `post-merge-work` declares that merging it is a *step*, and pogod's `OnMerged` hook reads that tag (`mg show <id> --json`) before acting. A declaring item takes the same lane as `--defer-done`: merge, mail, item stays **claimed**, polecat keeps running, bounded backstop armed. The declaration is set by the **filer**, on the item, so a polecat that never learns the flag exists still cannot be truncated:

```bash
mg new --title="Cut v0.8.0" --tags=release,post-merge-work ...
mg edit mg-XXXX --add-tags=post-merge-work    # on an item that already exists
mg list --tag=post-merge-work                 # every outstanding one
```

An item pogod **cannot read** takes the same lane as a declaring one. "I could not read the ticket" is not evidence that the merge completed it, and the cost of being wrong that way is a truncated ticket nothing catches; the cost of being wrong the other way is one backstop window (mg-d86e). This composes with macguffin's `declares-remainder` rather than duplicating it: that tag says something *else* must carry the work forward, this one says *this* item is not finished yet.

**When the post-merge step needs no polecat at all (`--post-merge-tag`).** The three mechanisms above all defer the **reap** and leave the polecat as the acting party. That is the right instrument for work only the polecat can do — opening a PR from its own branch, mailing its own report — and the wrong one for a **release tag**, which needs nothing from the polecat except the SHA its merge landed as.

Tagging from the worker's own sequence has no correct ordering, and the two wrong orderings are each other's obvious remedy. Tag **before** the merge (`bump-version.sh --tag`, mg-cef7) and the tag dangles off a pre-rebase SHA the refinery rewrites when it replays the branch. Tag **after** the merge and you race the reap — measured at 3 seconds on the v0.8.0 cut (mg-e084): the CHANGELOG landed as `21de0b1`, the process was stopped between "merge" and "tag", and the item read `done` with `exit_code=0` while `git describe origin/main` still said `v0.7.0`. Whoever performs the tag must **see the merged SHA** and **outlive the worker**. The polecat has the first property, a pre-merge script has the second, and the **refinery has both** — so the step belongs there:

```bash
pogo refinery submit "$BRANCH" --repo=/path/to/repo --author=mg-XXXX --post-merge-tag=v0.8.0
```

The refinery creates the annotated tag on `merged_sha` and pushes it inside the merge pipeline, so the step **completes before `OnMerged` fires at all**. That removes the race rather than widening the window around it, and the ordinary reap then applies unchanged: the polecat genuinely has nothing left to do, so it is stopped immediately with no backstop armed and no slot held. `merged_sha` is now recorded on the merge request (`pogo refinery show <id> --json`) and in the result sidecar — the refinery always computed it and used to discard it, which is why no actor downstream of a merge could name the commit that landed.

The tag step is **idempotent only in the safe direction**: a tag already on origin at the merged SHA is success, so a resubmit converges, and an already-merged branch whose tag never landed still gets it — which makes a resubmit the supported way to finish a half-cut release. A tag on origin pointing **elsewhere** is a hard failure and is never moved; a published release tag that silently relocates is worse than a missing one.

Failure here is **load-bearing, not diagnostic**, which is what separates `post_merge_error` from `deploy_error`. The merge is not unwound — it landed remotely — but pogod's reap consults the field and **refuses to mark the work item done** while it is set, keeps the polecat alive, and mails the coordinator with the failure in the subject line. The defect this closes was silent in exactly one way: a terminal `done` state that no backstop inspects. So a post-merge step that fails must not be able to resolve as completion.

**Corollary — never ask the merging worker to verify its own completion.** A "did the tag land" check placed in the polecat's sequence dies in the same three seconds as the tag step. Closing checks belong with whoever outlives the merge.

**Where a coordinator reads the classification after the fact.** `pr_flow` lives on the **merge request** (`pogo refinery show <id> --json`), and only there. The result sidecar records `target`, and it is written on the completion path only — a PR-flow merge returns before the sidecar writer, because pogod does not run `mg done` on that path at all. So a sidecar written by the refinery is by construction a default-branch completion, and there is no `pr_flow` key in it to read. This paragraph previously claimed the sidecar carried `pr_flow` "when true"; it never did (mg-c8d5).

**When a polecat completes without merging anything.** Every teardown above hangs off one event — the refinery reporting a merge — and that reaches only polecats whose deliverable is a branch. A **triage, audit-only or investigation** polecat merges nothing: it finishes by calling `mg done` itself, and the refinery never hears about it. Nothing stopped those, so they held a concurrency slot until a coordinator noticed. Measured on 2026-07-30: `d764` delivered its triage packet, filed its successor, went idle, and sat on one of five slots for 7m16s with two high-priority items queued and undispatchable. The loss is invisible in the readout operators actually use — `pogo agent list` says `running`, only `pogo agent diagnose` says `idle` — so it reads exactly like healthy saturation. Same family as mg-18d0: **the list reports liveness, never productivity.**

pogod therefore carries a second, independent teardown keyed on the **work item reaching a terminal state** (`done`/`archived`) rather than on a merge. Merge is one path to that state, `mg done` is the other, and `done` is the general fact both produce — so the merge hook is left exactly as it is, with the obligations only it has (writing the completion, honouring `--defer-done`/PR-flow/`post-merge-work`, arming the backstop), and this covers everything else. It **polls**: `mg done` runs in the polecat's own process against the macguffin store, and macguffin offers pogod no callback, so `done` can be observed but not delivered. One `mg show --json` per live polecat on the heartbeat tick, in `cmd/pogod/donereap.go`.

The condition is a **conjunction — item terminal AND the polecat quiet on its PTY for two minutes** — because `done` alone is not sufficient. The polecat protocol tells a polecat to call `mg done` and then stay alive until stopped, and work legitimately follows that call: mailing a verdict packet, filing a successor, answering a coordinator follow-up. Stopping on the `done` write alone kills it mid-sentence, which is strictly worse than the leak: a lost verdict is unrecoverable, a held slot is merely expensive. The grace is a **grace period rather than an explicit "I am finished" signal** — a signal the polecat must remember to send fails for exactly the polecats that most need stopping, the ones that ran off the end of their protocol, and mg-ddf7 already measured that failure mode. It is measured from the **last PTY write**, not from the `done` transition: a timer from the transition cannot tell a polecat that is still typing from one that stopped, and measuring from output means an incoming coordinator nudge (itself PTY traffic) extends the window for as long as the exchange lasts. A polecat whose item is still `claimed` is untouchable no matter how long it idles — item state is the gate, idleness only qualifies it — which is why a healthy 42-minute idle polecat mid-work survives structurally rather than by tuning. Configurable via `[done_reap] enabled` / `idle_grace`; it stops processes and can do nothing else (it never marks an item, mails, nudges, or spawns).

**The third clause: a builder is exempt while its reviewer is running.** That conjunction is right for a polecat whose deliverable is finished and wrong for a builder on the gh-issue PR track, where the build item is supposed to stay `claimed` through review. A builder that calls `mg done` at PR-open anyway looks identical from here — terminal item, quiet PTY — so two minutes later it is gone and the review polecat that was about to mail it findings has no counterparty. That happened twice (drellem2/pogo#131), and it is not marginal: measured over 17 real between-round builder waits, the median is 8.3m and **15 of 17 exceed the two-minute grace**, so a self-closed builder is reaped in the ordinary case.

The exemption keys on a **declaration written once at creation**: `reviews: <build ticket id>`, a fourth line of the review ticket's state carrier block, written by the coordinator when it files the ticket and never cleared. It is read by `workitem.ParseCarrier` off the same `mg show --json` payload the terminal-state probe already fetches, and a builder is spared exactly while some **live** polecat's work item declares it. **The clearing rule is the whole design.** The rejected shape was a tag the coordinator removes at the pass/abort transition — enforcement by instruction-following, which is precisely what failed twice — and a declaration that must be cleared is state whose drift leaves no artifact: forget it once and the item holds a dispatch slot forever, against the per-repo cap, with nothing saying so. That is mg-56d1's leak returning through the fix for it. A creation-time declaration cannot rot because its lifetime is bounded by something else's — the reviewer's process — so the exemption self-clears and needs no ceiling. A ceiling was priced and **disqualified**: 2 of those 17 waits exceed `deferDoneBackstopTimeout`'s 15 minutes, so that bound would reap live work about one round in eight.

**Why not `depends`, `gh:`, or the body.** `--depends` carries dispatch semantics: it would gate the review ticket behind a build ticket that cannot clear, so the coordinator deliberately files no such edge and only 2 of 23 real review carriers have one. A `gh:` ref join is ambiguous the moment an issue is split into parts — gh#131 itself produced two build carriers sharing one ref. A prose `mg-xxxx` scan resolves to the **wrong** item in 17 of 23 cases, because review bodies name the triage ticket too.

**It depends on `CarrierUnreadable` (mg-27d4) and would have been unsafe without it.** A carrier block one line below a lead-in sentence is out of the parser's reach, a state 4 of 27 live review carriers were already in. Before that third parse outcome existed, such a ticket dispatched normally with its `reviews:` line silently unread — the exemption would not fire, the builder would be reaped mid-review, and the failure would look exactly like the bug being fixed while `mg show` displayed the declaration perfectly to anyone who checked. Now the ticket gates instead. The guard also keeps a **positive record**, because an exemption never granted through misconfiguration and one correctly not needed produce the same nothing: the grant is logged once when it starts, the reap says the polecat had been exempt and names the reviewer now gone, and a probe that cannot read a declaration says so rather than reading it as absent.


**When the thing that STOPPED the fleet dies before restarting it.** Stopping orchestration is step 1 of a two-step sequence, and until mg-5af1 the only thing that could perform step 2 was the process that performed step 1 — `pogo service install`'s quiesce, an operator's `pogo server stop`, a redeploy quiescing a fleet it means to bring back. The fleet being up was contingent on the stopper surviving. On **2026-08-08** that contingency was collected: the crew was stopped cleanly at 00:44:20Z (five agents, `exit_code=0`, `reason=requested`, 0.42s apart — one StopAll-shaped command), the run that stopped it hung at 02:00:05Z inside an unbounded git call and did not return for 31h39m, and the fleet stayed dark for **33 hours**. Every supervisor behaved correctly, which is the point: a requested stop is not a crash, so nothing was entitled to undo it, and the scheduler logged 198 consecutive successful deliveries to an agent that was not there.

This is **the same structure as the release-cut tagging corollary above** — whoever performs step 1 cannot be trusted to reach step 2, so step 2 needs an owner that outlives step 1. `internal/server` therefore records a **resume obligation at the transition site**, inside `transitionToIndexOnly`, before the stop work and under the transition lock: an obligation written after `StopAll` would be missing for the window in which the fleet is already dark, and missing entirely if the process died mid-drain. `cmd/pogod/orchresume.go` fires it from the heartbeat tick — past the deadline it restores full mode and mails the coordinator, naming who stopped the fleet and when.

**The holder cannot be the stopper and cannot be the crew.** A shell `trap` dies with the shell and a background child dies with its process group, so nothing the stopper owns can hold this; and a watcher held by the fleet cannot fire when the fleet is what is down (the constraint mg-a14c shares). pogod satisfies both: a separate process from every stopper, up for the whole index-only window by definition, with a heartbeat that is not gated on the run mode. **It is the second acting detector on that tick** — the other is the done-reaper above — and the argument for acting rather than reporting is that every reporting channel here is downstream of the fleet being up. A report-only version would be a mechanism whose only output is destroyed by the condition it detects, so it acts first and mails into a fleet that can read it.

**Bounded so it cannot fight an ordinary deploy.** It cannot act before the deadline, cannot act while the mode is full, and cannot act twice on one stop (a return to full mode by any route discharges the obligation). A second stop does not push the deadline out — the obligation runs from the *original* stop, so a retry loop cannot defer it forever. `pogo server stop --hold 4h` declares a longer window when a long dark period is intended; there is deliberately no indefinite hold, because an undeclared indefinite dark fleet is indistinguishable from an outage. Both polarities are pinned by test: the negative arm kills the stopper and requires the crew back with an alarm, the positive control requires a correct stop/restart cycle to produce **no** second restart and **no** mail. Configurable via `[orchestration_resume] enabled` / `grace` / `retry`.

**mg-6d2f (974edc1) closed the alerting half of this and is untouched:** a deploy run that leaves the fleet stopped exits 11/12 and says the fleet is down. That is a different job. The 08-08 run never reached an exit at all, so a loud exit would not have fired — exiting loudly is not the same as somebody else holding the obligation.

**What it does not cover.** The obligation is in memory and dies with the process. That is correct for a pogod that is killed and restarted, because `server.New` hard-codes `ModeFull` and no config key selects otherwise — the successor is already in the state the obligation would have restored. It is **not** coverage for a pogod killed and never restarted; that is the tier-1 heartbeat reaper's problem. And when the restore itself fails, the notice is written into a fleet that is still down, so it says it may have no in-fleet reader rather than implying it was read.

**When a deferred polecat dies instead of finishing.** A polecat left running to end its own lifecycle can die between the merge and its `mg done` — and a process that exits on its own never goes through `Registry.Stop`, which is where a stopped polecat's work-item claim is released (mg-fb13). The `OnExit` hook therefore does not merely disarm the backstop: it asks whether the item is still in `claimed/`, releases it if so, and mails the mayor that a merged branch is short a PR. An exit with no claim held is a completion and stays silent, so the ordinary mayor-initiated stop and the fleet drain do not page anyone. This was a survivable gap while auto-stop-at-merge was the norm; PR flow is the default path now, so the reachable surface grew (mg-c8d5).

**Retry behavior.** If another commit lands on the target between fetch and push (e.g. a CI auto-bump), the ff-only merge fails with a retryable error. The refinery re-runs the full fetch→rebase→gates→merge→push cycle up to `max_attempts` times (default 7). Per-repo `<repo>/.pogo/refinery.toml`:

```toml
[gates]
max_attempts  = 7      # ff-only retry budget — raise on repos that race CI
skip_on_retry = true   # bypass gates on attempts > 1 (cost-saving when
                       # the only change between attempts is a version bump
                       # fetched from main)
pr_mode       = true   # push the rebased branch back so open GitHub PRs
                       # read "merged" (see below)
timeout       = "60m"  # bound on a single gate run; "0" removes the bound
```

**The default gate list, when a repo names none.** The refinery runs the conventional scripts it finds at the worktree root — `./build.sh` and `./test.sh` — with one exception: **if `build.sh` itself runs `test.sh`, only `./build.sh` is gated** (mg-da30). Listing both is right when they are independent steps and wrong when one calls the other, and on this repo it was the latter: `build.sh` runs `./test.sh`, the gate then ran `./test.sh` again, and every merge paid for the suite twice on the single slot everything else queues behind. Measured from pogod's own gate heartbeats over 49 two-gate merges, the second, redundant gate was **34% of all gate wall-clock** — a median of 2m30s per merge. That fraction is of **gate** wall-clock specifically: the duplication was in the gate's list, never in `build.sh`, which runs the suite once and always did, so a polecat running `./build.sh` in its own worktree costs exactly what it did before. This is a per-merge saving on a single slot, not a per-agent saving on the host.

The exception is conditional on the nesting rather than a blanket "prefer `./build.sh`", because a blanket rule would not halve the other repos' gates, it would stop testing them: of the seven repos on this fleet carrying both scripts, **five** (`bridget`, `libdig`, `macguffin`, `pogo-sleepwake`, `rent-a-programmer-api`) have a `build.sh` that only compiles. `buildScriptRunsTests` decides it textually, and its two failure directions are not symmetric — an unrecognised invocation form keeps both gates (the status quo, a suite run twice) while a phantom one would drop coverage, so everything from the first `#` on a line is discarded before matching and only executable forms (`./test.sh`, `bash test.sh`) count. A dropped gate is named in the merge's own gate output; a shorter gate list that nothing explains is indistinguishable from coverage quietly going missing.

The saving is smaller than "the suite runs twice" suggests, and the reason is worth knowing: Go's test cache means the second `go test ./...` returns almost everything cached (measured: 0 of 50 packages cached on the first run, 49 on the immediately following one). What the duplicate actually re-paid was `test.sh`'s dozen bash suites, several of which stand up real sandboxed daemons and cache nothing. Any per-repo `[gates] commands` is used verbatim and none of this applies to it.

**Failure classification, and what may be retried (mg-e5c2).** Every failing
attempt is classified before the refinery decides what to do with it, against
pm-pogo's ruling from mg-0d70: *retry a failure that establishes nothing about
the tree; do not retry one that establishes a fact* — concretely, would
re-running plausibly give a different answer for a reason unrelated to the code?

| class | means | retried |
|---|---|---|
| `infrastructure` | establishes nothing about the branch — network/DNS/transport, a remote that refused our credentials, or the refinery's own checkout | network yes, credentials/checkout no |
| `contention` | the target moved between rebase and push | yes, on `max_attempts` |
| `defect` | establishes a fact about the branch — gate verdict, rebase conflict, refused commit message | no |
| `host` | the gate ran and reported the **box**: the host ran out of a resource under it | no — free the resource first |
| `unclassified` | could not be placed | yes, twice |

Network-class retries have their **own** budget so a blip cannot consume the
attempts that exist to absorb a lost race. Only `defect` counts against an
author's consecutive-failure streak, and only `defect` invites dispatching a fix.

**`host` is the class the verdict rule could not express (mg-b41f).** The
no-retry reasoning for a gate — *"the gate ran on this tree and returned a
verdict, re-running establishes the same fact"* — is correct for a deterministic
failure and **false for an environmental one**. On 2026-08-12 the boot volume
reached 255 MiB free, `./build.sh` failed, and the refinery recorded
`class=defect` with a summary naming 50 packages and two tests by name; the raw
output underneath said `no space left on device` twenty-five times. Once 7.3G was
reclaimed the identical branch merged clean — the verdict was not reproducible,
which is precisely what the no-retry rule assumed it was. The larger cost was the
second one: **the summary accused the branch**, and the evidence that it was the
host sat one level down, where the reader that summary exists to serve does not
go.

The nature of the fix matters as much as the fix. Gate output is arbitrary text
and `failureclass.go` deliberately refuses to match it against the network table,
because a red test that happened to print `connection refused` would then be
retried forever. `host` does not cross that boundary — it is **not retried**, so
that runaway cannot occur — and its signal table carries only wordings **measured
to have occurred in this fleet's own gate output**. `exit status 137`, ENOMEM and
`disk quota exceeded` were each counted against the retained corpus and came back
zero, so none of them is in it; `too many open files` does occur on this host but
only in pogod's own scheduler (mg-d205), never in a gate, so it is not in it
either. Every speculative pattern there would be a fresh chance to take a real
defect away from its author.

The class does **not** loosen the no-retry rule for defects, which exists so a
broken branch is not re-run repeatedly and is correct. It is not retried
automatically either: a full disk is not a DHCP lease and does not come back on
its own, so an automatic retry spends a whole gate run on the single merge slot
to re-derive the same fact while the host is still in the state that fails every
gate on it. The failure mail already carries the class in its **subject** and the
triage note in its body, to both the author and the coordinator — free the
resource, then resubmit unchanged. Alongside the classification the refinery
`statfs`es the gate's own filesystem and reports the reading, **including when it
disagrees** with the wording (space can be reclaimed between the failure and the
measurement — a gate's scratch directory is deleted when it exits, and one
measured run of this repo's gate held 250.5 MiB of it). The text decides the
class; the reading is a second instrument beside it, never a veto.

**The network budget is sized past the longest observed outage, with margin
(mg-c3b7, arithmetic corrected by mg-7110, widened by mg-682d).** It was 5
attempts over 52s of backoff, which
was written against a *blip*. Then the outage was measured, by a sampler carrying
a positive control (`ping 1.1.1.1`: clean before onset, LOSS throughout, clean
after recovery — so its failures carry information): onset 03:37:23Z, recovery
03:52:49Z, **duration 15m26s**, on 2026-08-10. 52 seconds against that is short
by ~17.8x, so no rearrangement of backoff *inside* 52 seconds could succeed — it
could only fail faster.

That wave is not the size of the event, and no single wave is. It is one cycle of
a recurring DHCP fault (mg-964e) whose duration is a **distribution that has
widened twice**:

| stated as | status |
|---|---|
| "15 min ±41s, stable across four waves" | **withdrawn** — came from the first three waves, which happened to cluster |
| maximum 17m52s (n=9) | **superseded** |
| maximum ~35m03s (wave L, n=12) | current, and **open** — no established upper bound |

The budget is **28 attempts, backoff 15s/30s/60s then 2 minutes, ~49m45s of total
sleep**, with a 50-minute clock backstop (mg-682d). It was 14 attempts / 21m45s,
which covered every one of the eleven waves before wave L and none of wave L —
under the ~28-minute floor that three consecutive missed `*/10` mail-check fires
establish *without* the lease reading, and 13m18s under 35m03s.

**`networkMaxAttempts` is the constant that binds.** The schedule fits inside the
clock backstop, so raising the backstop alone changes nothing a merge
experiences; both move together or the widening is a no-op. A test asserts the
two have not drifted apart, so the failure names the right constant.

**The margin is deliberately large, and it is still not coverage.** 49m45s is the
observed maximum ×1.42. The distribution's own widenings were ×1.16 and ×1.96, so
**a third widening the size of the second one (~68m40s) would outrun this too** —
that is stated rather than left to be inferred. Two departures do not establish a
trend, so this is not sized against a projected next maximum; it is sized so that
being wrong again costs a ticket rather than an incident. Treat every maximum
above — including 35m03s — as the largest seen so far rather than the size of the
event; each of the first two was believed to be the size of the event when it was
written, and **the gap between the campaign and the newest maximum is margin, not
spare room to trim toward.**

**This is a mitigation; mg-964e (the DHCP fault) is the fix.** If the lease is
repaired the budget stops mattering. The durable shape remains the one that
distinguishes "the network is down" from "attempts exhausted" and requeues rather
than sleeping longer. Sizing is deliberately not done against any prediction of
*when* the next window opens.

Two costs, which are different things. **During an outage** the wait is the outage
plus at most one 2-minute probe interval, not 49m45s — the plateau exists so
recovery is noticed promptly, so widening the ceiling costs nothing on the waves
already covered. **Against a genuinely dead network** (deleted remote, revoked
credential) the merge's lane is held ~50 minutes before the failure is reported,
up from ~22; lanes are per-repo, so other repos still merge, and the branch is
untouched — the price is slower bad news, not lost work.

**A completed gate verdict is held across the wait, not discarded (mg-c3b7).**
Every network step except the first `fetch origin` runs *after* the quality
gates, so a socket that fails there does not cost a retry — it costs the whole
gate run. On 2026-08-10 a CLEAN 8m58s `./build.sh` was thrown away by a fetch
failure and re-run from scratch on resubmit. The refinery now records the **tree
object** of the rebased branch the gates passed on; a retry that rebases to the
same tree replays that verdict instead of recomputing it. The key is
content-addressed, so the moment the target or the branch moves, the tree
differs and the gates run again — a stale verdict is never trusted, and the
failure mail states how long the refinery *actually* waited so "the network was
down" is distinguishable from "we did not wait".

`pogo refinery show`, `refinery history` and the failure mail print
`failed(infrastructure)` rather than a bare `failed`, because the status is what a
coordinator reads first — thirty-one bare `failed` rows on 2026-08-05 invited
thirty-one fixes for defects that did not exist. The machine-readable `status`
field is unchanged (`merged`/`failed`/`lost`) so polecat poll loops still
terminate; the class travels beside it as `failure_class`.

Every failing attempt records the **transport** and git's **raw output verbatim**,
never a normalised summary, and a terminal failure always records why no further
retry was made. Of the 31 failures in that incident, 20 were ssh reporting
`Undefined error: 0` — which names no cause — and 11 were HTTPS reporting
`Could not resolve host: github.com`, which named it outright; readers working
from the ssh subset alone produced two confident wrong mechanisms over several
hours. See `internal/refinery/failureclass.go`.

**Telling a slow gate from a dead one.** Quality gates are the one step that can
run for tens of minutes, and the step used to log on entry and not again until it
produced a result. From outside, a gate running for thirty minutes and a gate
whose runner died thirty minutes ago produced *identical* evidence — and the two
call for opposite responses (wait / intervene). On 2026-07-29 an operator read
that silence as a hang, re-submitted a branch that had in fact merged, and the
failed re-submit reopened a work item whose work had landed (mg-8595).

Every gate now runs under a **heartbeat**, emitted by the goroutine running it,
**every 30 seconds** (`gateHeartbeatInterval`):

```
refinery: MR mr-abc step=quality-gates gate=./build.sh (1/2) alive elapsed=12m0s
          heartbeat=24/30s gate_output_lines=412 last_output=3s ago
          gate_last_line="ok  internal/refinery  12.400s"
```

The same record is persisted on the merge request and rendered by `pogo refinery
show <id>` (and carried in `--json` under `progress`), which matters because the
dead-runner case is exactly the case where the writing process is gone and the
state file is the only reader left.

Five signals are reported **separately**, because they answer different
questions and collapsing them would rebuild the ambiguity:

| Signal | Written by | Answers | A dead runner | A hung gate subprocess |
|---|---|---|---|---|
| `heartbeat` / `beats` | the goroutine running the gate | is the runner alive? | **cannot emit it** — goes stale | keeps beating |
| `output_lines` / `last_output` | the gate's own subprocess | is the gate talking? | frozen | **cannot emit it** — freezes |
| `output_excerpt` (mg-9adc) | the gate's own subprocess | *what did it say?* | frozen | frozen, at the words it stopped on |
| `cpu_cores` / `cpu_procs` (mg-0c51) | a sampler over the gate's process subtree | is the gate *computing*? | n/a | reads idle |
| `contention` (mg-1b8c) | a sampler over the fleet's process subtree | was there CPU to compute *with*? | n/a | unaffected |

So a stale heartbeat proves the runner is gone; a fresh heartbeat plus climbing
output proves the gate is slow, not stuck; and a fresh heartbeat with a silent
gate is resolved by the subtree reading rather than guessed at — a real gate was
observed silent for 8m31s while a descendant burned 3.9 cores, so output
staleness alone is confidently wrong in a case that happens routinely.
`pogo refinery show` prints that reading as a `Verdict:` line.

**Every signal is printed with the LAYER it measures, and the verdict names the
layer it is judging** (mg-48d8). The table above is the model; the output used
not to be. `Heartbeat: 33s ago` sat next to `Gate says: 124 lines, last 26m0s
ago` with nothing saying that the first is written by the supervisor and the
second by the thing supervised, and the verdict read `ALIVE and working ...
Slow, not hung — waiting is correct` — rendered from the heartbeat, about a gate
that had been silent for 26 minutes. It would have printed the same sentence for
a deadlock, because a runner beats identically either way. The evidence is now a
column of `RUNNER` / `GATE` / `HOST` rows, the verdict opens with its subject
(`GATE ALIVE and computing`, `RUNNER DEAD`), and where the heartbeat appears in a
verdict about the gate it is explicitly disclaimed as evidence there.

The same mistake was made from the other side on 2026-08-05 with `ps aux | grep
-i refinery`, which matched eleven of the operator's own shell wrappers at 0.0%
CPU and was read as a hung gate while the gate was producing output. Hence three
properties the subtree measurement holds and any replacement must: it finds the
gate **by ancestry** from the runner's pid and never by matching a command line;
it **sums the whole subtree**, because the gate's top-level process is a shell
blocking in `wait(2)` (measured: root 0.0%, child 543.4%); and it has a
**positive control** — `TestSubtreeGoneIsProvenAgainstARealProcess` finds a real
process before it is killed and reports it gone afterwards, on the same pid, so
that a "not found" comes from an instrument shown to be capable of finding.

**The last two are easy to confuse and answer opposite halves of one question.**
`cpu_cores` is rooted at the gate's own pid; `contention` is rooted at pogod. The
pair matters most in the case each would get wrong alone: **a saturated host
starves a runnable process, and a starved process consumes almost no CPU**, so a
perfectly healthy gate reads as *silent and idle* — the shape of a stall — for as
long as the host stays full. The verdict therefore names the host contention on
that branch and offers starvation as the alternative reading, so the two
measurements cannot combine into a confident wrong answer.

Nothing here is derived from **workspace state** — file mtimes, lock files,
worktree contents. That was the trap in the original misdiagnosis: a frozen
worktree mtime felt like corroboration, but a long test suite *reads* files
rather than writing them, so workspace state looks the same for a healthy slow
gate and a dead one. Two observations that cannot discriminate are not two
pieces of evidence.

**What the gate SAID, not just how much** (mg-9adc). Every signal above is a
measure of *volume* or *rate*, and volume cannot tell a compute phase from a
hang: a gate frozen at `140 lines, last 26m ago` reads identically whether those
lines end in a passing suite or in `Building pogod into the sandbox...`. The
gate's full output is stored on the merge request — but only once the merge
resolves, which is exactly when nobody needs it. On 2026-08-05 a hypothesis
about why a gate was slow travelled across three agents for over an hour and
cost a peer product two tickets; **the gate's own first line refuted it**, and
that line was unreachable for the whole window. A window in which the evidence
is dark does not merely delay the answer, it *selects for stories*: a hypothesis
that cannot be killed accumulates relays instead of tests, and each relay reads
as corroboration.

`StepProgress.OutputExcerpt` carries the running gate's text, rendered by `pogo
refinery show <id>` under `--- Gate output so far ---` and available in `--json`
under `output_excerpt` (**not** `last_output`, which is a timestamp and reads to
a JSON consumer like the field that would hold the text). Three properties are
load-bearing:

- **A head as well as a tail.** "Show me the last N lines" is what everyone asks
  for and it would have left the incident above fully intact — the refuting line
  was the gate's *first*, and at minute 77 no tail reaches it. Gates state what
  they resolved and what they are about to run in a header. The first 25 lines
  are captured once and never evicted; the last 40 roll.
- **The bound is stated wherever it bit.** The header says how many lines the
  gate produced, how many are *not* shown, and what the limits are; the gap
  between head and tail is printed as a gap; kept lines carry the gate's own
  line numbers so a head-then-tail cannot read as a contiguous transcript; an
  over-long line is cut at 500 bytes and says so *in the line*. A bounded read
  that manufactures an absence is its own defect — this arc has been bitten by a
  `head -40` on an 81-line mail producing a confident wrong diagnosis.
- **Silence is a measurement, absence is not.** A gate that has said nothing
  reports `NOTHING YET — ... nothing was elided`; a record from a pogod that
  never captured gate text reports `NOT RECORDED`. The two lead to opposite
  actions, so they must not render the same. An unterminated line — the gate
  mid-write — is reported separately and labelled, because that is precisely the
  reading a gate that halted mid-phase leaves behind.

`pogo refinery queue` carries a one-line summary of each end (`said first:` /
`said latest:`) and names the command that prints the bounded excerpt, because a
two-line summary that does not say where the rest is reads as the whole of it.

**Gate timeout.** A single gate is bounded at 60 minutes by default
(`[gates] timeout`, or `"0"` to remove the bound). The bound exists so a
genuinely hung gate fails instead of waiting forever; it ships *with* the
heartbeat deliberately, because a timeout alone would only convert
"indistinguishable" into "killed arbitrarily". A killed gate reports what it was
observed doing — elapsed time, lines produced, how long it had been silent — and
says how to raise the bound. The gate runs in its own **process group** and the
whole group is killed: `sh -c` forks rather than execs for anything compound, so
killing the shell alone would leave the real work running and holding the output
pipe, stalling the very runner that carries the heartbeat.

**Gates that write tracked files.** A gate is an arbitrary command run inside the
refinery's own clone, and nothing stops it writing tracked files — a regenerated
JSON record, a lockfile, a coverage report, a checked-in generated fixture.
Every git step after the gate then refuses: the target checkout, the ff-merge,
and the rebase on the next attempt all decline to touch a tree with unstaged
changes. Measured on mg-48dd (2026-07-30): the gate finished at 12:01:05, the
rebase failed at 12:01:08, and the only thing that ran in between was the gate.

The failure used to surface as git's own message — `cannot rebase: You have
unstaged changes. Please commit or stash them.` — which is **wrong twice**. It
names a worktree the author cannot see (the refinery's clone, not their polecat
worktree), and its advice is dangerous when followed: "stash them" applied by a
coordinator guessing whose changes those are can stash the real diff and merge an
empty change. The author has no reachable fix at all; they never wrote those
bytes. A clean polecat worktree is equally consistent with *fixed* and with
*never the cause*, and the message pushes a reader toward the first — it did,
and cost a whole debugging cycle.

**Rebasing before the gates was already the order, and it is not sufficient.**
mg-393f was filed on the premise that gates run before the rebase; they do not —
`attemptMerge` rebases first precisely so gates test what will land. That helps
attempt 1's rebase and nothing after it: the target checkout and ff-merge still
follow the gate within the same attempt, and a retry rebases again on a tree the
first attempt's gate already dirtied. Ordering cannot fix a step that must run
*after* the gate by construction, which is why the fix is to clean the tree
rather than to move the rebase.

Two things changed (`internal/refinery/gatedirt.go`, mg-393f):

- The refinery **discards tracked modifications in its own checkout** at the two
  points the pipeline needs it clean: on entry to each attempt (debris from a
  previous attempt, or from a previous MR reusing this clone) and immediately
  after the gates, before the target checkout. Everything the merge needs comes
  from `origin`, so discarding tracked changes in this clone cannot lose work.
  **Untracked files are deliberately left alone** — they never block git, and
  they are where gates keep build caches. When anything was discarded, the paths
  are logged and appended to the MR's gate output, so the record says *this
  repo's gate writes tracked files* rather than leaving the next reader to
  reconstruct it from timestamps.
- A dirty tree that reaches a git step **anyway** no longer relays git's
  message. The refinery names the paths, names the configured gate commands as
  the writer, states whether the branch touches any of those paths (if not:
  "this is NOT your change"), and says outright that committing or stashing
  cannot fix it. git's own output is quoted with its **advice lines removed** —
  keeping the file list, dropping "commit or stash", because quoting it verbatim
  reintroduces the exact instruction the message exists to withdraw. The
  classification is made from the tree, not from git's wording, which differs per
  step and per git version.

**Host contention.** Elapsed time answers "how long did this take", not "how
long *should* it have taken", and on a shared host those are different
questions. Measured on the fleet's 10-core box (mg-1b8c): identical work took
**11.5s** with capacity and **78.5s** with the host full — a **6.8x** inflation
of something the branch under test had no part in. Left unmeasured, that pushes
a gate through a fixed timeout and produces a merge failure that reads as a
defect in the change.

So a running gate **samples the host** and carries the summary on its progress
record — the fourth signal in the table above. `pogo refinery show` prints a
`Host:` line for every sampled run — saturated *or* not, because "the host was
quiet" and "we did not look" are different claims — and a timeout reached on a
saturated host says so in its error text, with the numbers.

The kill still happens and the merge still fails. A bound that could be
silenced by loading the host would not be a bound. What changes is the reading:
the failure no longer implies a verdict on the branch when the evidence does
not support one.

The measurement is `internal/hostload`: per-process CPU time differenced over a
window and attributed to the fleet **by process subtree** from pogod. Subtree
rather than agent count is the whole point — the instance that prompted it was
one agent with three compute children, and any count of agents reads that as an
idle host. It is deliberately **not** the load average: measured the same night,
a load average of 214 against ~7.5 of 10 cores in use, because Darwin counts
I/O waiters in that figure. The load average is carried as context and decided
on by nothing.

**Where the measurement can be taken is part of the answer.** The rate is only
as good as the precision of the CPU column it differenced, and that is a
property of the host. `internal/proctable` owns the read for both this and the
refinery's gate watch, and reports the resolution alongside the rows:
`linux-procfs` (10ms, `/proc/<pid>/stat` `utime+stime` in USER_HZ ticks) and
`darwin-ps` (10ms, BSD `ps`'s `MM:SS.ss`) both resolve sub-second windows;
procps `ps` prints whole seconds only, so a fallback `<goos>-ps` source needs a
multi-second window. Below a source's `MinWindow` the difference is not a small
number, it is **zero** — for a saturated host exactly as much as an idle one —
so `Sample.Unresolvable` and `StepProgress.CPUUnavailable` carry the reason
instead, and both records name the source they were taken with. Reporting that
quantised zero as a measurement is how the signal went silently blind on Linux
while reading correctly on the machine it was written on (mg-79e3).

The same measurement gates dispatch — see **Dispatch gates** below.

**Cancelling a merge.** `pogo refinery cancel <id>` reaches a **processing** MR,
not only a queued one — previously it refused anything but `queued`, the state
least in need of it, which left a hung gate with no recovery short of restarting
pogod. The two cases are reported as different things, because they are: a queued
MR is removed and resolved as `cancelled` immediately, while a processing MR has
its running gate killed and stops at the next step boundary. The second is a
*request*, not a result — an MR that had already pushed to the target has landed,
and cancel does not pretend otherwise, so callers must poll for the real status.
A cancelled MR fires **neither** `OnMerged` nor `OnFailed` and does not count
against its author's failure streak: it did not fail on its merits, and firing
`OnFailed` would reopen a work item on an operator's action rather than on a
defect in the branch.

**PR mode.** The refinery rebases before merging, so a branch's original SHAs never land verbatim on the target — GitHub would show any open PR for the branch as "closed" rather than "merged". With `pr_mode = true`, the refinery asks `gh pr view` whether an open PR exists for the branch and, if so, force-pushes (`--force-with-lease`) the rebased branch back to origin after gates pass and before the ff-merge push — realigning the PR head with exactly the gate-tested commits, so GitHub marks the PR merged when the tip lands. The path is fail-soft end to end: if the `gh` lookup or the push-back fails (missing `gh`, no network, someone pushed to the PR branch mid-merge), the merge proceeds normally and the PR merely reads "closed" — the pre-`pr_mode` status quo.

**Post-merge PR close + branch reap.** `pr_mode` only realigns the head when it can — when the lookup fails, the lease is lost, or the repo has `pr_mode` off, GitHub cannot auto-detect the merge and the PR dangles *open* even though the content landed (seen on every rebased 2nd-or-later MR in a batch: gh #81 stayed open while gh #80, merged first and verbatim, auto-closed). So after every successful merge, the refinery looks the branch's PR up once more and, if it is still open, closes it with a comment naming the SHA the content actually landed as; then it deletes the branch from origin. When GitHub already auto-detected the merge, the PR reads MERGED and only the reap runs — closing is skipped, not retried. Branches with no PR are left entirely alone (reaping is PR loop-closure, not general branch cleanup — gitgc owns that). Fail-soft throughout: this runs *after* the merge has landed on origin, so a `gh` outage or a lost delete race is logged and skipped, never unwinding a successful merge. See `closePRAndReap` in `internal/refinery/merge.go`.

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

**Stale mail-check GC (gh #15).** A schedule whose id starts with
`mail-check-` only makes sense while its target agent is alive. When the agent
disappears — stopped via `pogo agent stop`, crashed, or killed because pogod
itself restarted — the schedule would otherwise keep firing every interval into
a `scheduler_fire_failed` event. Two mechanisms reap it:

- **Tick sweep (backstop).** Before computing what's due, every Tick removes
  each `mail-check-*` entry whose target agent is **GONE**. This covers the case
  no in-process hook can see — pogod restarting kills its children without
  firing their exit callbacks. Reaped within one heartbeat, so the schedule is
  gone well before its next fire interval.
- **Eager onExit reap.** When the agent registry observes a non-restart agent
  exit (stop or crash), pogod immediately removes that agent's `mail-check-*`
  schedules rather than waiting for the next sweep.

**The reap requires positive evidence of death, never absence of evidence of
life (mg-de08).** `AgentLiveness` answers a tri-state, and only `AgentGone`
reaps:

| State | Meaning | Mail-check |
|---|---|---|
| `AgentAlive` | process running, or a restart-on-crash agent the registry still holds (transient mid-restart) | kept |
| `AgentExpected` | in pogod's **desired state** — an `auto_start`, not-parked crew prompt — whether or not it is registered | kept |
| `AgentGone` | a corpse the registry holds; **or** a polecat whose persisted witness proves its process is not ours (pid holds nothing, or holds a process that started at a different time); **or** an agent with no registry entry, no witness **and** no desired state — nothing on this machine has ever claimed it should exist or observed that it did | reaped |
| `AgentUnknown` | evidence exists but is not conclusive — a witnessed polecat whose process is alive, a prompt that exists but does not parse, a witness whose pid we cannot identify. Also the zero value, so an implementation that cannot classify fails safe | kept |

**Three sources, one strict order: registry → witness → desired state.** The
first two are *evidence* (something looked at a process); the third is
*expectation* (something read a config file). Evidence beats expectation, and a
fresher look beats a staler one, so a corpse in the registry is never
resurrected by either of the later sources (mg-8677).

The desired-state half is what a registry-only answer cannot supply, and its
absence caused a fleet-wide outage. A restarted pogod begins with an **empty
registry**: it loads the fleet's persisted `mail-check-*` from disk and starts
ticking *before* `AutoStartAgents()` spawns the crew, so a registry-only answer
reports every crew agent gone and reaps the whole fleet's mail loop seconds
before the crew boot into a world without their schedules. The old
restart-on-crash guard could not prevent it — it reads a flag off a registry
entry that does not exist yet.

**The witness half is what the desired state cannot supply (mg-13a3).** Fixing
the crew outage above left polecats classified from *two absences* — not in the
registry, not in the desired state — and the code called that death. Absence of
evidence is not evidence of death, whatever a comment says about it: crew
survived only because `auto_start` happened to be an independent second witness,
and polecats have no prompt at all. mg-61a0 reproduced the consequence
end-to-end (a live polecat, unregistered after a restart, lost its mail-check
from memory *and* disk and went permanently dark). Since the registry is
in-memory with **no adopt/reattach path**, a survivor's absence never heals on
its own.

So each polecat's `(pid, start_time)` is persisted at spawn and dropped at exit,
giving a successor pogod something to *look at*:

> **Registry-absent + OUR process alive = UNKNOWN, never GONE.**

`(pid, start_time)`, never pid alone — pids are reused, and a bare
`kill(pid, 0)` answers "is SOME process alive", never "is OUR process alive". A
recycled pid reading alive would keep a dead polecat's schedule firing at a
corpse forever, which is mg-8677 re-entered through the fix for mg-61a0. The
start time is the kernel's, read via `ps -o lstart=`, because it must be
re-derivable by a process that never spawned the polecat.

Across a restart the population now splits on **evidence** rather than on the
shape of its config: crew are `auto_start` (EXPECTED → keep), live polecats are
witnessed (UNKNOWN → keep), and dead polecats either had their witness dropped
at exit or fail the identity match (GONE → reap). Orphan-nudge prevention is
unchanged.

**The store is test-safe by default, not by remembering (mg-da48).** Under a
test binary (`testing.Testing()`), `WitnessPath()` resolves to a per-process
temp file and the live store is **not reachable from the function at all**;
`witnessPathOverride` still lets one test pick its own path, which is isolation
from *other tests* — a different question. This is a default and not a guard
because the opt-in guard already existed and already failed: `go test
./internal/agent/` wrote **phantom** polecats into the live store — real
test-process pids under Go fixture names — which pogod's orphan detector then
read back as leaked polecats and mailed the mayor an authoritative `kill <pid>`
for, three times in ten minutes on 2026-07-17. `witness_test.go` sandboxed
sixteen times; the two files that polluted the fleet sandboxed zero, because
they spawn agents while testing *nudges* and *attach* and had no reason to know
this store exists. **An opt-in guard is only ever remembered by the tests that
least need it** — so the acceptance bar is that a new test file which spawns an
agent and does nothing special cannot touch the real store.

**The orphan alert is re-verifiable at read time (mg-da48).** The alert repeats
hourly and is read at an unbounded delay, by which point the survivor has
usually exited and its pid is recyclable — so its body carries the recorded
`start_time` and gates every runnable `kill` behind `pogo agent witness --json |
grep -q ...`, which re-probes `(pid, start_time)` **now**. The identity match
above protects the *detector* from a recycled pid; without this it did not reach
the one consumer told to run `kill`. An instruction that is only safe in the
second it was written must not be handed out ungated by a channel with an hourly
repeat.

```
~/.pogo/polecat-witness.json   # versioned JSON, atomic temp+rename writes
{
  "version": 1,
  "polecats": [
    {
      "name":         "cat-13a3",
      "pid":          32471,
      "start_time":   "2026-07-17T08:12:03+01:00",  // the KERNEL's, via ps -o lstart=
      "work_item_id": "mg-13a3"
    }
  ]
}
```

If the start time cannot be read at spawn, **nothing is written**: no witness
leaves the classifier exactly as it was, whereas a pid-only record would be a
false witness that answers UNKNOWN at a corpse forever. Implementation:
`internal/agent/witness.go`.

**Startup grace.** The sweep is additionally held until pogod's first
`AutoStartAgents()` sweep completes plus a 30s settle window: the invariant
above is only as good as the data it reads, and at boot neither the registry
nor the desired state is loaded yet. It fails safe in the only direction that
matters — a delayed reap is invisible, a premature one is the outage.

**Do not delete the alarm.** The GC's rationale is that it keeps
`scheduler_fire_failed` events from accumulating. For an EXPECTED agent a fire
failure is not garbage — it is the fault reporting itself, so such an agent
stays noisy on purpose and `pogo agent diagnose` reports `no_mail_loop` (see
below). Where the noise needs a channel, escalate (as
`newMailCheckReachabilityEscalator` does); never by deletion.

Every GC removal emits the same `schedule_removed` event as an explicit `rm`,
tagged `reason: agent_gone`, so the sweep is auditable from `events.log` alone.

### The inverse check: a missing mail loop must be legible

`agent.IsExpectedAgent` — the desired-state predicate — has **two consumers and
one source of truth**, so they cannot drift apart:

- the **reap** enforces the invariant by removing mail-checks for agents *not*
  in the desired state;
- **diagnose** enforces it by flagging agents *in* the desired state with *no*
  mail-check (`health: no_mail_loop`, `mail_check_missing: true`).

**But the desired state is not the whole judged set (mg-738f).** An
`auto_start=false` agent is outside it *by definition*, so gating diagnose on
`IsExpectedAgent` alone meant a turned-on off-by-default agent could never be
reported MISSING — only UNKNOWN, before any lookup. It ran, answered nothing, and
diagnosed `healthy`: mg-de08's own pathology, in the population de08's bar
excludes. So diagnose *also* judges an agent that is **configured** — a crew
prompt exists (`agent.IsConfiguredAgent`) — **and running**, whatever its
`auto_start` says. That is the reap's own rule one consumer over: **evidence
beats expectation.** "Not in the desired state" answers *should this be
running?* — the wrong question for an agent that **is**. Liveness keeps the RED
conditional: an agent that is not there stays UNKNOWN, because a detector that
cannot tell *"not there"* from *"there and deaf"* is the defect it is meant to
catch. Polecats stay unjudged — the **witness** covers them, not diagnose. See
[docs/investigations/deaf-survivor-off-by-default-2026-07-17.md](docs/investigations/deaf-survivor-off-by-default-2026-07-17.md).

Before mg-de08 the only thing diagnose did with schedules was consult them to
*suppress* a stall label (`cron_covered`): schedule-awareness ran in exactly one
direction and could only ever make an agent look healthier. An agent whose mail
loop had been reaped therefore diagnosed clean — which is why a two-hour
fleet-wide mail outage stayed invisible. An agent that can be mailed but never
woken is unhealthy, and now says so. `bin/pogo-self-deploy` asserts the same
invariant from outside the daemon, post-bounce.

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
  [scheduler id=research-poll due=... fired=... ack=9f3c1ab2]
  When this fire's work is done, run: pogo schedule ack research-poll --agent crew-research --token 9f3c1ab2

…run your normal processing loop. The bracketed metadata tells you whether
this was an on-time fire or a recovery from a sleep — use the `due` /
`fired` gap to decide whether to skim or catch up. When the work is done,
run the `ack` command the fire handed you.
```

Polecats use the same surface for one-shot wakeups (`--once --in 1h`) when
they want to be re-prompted later without spinning their own polling loop.

### The completion signal (mg-a754)

Delivery is only half the transaction. During the 23h30m fleet outage of
2026-07-22, `scheduler_fire_delivered` logged **647 successful deliveries** and
`nudge_sent` **771** — every record true, none useful, because every consuming
turn was a synthetic zero-token failure that accomplished nothing in ~10ms.
With no completion signal, a 100%-dead fleet and a 100%-healthy fleet produce
the same events log. That is why the failure survived twice.

So every fire now carries a nonce **completion token** plus the one-line command
that redeems it. The agent runs `pogo schedule ack <id> --agent <a> --token <t>`
when the fire's work is done; pogod validates the token against the outstanding
fire and emits `scheduler_fire_completed`.

Three properties do the load-bearing work:

- **It fails in the same direction as the work.** Producing the ack requires a
  live model turn that ran a tool. A synthetic error turn never calls the API
  and never runs anything, so it cannot emit one. That is exactly what
  `scheduler_fire_delivered` does not do.
- **It is harness-independent.** mg-8cdb's synthetic-failure detector reads
  Claude Code session transcripts, which codex, pi and cursor do not expose. An
  ack is a shell command; any harness that can run a tool can produce it.
- **The denominator survives restarts.** `fires_delivered`, `fires_completed`
  and `unacked_streak` live on the persisted Entry in `~/.pogo/schedules.json`,
  not in memory — an in-memory counter would reset on exactly the restarts an
  outage tends to produce.

Two guards keep the signal honest. Only the newest token is redeemable, so a
token copied out of an old transcript cannot inflate the numerator. And a
schedule that has **never** acked is reported as UNKNOWN, not failing
(`completion_tracked: false`) — an agent can simply forget, so a missing ack is
never a per-fire verdict.

"Never" means never, across re-registrations. A same-`(agent, id)` re-Add
zeroes the counters on purpose (a carried-over ratio would span two regimes and
describe neither), but a separate one-bit `ever_acked` on the entry survives it.
Without that bit the UNKNOWN guard could not distinguish a schedule nobody ever
taught to ack from one whose agent acked for weeks and then stopped coming back.
The state clears only on the next ack — one cadence period for a healthy agent,
**never** for one that does not return, which is the case the guard exists to
report on. That mattered beyond this one command: `internal/ackwatch` requires a
majority of ack-aware peers before either of its ratio arms will judge, and it
names `pogo schedule completion` as a compensating control for that requirement.
Both read the same predicate, so both went blind together — a backstop that
shares a trigger with the thing it backs up is redundancy in name only (mg-00d6).

Two bounds, both pinned by tests rather than asserted here. Zeroing requires a
**boot**, and survivors carry the cohort, so the blind case needs a zeroed
*majority* rather than merely a bounce. And ackwatch's absolute (FLEET BLACKOUT)
arm is not gated by any of this: it reads a trailing window counted from the
**events log**, which a re-registration cannot reset, which is why it kept
emitting through the 2026-08-11 outage while the ratio arms could not. That
independence is now asserted in a test. mg-00d6 was filed on a fleet-wide
operational story that its author later withdrew; no outage is known to have
been missed this way. `pogo schedule completion` additionally reports how many
tracked schedules were re-registered since their last ack, so a thin ratio
denominator is stated rather than inferred.

The signal that matters is fleet-wide and ratioed: one agent skipping one ack
is noise; every tracked schedule going to zero within the same minute is one
upstream cause and should page a human rather than trigger N restarts.
`pogo schedule completion` is the query. Applied to 2026-07-22 00:00–23:40 it
reads **647 delivered, 0 completed**, with per-schedule streaks climbing to 202
(mayor) and 143 (each PM).

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
- `internal/agent/prompts/templates/polecat.md`, `polecat-qa.md`,
  `polecat-build-pr.md`, and `polecat-triage.md` — one
  per-polecat mail-check schedule with id `mail-check-<work-item-id>`. The
  coordinator removes these in step 3 of its coordination loop when stopping a
  polecat; pogod also auto-GCs them as a backstop (see **Stale mail-check GC**
  below) so an agent whose process vanishes without an explicit `schedule rm`
  doesn't leave a schedule firing into the void.
- `internal/agent/prompts/mayor.md` — unchanged. The coordinator's in-process
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
- **refinery** emits `refinery_merge_attempted`, `refinery_merged`, `refinery_merge_failed`, `refinery_merge_cancelled`.
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
│   └── mayor.md           # Coordinator prompt
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

## Agent Display Labels

Agent names are the identity system — no UUID, no database. Each agent also gets a
**display label** derived from its name, which is what a human sees.

| Pattern | Meaning | Example |
|---------|---------|---------|
| `pogo-crew-<name>` | Long-running crew agent | `pogo-crew-arch` |
| `pogo-cat-<id>` | Ephemeral polecat | `pogo-cat-a3f` |
| `pogo-crew-<coordinator>` | The coordinator (a crew agent; default: mayor) | `pogo-crew-mayor` |
| `pogod` | The daemon (this one really is the process) | `pogod` |

The label is human-facing: `pogo agent list` and `pogo agent info` render it, `/agents`
returns it as `process_name`, and it is injected into the agent's environment as
`POGO_PROCESS_NAME`. **It is not a process name.** Nothing sets it on any process —
agents are spawned as their harness command (`claude`, `codex`, a test's fake-agent),
and a harness that `exec`s replaces even that argv. `pgrep -f pogo-crew-mayor` against
a live, healthy mayor matches nothing (measured, mg-710c).

Discovery therefore goes through pogod's registry, not the process table: `pogo agent
list`, or `/agents` for the recorded pid. Never grep the process table for these
strings — the result is always empty, and empty reads as "the agent is gone" (mg-de08).

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

**Confirmed nudge delivery:** a write to a PTY master succeeds whether or not anything is listening, so "the nudge was sent" long meant only "the bytes left pogod" — and, worse, the idle precondition above is the *negation* of the state a working agent is in, which made a busy agent unreachable (mg-ebee). pogod now registers a harness hook that records every prompt the agent actually **submits** (Claude Code's `UserPromptSubmit`, wired through `Provider.SubmitReceiptHook` → `pogo hook prompt-submit` → `$POGO_HOME/agents/receipts/<name>.submits`), and the default nudge mode writes, then watches that count. On no movement it escalates: a **bare return** first — it submits text the harness left unsent in its composer and carries no content, so it cannot duplicate — then the message again, then a refusal. An agent whose harness cannot report submissions keeps the wait-idle behaviour unchanged, so absence of the signal degrades rather than misfires. Limits and the live measurements are in [docs/investigations/confirmed-nudge-delivery-2026-07-29.md](investigations/confirmed-nudge-delivery-2026-07-29.md).

**Warning on mail to an agent nobody is running:** `mg mail send <agent>` succeeds identically whether or not the recipient is a live process — exit 0, `Delivered`, a message id — so a dead channel is invisible to the sender until a reply fails to arrive, which is also what a busy agent looks like (mg-d924: five messages to a stopped `doctor` in one evening). mg owns the mailbox and pogod owns liveness, and they are separate products, so mg is not in a position to say this and is not made to ask. pogod instead registers a second harness hook — Claude Code's `PostToolUse` on the Bash tool, wired `Provider.MailRecipientHook` → `pogo hook mail-recipient` — which reads the command that just ran, parses any `mg mail send <name>` out of it (`internal/mailwarn`), and compares the recipient against `RosterReport`. An **absent** or **parked** configured agent produces a warning fed back as `additionalContext`; everything else produces nothing. It is a warning and not a refusal, because mail queued for an on-demand agent that will be started later is legitimate. Because "no warning" and "no hook" look identical, `pogo hook mail-recipient --self-check` reports whether the hook is registered here and whether the roster can be read, and a roster read that fails mid-send says so instead of going quiet.

**Which running agents that warning is actually in force for:** the hook is installed at *spawn*, so it protects an agent only from that agent's next start — the fleet running when it merged kept the old silent behaviour, `--self-check` answered for one agent at a time and only when someone remembered to run it, and nothing polled. The measured fleet-wide reading on the day it merged was **0 of 9 running agents armed**, and it looked exactly like a healthy listing (mg-503d). Every running row of `pogo agent list` now carries `mail-warn=<state>` (`AgentInfo.MailWarn` / `MailWarnDetail`, so `--json` and `pogo agent diagnose` carry it too) with a summary underneath counting the armed and naming the unarmed. Arming is an **observation**, not an inference from the settings file: a harness loads its hooks when the session starts, so a registration written afterwards — or one naming a binary that has since moved — is indistinguishable on disk from a working one. The hook therefore stamps `.claude/pogo-mailhook.stamp` on every invocation, including the silent majority, and `internal/hookarm` calls an agent `armed` only when that stamp is newer than the agent's own `StartTime` (a crew agent's working directory outlives its process, so an older stamp belongs to an earlier agent and reports `pending`). The report is held to the standard it enforces: an unmeasured agent renders `mail-warn=?` and a fleet where nothing reports renders `UNAVAILABLE`, never a count — a blank column would be the same substitution of silence for an answer, one level up.

**Initial-nudge readiness gate:** the *first* nudge after spawn — the one that bypasses the harness's interactive prompt — cannot rely on quiescence alone. A harness is also quiet during pre-TUI startup, so a quiescence-only gate can fire the nudge before the interactive input loop exists; the bytes then pile in the kernel input buffer and get read as one un-re-tokenized paste block, wedging the agent (mg-ce61). So the initial nudge waits in `NudgeWaitReady` mode (`internal/agent/nudge.go`), which defers delivery until the provider's `Nudge.PromptReadySentinel` (Claude: `"? for shortcuts"`) appears in PTY output *and* output then settles — proving the input loop has rendered, not merely that the harness is quiet. Providers with no sentinel (e.g. Codex) fall back to plain wait-idle. If the sentinel never appears (a harness UI change), delivery degrades to best-effort on timeout rather than dropping the nudge.

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

2. **Polecat concurrency: no limit in pogod.** The daemon doesn't enforce concurrency limits. The coordinator (or human) decides how many polecats to spawn. pogod is substrate, not policy.

3. **Refinery repo access: own worktrees.** The refinery maintains dedicated worktrees under `~/.pogo/refinery/worktrees/`, one per repo. All testing and merging happens there, never in agent or user working directories. Isolation prevents dirty-tree conflicts and keeps merge operations predictable. Post-merge, it will fast-forward the source checkout's target branch as a convenience — but only when that checkout is clean and on the target branch (gh #30).

4. **No tmux dependency.** pogod allocates PTYs directly and holds master file descriptors. Interactive access (`pogo agent attach`), input injection (`pogo nudge`), and output monitoring are all consequences of the parent-child process relationship. No terminal multiplexer in the stack.

5. **Single event log in pogo.** All events — agent lifecycle, polecat milestones, refinery merges, plus work item transitions and mail mirrored from macguffin — write to one JSONL file at `~/.pogo/events.log`. macguffin remains the source of truth for work item state, but the durable observability spine lives in `~/.pogo/` so pogod's writers (refinery, agent supervisor) don't need a macguffin dependency. `mg` mirrors its transitions in via the `pogo events emit` CLI bridge. Schema and event catalog: [`docs/event-log.md`](docs/event-log.md).

6. **Prompt files are the agent roster.** There is no separate roster file or registry. The set of agents that exist is the set of prompt files in `~/.pogo/agents/`; the boot set is the subset whose TOML frontmatter declares `auto_start = true`. This subsumes the earlier proposal of a `~/.pogo/crew-roster` file — the prompts already on disk are the roster, and adding a new agent is a matter of dropping a markdown file with the right frontmatter. Per-agent runtime knobs (`restart_on_crash`, `nudge_on_start`, `worktree`) live in the same frontmatter block, co-located with the prose that defines the agent's role.

## What This Is Not

- **Not an agent framework.** There is no "pogo agent SDK." Agents are harness processes (Claude Code today) that use CLI tools.
- **Not a job scheduler.** The coordinator decides when to spawn polecats. pogod just executes the spawn.
- **Not a database.** All state is files. All coordination is filesystem operations.
- **Not an IDE.** Pogo is a set of composable tools. It works with any editor, any shell, any workflow.
