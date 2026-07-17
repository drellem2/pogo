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
`templates/polecat-review.md` / `templates/polecat-architect.md` templates for polecats
(disposable worker agents); installed copies live in `~/.pogo/agents/`. The `extends <template> with config <toml>`
directive synthesizes a crew prompt from a base plus a TOML. See
[docs/prompt-customization.md](prompt-customization.md) and [PROMPT_GUIDELINES.md](PROMPT_GUIDELINES.md).

## Where `config.toml` lives, and how the two files combine

pogo reads up to two config files and **layers them key by key**, lowest
precedence first:

1. `~/.config/pogo/config.toml` (or `$XDG_CONFIG_HOME/pogo/config.toml`)
2. `$POGO_HOME/config.toml` — only when `POGO_HOME` is set

A key set in the `POGO_HOME` file overrides the same key in the XDG file. Every
key it does *not* set keeps the XDG file's value. So a `$POGO_HOME/config.toml`
holding nothing but `[server] port = 10001` changes the port and nothing else.

This used to be whole-file precedence: whichever file existed at the higher
layer was the *only* file read. That made `$POGO_HOME/config.toml` a trapdoor —
anything that created a partial one silently dropped every key the user's real
config carried, including the `[agents]` role pin the migration guard writes
there (below). Layering closes it (mg-cf9e).

Two consequences worth knowing:

- **Writes still go to one file.** `pogo install`'s role pin writes to
  `$POGO_HOME/config.toml` when it exists, otherwise the XDG file. It skips any
  role key already set in *either* layer, so pinning never overrides a value you
  set in the other file.
- **`Config.Sources`** lists the files that were actually read, in precedence
  order; `Config.Source` is the highest-precedence one. A daemon with neither
  file has an empty `Source` and does not auto-start crew (mg-3dc3).

Environment variables (`POGO_PORT`, `POGO_AGENT_COMMAND`, `POGO_AGENT_PROVIDER`,
`POGO_EXTRA_PATH`, `POGO_AGENT_AUTOSTART`, …) override both files.

## Coordinator name

The coordinator role is called "ringmaster" by default, but the name is policy, not
mechanism — rename it with:

```toml
[agents]
coordinator = "boss"   # default "ringmaster"
```

**A running coordinator is never renamed.** Whatever config resolves to, if a
coordinator process is currently running under a different name, pogo refuses the
rename, keeps the running name, and logs the refusal. Stop the coordinator first
if the rename is intended:

```
pogo agent stop mayor     # then edit [agents] coordinator, then start it again
```

The refusal is what keeps a config mishap from being fatal. The coordinator's
name is load-bearing — it is the agent's `mg` mailbox, its `mail-check-<name>`
schedule id, the name the stall watcher arms on, the address the refinery mails
merge results to, and the name pogod auto-starts. Renaming it out from under a
live process orphans all of that. Before the guard, the only thing preventing it
was the pinned config key below; now a lost pin leaves the wrong name in a file
that the next resolve overrides from the live process.

Mechanically: the agent registry writes `$POGO_HOME/coordinator.json` (name +
pid) when it spawns the coordinator and removes it when the process exits. A
record whose pid no longer answers signal 0 counts as "not running", so a
coordinator that stopped — or one whose pogod was `SIGKILL`ed — never freezes the
name permanently. Source of truth: `internal/config/coordinator.go` (mg-cf9e).

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

## Heartbeat reaper (tier 1)

A goroutine inside pogod that watches a declared list of launchd jobs and
`launchctl kickstart`s any whose **heartbeat has gone stale**. Liveness here is
**heartbeat freshness, never process existence and never PID liveness**: a job
at `state = running` whose heartbeat state file has not been touched within its
period is *dead*, and the reaper says so and restarts it. This is the failure
class `KeepAlive` structurally cannot see — a wedged run loop, a closed socket,
a timer that never rearmed — because the process persists, so launchd sees a
healthy job forever. See [docs/design/reaper-design.md](design/reaper-design.md)
for the full rationale (mg-d18b).

Each job touches a state file at the end of every *successful* loop iteration
(e.g. `seen.json`, `bridget.seen`, or a dedicated
`~/.pogo/health/<job>.heartbeat`); the reaper keys on that file's mtime, never
on a log line — a poller logs only when it delivers, so a quiet mailbox is
indistinguishable from a dead poller. Configure under `[reaper]`:

```toml
[reaper]
enabled = true            # default true; with no jobs it is a logged no-op
interval = "60s"          # how often the reaper sweeps (default 60s)
max_kickstarts = 3        # consecutive kickstarts before GIVING UP + escalating
# Each job: "<launchd-label>|<heartbeat-path>|<period>". A leading ~ in the
# path is expanded to $HOME; period is a Go duration. The period doubles as the
# post-kickstart settle/backoff window.
jobs = [
  "com.pogo.watchdog|~/.pogo/health/watchdog.heartbeat|5m",
  "com.pogo.gh-issues|~/.pogo/gh-issues/seen.json|10m",
]
```

Three properties are load-bearing and every one is tested:

- **Loud.** Every kickstart logs the job, observed staleness, attempt number,
  and resulting pid; every recovery and every give-up logs too. A silent
  supervisor eventually becomes the thing concealing the failure.
- **Bounded, backed off, gives up loudly.** After `max_kickstarts` consecutive
  kickstarts that do not restore freshness, the reaper **STOPS** and mails both
  `mayor` and `human`, then stays quiet. This is the mg-1679 defense: a job that
  FATALs on every start (launchctl reports a fresh pid each time) would
  otherwise be kickstarted forever — a new self-concealing failure.
  `"Kickstarted 3 times, heartbeat still stale"` is the most important line the
  reaper emits. The `period` is the settle window: a just-kickstarted job is not
  re-judged until it has had that long to write a fresh beat.
- **Kickstart only.** The reaper never kills by pattern (`pkill -f` is banned —
  mg-8c9c); it only issues `launchctl kickstart -k gui/$UID/<label>`, a demand
  spawn, which works on this host even though the nondemand-spawn wedge
  (mg-50e0) blocks `KeepAlive`/`RunAtLoad`.

### The gap this tier does NOT close (known single point of failure)

The reaper can restart every `com.pogo.*` job **except pogod itself** — a child
agent cannot reap its parent, and launchd will not (mg-50e0). "Who reaps pogod"
(tier 2) is deliberately **unbuilt**: it is blocked on the open experiment of
whether a reboot unwedges `gui/501`. The obligation tier 1 *does* carry is
**detection, not recovery**: an unnoticed pogod death is indistinguishable from
a quiet afternoon. So pogod publishes its **own** heartbeat to
`~/.pogo/health/pogod.heartbeat` on every heartbeat tick (independently of
`[reaper]` enablement), so an external, human-held check — the digest, or
bridget once threading is on — can surface pogod's own liveness. That one check
is the named, accepted single point of failure until the reboot settles tier 2.

Source of truth: `internal/reaper/`; see
[docs/design/reaper-design.md](design/reaper-design.md).

## Host reconcile + drift check

`pogo service reconcile` and `pogo service check-drift` close the gap the reaper
does **not**: the repo is not the running system. A fix can merge correctly into
git, the code can be correct, and the running host can stay on the old behavior
— because pogo generates correct artifacts and, until this, had no step that
reconciled them onto the host and no check that noticed when the host had
drifted. That defect produced four incidents in a single day, none with a worker
at fault; one (a stale recovery plist) hid for **six weeks** because nothing
compared the *loaded* job to what the generator would produce (mg-be0c).

**Complementary to the reaper, not overlapping.** The reaper kickstarts a job
whose *heartbeat* is stale (a dead or wedged process). Reconcile restarts a job
after its *file* changed (an alive process running old code). A fresh heartbeat
proves the process is doing work, not that it runs the current code — a hardened
poller still executing its pre-hardening loop ticks its heartbeat perfectly, and
the reaper correctly leaves it alone. Neither covers the other's case.

Declare the host-side artifacts to manage under `[reconcile]`:

```toml
[reconcile]
# Each mirror: "<name>|<source>|<target>[|<launchd-label>]". A leading ~ in
# either path is expanded to $HOME. The label is optional — omit it for a file
# that is not a running launchd job. Host artifacts are COPIES of their source,
# never symlinks into a checkout.
mirrors = [
  "watchdog|~/dev/pogo-reminders/bin/watchdog.sh|~/.pogo/pogo-reminders/bin/watchdog.sh|com.pogo.watchdog",
  "gh-issues|~/dev/pogo-reminders/bin/poll-gh-issues.sh|~/.pogo/pogo-reminders/bin/poll-gh-issues.sh|com.pogo.gh-issues",
]
```

Four properties are load-bearing and every one is tested (`internal/reconcile`),
with an end-to-end acceptance in `scripts/reconcile-acceptance.sh`:

- **Copies, never symlinks.** A symlink from `~/.pogo/…/bin/*.sh` into a
  `~/dev/…` checkout would make an *uncommitted local edit instantly live in
  production* — no merge, no review — inverting the repo/host boundary this whole
  step defends. Copies preserve the boundary; the cost is that copies can drift,
  and drift is detectable (that is what `check-drift` is for).
- **Atomic replace, never in-place rewrite.** `reconcile` writes a temp file in
  the target's directory and `rename(2)`s it over the target. bash reads a
  script by byte offset; rewriting the file under a live interpreter can resume
  it at a shifted offset and execute garbage. The idle interpreter keeps its
  original inode until it is replaced wholesale.
- **Restart the process, never just the file.** Writing bytes changes nothing
  for a long-lived bash `while` loop — bash parses the loop once and never
  re-reads the file, so a patched poller can run its pre-patch code for its
  entire life. After replacing the bytes `reconcile` issues an explicit
  `launchctl kickstart` (a demand spawn, which works on this host despite the
  nondemand-spawn wedge, mg-50e0); delegating the restart to `KeepAlive` would
  restart nothing. A re-run also heals a box whose file is already correct but
  whose process started before the file was written.
- **check-drift reports, never fixes — and compares the RUNNING reality.** It
  never reconciles (an auto-fix loop fighting a genuinely-broken artifact is the
  unbounded-reaper failure shape); it exits 1 when any mirror drifts so a
  schedule or CI step can gate on it. It checks three dimensions: **file** (the
  on-disk copy no longer matches its source), **loaded** (the launchd job execs
  a *different program* than the target — the recovery-plist case, exactly how a
  stale plist hid for six weeks), and **process** (the running process started
  *before* the target was last written, so it parsed old bytes even at the
  correct path — pa's pollers ran 41 minutes of pre-patch code). The last two are
  the running-reality checks: *the file is not the process.*

Run `check-drift` on a schedule so drift shouts rather than rots — a reconcile
step you must remember to run is another thing that silently goes stale:

```bash
pogo schedule "$POGO_AGENT_NAME" --cron "*/15 * * * *" --id reconcile-drift \
    --message "Run: pogo service check-drift; if it exits 1, run pogo service reconcile."
```

Source of truth: `internal/reconcile/`.

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

The pin is a belt, not the only belt. Two other mechanisms back it up, because a
config key that must never be lost is a bad single point of failure: config files
now **layer key by key** so a partial file cannot drop the pin, and a **running
coordinator is never renamed** whatever the config resolves to. See the two
sections above (mg-cf9e).

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

**`POGO_HOME` isolates *state*, not *config*.** Every path above hangs off
`PogoHome()`, but `config.toml` does not: `~/.config/pogo/config.toml` is read as
the base layer regardless of `POGO_HOME`, and `$POGO_HOME/config.toml` layers on
top of it (see "Where `config.toml` lives" above). A sandbox that sets only
`POGO_HOME` therefore inherits the real user's config keys it does not itself
override. To isolate config too, point `HOME` and `XDG_CONFIG_HOME` at the
sandbox as well — the isolation tests and `cmd/pogod`'s do exactly that.

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

`$TMPDIR` is itself unbounded, so if it is long enough to squeeze out the
reserved name budget (roughly 52+ bytes), the sockets degrade one step further to
`/tmp/pogo-agents-<hash of the root>`, which fits under any root. The hash — and
with it the per-root isolation — is unchanged. This only matters if you run pogod
with an unusually deep `TMPDIR`; the guarantee it protects is that **the 24-byte
agent-name budget below holds under every root and every `TMPDIR`**, so a legal
name never fails to bind.

Wherever the socket directory lands, pogod insists on owning it and on mode
`0700` before it binds anything inside. An attach socket brokers a PTY, so a
directory another local user can write to — a hashed leaf pre-created under
world-writable `/tmp`, or a symlink planted there — would let them read or
replace the socket. pogod tightens a too-permissive directory of its own,
refuses one owned by anyone else, and never follows a symlink at the leaf;
either refusal is a loud exit at startup, not a silent downgrade.

The same limit implies a hard ceiling on **agent names**: pogo reserves 24 bytes
for `<agent>.sock` when choosing the socket directory (`MaxAgentNameLen`). Real
names are far shorter — `pm-dealdesk` is 11, a polecat is named for its work
item — so you are unlikely to meet this limit. A name longer than 24 bytes is
rejected at spawn with HTTP 400 (`pogo agent start` and `pogo agent spawn-polecat`
print the error and exit non-zero).

The rejection is unconditional, not conditional on your root's depth. Only a root
deep enough to have consumed the socket directory's headroom (roughly 53+ bytes)
would actually push such a name's socket path past `sun_path` — the default
`~/.pogo` root has room for a 64-byte name — but a name that works on one machine
and silently loses attach on another is worse than a name that is refused
everywhere. If pogod cannot bind an agent's attach socket at all, the spawn now
fails outright rather than returning a running agent that `pogo agent attach`
cannot reach.

Length is not the only constraint. An agent name is path-joined onto the socket
directory, the prompt directory (`<prompt dir>/<agent>`) and, for a polecat, its
worktree root — so a name must be a **single path component**: no `/` or `\`, not
`.` or `..`, and no control characters. `../x` would otherwise place all three
outside the directory meant to contain them. Names that merely *contain* dots are
fine (`pm..pogo`, `.hidden`); only a bare `.`/`..` or an embedded separator
traverses. Like the length ceiling, a bad name is rejected at spawn with HTTP 400.
