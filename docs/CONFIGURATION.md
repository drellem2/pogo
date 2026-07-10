# Configuring pogo

A map of pogo's customization points — what you can tune, where each setting
lives, and which doc to read for depth. This is a survey, not a reference. For
the guided walkthrough of reshaping pogo for a non-coding workflow, start with
[docs/customizing.md](customizing.md).

## PM TOMLs

Per-product-manager config lives in `~/.pogo/agents/pm/<name>.toml` —
`repos`, `tags_any`, and `sources` define what a PM owns and scans during a
sweep. A PM crew prompt (`crew/pm-<name>.md`) composes by extending the shared
`pm-template` *with* its TOML (see the synthesis pattern below). To add one,
drop a new `<name>.toml` and a matching `crew/pm-<name>.md` stub.
See [docs/prompt-customization.md](prompt-customization.md).

## Prompt templates

Agent behavior is defined by prompt files under `internal/agent/prompts/` —
`mayor.md` (the coordinator), `crew/doctor.md`, `pm/pm-template.md`, and the
`templates/polecat.md` / `templates/polecat-qa.md` /
`templates/polecat-build-pr.md` / `templates/polecat-triage.md` /
`templates/polecat-review.md` templates for polecats
(disposable worker agents); installed copies live in `~/.pogo/agents/`. The `extends <template> with config <toml>`
directive synthesizes a crew prompt from a base plus a TOML. See
[docs/prompt-customization.md](prompt-customization.md) and [PROMPT_GUIDELINES.md](PROMPT_GUIDELINES.md).

## Coordinator name

The coordinator role is called "ringmaster" by default, but the name is policy, not
mechanism — rename it with:

```toml
[agents]
coordinator = "boss"   # default "ringmaster"
```

The configured name decides the coordinator's agent name (and therefore its
`mg` (the task-store CLI) mailbox, its `mail-check-<name>` schedule id, and
where pogod's refinery (the merge queue) and stall watcher address their
mail/nudges), and what the shipped prompts call the
role: prompt files reference the coordinator via `{{.Coordinator}}` (and
`{{.CoordinatorTitle}}` for headings), resolved at prompt-synthesis time.
Polecat templates resolve it through the same text/template pass as `{{.Id}}`;
static prompts (mayor.md, crew, pm-template) get a plain string substitution,
so user prompts containing other `{{` sequences are untouched. Two things stay
fixed regardless of the name: the prompt file path `~/.pogo/agents/mayor.md`,
and the `"mayor"` category label in `pogo agent prompt list --json`.

## Worker name

The worker role (the disposable per-task agents) is called "pogocat" by default.
Like the coordinator name it is policy, not mechanism — rename the display name
with:

```toml
[agents]
worker = "critter"   # default "pogocat"
```

**This is a display-only knob, and that is the important difference from the
coordinator.** The coordinator name IS an address — it decides a mailbox,
schedule ids, and prompt-file paths — so renaming it moves real routing. A
worker is never addressed by its role word: every polecat is reached by its bare
agent name (e.g. `30d5`), so the configured `worker` name feeds **only prose** —
prompt files reference it via `{{.Worker}}` (and `{{.WorkerTitle}}` for
headings), resolved at prompt-synthesis time the same way `{{.Coordinator}}` is.

Renaming the worker changes what the prompts *call* the role and nothing else.
Five load-bearing identifiers stay frozen at `polecat` regardless of the display
name (a rename touching any of them would orphan on-disk state or break a
cross-tool contract):

| Identifier | Value | Why frozen |
|---|---|---|
| Branch prefix (`gitgc.BranchPrefix`) | `polecat-` | orphan-sweep reads live branches back by this prefix |
| Polecats dir (`gitgc.DefaultPolecatsDir`) | `~/.pogo/polecats` | orphan-sweep reads the dir back from disk |
| Agent-type key (`agent.TypePolecat`) | `polecat` | written to `POGO_AGENT_TYPE`; matched by reap/park/gitgc/config lookups |
| Event-log actor prefix | `cat-<name>` | persisted actor identity; `classify.go` parses it back |
| Role env var | `POGO_ROLE=polecat` | cross-tool contract consumed by `mg prime` / role detection |

The `[agents.polecat]` config sub-table key (for per-worker provider overrides)
is likewise a frozen identifier, not a display name — it stays `polecat` even if
you rename the display. And `--type polecat` on the CLI keeps naming the frozen
accepted value: the flag documents an identifier, not the display role, so it is
deliberately *not* driven by the `worker` name.

## Crew auto-start

At boot pogod starts every crew agent whose prompt frontmatter says
`auto_start = true` — but only when a `config.toml` exists (a daemon with no
config file is treated as unconfigured/isolated and never spawns agents;
mg-3dc3). A *configured* daemon can turn the whole sweep off with the global
switch (mg-9a1c):

```toml
[agents]
autostart = false   # default true
```

`POGO_AGENT_AUTOSTART` (true/false) overrides the file setting. This is the
knob for sandboxes and tests that need a config file (e.g. an `[agents]`
command override) without putting a crew fleet on the machine. Per-agent
opt-out stays in prompt frontmatter — see
[docs/customizing.md](customizing.md) §"Opt out of auto-start".

## Agent PATH (extra_path)

Under launchd/systemd pogod inherits a minimal PATH, so spawned harnesses must
resolve from what `internal/pathenv` repairs at startup: the pogod binary's own
directory, the inherited PATH, discovered per-user toolchain dirs (`~/.local/bin`,
every installed nvm Node version's bin — newest first, the npm global prefix
from `~/.npmrc`, `~/.npm-global/bin`, `~/.volta/bin`), then system fallbacks. If a harness
runtime lives somewhere the probe can't find (gh #25 — pi under an exotic Node
install), add it explicitly:

```toml
[agents]
extra_path = ["~/my-node/bin", "/opt/tools/bin"]   # prepended to pogod's PATH
```

`POGO_EXTRA_PATH` (colon-separated) overrides the file setting. Entries support
`~` and `$HOME` expansion and win over every discovered location.

## Scheduler

`pogo schedule` registers recurring (`--cron`) or one-shot (`--once --in N`)
wakeups that fire from pogod's heartbeat and survive host sleep and restarts.
`--id` makes a schedule idempotent (re-running replaces, not stacks); the
default `--replay once` is at-most-once, firing once after a long sleep then
rescheduling forward. Source of truth: `internal/scheduler/`; run
`pogo schedule --help` for the full flag set.

## Stall watcher

A passive watcher inside pogod that rides the heartbeat loop and nudges the
mayor when work piles up *behaviorally* — the mayor's process is healthy but
its loop has stopped draining work. Two thresholds: an `available` work item
the mayor owns (assigned to it, or unassigned) sitting unclaimed past an age
limit, and the mayor's `new/` maildir holding an over-age message or more than
a count ceiling. On a cross it sends one nudge per offending batch and appends a
`stall_watch_fired` event to `~/.pogo/events.log`; a per-category cooldown caps
the nudge rate. Running in pogod's *independent* heartbeat is the point — a
watcher inside the mayor's own loop can't catch that loop skipping its own
check-work / check-mail steps (gh drellem2/macguffin #12). Configure under
`[stall_watch]` in `config.toml`:

```toml
[stall_watch]
enabled = true                          # default true
agent = "mayor"                         # which agent to watch (default: the
                                        # configured [agents] coordinator)
unclaimed_item_age_threshold = "10m"    # Threshold A
unread_mail_age_threshold = "10m"       # Threshold B (age)
max_unread_mail_count = 5               # Threshold B (count)
nudge_cooldown = "5m"                   # min gap between same-category nudges

# Priority wake (gh #61): a high-priority available item skips the 10m gate.
priority_wake_enabled = true            # default true
high_priority_wake_delay = "30s"        # min age before a high-priority item wakes
high_priority_wake_cooldown = "3m"      # min gap between priority-wake nudges
fast_priorities = ["high"]              # Priority values that trigger the wake
```

### Priority wake

Threshold A treats every unclaimed item the same — it waits out the full
`unclaimed_item_age_threshold` (10m) regardless of priority. That is the wrong
latency for urgent work: when the coordinator is idle and has backed its polling
off, a `priority = high` item with no accompanying mail could sit up to ~30
minutes before pickup (gh drellem2/pogo #61).

The priority wake is a priority-aware branch on the *same* 30s available/ scan.
An item that is **ready** (deps met — it is in `available/`, not `pending/`),
**assigned to the watched agent** (or unassigned), and carries a priority in
`fast_priorities` bypasses the 10m gate and is delivered after only
`high_priority_wake_delay` — via the **same wait-idle nudge**, so a busy agent is
never interrupted (the write lands at its next turn boundary) and an idle agent
is woken at once. A dedicated `high_priority_wake_cooldown` keeps an item that
stays available (e.g. it can't be dispatched yet) from re-nudging every tick;
blocked (`pending/`) and already-claimed (`claimed/`) items are never scanned, so
they cannot loop-nudge either. When the wake is disabled, high-priority items
fall back to the standard 10m gate — disabling it never silences them.

This is a sanctioned system-event nudge (gh #33), not a producer-attributed ask:
the wake policy lives entirely in pogod, keyed off the generic
`WorkItem.Priority` field, so `mg` stays a decoupled work queue with no
pogo-specific "wake" flag.

Note on `pogo agent diagnose`: diagnose measures a coordinator's health against
its ~30-min backstop cron, so it does **not** surface this idle-latency gap — the
priority wake, not diagnose, is the real fast path for urgent work.

Source of truth: `internal/stallwatch/`; see
[docs/design/stall-watch-design.md](design/stall-watch-design.md) and
[docs/design/priority-wake-design.md](design/priority-wake-design.md).

## Agent registry

Each agent has a directory under `~/.pogo/agents/<name>/` holding its prompt,
PID, and last-activity state; `pogo agent start`/`stop` manage the lifecycle and
`pogo agent diagnose <name>` reports health. A dead-process entry is now
cleared on the next start so a stale record can't block a respawn (mg-427f /
78b69d7). See [docs/design/agent-state-machine-design.md](design/agent-state-machine-design.md)
and [docs/operations.md](operations.md).

## Refinery / build.sh gates

The refinery is a deterministic merge loop inside pogod (not an agent): it
checks out each merge-ready polecat branch in its own worktree, runs the repo's
quality gate, and fast-forward-merges to `main` only on success. The gate is
your repo's `build.sh` / `test.sh` (or a `.pogo/refinery.toml`). Worktrees and
logs live under `~/.pogo/refinery/`; disable with `[refinery] enabled = false`.
See [ARCHITECTURE.md](../ARCHITECTURE.md) §"The Refinery".

**QA gate (hardcoded).** Before processing any MR, the refinery scans the
macguffin workspace (`Config.MacguffinDir`, default `~/.macguffin/work`) for a
work item with `type: qa` whose `source` matches the MR author (the work-item
ID behind the branch). If a matching QA item sits in a pending state
(`available` / `claimed` / `pending`), the merge is **held** — moved to
`held` status and re-queued at the tail so other MRs proceed. The merge runs
only once the matching QA item reaches `done`/`archive`, or when no matching QA
item exists at all (the gate is opt-in per work item, but always-on as a
mechanism). This is enforced in code — `internal/refinery/qa_gate.go`, called
from `internal/refinery/refinery.go:499` — not a layered or optional pattern.
The only knob is `MacguffinDir`: set it empty to disable the gate entirely.
There is no per-project, per-repo, or per-branch toggle.

The companion convention is the `polecat-qa` prompt template
(`internal/agent/prompts/templates/polecat-qa.md`), which scripts the polecat
that completes a QA item — verifying the source work item's acceptance criteria
and reporting pass/fail. The refinery's gate enforces the existence and
completion of the QA item independently of which polecat actually runs it.

## `pogo install`

`pogo install` is one-step setup: start pogod, run `mg init`, and install the
default agent prompts to `~/.pogo/agents/`. It is idempotent — stale canonical
prompts are auto-updated, user edits preserved (`--force` overwrites, backing up
to `<name>.bak.<timestamp>`). The bundled `install.sh` runs it as its final
step; opt out with `--no-pogo-install` or `POGO_NO_POGO_INSTALL=1` (mg-6bfd).
See [docs/customizing.md](customizing.md).

## Role default-migration guard

pogo never writes `config.toml` on its own, and the role-name defaults
(`coordinator = "ringmaster"`, `worker = "pogocat"`) live only in code — `Load()`
fills them in-memory from a const when the key is absent. So the common existing
install has **no `[agents]` role keys on disk**. That is normally fine, but it
means the day a future pogo release changes a shipped default, every existing
install would *silently* adopt the new name on the next binary run — moving the
coordinator's mailbox/schedule-ids or the worker's display out from under a
running deployment.

The guard closes that gap. On an **existing install** it pins the frozen
historical role names into `config.toml`, once — these are the pre-flip
defaults (`mayor` / `polecat`), deliberately distinct from the current shipped
defaults above, so a default flip cannot move a running deployment:

```toml
# pinned by pogo default-migration guard (mg-7d95) — keeps this existing install
# on its current role names if a future pogo release changes the shipped defaults.
[agents]
coordinator = "mayor"
worker = "polecat"
```

It runs at two seams: `pogo install` (the explicit upgrade) and pogod boot (so a
daemon restart alone propagates it). Behavior:

- **Existing install** (a `config.toml` exists, *or* a stamped prompt exists
  under `~/.pogo/agents/` for installs predating config.toml) with a role key
  **absent** → the current default is appended, without reformatting the rest of
  the file. An operator-set value is never overwritten.
- **Fresh install** (neither signal) → **no-op**; nothing is written, so a fresh
  machine adopts whatever default the binary ships. This is the intended "fresh
  gets the new default" path.
- **Idempotent** — a role key already present under `[agents]` is the durable
  done-signal; re-runs (and every subsequent daemon boot) rewrite nothing.

The guard is generic over role keys — it covers `coordinator` and `worker`
today, and any role key added later — so no future default-flip is unsafe for
existing installs *provided the guard has already rolled out to them*. That
ordering is a hard constraint: the guard pins the default in effect **when it
runs**, so it must reach existing installs (via an upgrade or a daemon restart)
**before** any release flips a default. Once a default is flipped on an install
that never ran the guard, the original name is unrecoverable — nothing recorded
it. Source of truth: `internal/config/migrate.go`.

## Mail

Inter-agent coordination flows through Maildir mailboxes under
`~/.macguffin/mail/`, one per agent plus a `human` mailbox the notifier watches.
Each uses the standard `cur/new/tmp` convention, so delivery is an atomic
rename — no locks, no server. Send with `mg mail send <to> --from=<id> ...` and
read with `mg mail list <id>`. See [ARCHITECTURE.md](../ARCHITECTURE.md) for the
filesystem-coordination model.

## State directory (`POGO_HOME`) and running multiple instances

Every pogo state path derives from a single root: `$POGO_HOME`, or `~/.pogo`
when the variable is unset (`PogoHome()` in `internal/config/config.go`). That
one function seeds `refinery-state.json`, `schedules.json`, `agents/` (including
the `agents/sockets/` attach sockets), `polecats/`, `events.log`, `recovery/`,
`projects.json`, and `plugin/` — so the root you pick determines where *all*
daemon state lives.

**Running N pogo instances requires a distinct `POGO_HOME` per instance.**
Because every state path hangs off `PogoHome()`, overriding `POGO_HOME` (or
`HOME`, which supplies the default) fully isolates a daemon's state (mg-3dc3):
two daemons with different roots share nothing.

**Sharing one `POGO_HOME` shares *all* state — by construction, not by leak.**
If two instances resolve to the same root, they read and write the same
refinery queue, the same scheduler entries, the same `agents/` and Maildir. This
is not a bug or a state leak; it is the direct consequence of every path deriving
from the shared root. Refinery counts, schedules, and mailboxes co-mingle because
they are literally the same files. If you want isolation, give each instance its
own `POGO_HOME`; if you want a single shared fleet, point them at the same one on
purpose.

One caveat on the default: an old shell integration exported `POGO_HOME=$HOME`,
and pogo normalizes a `POGO_HOME` equal to the user's home directory to
`$HOME/.pogo` (the documented default) rather than scattering state across the
home root. See the `PogoHome()` doc comment for the full rationale, including why
it never falls back to `os.TempDir()`.

One caveat on the attach sockets: a unix domain socket path cannot exceed
`sun_path` (103 usable bytes on darwin, 107 on linux), and a deep enough
`POGO_HOME` leaves no room for `agents/sockets/<agent>.sock`. Such a root — a
scratch dir under `/var/folders` on darwin, say — puts the sockets in
`$TMPDIR/pogo-agents-<hash of the root>` instead. The hash keeps distinct roots
distinct, so the isolation guarantee holds either way; pogod logs a line at
startup when it takes this path. Everything else still lives under the root. If
you want your sockets under `POGO_HOME` (nicer to inspect and clean up), pick a
shallow root: `~/.pogo-sandbox` fits comfortably, a 90-byte path does not.
