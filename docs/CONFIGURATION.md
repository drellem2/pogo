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

`sources` is the per-PM extension point: entries there are scanned *in addition
to* the baseline in `pm-template.md`, and the list is the source of truth — a
source in the list is scanned, one that is not is skipped. One entry has a spec
of its own, because it closes a gap the baseline cannot see: `"open-prs"`, the
open-PR pass — see [docs/pm-open-pr-pass.md](pm-open-pr-pass.md).

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

The coordinator role is called "mayor" by default, but the name is policy, not
mechanism — rename it with:

```toml
[agents]
coordinator = "boss"   # default "mayor"
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

**Naming the coordinator after a crew agent shadows that crew prompt.** A name
is one address, so `coordinator = "doctor"` makes `doctor` the coordinator, and
`~/.pogo/agents/crew/doctor.md` is then unreachable — nothing can start it. The
collision is not silent: prompt resolution logs which crew prompt the
coordinator name shadowed and which file won, so the fix (rename the crew
prompt, or pick a coordinator name no crew prompt uses) is in the log rather
than left to be discovered. If `~/.pogo/agents/mayor.md` is absent, the
coordinator falls through to `crew/<name>.md` rather than failing on a path
nobody configured, and a lookup that finds nothing names every path it tried
(mg-4469).

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

## The product SME

The gh-issue triage workflow has a consult step: before a triage worker
finalizes its recommendation, it mails the draft to a product subject-matter
expert and waits for a reply. That SME is named here, and **there is no default
name**:

```toml
[agents]
sme = "pm-yourproject"   # default "" — no SME, consult step omitted
```

Unset is the shipped state and it means the consult does not happen: the triage
prompt renders without the step, and the recommendation packet reports
`"sme_consulted": false`. That is a stated absence, not a skipped obligation.

**Why there is no fallback name.** `sme` is a mail target, and mail is where a
wrong name hides. `mg mail send` creates a maildir for an unrecognized recipient
rather than refusing, so a consult addressed to an agent that does not exist
succeeds, is never read, and cannot be told apart from one that was answered.
This setting once *was* a hard-coded `pm-pogo` inside the shipped prompt
(mg-f04b) — one deployment's PM — so every other install's triage worker mailed
that void and then held for two hours waiting for the reply. An empty default
cannot make that mistake; a guessed one can only make it quietly.

## The escalation mailbox

Four watchers — `ackwatch`, `deafwatch`, `ghintake`, `ghteardown` — first mail a
fleet agent, and then, once a finding has persisted long enough that the fleet
has demonstrably not cleared it, **also** mail a box where a person will see it.
That second recipient is named here:

```toml
[agents]
escalation_box = "operator"   # default "human"
```

The default is `human` — the same box the whole fleet already writes — so an
install that has never heard of this setting escalates exactly where it always
did. **Most deployments should leave it alone.**

**When to change it: you have put a RELAY in front of `human`.** pogo supports a
*representative* pattern (designed in mg-b17b, built in mg-65d2) in which a crew
agent owns `human` as its inbox, reads it, and rewrites what matters into a
separate terminal box that the desktop notifier polls. The point of that
inversion is that it moves two READERS instead of twenty-one writers: every
`mg mail send human` in the fleet — and every one that does not exist yet —
stays correct, and no prompt or watcher needs re-pointing.

Escalation is the one exception, and it is not a matter of taste. Once a
representative owns `human`, an escalation reading *"the representative is deaf"*
is delivered into the inbox of the agent it is reporting as unable to read its
inbox. The bypass has to be **structural** rather than a timeout, because a
wedged relay noticing anything is precisely what cannot be relied on. Set this
to the relay's OUTPUT box and the loop cannot form.

**Point it at a terminal box** — one no agent reads as its inbox. Nothing
validates that; a mailbox is created on first delivery, so a name that is merely
plausible is delivered to, filed, and never read, and no instrument tells that
apart from a working channel (mg-f04b).

**It is one setting and not four.** "Which box does a person actually read" is a
fact about the deployment, not a per-watcher preference, and four knobs that must
agree are four knobs that can disagree — mg-b201 is the incident where three
artifacts declaring one schedule drifted apart. `pogod` resolves the value once
at startup and hands the same string to all four watchers; the log line for each
watcher prints its `escalate_to=` so the running value is observable without
reading config.

Note that a watcher drops the second recipient when it equals the first, so
setting this to a watcher's `notify_to` disables that watcher's escalation
rather than double-sending it.

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

`once` is the default, not the only right answer — which policy a cadence wants
(`once` for sweeps and mail-checks, `skip` for pollers, `count` for counted batch
jobs) is a per-cadence choice. That table, and the argument for why it belongs in
the agent layer rather than a generic replay engine, is §2 of
[design/sleep-resilience-design.md](design/sleep-resilience-design.md#2-replay-policy-per-cron-default-at-most-once-catch-up).

## Stall watcher

A passive watcher inside pogod that rides the heartbeat loop and nudges the
mayor when work piles up *behaviorally* — the mayor's process is healthy but
its loop has stopped draining work. Two thresholds: an `available` work item
the mayor owns (assigned to it, or unassigned) sitting unclaimed past an age
limit, and the mayor's `new/` maildir holding an over-age message or more than
a count ceiling. On a cross it sends one nudge per offending batch and appends a
`stall_watch_fired` event to `~/.pogo/events.log`; a **per-item** escalating
cooldown caps the nudge rate (see "Repeat suppression" below). Running in
pogod's *independent* heartbeat is the point — a
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
nudge_cooldown = "5m"                   # gap before the SECOND notice about an item
repeat_backoff_cap = "4h"               # ceiling on the doubling repeat backoff
mail_fallback_backlog_cap = 3           # consecutive mail fallbacks per recipient
                                        # before further ones are withheld
                                        # (negative disables the damping)

# Assignees that mean "do NOT dispatch this" — see "Ownership vs execution".
non_dispatchable_assignees = ["human", "parked"]  # everything else is watched

# Priority wake (gh #61): a high-priority available item skips the 10m gate.
priority_wake_enabled = true            # default true
high_priority_wake_delay = "30s"        # min age before a high-priority item wakes
high_priority_wake_cooldown = "3m"      # gap before the 2nd notice about an item
fast_priorities = ["high"]              # Priority values that trigger the wake
```

### Repeat suppression is per item, and it backs off

Both work-item cooldowns are keyed on the **(category, item)** pair, not on the
category. The first notice about an item is immediate; each repeat about that
*same* item waits twice as long as the last — 3m, 6m, 12m, 24m… — up to
`repeat_backoff_cap`. A heartbeat tick on which every offending item is still
inside its own backoff sends nothing at all, and each nudge names only the items
that are actually due, marking any it has raised before with `[repeat] … (notice
#N)`.

This matters because the coordinator legitimately *holds* items — behind its
polecat cap, a snooze, a sequencing decision. Keyed per category (as it was
before mg-1693) the cooldown suppressed repeats of a *kind of alert* rather than
repeats *about a given item*, which broke it in both directions:

- A held item was re-detected and re-notified every cooldown, indefinitely. On
  the night of 2026-07-30, `mg-61f4` drew 22 notices and `mg-0e24` 27 — 212
  item-notices across 29 items in four hours. **Detection was correct in every
  one of those fires**; only the repetition was wrong, which is why the fix is
  here and not in the detector. Making detection stricter would have suppressed
  true positives — and a correct detector with a broken repeat-suppressor is
  indistinguishable from an over-firing detector unless you count *per item*.
- A genuinely new item arriving mid-cooldown was silently swallowed by the held
  item's timer. That half was never in the noise complaint, and it was a miss,
  not a nuisance.

Repeats back off rather than stopping outright so a genuinely forgotten item
still resurfaces; at the 4h default a held item settles to roughly one notice
per four hours. Setting `repeat_backoff_cap` at or below a category's base
cooldown disables the escalation and leaves a flat per-item cooldown. The state
is in-process, so a pogod restart re-notifies each held item once — deliberate,
since a restart is also when the coordinator's own picture of what it was
holding is gone.

`stall_watch_fired` events carry `repeat_counts` (item → notice number),
`backoff_suppressed_ids`, and `next_backoff` so the repetition stays countable
per item without hand-correlating ids across fires.

### The mail fallback is damped per recipient

When the watched agent is running but too busy to go idle, the notice takes the
durable mail road instead of its terminal (mg-79dc). That fallback had no
damping term, and the direction it ran in was perverse: it fires *because* the
recipient is saturated, and it answers by adding work to that recipient's inbox.
The busier the coordinator, the more often the PTY refuses; the more it refuses,
the more stall-watch mails the agent it has just observed to be overloaded.

The `unread_mail` category closed the loop outright — its notice reads "your
inbox is too full" and arrives *as one more message in that inbox*, re-arming
its own trigger. Measured on one box over 20 000 events: 1814 stall fires took
the mail road, and the coordinator's maildir held 766 stall-watch messages, 742
of them fallbacks — the largest subject line in a 5978-message mailbox by nine
times — of which 179 were the self-referential unread-mail notice.

`mail_fallback_backlog_cap` (default 3) bounds it. It counts, per recipient,
consecutive fallbacks since the last **successful PTY delivery** — direct
evidence the agent went idle and can therefore drain. Past the cap further
fallbacks are withheld until that happens; the counter is not a measure of
inbox depth, so a coordinator's legitimate mail from other agents never silences
the watcher.

Withholding is not silence. The transition is logged loudly once per run, and
every suppressed fire stamps `nudge_suppressed_consecutive` into the
`stall_watch_fired` event — a value that keeps climbing means the coordinator
has not gone idle once across that whole run, which is a sharper signal than the
flood it replaces. A suppressed fire carries `nudge_delivery = "suppressed"` and
**no** `nudge_error`: nothing was delivered, but that was a decision rather than
a fault, and only the latter should read as an outage.

Nothing is lost to the deferral. Stall-watch re-derives every condition from
scratch each tick and never queues, so the moment the recipient becomes
reachable the *current* state fires, not a stale replay.

Two scope notes. The cap bounds the **fallback** road only — the offline road
(recipient not running) is undamped, because it has no reset signal and a cap
there would latch permanently the first time a coordinator went down. And a
recipient going offline clears its run, so a restarted coordinator is not
silenced by a run accumulated against the process that died.

On the 30s wait-idle budget this fallback hangs off: it is not worth
lengthening, and that is measured rather than assumed. Across 1702 recorded
fallbacks the gap since the coordinator's last PTY write *at the moment the
deadline expired* had a median of 218 ms and a p99 of 941 ms, against a 2 s idle
threshold — only 10 of 1702 had reached even one second. The coordinator is not
almost-quiet when the deadline fires; it is writing continuously. A longer
budget buys nothing and holds the heartbeat longer for it.

### Ownership vs execution — which items the work-item detectors watch

Both work-item detectors (Threshold A and the priority wake) watch **every**
available item **except** those whose assignee names a non-dispatchable executor
— by default `human` and `parked`. Ownership does not affect visibility: an item
owned by `pm-pogo`, by `pm-anyone-else`, or by nobody is watched identically.

That is because `assignee` carries two incompatible meanings:

| Value | Means | Coordinator should |
|---|---|---|
| `pm-pogo` (any agent) | **ownership** — who to ask about it | dispatch a worker |
| *(empty)* | unowned | dispatch a worker |
| `human` | **execution gate** — a person must do this by hand | never dispatch |
| `parked` | **execution gate** — deliberately set aside, nobody is expected to act on it now | never dispatch |
| `blocked:<agent>` | **execution gate** — waiting on a *named* agent (`blocked:mayor`, `blocked:daniel`) | never dispatch; ask that agent |

The last row is a **shape**, not a value in `non_dispatchable_assignees` — see
["blocked:&lt;agent&gt;"](#blockedagent--the-one-shape-the-gate-recognises) below.

`--assignee=human` is a *gate wearing an assignment's clothes*: `mayor.md` files
manual-QA items that way precisely so no worker is dispatched at them. So the
detectors test "is this gated?", never "is this assigned to the coordinator?".

**The gate is enforced at dispatch, not just at detection (mg-4798).**
`non_dispatchable_assignees` governs two different paths, and it is worth being
precise about which is which:

| Path | Question | Effect of a gated assignee |
|---|---|---|
| stall-watch (`internal/stallwatch`) | what to **watch** | no nudge about the item |
| `pogo agent spawn-polecat` (`internal/agent`) | what to **dispatch** | **the spawn is refused, HTTP 409** |

Until mg-4798 was built, only the first existed. The second was a *sentence in a
prompt template* — `mayor.md` asserted that a `--assignee=human` item "won't be
dispatched" — so the actual guarantee was that an agent had read and retained a
paragraph, and `pogo agent spawn-polecat --id <a human-assigned item>` would
spawn a worker without complaint. Both paths now read one predicate,
`config.IsDispatchGated`, so the vocabulary cannot mean one thing to the watcher
and another to the dispatcher.

The refusal is `409 Conflict`, deliberately **not** the retryable `503` the
redeploy drain uses: retrying an unchanged request will be refused identically
forever. Reassign the item or clear its assignee. The refusal is recorded as an
`agent_spawn_failed` event carrying the reason, so a gap in the spawn record can
be read back to its cause.

**What the dispatch gate does not do.** It **fails open** when it cannot answer:
no `--id` (which is optional by design), an id absent from the store, or an
unreadable store all dispatch normally, the last with a loud log line. It stops a
coordinator dispatching a gated item it read out of `available/` — the actual
failure mode — and is not a proof that no worker can reach gated work. Failing
closed was rejected because `--id` is optional and because one bad path in
macguffin would then halt the whole fleet.

**The gate's twin, which fails the other way (mg-9a04).** Sitting immediately
beside it in `handleSpawnPolecat` is the type→template router: with no
`--template`, the work item's `type` selects the worker template through a
**closed** map (`design` → `polecat-architect`, `qa` → `polecat-qa`), and any
other type — `scoping`, `audit`, `bug`, bare `task` — selects **none** and the
spawn is refused with the same `409`. That table had the same history as this
gate: it existed only as prose in `mayor.md`, so the routing guarantee was again
that an agent had read and retained a paragraph. The two directions differ
because the costs do: refusing a *dispatchable* item wastes work and can halt the
fleet on one bad file, while guessing a *template* wrong sends a design item to a
builder that implements it and merges it. Full rules in
[customizing.md](customizing.md#template-routing-is-closed).

**Why `parked` is a separate sentinel and not a convention about `human`
(mg-a3a2).** Until mg-a3a2, `human` was the only value that silenced these
detectors, so it accumulated three incompatible senses in one queue: *gated on
Daniel*, *parked, do not chase*, and *filed here because nothing else was
expressible*. Use `--assignee=parked` for the second.

This is not a discipline problem that a convention would fix. Two agents who
both understood the conflation misfiled items into `human` within a single
session, because the gate had exactly one expressible value. And the cost is not
confined to the misfiled rows: everything that reads `assignee` to decide what to
escalate — stall-watch, PM digests, mayor, architect — re-derives the same
conflation independently and *cannot see the error from the field*, because the
data does not record which sense was meant. Architect summarized the queue to
Daniel as "entirely gated on you" when most of it was parked fleet-internal work.
A convention about how to use `human` cannot be read back out of the data; a
distinct value can. `mg list --assignee=parked` is now an answerable question,
and `human` means "Daniel must decide" again — the only property that makes that
queue worth reading.

**What `parked` does not do.** It buys silence from the nudge channel, not
disappearance from listings (the `gh-open:` precedent, mg-6e57): a parked item
still shows up in `mg list` with its assignee and age. And every gate here is
**unconditional and permanent** — a gated item never ages and never re-alarms,
whatever sentinel it carries. That is correct for a detector whose job is
*dispatchable* work: aging gated items would re-alarm on exactly the things the
gate exists to silence. The aging belongs to the PM sweep, which reads the gated
queue anyway and can flag "gated N days" with no code change. Live example at
time of writing: `mg-0ffc` had been `available` and gated for eleven days, and
stall-watch is structurally incapable of noticing. Stated here so the gap has a
home rather than being assumed closed.

**Why a denylist of gates rather than an allowlist of agents.** Until mg-4bd4 the
predicate was `assignee == "" || assignee == "mayor"` — an allowlist of the
values a *dispatcher* carries, which skipped every item naming an *owner*. Since
`pm-template` files every ticket with `--assignee=pm-<name>`, that hid 13 of 14
available items on 2026-07-17. It was never silent — unassigned items fired
routinely, and both detectors fired 9 times that day — which is exactly why it
held confidence for so long: a detector watching a shrinking population looks
identical to a healthy queue.

An agent allowlist would have re-introduced the same bug on a timer, since it
must be edited every time an agent joins — and until someone noticed, the new
agent's work would be invisible exactly as `pm-pogo`'s was. The gate vocabulary
is closed instead of growing: it only changes if someone invents a second meaning
for "do not execute this automatically", and then it changes by a config line.

**The failure directions are asymmetric on purpose.** An unrecognized assignee is
watched. Guessing wrong costs one nudge about an item the coordinator cannot
dispatch — loud and self-correcting. The old default guessed the other way and
paid in silence, which is indistinguishable from having no stalls.

#### `blocked:<agent>` — the one shape the gate recognises

    mg edit <id> --assignee=blocked:mayor     # gates dispatch AND says who it waits on

`blocked:<agent>` is the only value the gate matches by **shape** rather than by
membership in `non_dispatchable_assignees`. It gates exactly as `human` and
`parked` do, and additionally records *who the item is waiting on* — so
`mg list --assignee=blocked:mayor` is an answerable question in the same way
`--assignee=parked` became one.

**Why a shape and not a third sentinel (mg-6fb0).** Within days of `parked`
shipping, three items were filed with an agent name as the assignee — `pm-pogo`
(mg-bb43), mg-779b, `mayor` (mg-bf5e) — each meaning *blocked pending this
agent*, and each correctly getting **no gate**: an item merely owned by mayor
*is* dispatchable. The gap was that "blocked on a named agent" could not be said
at all. A filer with that intent had to choose:

| what they wrote | what they kept | what they lost |
|---|---|---|
| `assignee=<agent>` | **who** | the gate |
| `assignee=parked` | the **gate** | who |

That is not a prediction — agents had already invented a channel for it. The
tags `blocked-on-daniel`, `blocked-on-daniel-confirm` and `blocked-on-redeploy`
exist in the store (mg-cf48, mg-e925, mg-a96c), and `mg archive` was taught to
respect them (mg-3c53). The intent was being expressed; the gate could not hear
it.

Three properties are deliberate:

- **One shape, not a roster.** `blocked:mayor`, `blocked:pm-anyone` and
  `blocked:an-agent-hired-next-year` all gate with no config line and no code
  change. An *allowlist of agents* would have to be edited every time the crew
  grows — that is mg-4bd4's defect, and this door does not reopen it.
- **Independent of the configured vocabulary.** Replacing
  `non_dispatchable_assignees` does not switch the shape off, because it is a
  structural rule about how the field is written rather than an entry in a
  denylist. A deployment that drops `parked` still gates `blocked:mayor`.
- **Additive; nothing was migrated.** `human` and `parked` read exactly as
  before. Measured before the change: zero of the eight then-`human` items
  carried a `blocked-on-*` tag, so any change that *stopped* reading `human`
  would have stranded all eight as dispatchable. There is no window in which the
  queue is unguarded.

A bare `blocked:` still gates — the author wrote "blocked", and declining to gate
on a truncated agent name would fail in the unsafe direction. The refusal says
the value names nobody and asks for it to be rewritten, rather than quietly
inventing an agent to blame.

**The gate reads `assignee`, and only `assignee`.** The `blocked-on-*` tags stay
what they are — human-facing markers — and are **never** consulted for gating.
Moving the gate onto tags would split it across two channels and forfeit the
property that makes the field worth reading: `mg list --assignee=…` is the single
answerable question about whether an item is dispatchable.

**The block-intent advisory.** Because the old tag idiom still exists, both paths
say so when an item *declares* a block in its tags while its assignee leaves it
dispatchable:

- stall-watch appends `[block-intent] mg-xxxx is tagged blocked-on-daniel but its
  assignee does not gate dispatch — if it is genuinely waiting, set
  --assignee=blocked:daniel` to the nudge it was already sending, and stamps
  `block_intent_mismatch_ids` into the event.
- `spawn-polecat` logs the same contradiction at the dispatch point and
  **dispatches anyway**.

It is advice, not a gate — a tag is not a gate, and making it one would be the
two-channel split just rejected. It fires only on the contradiction (declared
block + dispatchable assignee), never on ordinary ownership: `pm-template` files
every ticket with `--assignee=pm-<name>`, so an advisory on any named assignee
would ride on nearly every nudge and be trained away within a day. A
`blocked-on-mg-1234` tag names another *work item*, so the advice there points at
`mg new --depends`, not at the assignee field.

### Priority wake

Threshold A treats every unclaimed item the same — it waits out the full
`unclaimed_item_age_threshold` (10m) regardless of priority. That is the wrong
latency for urgent work: when the coordinator is idle and has backed its polling
off, a `priority = high` item with no accompanying mail could sit up to ~30
minutes before pickup (gh drellem2/pogo #61).

The priority wake is a priority-aware branch on the *same* 30s available/ scan.
An item that is **ready** (deps met — it is in `available/`, not `pending/`),
**awaiting the watched agent's dispatch** (see "Ownership vs execution" below),
and carries a priority in `fast_priorities` bypasses the 10m gate and is delivered after only
`high_priority_wake_delay` — via the **same wait-idle nudge**, so a busy agent is
never interrupted (the write lands at its next turn boundary) and an idle agent
is woken at once. A dedicated `high_priority_wake_cooldown` keeps an item that
stays available (e.g. it can't be dispatched yet) from re-nudging every tick —
per item and escalating, so it also cannot re-nudge every cooldown forever (see
"Repeat suppression is per item"); blocked (`pending/`) and already-claimed
(`claimed/`) items are never scanned, so they cannot loop-nudge either. When the wake is disabled, high-priority items
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

## Dispatch cap — how many workers may enter ONE repository

**On by default, everywhere.** Unlike `[dispatch_pairing]` below, this section
carries platform behaviour rather than one deployment's policy: what it prevents
is a property of running a shared test suite concurrently, which every consumer
with a repo and more than one worker has.

```toml
[dispatch]
# Most workers that may be live in ONE repository at once.
# 0 = unlimited (the gate is disarmed). Default: 3.
max_polecats_per_repo = 3

# Slots withheld from worker dispatch while the refinery has a merge request
# for that repo — IN FLIGHT OR QUEUED. 0 = no reservation. Default: 1.
refinery_reserve = 1
```

`pogo agent spawn-polecat --repo=<path>` refuses with **503** when the repo is
full. That is a **later, not a no**: nothing about the work item is wrong, and
the identical request succeeds once a worker there finishes. It is also
**repo-scoped** — a dispatch into a different repo is unaffected.

Read the count the daemon will enforce on:

```bash
pogo host load --repo=/Users/daniel/dev/pogo
```

### Why per-repo and not per-fleet

The shipped rule counted the fleet: *"a reasonable limit is 3-5 concurrent
workers"*. mg-3977 measured why the fleet is the wrong denominator. Seven
workers went into one Go repository on 2026-08-05; the 10-core host reached a
1-minute load average of **337**, commands began timing out, and the refinery
stopped starting gates — three merges sat queued **24+ minutes** without one
beginning.

Five workers across five repositories would have been fine. Five in one Go
repository is not: every worker verifies itself by running *that repo's* test
suite, and `go test ./...` parallelises across packages on its own. The unit of
contention is the repository.

### Why this is not the host load gate

`[dispatch]` and the host load gate (`pogo host load`) are complementary and
neither subsumes the other. The load gate refuses when the fleet is already
holding most of the host's CPU — a *measurement of load that has already
arrived*. Seven concurrent `go test ./...` runs do not saturate the host at the
moment the seventh is dispatched; they saturate it a minute later, when all
seven reach their compile phase together. A count is available at dispatch time
and a sample of the consequence is not.

### Why the refinery gets a reserved slot

The incident was self-defeating rather than merely slow: **the refinery runs the
same `./build.sh` the workers run**, so the workers verifying their branches
starved the one process that could merge them. A gate that does start under that
load can also fail with `signal: terminated`, which reads exactly like a real
verification failure on somebody else's branch (observed 2026-07-31, mg-069f).

**Queued merges reserve, not only in-flight ones.** Reserving only while a gate
is running would be close to useless — the starvation is built by workers
dispatched *before* the gate starts, and by then they cannot be taken back.

**The reserve can never take the last slot.** A `refinery_reserve` greater than
or equal to `max_polecats_per_repo` floors the effective cap at 1 rather than
refusing everything: on a repo whose merge queue is almost never empty, refusing
all dispatch would be a wedge with no way out but a config edit.

**What it cannot do.** It is enforced on the dispatch side only, because that is
the only side that can be told "not yet" — the refinery cannot evict a worker
that is already building. This prevents the starvation from forming; it does not
cure one that has already formed.

### What the count is built from, and where it can be wrong

| Source | Covers | Note |
|---|---|---|
| pogod's in-memory registry | workers this daemon spawned | EMPTY after a restart, permanently — the registry has no adopt path (mg-13a3) |
| the persisted polecat witness | workers that outlived an earlier pogod | records written before mg-3977 carry no repo |

The two are unioned and deduplicated by name. Both failure directions **fail
open**: an unreadable witness store dispatches anyway (and says the count may be
missing survivors), and a refinery that cannot be asked reserves nothing.
Refusing on missing information would halt dispatch into every repo for a reason
the caller cannot check or clear.

Live workers whose repository is unknown — pre-mg-3977 witness records, and
`--no-worktree` polecats, which have no repo at all — are **reported but not
counted**. Attributing one to a repo on a guess would refuse a correct dispatch;
hiding it would let an undercount look exact.

A `--no-worktree` dispatch is never capped: it runs no repository's test suite.

Source of truth: `internal/config/dispatchcap.go` and
`internal/agent/dispatchrepocap.go`.

## Dispatch pairing — items that owe a paired work item

**Off by default, everywhere.** `[dispatch_pairing]` is empty unless a
deployment fills it in, and an empty repo list means the gate never fires. This
section is a *mechanism*; the policy it carries is one deployment's business.

The mechanism: **an item in a declared repository may not be dispatched until a
second work item exists that references it and carries a declared marker tag.**
`pogo agent spawn-polecat` refuses with **409** and a message naming the item,
the repo that put it in scope, how to file the pair, and how to waive.

```toml
[dispatch_pairing]
# Repos whose items owe a pair. Empty (the default) = the gate is inert.
# A path covers itself and everything beneath it, on path boundaries — so
# /r/prog does NOT cover /r/prog_v2.
repos = ["/Users/daniel/research/onethird_program"]

# Which items in those repos owe one. EMPTY MEANS EVERY ITEM — see below.
require_tags = []

# The marker a paired item carries. Required: a rule with no pair_tags can
# never be satisfied, so it is reported and NOT enforced rather than
# deadlocking the repo.
pair_tags = ["independent-audit"]

# The visible opt-out. An item carrying one of these dispatches. Empty means
# the repo has no opt-out at all.
waiver_tags = ["audit-waived"]
```

### Why the obligation is default-on inside a covered repo

`require_tags = []` means *every* item in the repo owes a pair. That is
deliberate. The rule this was built for — a research ticket owes a pre-filed
independent audit — existed in writing, was agreed by everyone involved, and was
missed **twice in two days** anyway. An over-filed pair costs one `mg shelve`; a
missed one reached the program's state document both times. Requiring a positive
act to *create* the obligation is the thing that failed, so the obligation exists
by default and the **opt-out** is the positive act.

Set `require_tags` if you want the narrower repo+tag shape instead.

### Why it routes on `repo`, and not on who filed the item

The rule failed twice because it lived in one agent's **filing checklist**: any
ticket that agent did not file bypassed it silently, by construction. A guard
owned by one filer cannot catch a filing that filer did not do. Dispatch is the
one place every item passes through before any worker touches it.

A filer-set marker (the `qa:` field shape) was considered and rejected for this
gate — not because filers are unreliable, but because a marker still requires
**someone to remember something at filing time**, which is the dependency being
removed. `repo:` is written by `mg` from the filing context; the gate reads it,
and nobody has to remember anything.

Note for anyone extending this: routing on *who filed* an item is not merely
undesirable, it is **unimplementable here**. Every agent on the host runs as one
unix identity and `creator` records that identity, so authorship is not
recoverable from the artifacts.

### The two failure directions

| Situation | Direction | Why |
|---|---|---|
| No `--id`, item not in the store, store unreadable, `[dispatch_pairing]` empty | **Open** — dispatches | `--id` is optional by design; a gate that halted the whole fleet over one repo's policy is worse than the miss |
| Item **is** covered, and the store scan then fails | **Closed** — refuses | "I could not check" is not "there is nothing to check". Blast radius is one repository |

### What it does not cover

- **A spawn with no `--id`.** Nothing links it to a work item, so there is no
  repo to route on.
- **Work that never passes through dispatch.** A deliverable produced by an agent
  working directly, or by a human, reaches the repo without a spawn. No gate at
  dispatch can see it — that half belongs to the owning PM's sweep, and the two
  halves are independent on purpose.
- **Quality.** It checks that a pair *exists*, never that it is any good. A pair
  filed to satisfy the gate and then shelved unread satisfies it.
- **Shelved and archived items** are not scanned as candidate pairs; a shelved
  pair is a dropped obligation, not a discharged one. `pending/` **is** scanned
  — see below.

### Which statuses count as a filed pair

`available`, `claimed`, `done` and **`pending`**.

`pending` is the load-bearing one. The canonical pair is filed with
`depends: [<target>]`, and `mg` parks an item whose depends are unmet in
`pending/` until `mg schedule` promotes it. The target of a pairing obligation is
by definition not done — it has not been dispatched yet — so **a correctly
pre-filed pair sits in `pending/` for exactly the window this gate runs in.** A
scan that skipped it read *pre-filed* as *never filed* and refused the very
dispatches the gate was built to permit.

`shelved/` and `archive/` stay out, and the asymmetry is the point: pending is a
pair waiting its turn, shelved is a pair somebody dropped. If shelving counted, an
obligation could be discharged by abandoning it.

### The escape hatch: `--pairing-override`

```bash
pogo agent spawn-polecat cat-1234 --id mg-1234 \
    --pairing-override="the audit is mg-9999, filed under a tag this config does not name"
```

A refusal with no override becomes a **wedge** the first time the marker is wrong
— a repo named too broadly, a pair filed under a tag `pair_tags` does not list —
and a wedge under time pressure gets resolved by disarming the gate. A cheap,
loud override is what keeps the gate armed.

- **It is a string, not a boolean.** A bare `--force` records that someone
  overrode the gate and loses the only thing a later reader needs: what they knew
  that the gate did not. An empty or whitespace value is not an override.
- **It is recorded.** Each use emits a `dispatch_pairing_overridden` event
  carrying the item, the stated reason, **and the bypassed refusal verbatim**.
  Those answer different questions — the reason is what the operator believed,
  the refusal is what the gate objected to, and only both together distinguish a
  config bug from an unaudited deliverable that shipped anyway.
- **It overrides this gate only.** The assignee gate, the type→template map, the
  drain gate and the load gate are untouched.

`waiver_tags` remains the other opt-out and they are not interchangeable: a
waiver tag says *this item never owed a pair* and lives on the item permanently;
an override says *this item owes one and I am dispatching anyway*, and lives in
the event log. Note that `waiver_tags` is itself optional, so a deployment that
sets `repos` without it has **no** item-side opt-out — `--pairing-override` is
then the only way out, which is why it does not depend on configuration.

### Choosing `pair_tags`

The tag does double duty: it marks a paired item, **and** it exempts an item from
owing one (otherwise an audit ticket filed into a covered repo would owe its own
audit forever). So it must be a *tight* marker. If a program uses its pair tag
loosely — `audit` on anything that touches the audit process — every
loosely-tagged item becomes exempt and the gate goes quiet exactly where the
program is busiest.

A pair is recognised by either reference channel the store already uses:
`depends: [mg-1234]`, or a tag containing the id (`mg-1234-followup`).

### Before you arm it: measure the refusal rate on your own store

Everything above describes a mechanism that works. Whether *your* `pair_tags`
match *your* store is a separate question, and it is the one that decides whether
arming this helps. Answer it with a number before you write the config, not
after — the gate goes live at the next daemon start, unattended, and the first
thing a mis-set marker produces is a wall of refusals aimed at the people already
filing pairs.

The measurement is a dry run of the gate's own predicate over two populations:

- **Completed dispatches** (`done/`, covered repo, not themselves pairs) — how
  many *would have been refused* had the gate been armed. This is the retrospective
  false-refusal rate.
- **The dispatchable backlog** (`available/` + `claimed/`) — how many refuse on
  the next dispatch.

For each item, ask what the gate asks: does any item in `available`, `claimed`,
`done` or `pending` carry a `pair_tags` tag **and** reference this item by
`depends:` or by a tag containing its id?

A high number is not automatically a veto — it can equally mean the discipline
lapsed and the gate would be corrective — but it is never a detail. Distinguish
the two by checking whether the pairs exist somewhere the gate cannot see
(`shelved/`, `archive/`) versus not existing at all, and by checking whether the
pair-tag convention is still in current use rather than historical.

Worked example, mg-bd92 on this deployment's store, `pair_tags =
["independent-audit"]`, `require_tags = []`:

| Population | n | owed | pair found | would refuse |
|---|---|---|---|---|
| Completed onethird dispatches | 47 | 43 | 6 | **37 (86%)** |
| Dispatchable onethird backlog | 40 | 20 | 3 | **17 (85%)** |

The pairs were not merely invisible — widening the scan to `shelved/` and
`archive/` recovered **zero** of the 37, so archive-blindness was not the cause.
The convention had drifted: recent audits carried the loose `audit` tag and often
no reference to their target at all, and no `require_tags` subset brought the
rate below 40%. That deployment was left **unarmed** on the strength of the
number, with the config recorded rather than written.

Source of truth: `internal/config/dispatchpairing.go` (policy vocabulary and
predicates) and `internal/agent/dispatchpairing.go` (the gate).

## Audit successors — merged audits that nothing answered

**Off by default, everywhere.** `[audit_successor]` is empty unless a deployment
fills it in, and an empty repo list means the detector examines nothing.

**This is a DETECTOR, not a gate.** It refuses nothing, blocks nothing, and has
no caller at dispatch. It reports on one line of `pogo doctor --check`, and the
line is a `warn` — never a `fail`, so it never changes doctor's exit code.

### What it covers, and why it is the shape it is

`[dispatch_pairing]` above enforces that a paired audit **exists at dispatch**,
and states its own limit: *it checks that a pair exists, never that it is any
good; an audit filed to satisfy the gate and then shelved unread satisfies it.*

That residue was covered by one agent remembering to read each audit as it
merged. On 2026-07-30 that produced three successor tickets filed by hand — every
one of which would otherwise have existed only in a commit message, where nothing
acts on it.

**The rule: an audit that MERGES with no successor inside a bounded window is a
computable failure.** A successor is another work item that references the audit
(`depends: [mg-1234]`, or a tag containing the id), or a clean verdict recorded
on the audit itself. It needs nobody to remember, and it fires on exactly the
case the gate cannot see.

It is a detector rather than a gate deliberately. A gate on this event would have
to decide whether an audit's findings **warrant** a successor, which is a
judgement and not mechanically decidable. What *is* decidable is that nothing
happened at all. Do not grow it into a refusal.

```toml
[audit_successor]
# Repos whose merged audits are checked. Empty (the default) = inert.
# Same path matching as [dispatch_pairing]: covers itself and everything
# beneath it, on path boundaries.
repos = ["/Users/daniel/research/onethird_program"]

# What marks an item whose deliverable is FINDINGS. Required — empty means
# inert, because with no marker every merged item in the repo would read as an
# audit owing a successor. Use the same tight marker as pair_tags.
audit_tags = ["independent-audit"]

# Carried BY THE AUDIT to record that it found nothing to repair. Optional;
# empty means a successor ticket is the only way to answer an audit.
clean_verdict_tags = ["audit-clean"]

# Grace period after the merge. Default 4h — see the calibration below.
window = "4h"
```

### The window, and what it was calibrated against

A window is a judgement, and an uncalibrated one is a number somebody invented.
This one was measured against **2026-07-30 in the onethird program**, whose store
held 30 items tagged `independent-audit`, 27 of them merged. Merge time was taken
from each item's `<id>.result.json` mtime, successor time from the successor's
`created:`:

| | |
|---|---|
| Merged audits examined | 27 |
| Produced a successor | 23 |
| First successor within 20 min | 21 of those 23 |
| Two slowest | 2h04m (mg-6653), 2h05m (mg-f7bc) |
| Produced nothing | 4 |

**4h is a little under twice the slowest successor actually observed.** That
leaves room for a lag half again as bad as the worst real one before a healthy
audit is reported, and still fires inside the working day the audit merged in —
which matters, because the person who can act on it is the one who still
remembers the audit.

That day is a usable positive control precisely because it is dense: audits
merged roughly hourly and repairs followed within minutes, so a silent one stands
out against the day's own pattern rather than against a threshold picked from
nothing. `TestCalibration_2026_07_30` replays every row above and fails if the
window drops below an observed healthy lag. Recalibrate against a comparable day
if the cadence changes; do not tune this to make a report go away.

### Two limits, stated rather than left to be discovered

1. **A recorded clean verdict is an artifact anyone can produce cheaply.**
   Tagging an unread audit `audit-clean` silences this detector exactly as filing
   an unread audit satisfies the pairing gate. This moves the proxy one step
   closer to *merged-and-acted-on* without reaching it. The `doctor` line says so
   on its face, so a reader does not have to rediscover it. The step is worth
   taking only because a successor ticket cannot express a genuinely clean audit
   at all.
2. **It detects after the fact rather than preventing.** By the time it fires the
   audit has merged and the window has elapsed. It buys nothing at the moment of
   the merge; it buys that the omission is findable afterwards by someone who was
   not watching. That is the right shape for a failure mode whose symptom is
   silence.

### It converts a filing race into a fallback

Two duties bound to two events seconds apart — a dispatcher's successor duty at
AUDIT-MERGE, a product owner's pre-file duty at FILE — and on 2026-07-30 both
fired and produced duplicate tickets. **The agreed line is that the product owner
files the repair, the dispatcher does not, and this detector observes the
silence.** Neither party has to be fast.

**What that line costs, and this detector does NOT cover:** the non-filer is
often the second reader and may hold findings the owner's ticket lacks (this
happened — four findings, filed separately as mg-f8fa). This sees whether *a*
successor exists, never whether it carries everything the audit found. **Reading
the audit and mailing what you find stays human and stays explicit.**

### What else it does not cover

- **Non-audit items.** Deliberately not widened: the signal is specific to a
  ticket whose deliverable is findings, and an untagged repo-wide check would
  report most of the repo as failing.
- **Whether a successor is any good.** Same class of residue the pairing gate
  has, one level along.
- **Audits with no recorded completion time.** `mg done` *renames* the item file,
  and a rename preserves mtime, so a done item's own mtime is its **filing**
  time. The merge instant comes from the sibling `<id>.result.json`; an item
  without one cannot be aged and is counted as `no recorded completion time`
  rather than folded into either verdict.
- **Shelved successors.** A shelved successor is a dropped one — same asymmetry
  the pairing gate draws. `pending/` counts: a successor parked behind an unmet
  `depends:` is filed and will run.

### Where it reports

One line on `pogo doctor --check`, named `audit successors`:

```
! audit successors  2 merged audit(s) answered by NOTHING after 4h: mg-f1b2
  (silent 11h17m, merged 2026-07-30 06:43Z), mg-3c24 (silent 10h39m, merged
  2026-07-30 07:21Z). Read each one and file a repair ticket referencing it …
  27 merged audit(s) examined: 23 answered by a successor, 0 by a recorded
  clean verdict, 2 still inside the 4h window, 0 with no recorded completion
  time
```

A checklist someone reads on purpose, rather than a maildir already carrying
hundreds of unread notices. The line renders on **every** run, including when the
detector is unconfigured or could not read the store — in both cases it says so
outright, because a detector whose subject is silence must never report its own
silence as a clean result.

Source of truth: `internal/config/auditsuccessor.go` (policy vocabulary,
predicates and the calibrated window), `internal/auditwatch/` (the scan) and
`cmd/pogo/auditsuccessors.go` (the rendered line).

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

### The built-in drift-check runner

`check-drift` is only useful if something actually runs it. mg-5701 shipped the
detector with **no runner** — "a detector you have to remember to ask," the
guard-that-depends-on-memory class that already failed twice on pa. So pogod runs
it for you: on a **coarse** interval, from the heartbeat `OnTick` loop, pogod
samples every `[reconcile]` mirror with the same `CheckDrift` the CLI uses and
**mails `human`** naming any drifted artifact (`internal/driftwatch`, mg-345b).

This is the **detection backstop** for the four deploy paths the refinery
`[deploy]` prevention (deploy-at-merge) does not cover (mg-75f9): a
`probeAlreadyMerged` early-return that resolves as merged but *skips* deploy, a
`deploy_command` that fails silently, a service that dies *after* a good deploy,
and any un-enrolled repo. Prevention keeps drift from opening; this catches it
when prevention was never in the path.

Three properties are deliberate and tested (`internal/driftwatch`):

- **Heartbeat, NOT launchd.** The nondemand-spawn wedge on this box (mg-50e0)
  means a launchd timer would silently never fire — the exact "inert while
  appearing correct" failure the detector exists to catch. The heartbeat already
  ticks ~30s and drives the reaper and stall-nudger; the runner rides it.
- **Report-only.** It mails; it **never** reconciles. An auto-fix loop fighting a
  genuinely-broken artifact is the unbounded-reaper failure shape. A human (or a
  deliberate `pogo service reconcile`) acts on the mail.
- **Coarse throttle.** It samples at most once per `interval` no matter how often
  the heartbeat ticks, which also rate-limits the mail: a persistent drift
  re-reports once per interval, never once per tick.

```toml
[drift_watch]
enabled = true       # default true; the mirror half is a no-op with no [reconcile] mirrors
interval = "15m"     # coarse sample/mail cadence (default 15m)
```

Source of truth: `internal/driftwatch/` (runner) and `internal/reconcile/`
(detector).

#### …and the revision-staleness check it also carries

The same runner answers a second question on the same coarse slot, and this one
is asked **positively**: *is the daemon running current code?* (mg-5bd2)

Every previous alarm on that question was indexed to the nightly deploy job's own
exit code, which answers a proxy — *did last night's job exit zero?* — and that
proxy goes dark exactly when the job stops running. On 2026-08-01..08-04 the job
never fired, so there was no exit code, so there was no alarm, and four silent
nights were indistinguishable from four healthy ones because health is also "no
alarm". By 2026-08-07 pogod was 85 commits behind `main`.

So the check does not consult the job at all. It reads the running process's own
`vcs.revision`/`vcs.time` build stamp and mails `human` when that **commit** is
older than `self_stale_after`. That fires under all three deploy failure modes —
the job failing loudly, the job never firing, and the job exiting 0 without the
new binary reaching the daemon — because all three produce the same observable:
the running revision stops advancing.

- **Armed with no configuration.** No repo, no network, no config key is needed
  for the verdict, because a detector that stays inert until somebody remembers
  to configure it is the failure this whole lineage keeps repeating. `enabled`
  is the off switch; a binary with no vcs stamp (any `go test` build) disarms
  itself and *says so once* rather than going quietly silent.
- **`vcs.time` is the COMMIT's time, not the build's** — and neither uptime nor a
  recent restart substitutes for it. On 2026-08-04 pogod restarted onto the same
  2026-07-30 binary; only the revision said which code was running.
- **The commits-behind number is context and never a gate.** `origin/main` is a
  remote-tracking ref that only a fetch refreshes, so suppressing on "0 behind"
  would go dark on an unfetched repo — the same proxy failure, one layer down.
  The cost is a false positive if `main` goes quiet longer than N while the
  daemon is genuinely current; that trade is deliberate and the right way round.
- **Bounded, because the condition lasts days.** At a 15-minute cadence an
  uncapped alarm would mail ~96 times a day. Notices double instead — at
  detection, +1d, +3d, +7d — and then stop for that revision. The `revision_stale`
  **event** is still emitted on every sample, so a spent mail budget never makes
  the condition invisible; see [event-log.md](event-log.md).

```toml
[drift_watch]
self_stale_after = "168h"          # N; default 7 days (see below)
self_repo = "~/dev/pogo"           # OPTIONAL; only adds the commits-behind number
```

**Why N = 7 days.** The deploy is nightly, so a healthy daemon sits on last
night's commit; seven days is seven consecutive missed deploys, well past any
single bad night or weekend. Against the incident that prompted it — a binary
built from a 2026-07-30 commit — it would have fired on 2026-08-06, a day before
a human noticed the gap by hand. It is not a threshold picked to be safely
un-fireable.

**Who is expected to act, and the honest caveat.** The notice goes to `human`,
the same maildir the five `pogo-deploy` REDs of this arc reached — all five
delivered, all five notified, and nothing acted on for a week. A sixth alarm on
that channel is only an improvement because of what differs: it fires on the
nights that produced *no* deploy mail at all, its subject states the consequence
with a number that **grows** (`pogod is running 8-day-old code — revision
d31297f4 (2026-07-30), 85 commits behind main`) rather than repeating a constant
string a filter can match, and it is capped so it cannot become background noise.
If it is nonetheless filtered the same way, that is a finding about the channel,
not about the detector — and the `revision_stale` event stream is the pull-side
answer that does not depend on anyone reading mail.

## The credential-expiry warner

The fleet's harness credential holds an OAuth refresh grant with a **hard 30-day
life that use does not extend**. When it lapses the fleet coasts on its last
8-hour access token and then stops. This has happened twice, and both times it
was noticed only after ~24h of destroyed output. Unlike the chronic rate/weekly/
spend limits, auth expiry is **periodic**, so it can be predicted rather than
merely detected: the expiry is a plain integer on local disk
(`refreshTokenExpiresAt`, in the `Claude Code-credentials` keychain item).

pogod reads it on a coarse heartbeat interval and mails `human` at **T−7d,
T−72h, T−24h and T−2h**, plus once on lapse. `pogo credential expiry` answers the
same question on demand. Both are **report-only, necessarily** — the fix is a
human running `/login`, and nothing here can re-mint a credential.

- **Only `refreshTokenExpiresAt` is predictive.** The 8-hour `expiresAt` is
  routinely in the past on a perfectly healthy machine, because the harness
  re-mints on demand without always rewriting the stored blob. Threshold-alerting
  on it would fire constantly and get the mechanism muted. It is reported for
  context only.
- **Unreadable is not healthy.** Three distinct outcomes: *present* warns on
  schedule; *absent* (no item, not macOS, no `security`) disarms silently in mail
  but **loudly in the log** plus a `cred_expiry_disarmed` event, so a sandbox
  stays quiet without ever claiming health; *unreadable* (present item, decode
  failure, timeout, or a moved harness schema) **mails**, throttled, to say the
  warning is blind. Collapsing the last case into "fine" is the absence-as-
  evidence error the check exists to avoid.
- **Escalation ratchets.** Tiers only deepen and each mails once, so a 15-minute
  cadence yields five mails per 30-day grant. A `/login` resets the ratchet so
  the next cycle escalates afresh.
- **Harness internals.** The keychain item name and JSON schema are observed
  values, not a pogo contract. The check probes, uses when present, and degrades
  as above when absent.
- **No token value** is ever read, echoed, logged, mailed or committed. The
  decoder has no field capable of holding one, and the raw blob is zeroed
  immediately after the two integers are extracted.

```toml
[cred_expiry]
enabled = true           # default true; self-disarms where there is no credential
interval = "15m"         # coarse sample cadence (default 15m)
blind_renotify = "24h"   # throttle on the "cannot read the credential" mail
```

Source of truth: `internal/credexpiry/`. Mechanism:
`docs/investigations/credential-expiry-mechanism-2026-07-23.md` (mg-ed45).
This complements, and does not replace, reactive detection — an early
**revocation** produces no warning here.

## The gh-issue teardown detector

The gh-issue workflow ends by closing the GitHub issue behind a carrier work
item. That last step can silently not run. mg-07ba reached `status=done,
stage: merge` with every promise in the thread fulfilled — but nobody closed
drellem2/pogo#89, and it sat OPEN for four days. Nothing noticed: from the
outside, a carrier that completed its teardown and one that skipped it are the
same three characters. The miss is an **absence**, and an absence emits nothing.

`pogo check-teardown` audits it on demand; pogod runs the same detector on a
coarse heartbeat interval and mails `human`. Both are **report-only** — neither
closes an issue nor comments, because posting on an external thread is
outward-facing and stays human-gated.

- **The predicate** is `workflow: gh-issue` + `status=done` + a `gh:` issue that
  is still open. Deliberately NOT gated on `stage:`, which is not reliably
  maintained on live carriers.
- **Issue state is a tri-state**: open, closed, or **unknown**. A `gh` call that
  fails — expired auth, rate limit, renamed repo, transferred or deleted issue —
  produces no "OPEN" token, so a parse that reads "not open" as closed would
  report every carrier clean at exactly the moment the detector went blind. Only
  a positive, parsed `CLOSED` clears a carrier; everything else is reported and
  counts as actionable.
- **A failure to measure is not a measurement (mg-dd22).** Keeping "unknown" out
  of "closed" was necessary and not sufficient. On 2026-08-04 one network blip
  made all 12 carriers in a batch report `indeterminate` — technically correct
  and useless: 6 were clean, 6 were real teardown misses, and the report looked
  like a completed scan. It recurred 13-for-13 fifteen hours later. Two changes:

  - **Network-class failures are retried** with doubling backoff (3 attempts,
    2s then 4s) before anything is reported. **Only** network-class: auth, rate
    limits and unclassifiable failures are repeatable, so re-running them
    reproduces the same error while spending the sample window.
  - **A non-answer is reported in a different shape from an answer.**
    `indeterminate` now means the lookup *worked* and its answer is unusable — a
    determination about the carrier. **`not checked`** means the lookup failed
    and the carrier was never audited — a fact about us. `pogo check-teardown`
    prints them under separate headings, `--json` carries `not_checked` and a
    `class` on every finding (`network`, `auth`, `rate_limit`, `subject`,
    `unclassified`), and the event log gains `blocked_count` and
    `failure_classes`. Today's network outage no longer has to be
    hand-separated from mg-03ea's auth gap by reading `gh`'s error prose.

- **An all-blind run is reported as a broken instrument, not as a result
  (mg-dd22).** When *no* scanned carrier reaches a verdict, the report leads with
  `SUSPECTED INSTRUMENT FAILURE`, the mail subject says the run measured nothing
  instead of carrying a count that reads like a finding, `instrument_failure` is
  set on the event, and `pogo check-teardown` exits **3** rather than 1 — so a
  schedule can tell "the detector found something" from "the detector could not
  run" without parsing the report. N carriers all failing at once is not what N
  broken carriers look like. It takes 2 scanned carriers to make the claim: from
  a sample of one, a blind run and a blind carrier are the same observation.

  A blind run also **does not touch the escalation clocks** below. It observed
  nothing, so it is no evidence that a miss cleared — and on a box whose network
  is ~50% intermittent (mg-0ffc), letting a blip reset those clocks would be a
  standing mechanism for keeping a forgotten finding forgotten.
- **Open on purpose.** A carrier whose issue is legitimately open (waiting on a
  reporter, say) declares it in the carrier body:

  ```
  gh-open: waiting on reporter for a format-patch — closing would retract the ask
  ```

  It then stops counting as a miss, but stays LISTED under "declared open".
  Suppression buys silence from the alert channel, not invisibility: a
  declaration that outlives its reason is the same silent absence the detector
  exists to catch. Nothing infers this line — a human writes it, so an
  un-annotated carrier always fails toward being noticed.
- **Scope.** Scans `status=done` by default. Archived carriers need
  `--archived`: each carrier costs a network round-trip, and the store holds
  ~80 archived carriers against a handful of done ones. That is a real coverage
  gap, stated rather than hidden — a carrier archived while its issue is still
  open is the most thoroughly forgotten case of all.
- **Notification policy.** A changed set of findings mails immediately; an
  unchanged set stays quiet until `renotify_after`. Neither extreme is safe:
  mailing every interval trains a human to filter the sender, but going
  permanently quiet after one notice is how #89 stayed open for four days.
- **Routing (mg-b586).** Findings go to `notify_to`, a **fleet** mailbox
  (the coordinator by default) rather than `human`. The finding is "our gh-issue workflow's last
  step did not run on carrier X" — a workflow failure the fleet chases, not a
  decision a human can action better than the fleet can. The same reasoning that
  set the cadence sets the recipient: a human mailed operational work he can
  only forward back learns to filter the sender exactly as surely as one mailed
  too often. It also keeps the mail contract intact — `human` gets urgent items
  and one batched daily digest, and this class belongs in the digest.
- **Escalation.** Once a **single** finding has persisted unbroken for
  `escalate_after` (72h), the notice also copies `human`. At that point the news
  is no longer the teardown miss but "the fleet is not clearing it", and that
  one *is* a human's to know. Escalation copies rather than redirects — the
  fleet still owns the remedy — and ages each finding separately, so a new miss
  arriving cannot reset an older one's clock. The clock is in memory, so a pogod
  restart restarts it; the daily fleet notice is unaffected. Disable escalation
  with a negative duration (`escalate_after = "-1s"`); zero means "unset, use
  the default".
- **Arming.** The runner is skipped entirely when `gh` is not on PATH. Without
  it every lookup is indeterminate, and reporting an environment gap as a wall
  of findings would get the detector muted before the run that matters.
- **Authentication (mg-03ea).** Being on PATH is not enough — a `gh` that runs
  but cannot authenticate also returns indeterminate for every carrier. launchd
  execs pogod directly, without a shell, so the daemon inherits an environment
  with no `GH_TOKEN` and every lookup failed with "populate the GH_TOKEN
  environment variable". `internal/ghtoken` repairs this at pogod startup, and
  `pogo check-teardown` calls it too so the CLI works from cron as well as from
  a terminal: when the environment has no token, a **user shell** is asked for
  one (`zsh -c` sources `~/.zshenv` on every invocation, so the secret stays
  where it already lives). The token is never written to a plist, a log, or an
  error message — pogod logs only *where* the token came from. Sibling of
  `internal/pathenv`: that one fixes children that cannot be **found** under
  launchd, this one fixes children that run and cannot **authenticate**. The
  value is read once at startup, so a rotated token needs a pogod restart; the
  failure mode is a return to indeterminate, which is reported, never mistaken
  for closed.

  Because every unit test in the package injects its lookup, they all pass just
  as happily when the real `gh` is unauthenticated. The guard against a silent
  re-break is therefore a **live control** that calls the real `gh` under a
  reproduction of launchd's minimal environment, against two issues whose state
  is externally known, and keeps the *failing* arm permanently:

  ```
  POGO_GH_TEARDOWN_CONTROL=1 go test ./internal/ghteardown/ -run TeardownTokenControl -v
  ```

  The raw arm must report `89=indeterminate 91=indeterminate` (the bug, still
  reproducible without the repair) and the repaired arm `89=closed 91=miss`. It
  needs network and a credential, so it is opt-in rather than part of
  `./test.sh`. A detector that only ever returns indeterminate must not be
  trusted as passing — that is what this control exists to make impossible.

```toml
[gh_teardown]
enabled = true             # default true; skipped when `gh` is unavailable
interval = "1h"            # coarse sample cadence (default 1h)
renotify_after = "24h"     # unchanged findings re-mail after this (default 24h)
notify_to = "mayor"        # mailbox findings go to (default mayor, a FLEET box —
                           # name a PM here if one owns the gh-issue workflow)
escalate_after = "72h"     # one unresolved finding also copies `human` after this
                           # (default 72h; negative disables, zero means default)
```

Exit status of `pogo check-teardown` is **0** when nothing is actionable, **1**
when anything is found, and **3** when the run reached no verdict at all, so a
schedule or CI step can gate on findings without treating a blind run as a clean
one — or as a finding.

Source of truth: `internal/ghteardown/`.

## The gh-issue intake detector

The sibling of the teardown detector at the **other end** of the same workflow.
That one catches a carrier that finished while its issue stayed open; this one
catches an issue that never got a carrier at all.

A delivered `[gh]` mail can be dropped with nothing noticing. drellem2/pogo#99
was filed 2026-07-29 at 18:53:58Z; the poller mailed the coordinator 46 seconds
later, and again 20 minutes after that when Daniel commented. Both mails were
delivered. Neither produced a work item, and the issue went **~10 hours** with no
carrier — invisible to `mg list`, to `mg list --tag=gh-issue`, and to every other
board the fleet reads. Its paired issue #100, filed 19 minutes later, *was*
carried, so a pair filed to be considered together got split and the untracked
half went dark. It surfaced only because a PM ran an open-issue sweep by hand,
early, on a hunch.

Neither the poller nor mail delivery failed. What failed was follow-through, and
the coordinator prompt already names the failure mode and prescribes the fix
(`mg mail read` marks a message read immediately, so act-then-mark plus an
end-of-turn unread check). **Prescribing it was not sufficient — there was no
detector, only an instruction.** The set difference between open issues and
carried refs is trivially computable and nothing computed it.

`pogo check-intake` runs it on demand; pogod runs the same detector on a coarse
heartbeat interval. Both are **report-only** — neither files a work item nor
comments on an issue, because what an issue *is* (triage, duplicate, out of
scope, a question) is a judgement that stays with the coordinator.

- **The predicate** is the `gh:` **body marker**, at any work-item status
  including archived and shelved. Deliberately not the `gh-issue` tag (a carrier
  filed without the tag is still a carrier, and treating it as absent produces a
  finding nobody can clear) and deliberately not a title match (titles drift; the
  marker is a declaration, and it is the state carrier the playbook defines). For
  intake the question is only "does a carrier exist at all", so an archived
  carrier answers it yes.
- **A ref counts only on a structural line**: one starting with `gh:`, outside
  blockquotes and outside fenced code blocks. Prose citing an issue does not make
  an item its carrier — mg-039b's own body quotes the marker syntax and cites #99
  repeatedly, and a loose parse would have let the ticket silence its own
  positive control.
- **Both blind spots are named outcomes, not silences.** A failed `gh issue list`
  yields no issues, which folded into the scan would read as "no open issues,
  nothing uncarried, all clear" — so an unreadable repo is reported as a finding.
  A carrier scan that examined **zero** work items yields no carriers, which
  folded in would read as "every open issue is uncarried" — a wall of findings
  that is entirely an artefact of the scan, so it is reported as a **blind scan**
  instead. Opposite shapes (one silently clean, one loudly wrong), identical
  consequence: the detector stops being trusted.
- **Grace window.** An issue younger than `grace` (30m) is listed as **fresh**
  but never alarmed. The poller mails within ~60s of an issue being filed and the
  coordinator needs a turn to read it and file the carrier; alarming inside that
  window would fire on every new issue, and a detector that fires on the happy
  path gets muted. A negative `grace` reports immediately.
- **Ambiguous short ids are resolved, not reported.** Short ids are 4 hex digits,
  so the store collides: 12 pairs of archived twins in different monthly
  partitions already share an id, and `mg show <id>` refuses to guess between
  them. The first real run of this scan died on exactly that. The refusal is
  resolvable — the error names the colliding paths and mg accepts
  `<id>@<partition>` — so a collision is retried per partition and both twins'
  refs count. Anything still unreadable is listed as a **coverage gap** but is not
  actionable on its own: an item whose body cannot be read is an item whose
  marker cannot be seen, so it can only cause a *false* uncarried finding, never
  silence about a real one. Total blindness is a different fact and stays
  actionable via the blind-scan finding.
- **Notification policy is by condition, not by message.** A changed set of
  findings mails immediately — a newly uncarried issue is news. An unchanged set
  stays quiet until `renotify_after` (24h), so an issue uncarried for a week costs
  one mail a day rather than one a sample. Crossing the escalation threshold is
  itself a change and mails at once, so a slow renotify interval cannot postpone
  escalation.
- **Routing: the coordinator, not the PM and not `human`.** This is where the
  intake detector parts company with its sibling. The remedy for an uncarried
  issue is to file a carrier and dispatch triage, and the coordinator is the only
  agent that does either — "alarm the agent that can act". `human` would land it
  in a maildir carrying ~990 unread messages, where it could only be forwarded
  back. There is an added reason here: the failure being detected *is* a
  coordinator failure, so mailing the coordinator closes the loop on the agent
  whose dropped mail created the gap.
- **What happens if the coordinator is down.** The notice does not evaporate: mg
  mail is a durable maildir, so it waits, and the finding is recomputed from live
  state every interval regardless of who read what. But a durable notice nobody
  acts on is still an uncarried issue, so once a **single** issue has gone
  uncarried for `escalate_after` (4h — far shorter than the teardown detector's
  72h, because an uncarried issue is a reporter waiting with no acknowledgement
  and no record anywhere) the notice also copies `human`. At that point "the
  coordinator is not handling this" has become the news. Escalation ages each
  issue separately, so a new finding cannot reset an older one's clock.
- **The watch list** comes from `repos` if set, else from the issue poller's own
  state directory (`$POGO_HOME/gh-issues/seen-<owner>-<repo>.json`), else it is
  **empty** and the detector examines nothing. There is no built-in repo list:
  one used to name pogo's own upstream repos, which meant an install that
  configured neither source reconciled a stranger's issue tracker against its
  local work items (mg-f04b). An empty watch list is reported as such, since
  "examined nothing" and "found nothing" otherwise render identically. Reading the poller's state rather than duplicating its list is
  the point: a repo added to the poller is covered on the next pogod restart with
  no second edit to forget. It reads *state*, not the sent ledger — so a poller
  that is stopped, wedged, or has never delivered a mail still yields a correct
  watch list. The report says which source it used.
- **Why not in the poller?** The poller holds the *sent* side of the ledger, which
  is a real argument for putting the reconciliation there, and it was declined for
  three reasons. The sent ledger is the **wrong population** — what matters is not
  "did we mail about this" but "does a carrier exist for this open issue", and
  GitHub's open-issue list is authoritative. The poller is a standalone utility in
  another repo, deliberately kept a dumb transport. And decisively: it would make
  the poller responsible for the coordinator's follow-through while giving it no
  way to notice its own, where pogod already has the durable mail, condition
  annunciation, and escalation path this needs.
- **Arming.** Skipped entirely when `gh` is not on PATH, since every repo lookup
  would fail and the runner would report an environment gap as a wall of
  unreadable repos. That state raises the A13 condition
  (`ghintake_not_armed`) to the intake mailbox — a separate notice from the
  teardown detector's on the same row, because one root cause with two readers
  needs two notices or one reader learns nothing.

```toml
[gh_intake]
enabled = true             # default true; skipped when `gh` is unavailable
interval = "15m"           # coarse sample cadence (default 15m)
grace = "30m"              # how long an issue may go uncarried before it counts
                           # (default 30m; negative reports immediately)
renotify_after = "24h"     # unchanged findings re-mail after this (default 24h)
notify_to = "mayor"        # mailbox findings go to (default mayor, the ACTOR)
escalate_after = "4h"      # one uncarried issue also copies `human` after this
                           # (default 4h; negative disables, zero means default)
repos = ["owner/repo"]     # explicit watch list; unset falls back to poller
                           # state, then to watching nothing
```

Exit status of `pogo check-intake` is 0 when nothing is actionable and 1 when any
uncarried issue, unreadable repo, or blind scan is found, so it can gate a
schedule or CI step.

Source of truth: `internal/ghintake/`.

## The scheduler-completion deficit detector (ack-watch)

Since mg-a754 every scheduler fire carries a completion token and the agent
redeems it with `pogo schedule ack` when the fire's work is done. The scheduler
keeps `fires_delivered` / `fires_completed` / `unacked_streak` on each persisted
entry, and `pogo schedule list` renders them. **Nothing read them.**

On 2026-07-29 at 01:52 the table read:

```
mail-check-architect     751/757
mail-check-pa            753/757
mail-check-pm-onethird   751/757
mail-check-pm-pogo       270/757   <-- 36%
```

`pm-pogo` had been completing about a third of its mail-check fires **for its
entire run**, and the only path to noticing was a human reading that table and
comparing rows. Every liveness instrument said healthy — process alive,
`pogo agent diagnose` reporting `health=healthy` with last-activity 0s ago, PTY
output flowing. Claude Code's working spinner *is* PTY output, so no
output-based stall check can fire on a spinning agent. The completion ratio is
the one number that saw through it, because it measures **completed work**
rather than liveness.

`pogo check-acks` runs the detector on demand; pogod runs the same one on a
coarse heartbeat interval and mails `mayor`. Both are **report-only** — neither
nudges, restarts, nor unregisters anything.

- **The comparison is cross-agent, not self-historical.** `pm-pogo` was always
  broken; there was no regression for a self-comparison to find. A schedule is
  judged only against **peers**: same kind, same cadence, and a comparable
  number of fires since registration. 36% against ~99% on an identical cadence
  is a per-agent fault; everyone at 40% is a scheduler or fleet fault, and is
  reported once as a `FLEET DEFICIT` rather than as N per-agent alerts.
- **Two gates, not one.** A finding needs a rate **both** far below the peer
  median (`min_gap`, 20 points) **and** below an absolute floor (75%). They are
  tuned together: since a median cannot exceed 1, the floor would be dead weight
  if `1 - min_gap <= floor`. At the defaults the floor is what actually decides
  for a high-performing cohort, which is every healthy cohort observed.
- **Re-registration zeroes the counter, and that is load-bearing.** Registering
  a schedule with an existing `--id` *replaces* the entry, resetting
  `fires_delivered`/`fires_completed` to zero. Every crew agent re-registers its
  mail-check on startup, and mg-42ac made the redeploy nightly — so a naive
  absolute-floor rule would flag the whole crew every morning. Measured
  2026-07-29 03:03: `mail-check-mayor 6/7` before re-registration, `—` after,
  with one `pogo schedule` call in between. The detector treats a reset counter
  as what it is — a known-benign event after which the ratio is
  unrepresentative — via the same mechanism that handles `system_wake`. It does
  **not** try to preserve the counter across re-registration: a preserved ratio
  would mix fires from before and after a cadence or prompt change and describe
  no single regime.
- **Suppression.** A `system_wake` inside the settle window (30m) suppresses the
  whole report — post-sleep replay makes stale acks expected — as does the first
  30 minutes after a pogod restart. Both emit an `ack_watch_suppressed` event, so
  a deliberate silence is distinguishable from a clean scan.
- **What is deliberately not judged.** Schedules with a fresh counter, with
  fewer than 20 fires, that are not recurring, or with no comparable cohort are
  reported as **not judged** with a count per reason. They are unjudged, not
  healthy — a detector that lets "nothing measurable" read as "nothing wrong"
  reproduces the bug it was built to end.
- **A correct silence is recorded too.** A sample that ran and found nothing
  emits `ack_watch_clear` with `scanned`, `eligible` and the four skip counts
  (mg-ddf7). A silent correct outcome and a control that is not running are
  otherwise the same observation — and `eligible 3 of 41` and `eligible 41 of 41`
  are both no-findings, of which only one is a clean bill of health. Emitted on
  every clear sample, not only on transitions.
- **What the ratio actually measures — read this before tuning anything.**
  `FiresCompleted/FiresDelivered` is *exactly* the reciprocal of the agent's mean
  attention gap in cadence periods (measured to zero residual across 114
  schedules, mg-ddf7). An agent whose turns run longer than its cadence therefore
  **cannot** score 100%, however diligent: on the 2026-07-29 storm night `pa`
  acked every token it was handed and read 83%. Read the deficit as **delivery,
  not diligence**, and read `⚠ N unacked` — the run length — for the wedge this
  detector exists to catch, because it is the statistic that does not saturate.
  `pogo check-acks --populations` splits any deficit by mechanism. Two repairs
  have already been rejected with reasons recorded in `internal/ackwatch`; see
  [ack-deficit-populations-2026-07-30.md](investigations/ack-deficit-populations-2026-07-30.md)
  before proposing a third, and validate it against storm data rather than calm.
- **Never-acked.** A schedule with hundreds of fires and zero acks *is* a
  finding when the majority of its peers do ack — deliberately going beyond
  `scheduler.Entry.CompletionTracked`'s "untracked = unknown", because the
  cohort supplies the evidence that acking is expected here. A cohort where
  **nobody** has ever acked stays unknown; `pogo schedule completion` reports the
  tracked count for that case.
- **Routing.** Findings go to `notify_to` (`mayor`): the remedy is
  `pogo nudge <agent> --immediate` or a doctor restart, which is coordination
  work. A **standing** finding also copies the escalation box after
  `escalate_after` (24h, shorter than gh-teardown's 72h) — the coordinator is
  itself a crew agent and can have the exact defect being reported (mg-d385, the
  same night), so an alert routed only to the patient reaches nobody.
- **Notification policy.** Findings are fingerprinted by **identity**, not by
  rate: a ratio drifting from 36% to 34% is the same finding and must not
  re-mail. A changed set mails at once; an unchanged set waits `renotify_after`.

### The second arm: FLEET BLACKOUT (mg-e2a4)

Everything above is **peer-relative**, and a peer-relative test keys on
**dispersion**. The worse an outage is the more *uniform* it is, so that arm is
weakest exactly where the failure is worst: when every schedule degrades in
lockstep the median falls with the members and no gap clears `min_gap`.

Measured, 2026-08-09. Every agent stopped completing turns at ~12:50Z and
resumed at ~17:20Z (`ENOTFOUND` on the model API). `pogo schedule list` showed
every mail-check carrying the **same** `⚠ 27 unacked` — 27 × 10 min = 4.5 hours.
Over 13:20–17:20 the events log holds 251 `scheduler_fire_delivered` against
**3** `scheduler_fire_completed`. And every `ack_watch_fired` that day, including
the one at 16:12:59 in the middle of it, reported `deficit_count: 0`. A
100%-dead fleet produced zero per-schedule findings, because there was no
outlier to find.

So there is a second arm that keys on the **absolute** completion rate:

- **What it measures.** Fires delivered vs. fires completed across the fleet over
  a trailing **3 hours**, read from **events**, not the counters. No median, no
  peer set, nothing compared to anything — it gets *more* confident as the failure
  becomes more uniform, the exact inverse of the peer arm. Fires when the windowed
  rate is ≤ 10% with ≥ 24 deliveries across ≥ 3 schedules and ≥ 3 agents.
- **Two false-positive paths, both measured and both gated.** "Fires delivered,
  nothing completed" describes three situations and only one is a fault.
  1. *The fleet is not there.* Between 00:00 and 09:30Z on 2026-08-09 the
     scheduler delivered ~30 fires an hour and completed **zero**, every hour, all
     night — no crew agent was running. Ungated, this mails a person at 4am
     nightly, which is strictly worse than the silence it replaced. So the arm
     judges **running** agents only, and only those up for the *whole* window: a
     spawn 40 minutes ago cannot speak for the last three hours, and that produced
     a measured false positive at 10:00Z right after the crew came up.
  2. *The fleet is working in long turns.* A fire is only ackable at the end of an
     agent turn, so completions arrive in bursts — the same day had a **two-hour**
     stretch, all six agents up and working, with 84 fires delivered and none
     completed.

  The window was swept against that day's real log to pick the shortest value
  with no false positive: 1h → 9 FPs, 2h → 4, 2h30m → 1, **3h → 0**, 4h → 0. With
  both gates the arm fired on 4 samples across the whole day, all inside the
  outage.
- **The cost is stated, not hidden.** Detection lands about **three hours** into
  an outage, not thirty minutes, and an outage shorter than roughly three hours
  is not caught by this arm at all — no completion-based measurement can separate
  it from healthy bursty acking any sooner. Three hours is still the difference
  between a notice and none: on the day this was filed nothing reached a person
  for 4.5 hours and the human found out because it was his own wifi. Do not
  shorten the window without re-running the sweep on data containing a real
  outage.
- **Why events.** The counters are lifetime totals, so a low cohort median cannot
  tell "dead right now" from "carried a bad ratio for days"; and they are zeroed
  by re-registration, after which `min_fires` blinds the counter arms for over
  three hours. An outage starting just after the nightly redeploy is invisible to
  anything reading the table.
- **Routing is structural, not a timer.** A blackout copies the escalation box on
  its **first** sample, ignoring `escalate_after` entirely — including a negative
  value, which disables the *age*-based escalation only. `notify_to` is an agent,
  and the subject of the finding is that no agent is completing work: on
  2026-08-09 the one notice a dead fleet produced was addressed to a coordinator
  whose own mail-check carried the same 27 unacked fires. The asymmetry that makes
  the out-of-band path work is that **pogod survived** — it kept sampling and
  writing while every agent it hosts was inert, and the escalation box is polled
  out of process, so no fleet agent, agent turn, or ack sits on that path. Set
  `[agents] escalation_box` if a relay agent owns `human`.
- **The recipient is checked against the population.** `ack_watch_fired` carries
  `notify_to_stalled` and `escalate_to_stalled`. If the escalation box is itself
  one of the stalled agents the mail says so in as many words, because that state
  — an alarm with no recipient outside the outage — is this defect one level in.
  A blackout notice every recipient refused emits
  `ack_watch_blackout_unreported`.
- **It renotifies on its own clock.** `blackout_renotify` (30m, the sampling
  interval) replaces `renotify_after` while a blackout stands. 6h is right for
  "one schedule lags its peers" and wrong for "nothing has completed a fire": the
  4.5-hour outage began and ended inside one 6-hour shadow, and a second identical
  outage 30 minutes later would have been suppressed entirely. Repetition is the
  signal here — the notice leaves for an out-of-process reader with no
  acknowledgement path back, and the all-clear is the notice stopping.
- **Blind is not calm.** A window that could not be measured — no measurement
  supplied, no running-agent set supplied, unreadable events log, too few
  deliveries, too few agents old enough to judge — is reported as
  `blackout_blind` with a reason, on both the fired and the `ack_watch_clear`
  paths (`blackout_judged`), and rendered in `pogo check-acks`. Zero completions
  is what a blackout *looks* like, so a failed read must never arrive looking
  like a measurement of zero.
- **The one gate it keeps** is the disruption suppression: after a `system_wake`
  or a restart the traffic describes a regime that has just ended. The cost is
  bounded at 30 minutes, and unlike `min_fires` it does not scale with the
  cadence.

```toml
[ack_watch]
enabled = true             # default true; inert when no cohort can be formed
interval = "30m"           # coarse sample cadence (default 30m)
renotify_after = "6h"      # unchanged findings re-mail after this (default 6h)
blackout_renotify = "30m"  # renotify window while a FLEET BLACKOUT stands
                           # (default 30m — must be shorter than renotify_after)
notify_to = "mayor"        # mailbox findings go to (default mayor)
escalate_after = "24h"     # one standing finding also copies the escalation box
                           # after this (default 24h; negative disables the AGE
                           # escalation, zero means default). A FLEET BLACKOUT
                           # escalates on its first sample regardless.
```

Exit status of `pogo check-acks` is 0 when nothing is actionable and 1 when any
deficit is found, so it can gate a schedule or CI step.

A default `pogo nudge` cannot reach the agent this typically finds: it waits for
2s of PTY silence, and a spinner guarantees that silence never arrives. Use
`pogo nudge <agent> --immediate`.

The detector's statistical knobs (`min_fires`, `min_peers`, `scale_band`,
`min_gap`, `floor`, `settle_after`, and the blackout arm's `blackout_window`,
`blackout_rate`, `blackout_min_deliveries`, `blackout_min_schedules`,
`blackout_min_agents`) are
compiled-in defaults in `ackwatch.Params` rather than config keys — they are
tuned against a specific observation and a wrong value produces either a
false-positive storm or silence, neither of which should be one line of TOML
away. Only the two routing/cadence knobs above are configurable, because "which
box does a person actually read" and "how often may this shout" are facts about
the deployment.

Source of truth: `internal/ackwatch/`.

## The missing-mail-loop announcer (deaf-watch)

An agent with no `mail-check-<name>` schedule can be mailed, and **nothing will
ever wake it to read the mail**. Every coordination path this fleet has runs
through mail, so such an agent is unreachable — while its process is alive, its
PTY output is flowing, and every liveness instrument reads green. mg-de08 spent
two hours of a fleet-wide mail outage on exactly that.

`pogo agent diagnose <name>` has reported this as `health=no_mail_loop` since
mg-de08, and mg-738f widened it to the **deaf survivor**: an `auto_start=false`
agent someone turned on, running with its loop dead underneath it. Both were
correct. Until mg-032b neither was **loud**: the only consumer was a subcommand
that takes the agent's *name* as an argument, and not knowing which name to type
is what this fault looks like from the outside. It was detectable, never
announced.

pogod now applies the **same** judgement across the whole registry on its
heartbeat and mails. `pogo check-mailloops` runs the same read on demand. Both
are **report-only**.

- **The judgement is not re-derived.** The source is
  `agent.Registry.MailLoopReport`, which runs diagnose's own `mailLoopFor` over
  every agent. Two implementations of "who is owed a mail loop" would drift, and
  the announcer would start reporting REDs `diagnose` denies.
- **Who is not judged, deliberately.** Polecats (they register their own loop at
  spawn, mg-e633, and escalate on failure, mg-6fe0); a configured agent that is
  not running ("not there" is not a fault); an agent whose prompt tree cannot be
  read. That last one is the cry-wolf guard mg-738f argued for: a wrong "yes"
  costs a false RED, and a health signal that cries wolf gets ignored.
- **A hold-down, not an instant alarm.** A missing loop must persist unbroken for
  `hold_down` (15m) before it is announced. Spawn and schedule registration are
  not simultaneous, and a nightly redeploy (mg-42ac) re-runs that gap for the
  whole fleet — without the hold-down every restart would announce everyone. A
  loop that comes back **resets** the clock rather than accumulating toward one.
  Same mechanism, same reasoning, as mg-4904's hold-down on usage-limit hits.
  Each entry into the window emits `deaf_watch_pending`, so "saw it and waited"
  is distinguishable from "never saw it".
- **"Could not look" is never an all-clear.** A pogod with no mail-check provider
  (scheduler failed to load) emits `deaf_watch_error` and evaluates nothing;
  `GET /agents/mail-loops` answers **503**, not `200` with an empty list; and
  `pogo check-mailloops` exits non-zero with the reason. A report that judged
  nothing says so in as many words.
- **Routing, and the one rule unique to this detector.** Findings go to
  `notify_to` (`mayor`) — re-registering a loop and deciding whether the agent
  also needs a restart is coordination work. A standing finding also copies
  `human` after `escalate_after` (24h). **But a finding that names `notify_to`
  itself escalates immediately**, regardless of `escalate_after`: mailing an
  agent that has no mail loop about its own missing mail loop is not a weaker
  alert, it is *no* alert. The coordinator is itself a crew agent and has had the
  fleet's defects before its peers (mg-d385).
- **Episodes.** While at least one agent is unreachable an episode is open; a
  changed roster mails at once, an unchanged one waits `renotify_after`. On close
  the all-clear goes to **everyone who was alarmed**, and a generic
  `incident_episode_cleared{kind:"deaf_agent"}` event carries the roster and
  window (the mg-55b2 contract) so the notifier coalesces the close into one
  notification instead of a swarm.
- **It never registers the loop for you.** Doing so would paper over *why* the
  loop vanished — a reap, a failed registration, a manual `pogo schedule rm` —
  and that is the part worth knowing.

```toml
[deaf_watch]
enabled = true             # default true
interval = "5m"            # sample cadence (default 5m; the condition is a
                           # boolean state, not a rate — nothing to average)
hold_down = "15m"          # a missing loop must persist this long before it is
                           # announced (default 15m; negative disables — tests only)
renotify_after = "6h"      # an unchanged roster re-mails after this (default 6h)
notify_to = "mayor"        # mailbox announcements go to (default mayor)
escalate_after = "24h"     # a standing finding also copies `human` after this
                           # (default 24h; negative disables AGE-based escalation
                           # only — a deaf `notify_to` still escalates at once)
```

Exit status of `pogo check-mailloops` is 0 when every judged agent has a loop and
1 when any agent is unreachable, so it can gate a schedule or CI step.

`deaf-watch` and `ack-watch` are **disjoint**, not redundant: ack-watch reads
schedules that *exist* and compares completion rates, so an agent with no
schedule at all has no counter row, joins no cohort, and disappears from it
silently. ack-watch catches "registered and not completing"; deaf-watch catches
"never registered, or reaped".

Source of truth: `internal/deafwatch/`, `internal/agent/mailloop_report.go`.

## The first-completed-turn floor (first-turn)

**A spawn is not a success.** `autostart: started pm-pogo (pid=41773)` plus a
registered mail-check schedule is evidence that pogod did its job, and no
evidence whatever that the agent is alive in the only sense that matters. On
2026-08-11 this daemon logged that line five times at 03:01, re-registered every
schedule, and passed its own post-check ("5 mail-check schedule(s) present")
ninety seconds later — over a fleet that then completed **zero turns for
seventeen hours**. Everything pogod asserted was true.

first-turn watches for each agent's **first completed fire after each spawn** and
alarms when it never arrives. Report-only.

### It does not replace the blackout arm — read this before assuming it does

ack-watch's FLEET BLACKOUT arm (above) **fired 33 consecutive times through that
same 22-hour outage**, correctly, naming all five agents, escalating each one to
the escalation box, and the out-of-process notifier surfaced them. Detection,
routing and delivery all worked. The premise "no instrument saw it" is false and
should not be repeated.

What that arm cannot do is speak about a **fresh** agent, and that is not a
defect in it. It judges an absolute completion *ratio* over a trailing window, so
`RunningSince` holds an agent ineligible until it has been up for the whole
window — a gate that is load-bearing, because "fires delivered, nothing
completed" is also exactly what an *empty* fleet looks like, every night between
midnight and 09:30. The cost is arithmetic: after a bounce the earliest it can
say anything is one full window later. Measured on the outage above, its first
post-bounce firing was at 05:03:36Z, 3h02m after the 02:01:33Z spawn. first-turn
is red at 02:46:33Z.

| arm | claim | earliest |
|---|---|---|
| ack-watch blackout | the fleet **was** alive and went dark | one 3h window |
| first-turn | the fleet was **never** alive | 45m grace |

### Why the grace is 45 minutes

It is a measurement, not a preference. Every crew `agent_spawned` on this box
since completion tracking existed (2026-07-23), paired with the first
`scheduler_fire_completed` addressed to that agent at or after the spawn — 87
spawns, bimodal, with nothing at all in the middle:

| population | n | spawn → first ack |
|---|---|---|
| healthy | 67 | max **33.7 min** (p50 12.6 min) |
| outage | 20 | min **150.8 min** (max 1139 min = 19h) |

The 20 in the upper mode *are* the three outages: the 2026-08-10 spend-limit
episode (150–181 min), the 2026-08-11 nightly-deploy spawn of five inert agents
(1044–1064 min), and the 2026-08-08 hung-deploy outage (1139 min). 45 minutes
sits inside the empty band — 1.33× the healthy maximum, 3.35× below the smallest
real outage. **Rerun the sweep before moving it**; a threshold justified by a
measurement whose data has moved is justified by nothing.
`TestGrace_SitsInsideTheMeasuredEmptyBand` pins both edges.

### The rules

- **Crew only.** A polecat's first ack legitimately trails its first real work,
  which is a task of unbounded length, so the separation above is a property of
  the crew population and does not transfer. Polecats have stall-watch and
  wedge-watch.
- **Evidence is the events log, not the counters.** `scheduler.Entry`'s
  completion counters are zeroed by re-registration, and every crew agent
  re-registers on startup — so on precisely the boot this floor exists to judge,
  the counters read 0/0 for reasons that have nothing to do with the agent.
- **A completion before the spawn does not count.** The 2026-08-11 events log
  holds thousands of completions; every one belonged to the incarnation that died
  the previous evening. A detector matching "has acked at some point" reads green
  through the whole outage.
- **An agent nothing was asked of is not blamed.** A finding needs at least 2
  fires actually *delivered* since spawn. Fewer, and the agent is reported as
  `never_addressed` — that is deaf-watch's finding and a different remedy.
- **Routing.** A single dark agent goes to `notify_to` (`mayor`): restarting one
  crew agent is coordination work. The **fleet-wide** case escalates immediately
  and structurally to `[agents] escalation_box`, not on any age gate — the mayor
  is inside every fleet outage in this system's history (mg-e2a4), and a fleet
  that has never come up cannot be the thing that fixes it.
- **The subject line carries a duration that grows.** Repeats climb a doubling
  ladder (+1h, +3h, +7h, +13h, capped at 6h apart), each naming how long this has
  been going on. This is deliberate contrast with the blackout arm, whose 33
  notices through the outage carried a byte-identical subject: the 33rd held no
  more information than the 1st, and none of them ever said how long.
- **Quiet is recorded.** A clean sample emits `first_turn_watch_clear` with its
  judged roster and the populations it declined to judge. A silent correct
  outcome and a control that is not running are otherwise the same observation —
  which is this whole ticket, one level up.
- **Blind is never calm.** No registry, an unreadable events log, or a source
  error emits `first_turn_watch_blind` and judges nothing.
- **A notice that reached nobody** emits `first_turn_watch_unreported`.
- **Report-only.** It never restarts, nudges or respawns. No member of the
  synthetic-failure class is fixable by a restart (mg-18d0), and pogod may
  already be suppressing respawns for that reason when this fires.

```toml
[first_turn]
enabled = true             # default true
interval = "10m"           # sample cadence (default 10m; well under the grace)
grace = "45m"              # a spawned agent may complete nothing for this long
                           # (default 45m — see the sweep above before changing)
notify_to = "mayor"        # SINGLE-agent findings go here (default mayor); the
                           # fleet-wide case always also goes to
                           # [agents] escalation_box
```

Source of truth: `internal/firstturn/`.

### `pogo check-strandedmail` — mail in a mailbox nobody reads

A third disjoint question, and the one neither of the above can ask. deaf-watch
asks whether an agent can be **woken**; ack-watch asks whether its schedule is
**completing**. Both are satisfied by a mail-check that fires perfectly into the
wrong mailbox — which is what every polecat had before mg-aa96, when the message
body derived the mailbox from the **work item** while correspondents address the
**agent name** (`--from=$POGO_AGENT_NAME`).

Registration now refuses that mismatch (`Entry.Validate`, see
`internal/scheduler/mailbox.go`), but a refusal only governs schedules registered
from now on. **Repointing an existing one only changes where the agent looks
next**: everything already delivered to the abandoned box stays there, and the
repoint turns a misdelivery into an orphan — mail exists, nobody reads it,
nothing says so, which is the same shape as the bug it fixes. The 2026-08-05
sweep of the live fleet found one such message, an urgent correction to a builder
mid-flight.

`pogo check-strandedmail` takes every live mail-check, derives the mailbox it
*would* have read under the old work-item form, and asks `mg mail list --json`
whether anything unread is sitting in it. Findings name the sender and subject —
**corrections** are the traffic most at risk, because they are sent off-cadence
to an agent already working — and print the exact `mg mail read <box>/<id>
--force` that opens each one. `--force` is required rather than decorative: mg
refuses a cross-box read without it, and nobody running this report is the
abandoned mailbox.

It **reports only**. Re-delivering would mean `mg mail send` writing a new
message under a new `From`, and a correction whose provenance is a lie is worse
than one that arrived late; if the recipient is still running, the original
sender must re-send. A sweep with no mail-checks to judge says so rather than
printing an all-clear. Exit status is 0 when nothing is stranded and 1 when
anything is, so it can gate a schedule or CI step.

Source of truth: `internal/strandedmail/`, `cmd/pogo/checkstrandedmail.go`.

### `pogo check-verdicts` — work that landed while its filer was never told

A fourth disjoint question, at the other end of the loop. The three above ask
whether an agent can be reached; this one asks whether the party who FILED a
piece of work ever heard how it came out.

The predicate is one line, and both halves come from macguffin's own store:
**an item reaching `done` (or `archived`) with no verdict mail received by its
filer is a dropped verdict.** The landing comes from `work.done` / `work.archive`
in `events.jsonl`, the filer from the item's `creator:` frontmatter, the worker
from `polecat-<name>` in its result sidecar, and the delivery from a message in
the filer's mailbox whose `From:` is that worker. Findings are ordered **oldest
landing first**, because the report exists so a backlog can be *recovered* and
not merely alarmed about.

Three kinds, not two. `DROPPED` is the finding. `DELIVERED` counts archived mail
— filing a verdict away is not losing it, which is why this reads the maildir
directly instead of `mg mail list --all`, whose output excludes archived
messages. `UNDECIDABLE` is the detector's own reach: an item whose worker cannot
be resolved is counted and listed on its own rather than folded into either
verdict, and it does **not** make the run actionable.

What it cannot see is stated rather than footnoted: a verdict delivered by any
channel other than macguffin mail — a commit subject, a docs file, a spoken
handover — is invisible and is reported as dropped. That is the intended
polarity; the complaint it was built for is precisely that a commit subject is
not delivery.

It **reports only**, and that is a boundary rather than a stage. It never files
the missing verdict, never mails on anyone's behalf, and never edits an item;
re-sending would have to forge a sender. If a future version should *file* the
missing verdict, that is a different command and it does not join this family.

**Exit status** is 0 for no dropped verdicts, 1 for at least one, 2 for a usage
error, and 3 when the run **measured nothing**. That third answer is the whole
reason this was ported rather than scheduled where it lay: lose `events.jsonl`
and every item reads as never landed, so a careless detector reports "0 dropped"
over a fleet losing every verdict it has. An unreadable mail tree, an
unresolvable store, and an *unscoped* scan that judged zero items are all exit 3.
A scan **scoped** by `--filer` or `--since` that matches nothing is a different
thing — an answer to the question asked — and exits 0 while saying, in words,
that it judged nothing.

**`--probe` asks whether the detector can still fire.** It builds a throwaway
macguffin store, drives the real `mg` binary through new/claim/done, drops one
verdict on purpose and delivers its matched control, and reports whether the
detector went RED on the first and GREEN on the second:

```
$ pogo check-verdicts --probe
  [PASS] known-bad input: work landed, worker never mailed the filer
         want DROPPED, got DROPPED
  [PASS] known-good input: the same work, verdict mailed to the filer
         want DELIVERED, got DELIVERED
```

The same probe runs in `go test ./...`, so the refinery gate exercises it on
every merge. That is deliberate and it is the lesson this port carries: the
original's two constructive probes were killed ~22 hours after landing by
mg-d639 — a **correct** change, making an unknown mail recipient a refusal rather
than a silent create — and nobody noticed for two days, because the read-only
census kept working and stayed green. A probe that runs only when somebody
remembers to look is in the position that one was in. The mg behaviours the
fixture rests on are declared as clauses in `internal/mgcontract`, so when mg
moves again the red arrives once, by name, instead of as an unexplained failure
inside a detector's probe.

Ported from macguffin's `verdictwatch.py` (mg-bf3f, audited by mg-f911), which
was confirmed correct and then sat unrun in a research working directory — where
code has no runner by construction. Verified against the live store at the port:
identical scanned/delivered counts to the original, with two rows correctly moved
from `UNDECIDABLE` to `DROPPED` because this version prefers the copy of a
duplicated item that names a worker rather than whichever the glob yielded last.

Source of truth: `internal/verdictwatch/`, `cmd/pogo/checkverdicts.go`.

## The wedged-agent detector (wedge-watch)

On 2026-08-04 twelve polecats and the doctor crew agent sat at a Claude Code
login prompt for **thirteen hours**. On 2026-08-05 it recurred for seven. About
twenty agent-hours of nothing — and every liveness instrument pogo has read
healthy for the whole of both windows:

```
pogo agent list
  teaa9   status=running   uptime=13h44m   last-activity=just now
```

The agents were not frozen. They were **animating**. Claude Code redraws a
spinner and an elapsed counter while parked at a prompt, and that redraw is PTY
output — so `last-activity`, which tracks PTY writes, said "just now" forever;
the process was alive, so status said running; and CPU was near zero, which is
also what a legitimately blocked agent looks like. Every instrument was
measuring the animation. It was found by hand.

pogod now runs two checks on its heartbeat. Both are **report-only**.

- **(1) A PTY-content check for known dead-end states.** `Please run /login`,
  `API Error: 401`, `Unable to connect to API` / `ENOTFOUND` / `EAI_AGAIN`, the
  rating dialog, and the rate-limit modal. Matching is whitespace-insensitive
  against the ANSI-stripped buffer, because Claude Code spaces TUI columns with
  cursor-move escapes that `StripANSI` deletes rather than replaces — a literal
  compare is how mg-f36b's watcher logged **zero** dismissals for two months
  while looking installed.
- **(2) The agent's own declared work counter, cross-checked against process
  uptime.** This is the half that matters, because (1) can only recognise a dead
  end somebody has already met and the enumeration is permanently one incident
  behind. The live signature on both nights was a 7h+ uptime beside a counter
  reading **"Baked for 2m 56s"**.

**The cross-check gates on the counter being FROZEN, not on the ratio.** This is
the part to read before retuning anything. The declared counter measures *one
turn*, not cumulative work, so a perfectly healthy agent seven hours into its
life and three seconds into a new turn also shows a tiny counter beside a huge
uptime — on a ratio-only rule every agent in the fleet reports constantly and
the detector is muted inside a day. What made 13h44m beside "2m 56s" damning is
that the counter did not *move*: had it been advancing it would have read 13h,
and had turns been starting and finishing it would have read a different value
at every sample. One value, unchanged across a window spanning several
10-minute mail-check fires, means the fires are being absorbed without running
anything. `ratio` and `min_uptime` survive as guards, not as the signal.

**A 401 shortly after a connectivity failure is ONE signature, not two.** mg-fc8d
was *filed* blaming an interrupted `/login` for revoking the OAuth token. That
was wrong, and the correction is the most important behaviour here: nothing was
revoked (refresh grant good for 16.5 more days, subscription intact), nobody
logged in, and every agent resumed on the same credential. A network outage
swallowed an access-token refresh, and the failed refresh surfaced as
`401 ... revoked/expired`. Concluding "credential revoked, page the human" from a
401 alone pages Daniel for a re-login **that fixes nothing** — and since the
access token turns over roughly every 8h, any outage overlapping a refresh window
reproduces this, about three chances a day. So a connectivity failure observed
anywhere in the fleet within `coincidence_window` merges with a later 401 into
one cause.

**`ENOTFOUND` and a poisoned credential get opposite responses, and an
unresolvable case says UNKNOWN.** An outage-swallowed refresh wants the agent
*left alone* until connectivity is observed back — its context is intact.
A genuinely poisoned credential wants the opposite: stop and re-dispatch, since
it will never resume. Because those are opposites, the detector refuses to guess:
a 401 with no connectivity evidence and a credential that is *readable and in
date* is reported as `unknown` / `investigate`, never as revocation, because the
credential has actively refuted revocation. A bad credential is named **only**
when the credential itself says so — the refresh-grant expiry, never the
8-hour access-token expiry, which is routinely stale on a healthy machine.

**A CPU-starved agent is reported as degraded, not wedged.** There are *three*
states that look identical to every instrument this fleet has: wedged at a dead
prompt; CPU-starved (genuinely working, achieving nothing); and healthy. On
2026-08-05 thirteen polecats sat at `last-activity: just now` for hours during a
load event while plain local `git log` calls timed out at 180s. The remedies are
opposite yet again — a wedged agent needs intervention, a starved one needs to be
**left alone** and the load reduced, since waking or restarting it destroys real
work and adds to the load. So when the only evidence is a frozen counter and the
host is measurably saturated, the verdict is `host_oversubscribed` /
`reduce_load_do_not_intervene`. Saturation does **not** reinterpret an enumerated
finding: a login prompt is not caused by CPU contention. The instrument is
deliberately **not** the load average — mg-1b8c measured a load average of 214 on
this box against ~7.5 of 10 cores actually in use, because Darwin counts
uninterruptible-sleep tasks — but used cores against core count, at
`hostload.SaturatedAt`. An unmeasurable host reports `unknown` and says
starvation could not be ruled out. This is a state pogo creates for itself
(mg-3977, mg-da30), which is an argument for measuring it rather than assuming it
away.

**No intervention is named, because none is established.** An early reading held
that a nudge revived the fleet on 2026-08-05. mayor retracted it with a control:
968 nudges inside the outage window produced **0** acks, and `crew-doctor` —
which received no immediate nudge — woke anyway on an ordinary scheduled fire ten
minutes later. What changed was the network. The detector therefore names a
*recovery condition* (connectivity returning) and no remedy; a remedy that merely
correlates with recovery would be worse than none, because it would be trusted.

**"Could not look" is never an all-clear.** A failing source emits
`wedge_watch_error` and evaluates nothing. An agent whose work counter cannot be
parsed falls back to event-log staleness, and if that is unavailable too the
agent is reported as *unjudgeable*, not healthy — a harness that renames its
status line must make this detector coarser, never silent.

That fallback **had no production writer until mg-20eb**, and the whole of this
detector's first 25 minutes on this box was 40 `wedge_watch_error` events and
zero verdicts. `Observation.EventsLastSeen` was set only by a unit test, so the
degradation path above was unreachable and an unparseable counter did not
coarsen the detector — it disabled it. The event-log index is now built by
`wedgewatch.SystemEvents`, **lazily**: it is read at most once per sample and
only when some agent's counter could not be parsed, so a fleet whose counters
all read never opens the log. Only the live `events.log` is scanned, not the
rotated files, so an identity whose last line has rotated out reports as
unjudgeable rather than as stale. Recency is keyed on the event's own `agent`
field, which is why pogod's `scheduler_fire_delivered` traffic — 64k lines,
logged against `pogod` — cannot keep a wedged agent's clock warm; a delivery
record proves the sender ran, never the receiver.

The same ticket found all four counter stems missing on all agents at once. The
current harness renders the completed-turn line as `✻ worked for 55s` (not
`Baked for`), moved the live counter into a spinner parenthetical whose verb is
randomized (`cerebrating…`, `crystallizing…`, `slithering…`) so only its shape
`(11m53s · ↓ 29.6k tokens)` is stable, and turned `esc to interrupt` into a
permanently-rendered hint bar. That last one is the trap worth remembering: a
stem on a permanently-rendered string is a **false anchor** — it never goes
quiet, it just reports whatever number drifts into its window.

**It is not routed, and that is deliberate.** mg-fc8d's item (3) — escalating a
fleet-level wedge **outside** the wedged party, to Daniel rather than to an inbox
inside the failure — is an alerting-policy decision reserved to Daniel and not
yet ruled. The runner therefore holds **no mail seam at all**: there is no
`notify_to` to set. Findings go to `wedge_watch_fired` on the event spine and to
pogod's log, and every emission states that nothing was routed so a reader does
not assume somebody else was told. This is the item that actually bounds the
damage: on 2026-08-04 stall-watch fired correctly every five minutes for thirteen
hours into an inbox that belonged to a wedged agent.

```toml
[wedge_watch]
enabled = true                # default true
interval = "5m"               # sample cadence (default 5m)
marker_hold_down = "10m"      # a known dead-end marker must sit beside a stalled
                              # agent this long (default 10m; negative disables —
                              # tests only. It is not zero because an agent
                              # WRITING about a marker has it in its own PTY)
freeze_hold_down = "30m"      # the work counter must hold one unchanged value
                              # this long for the un-enumerated case (default 30m;
                              # sized to span two mail-check fires)
min_uptime = "1h"             # process-age floor (default 1h)
ratio = "20"                  # uptime must be >= this many times the frozen
                              # counter (default 20; the live signatures were
                              # 280x and 143x)
coincidence_window = "2h"     # a connectivity failure keeps a later 401 explained
                              # for this long (default 2h — long on purpose)
renotify_after = "6h"         # an unchanged roster re-emits after this (default 6h)
```

The host reading is taken on **every** sample, unlike the credential (which is
read lazily, only when an auth symptom appears, because it shells out to
`security` and can block on a keychain prompt). Host contention is what
*positively rules starvation out*, so it has to be present on findings where the
answer is "the box had headroom" as well as on the ones where it is not.

`wedge-watch` and the **modal watcher** (mg-4421) are disjoint. The modal watcher
*dismisses* the two enumerated Claude Code modals from inside the PTY stream;
wedge-watch reports that one is still up beside a stalled agent, which means the
dismissal did not win. `wedge-watch` and **deaf-watch** are also disjoint:
deaf-watch catches an agent with no way to be woken, wedge-watch catches one
being woken and absorbing it. And it shares `internal/hostload`'s threshold with
the dispatch guard but not its denominator: dispatch asks whether pausing *our
own* work would help, wedge-watch asks whether there is any CPU for this agent to
progress with, so it reads the whole host.

Source of truth: `internal/wedgewatch/`, `cmd/pogod/wedgelog.go`.

## The done-item polecat reaper (done-reap)

Every automatic polecat teardown pogod had was keyed on **merge**: the refinery's
`OnMerged` hook marks the item done, stops the polecat, and lets the exit hook
remove its worktree and mail-check schedule (gh #35). That is the right hook for a
build polecat, whose deliverable *is* a branch. It reaches nothing else.

**Triage, audit-only and investigation polecats produce no merge.** They finish by
calling `mg done` themselves, often with a `--result` and a mail, and the refinery
never hears about them:

```
merge-producing polecat  ->  refinery merges  ->  pogod stops it, marks done   (automatic)
non-merge polecat        ->  calls `mg done`  ->  nothing whatsoever           (was manual)
```

So they held a concurrency slot until a coordinator noticed. Measured 2026-07-30:
`d764` finished its triage, delivered its packet, filed its successor, went idle,
and sat on one of five slots for **7m16s** while two high-priority items were
queued and undispatchable. Nothing would have ended it but a human. And the loss
is invisible in the instrument operators actually read — `pogo agent list` reports
`status=running`, only `pogo agent diagnose` reports `idle` — so a slot held by a
finished agent looks exactly like healthy saturation. Same family as mg-18d0:
**the list reports liveness, never productivity.**

- **It keys on the WORK ITEM, not the merge.** The condition is the item reaching
  a terminal state (`done`/`archived`). Merge is one path there, `mg done` is the
  other, and `done` is the general fact both produce — so the merge hook is
  untouched (it has obligations this does not: writing the completion, honouring
  `--defer-done`/PR-flow/`post-merge-work`, arming the deferred backstop) and this
  is a second, independent detector on the general condition. Extending the merge
  hook would have been the smaller change and would have fixed nothing, because
  the polecats that leak are the ones the refinery never sees.
- **It polls; it is not event-driven.** `mg done` runs in the *polecat's* own
  process against the macguffin store, and macguffin gives pogod no callback —
  `OnMerged` exists only because the refinery lives inside pogod. `done` can be
  observed but not delivered. One `mg show --json` per live polecat on the
  heartbeat tick, same shape as every other detector here.
- **`done` alone is NOT the condition — it is `done` AND quiet.** The polecat
  protocol tells a polecat to call `mg done` and then stay alive until stopped,
  and work legitimately follows that call: mailing a verdict packet, filing a
  successor, answering a coordinator follow-up. Stopping on the `done` write alone
  would kill it mid-sentence, which is strictly worse than the leak — a lost
  verdict is unrecoverable, a held slot is merely expensive.
- **A grace period, not an explicit "I am finished" signal.** A signal the polecat
  must remember to send fails for exactly the polecats that most need stopping:
  the ones that ran off the end of their protocol. mg-ddf7 measured that failure
  mode on an instructed step a third of agents simply never performed. A grace
  period asks the polecat for nothing.
- **The grace is measured from the last PTY write, not from the `done`
  transition.** A timer from the transition cannot tell a polecat that is still
  typing from one that stopped; PTY quiescence can. It also self-extends in the
  case that motivated the question: an incoming coordinator mail is delivered as a
  PTY nudge and the answer is more output, so a polecat handling a follow-up keeps
  resetting its own clock and is reaped only once it goes quiet again.
- **A polecat with its item still `claimed` is untouchable, at any idle time.**
  Item state is the gate; idleness only qualifies it. That is why the healthy
  42-minute idle polecat mid-work survives structurally rather than by tuning, and
  it is the acceptance control the mechanism is required to pass — one that cannot
  tell the two apart is worse than the manual sweep, because it kills working
  agents.
- **A polecat that has never written to its PTY is skipped.** Its idle time is
  *unmeasurable*, not zero: it may be seconds into spawn, or wedged before its
  first turn (mg-ce61).
- **An unreadable item leaves the polecat running.** A store pogod could not read
  is not evidence of completion — the same direction, for the same reason, as the
  `post-merge-work` probe (mg-d86e).
- **It acts, and that is all it does.** This is the one heartbeat detector here
  that is not report-only: it stops a process whose work is provably concluded. It
  has no seam through which it could mark an item, mail, nudge, or spawn, and it
  never escalates — a completed triage is not a fault. The teardown that follows
  (worktree removal, mail-check reaping) is the ordinary `OnExit` path, so
  non-merge polecats have always had their mail-check schedule reaped; that half
  was never the gap.

```toml
[done_reap]
enabled = true             # default true
idle_grace = "2m"          # a done polecat must be quiet on its PTY this long
                           # before it is stopped (default 2m; negative removes
                           # the window entirely — tests only, since a zero-grace
                           # reaper stops a polecat mid-mail)
```

Source of truth: `cmd/pogod/donereap.go`, `internal/agent/polecatactivity.go`.

## Orchestration resume deadline

**A procedure that stops the fleet must not also be the only thing that can start
it.** Stopping orchestration is step 1 of a two-step sequence — `pogo service
install` quiesces the crew and restores it seven steps later, an operator's `pogo
server stop` is undone by their later `pogo server start`, a redeploy stops a
fleet it intends to bring back. Until mg-5af1 the only thing that could perform
step 2 was the process that performed step 1, so the fleet being up was
contingent on that process surviving.

On 2026-08-08 it did not. The crew was stopped cleanly at 00:44:20Z, the run that
stopped it hung at 02:00:05Z and did not return for 31h39m, and the fleet stayed
dark for **33 hours**. Every supervisor behaved correctly: a requested stop is
not a crash, so `restart_on_crash` did not fire, and nothing is entitled to undo
a deliberate shutdown it cannot distinguish from an intended one. The scheduler
delivered 198 consecutive fires to an absent pm-pogo and logged every one as a
success.

pogod now **arms a deadline at the moment of the stop** and, if nothing has
brought the fleet back by then, restores full mode itself and mails the
coordinator naming who stopped it and when.

- **The holder is pogod, not the stopper.** A shell `trap` dies with the shell, a
  background child dies with its process group, and a deferred restore inside the
  procedure is precisely the thing that did not run. pogod is a separate process
  from every stopper and stays up for the whole index-only window — that is what
  index-only *means*.
- **It is not the crew.** A watcher held by the fleet cannot fire when the fleet
  is what is down (the same constraint as mg-a14c).
- **It cannot fight an ordinary deploy.** The deadline is sized well above any
  legitimate stop/restart cycle, it cannot act while the mode is full, and a
  return to full mode by any route discharges the obligation — so a correct
  deploy produces no second restart and no alarm. That positive control is a
  test, not an aspiration (`cmd/pogod/orchresume_test.go`).
- **A second stop does not push the deadline out.** The obligation runs from the
  *original* stop, so a retry loop cannot defer it forever.
- **It does not survive pogod's own death, and does not need to.** A restarted
  pogod boots into full mode by construction, which is the state the obligation
  would have restored. A pogod that is killed and *never* restarted is a
  dead-daemon problem, handled by the tier-1 heartbeat reaper.

**Declaring a longer stop.** When a long dark window is intended, say so:

```bash
pogo server stop --hold 4h        # or POST /server/stop-orchestration?hold=4h
```

There is deliberately no indefinite hold. An undeclared indefinite dark fleet is
indistinguishable from an outage, and it was one. A malformed hold is refused
rather than silently defaulted.

`pogo server stop` prints when the fleet comes back, and says so loudly when NO
deadline is armed. `GET /server/mode` gains `stopped_since` and `resume_due`.

```toml
[orchestration_resume]
enabled = true             # default true. `false` restores the pre-mg-5af1
                           # behaviour — the fleet coming back is once again
                           # contingent on whatever stopped it surviving. pogod
                           # logs that at boot in those words.
grace = "15m"              # how long orchestration may stay stopped by a caller
                           # that declared no hold (default 15m; negative disarms
                           # the deadline while still recording the stop)
retry = "1m"               # floor on re-attempting a restore that FAILED. Not
                           # the detection interval — detection rides the
                           # heartbeat.
```

Source of truth: `internal/server/orchestrationresume.go` (arming),
`cmd/pogod/orchresume.go` (firing).

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

**Which scripts get gated (defaults).** With no `[gates] commands` in
`<repo>/.pogo/refinery.toml`, the refinery gates the conventional scripts
present at the worktree root — `./build.sh` then `./test.sh` — **except that a
`build.sh` which itself runs `./test.sh` is gated alone** (mg-da30). Otherwise
the suite runs twice per merge on the one slot every other merge waits behind;
measured from pogod's gate heartbeats over 49 such merges, the redundant second
gate was 34% of all gate wall-clock (median 2m30s per merge). Repos whose
`build.sh` only compiles are untouched — both scripts still run — and a repo
that names `commands` explicitly gets exactly what it names. When the default
drops a script, the merge's gate output says so:

```
(omitting gate ./test.sh: ./build.sh runs it, and running it twice per merge tests nothing new)
```

To gate both deliberately on a repo where one calls the other, name them:

```toml
[gates]
commands = ["./build.sh", "./test.sh"]
```

**Per-repo lanes (`[refinery] max_concurrent_merges`).** Merges are partitioned
into **lanes, one per repo**. Two merge requests for the same repo are never
processed at the same time — they share the refinery's single clone of that repo
(`~/.pogo/refinery/worktrees/<repo>`) and each rebases onto a target ref the
other is about to move. Two merge requests for **different** repos have no such
relationship and run concurrently.

```toml
[refinery]
max_concurrent_merges = 2   # default; 1 = the historic single-slot refinery
```

The cap bounds how many *different* repos may merge at once. It does not, and
cannot, allow two merges for one repo to overlap. Raising it buys parallelism
across more repos at the cost of gate contention: a quality gate is the most
expensive thing pogod runs, the host is shared with the polecat fleet, and gates
running against each other inflate one another's wall time until a gate timeout
starts failing branches that were fine. When that happens the failure says so —
the contention record on a timed-out gate reports what the host was doing — but
the cheaper remedy is a lower cap.

Read what is running with `pogo refinery status` (one `Active:` line per lane,
each naming its repo) or `pogo refinery queue` (running merges lead the list).
`QueueLen` still counts pending requests only, so it can be 0 while merges run.

Why it exists: with one global slot, gate cost in the slowest repo set merge
latency for **every** repo. On 2026-08-05 twelve merge requests waited 70 minutes
behind a single gate, seven of them for a repo the refinery was otherwise idle
on — including the fixes for that day's outage. See
[docs/design/refinery-concurrency-design.md](design/refinery-concurrency-design.md).

**QA gate (hardcoded).** Before processing any MR, the refinery scans the
macguffin workspace (`Config.MacguffinDir`, default `~/.macguffin/work`) for a
work item with `type: qa` whose `source` matches the MR author (the work-item
ID behind the branch). If a matching QA item sits in a pending state
(`available` / `claimed` / `pending`), the merge is **held** — moved to
`held` status and re-queued at the tail so other MRs proceed. The merge runs
only once the matching QA item reaches `done`/`archive`, or when no matching QA
item exists at all (the gate is opt-in per work item, but always-on as a
mechanism). This is enforced in code — `internal/refinery/qa_gate.go`, called
from `holdForQA` in `internal/refinery/lanes.go` — not a layered or optional
pattern. A held merge releases its repo's lane, so it does not block other
merges for the same repo while it waits.
The only knob is `MacguffinDir`: set it empty to disable the gate entirely.
There is no per-project, per-repo, or per-branch toggle.

The companion convention is the `polecat-qa` prompt template
(`internal/agent/prompts/templates/polecat-qa.md`), which scripts the polecat
that completes a QA item — verifying the source work item's acceptance criteria
and reporting pass/fail. The refinery's gate enforces the existence and
completion of the QA item independently of which polecat actually runs it.

**Gate progress and timeout (`[gates] timeout`).** A running gate emits a
heartbeat **every 30 seconds** — to pogod's log and to the merge request's
`progress` field — so a gate that is merely slow is distinguishable from one
whose runner has died. Check it without restarting anything:

```bash
pogo refinery show <mr-id>            # the Verdict: line reads the record for you
pogo refinery show <mr-id> --json | jq .progress
```

`Verdict:` says one of three things: `ALIVE and working` (heartbeat fresh, gate
producing output — wait), `DEAD` (no heartbeat for more than 3 intervals — the
runner is gone, waiting will not help), or `ALIVE, gate silent` (the runner is
fine but the gate has said nothing, which cannot be resolved from outside — the
line reports that honestly and names the timeout that bounds it).

A single gate is bounded at **60 minutes** by default. Override per repo in
`<repo>/.pogo/refinery.toml`:

```toml
[gates]
timeout = "90m"   # Go duration, or a bare number of minutes
# timeout = "0"   # remove the bound entirely (a gate then runs until pogod stops)
```

An unreadable value (`timeout = "eventually"`) logs a warning and keeps the
default bound rather than silently removing it. A timed-out gate fails the MR
with what was observed while it ran — elapsed time, lines of output produced,
how long it had been silent — so a bound set too low is diagnosable rather than
mysterious. See ARCHITECTURE.md §"Telling a slow gate from a dead one" for why
the heartbeat and the timeout ship together.

**Cancelling.** `pogo refinery cancel <mr-id>` works on a **processing** merge
request as well as a queued one. A queued MR is removed immediately; a
processing one has its running gate killed and stops at the next step boundary —
that is a request, not a final status, so poll `pogo refinery show <mr-id>` for
the outcome. An MR that had already pushed to the target still resolves as
`merged`. A cancelled MR does not count as a failure for its author and does not
reopen its work item.

## `pogo install`

`pogo install` is one-step setup: start pogod, run `mg init`, and install the
default agent prompts to `~/.pogo/agents/`. It is idempotent — stale canonical
prompts are auto-updated, user edits preserved (`--force` overwrites, backing up
to `<name>.bak.<timestamp>`). The bundled `install.sh` runs it as its final
step; opt out with `--no-pogo-install` or `POGO_NO_POGO_INSTALL=1` (mg-6bfd).
See [docs/customizing.md](customizing.md).

## Role default-migration guard

pogo never writes `config.toml` on its own, and the role-name defaults
(`coordinator = "mayor"`, `worker = "pogocat"`) live only in code — `Load()`
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
