# Event Log Schema

Append-only event log for pogo. Captures agent lifecycle, work item transitions, mail, nudges, and refinery merges so the system is observable without coordination overhead.

This document is the design contract for phase F (work items mg-0241, mg-700a, mg-22ed, mg-4fa7, mg-287e, mg-156b, mg-214a). F1 (this doc) defines the schema. F2 onward wires emission into pogod, agent lifecycle, mail, nudge, and the refinery.

## File

- **Path:** `~/.pogo/events.log`
- **Format:** JSONL (one JSON object per line, UTF-8, terminated by `\n`)
- **Mode:** append-only. Writers must `O_APPEND | O_WRONLY | O_CREAT` and emit a single line per event. No edits, no deletes (rotation is handled by the writer — see F7 below).
- **Concurrency:** multiple writers (pogod, mg, mail, refinery, polecats) may append concurrently. POSIX `write(2)` of a single line ≤ `PIPE_BUF` (4096 on Linux, 512 on macOS) is atomic against other appenders. Events larger than `PIPE_BUF` must use a process-level mutex or `flock(2)` on the file. Implementations should keep the JSON object well under 512 bytes whenever possible; lines that exceed it must take an advisory lock.
- **Persistence:** survives pogod restarts (unlike the in-memory refinery history). This is the durable observability spine.
- **Not coordination:** the log is purely observational. It is not used to drive state transitions. macguffin remains the source of truth for work item state.

## Aggregate reliability: the test-contamination cutoff (mg-e06d)

**Per-record integrity holds; pre-cutoff aggregates do not.** From **2026-06-21 until the mg-e06d fix landed**, running `go test ./internal/scheduler` on a developer or CI machine appended real `schedule_removed` records to the operator's live `~/.pogo/events.log`. The scheduler's event emitters resolved the log path *globally* (to `$POGO_HOME/events.log`) instead of from the root the caller handed the scheduler, so a temp-rooted test store still wrote its audit events to the real log. Fixture agents you will see from this window — `mail-check-cat-dead`, `mail-check-cat-alsodead`, `cat-ghost`, `cat-bye`, `mg-e633`, and similar — are **test artifacts, not production events**.

Consequences, and how to read the log around them:

- **A specific record is still trustworthy.** Each line is authoritative for its own `schedule_id`/`agent`/`reason`, so an operator can still answer "why did *this* schedule disappear?" from the log alone — the property `emitSchedulerRemovalEvent` exists to provide (mg-8e5d). This is why the mayor and pa correctly falsified a data-loss theory from it.
- **Any COUNT or RATE over the pre-cutoff window is contaminated.** Aggregates such as "how many `agent_gone` events this hour" mix production churn with test churn and are not production statistics. Nothing in an individual line marks it as test-originated, so aggregates cannot self-clean.
- **Fixture records may appear duplicated under one `schedule_id`** (e.g. `mail-check-cat-dead` reaped twice in a tick) — an artifact of parallel/re-run test binaries, another reason not to trust pre-cutoff aggregates.

**What was deliberately NOT done:** the existing records were **not** rewritten, deleted, or truncated. The log is append-only and the architect's mg-0a89 acceptance tamper-check depends on that per-record property (a record's existence and integrity may be verified; aggregates may not be reasoned over). No marker record was appended to the live log either — that would be one more write to operator state, and the fix's job is to stop *new* pollution, not to edit the log. This documented cutoff **is** the marker: records with the fixture agents above, dated before the mg-e06d fix, are contaminated-in-aggregate; the fix stops any further test writes so aggregates computed over the post-cutoff window are clean.

See `internal/scheduler/events_isolation_test.go` for the regression guard (with a positive control) that keeps this closed, and mg-4fa7 for the same defect class fixed in mg.

## The writer is test-safe by default (mg-3f1b)

The mg-e06d fix above was a **point fix in the scheduler**, and so was mg-c33e's later repair of an `internal/agent` test helper. Neither touched the thing that made "live" the fallback in the first place: `resolvePath()` in `internal/events` resolved an **empty** override to `DefaultLogPath()`. The zero value pointed at the operator's real log, so a test only stayed out of it by *remembering* to call `SetLogPathForTesting`. events.log was polluted twice on that shape — mg-e06d's three-week window above, and six phantom `auto_renudge` rows on 2026-07-20.

Since mg-3f1b the writer follows the default ratified at `ARCHITECTURE.md:433-447` (mg-da48) and implemented at `internal/agent/witness.go:196`: **under a test binary (`testing.Testing()`), an empty override resolves to a per-process temp file and the live log is not reachable from `resolvePath` at all.** An opt-in guard is only ever remembered by the tests that least need it — `internal/refinery` and `internal/agent` sandbox because the log is near their subject, while a test that emits an event incidentally has no reason to know this store exists.

Two things this deliberately does **not** change:

- **`SetLogPathForTesting` still wins, and `""` is still sayable.** One test picking its own path is isolation from *other tests* — a different and legitimate question the default does not answer. The empty sentinel was made *safe*, not un-representable.
- **Subprocess tests are unaffected.** The branch turns on whether *our* binary is a test binary. A test that boots real pogod as a child leaves that child resolving `POGO_HOME` exactly as production does, which is correct.

The guard is `internal/events/default_sandbox_test.go`. It runs its acceptance check against a verbatim replica of the pre-fix resolver first and **requires that replica to fail** — a default exercised only by tests that already set an override has not been tested, which is precisely how this survived two incidents and one ratification.

## Envelope

Every line is a JSON object with the same envelope:

| Field           | Type   | Required | Description |
|-----------------|--------|----------|-------------|
| `schema_version`| int    | yes      | Schema version. `1` for the initial release. Bump on incompatible changes. |
| `timestamp`     | string | yes      | RFC3339 with nanosecond precision, UTC. Example: `"2026-04-25T17:42:09.123456789Z"`. |
| `event_type`    | string | yes      | One of the event types in the catalog below. Dot-separated namespace is reserved for future expansion (e.g. `agent.spawned`); v1 uses flat names listed below. |
| `agent`         | string | see note | The acting agent's identity. `"mayor"`, `"crew-arch"`, `"cat-mg-0241"`, `"refinery"`, `"mg"`, `"human"`, etc. Required for every event except those with no clear actor (none in v1, so effectively always required). |
| `work_item_id`  | string | optional | macguffin work item ID (e.g. `"mg-0241"`). Required for events that reference a specific item; omitted when no item is in scope (e.g. `agent_spawned` for crew). When absent, the field is omitted entirely (not emitted as `""` or `null`). |
| `repo`          | string | optional | Absolute path to the repository the event pertains to. Omitted when not applicable. |
| `details`       | object | yes      | Event-specific payload. Always present, even if `{}`. Schema is defined per `event_type` below. Unknown keys are tolerated by readers — additive changes do not require a `schema_version` bump. |

### Schema versioning

`schema_version: 1` is the initial value. Rules:

- **Additive changes to `details`** (new optional keys) do **not** bump the version. Readers must ignore unknown `details` keys.
- **Adding a new envelope field** that is optional does **not** bump the version.
- **Adding a new `event_type`** does **not** bump the version. Readers must skip unknown event types without erroring.
- **Renaming or removing fields, or changing semantics** of an existing field bumps `schema_version` to `2` and triggers a migration plan documented in this file.

This means v1 readers can consume future v1 logs even after we add new event types or details keys. Only breaking changes force a version bump.

### Time

`timestamp` is recorded by the writer using its local clock at the moment of emission. RFC3339Nano (Go's `time.RFC3339Nano`) is required so the log is sortable as text and round-trips through `jq` cleanly. UTC ("Z" suffix) is required — local timezones are forbidden so that logs from different machines or after DST changes remain comparable.

### Identity conventions

- **Crew agents:** `crew-<name>` (matches the agent's display label `pogo-crew-<name>` — the string `pogo agent list` shows and `/agents` returns as `process_name` — minus the `pogo-` prefix; the label is not a process name and is not discoverable with `pgrep`). Examples: `crew-arch`, `crew-ops`. Exception: the coordinator uses its bare configured name (`[agents]` coordinator, default `mayor`) with no `crew-` prefix.
- **Polecats:** `cat-<work-item-id>` for polecats spawned from a work item, `cat-<id>` for free polecats. Examples: `cat-mg-0241`, `cat-a3f`.
- **System actors:** `refinery`, `mg`, `pogod`, `human` for events not attributable to a Claude Code agent.

## Event Catalog (v1)

Event types are grouped below. For each: required envelope fields, `details` schema, and an example JSON line.

In every example, the line is shown wrapped for readability. The actual on-disk format is a single line with no internal whitespace beyond what JSON requires.

### Agent lifecycle

#### `agent_spawned`

A crew or polecat process has been started by pogod (PTY allocated, Claude Code launched).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **Optional envelope:** `work_item_id` (set for polecats with an assigned item), `repo` (the polecat's worktree, if any)
- **`details` fields:**
  - `agent_type` (string, required): `"crew"` or `"polecat"`
  - `pid` (int, required): operating system PID
  - `prompt_file` (string, required): absolute path to the prompt markdown
  - `worktree` (string, optional): absolute path to the polecat worktree, if applicable

```json
{"schema_version":1,"timestamp":"2026-04-25T10:00:00.000000000Z","event_type":"agent_spawned","agent":"cat-mg-0241","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"agent_type":"polecat","pid":48213,"prompt_file":"/Users/daniel/.pogo/agents/templates/polecat.md","worktree":"/Users/daniel/.pogo/polecats/pc-0241"}}
```

#### `agent_spawn_failed`

A polecat dispatch was refused or failed, and **no agent process was created**. The counterpart to `agent_spawned`: emitted on every failure path of `/agents/spawn-polecat`, including the drain-gate refusal.

Read this event as the cause of a gap in the spawn record. Without it, a work item with no `agent_spawned` line is ambiguous — a throttled dispatch, a failed dispatch, and a dispatch never attempted all emit the identical nothing, and a reader reconstructing the history has to supply a mechanism from imagination. That is not a hypothetical failure mode: it produced a false "the dispatch cap throttled it" finding that was written into a ticket and mailed as a stop order (mg-d22a). An absence is a fact with a cause, and the cause is only recoverable if the system emitted it.

Note the identity: `agent` names the polecat that was *intended*. It does not exist and never did — that is the point of the event. A request too malformed to name an agent is attributed to `pogod`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **Optional envelope:** `work_item_id` (the item being dispatched), `repo` (the target repository)
- **`details` fields:**
  - `agent_type` (string, required): always `"polecat"`
  - `agent_name` (string, required): the intended agent name; empty when the request could not be parsed
  - `status_code` (int, required): the HTTP status returned to the caller. `503` is a retryable throttle (drain); `409` is a conflicting branch; `4xx` otherwise is a bad request; `5xx` is a genuine failure
  - `reason` (string, required): the underlying error, verbatim — e.g. `"worktree creation failed: exit status 255\nfatal: a branch named 'polecat-4bd4' already exists"`

```json
{"schema_version":1,"timestamp":"2026-07-17T13:21:29.000000000Z","event_type":"agent_spawn_failed","agent":"cat-4bd4","work_item_id":"mg-4bd4","repo":"/Users/daniel/dev/pogo","details":{"agent_type":"polecat","agent_name":"4bd4","status_code":500,"reason":"worktree creation failed: exit status 255\nfatal: a branch named 'polecat-4bd4' already exists"}}
```

#### `agent_stopped`

An agent process exited cleanly (received stop signal, completed task, or `pogo agent stop` was issued).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:**
  - `pid` (int, required)
  - `exit_code` (int, required): process exit code (0 for clean exit)
  - `reason` (string, required): one of `"task_complete"`, `"signal"`, `"requested"`, `"idle_timeout"`. Use `"signal"` only for clean shutdown signals (SIGTERM); see `agent_crashed` for unexpected exits.
  - `duration_seconds` (number, optional): wall-clock seconds since `agent_spawned`

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:14.555000000Z","event_type":"agent_stopped","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"pid":48213,"exit_code":0,"reason":"task_complete","duration_seconds":1394.555}}
```

#### `agent_crashed`

An agent process exited unexpectedly (non-zero exit code, killed by signal other than SIGTERM, or pogod detected hang).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:**
  - `pid` (int, required)
  - `exit_code` (int, required): non-zero, or -1 if killed by signal
  - `signal` (string, optional): signal name if killed (e.g. `"SIGKILL"`, `"SIGSEGV"`)
  - `last_output` (string, optional): tail of PTY ring buffer, truncated to ~512 bytes for log size discipline

```json
{"schema_version":1,"timestamp":"2026-04-25T11:02:47.200000000Z","event_type":"agent_crashed","agent":"crew-arch","details":{"pid":47011,"exit_code":-1,"signal":"SIGKILL","last_output":"... claude: out of memory"}}
```

#### `agent_restarted`

A crew agent that crashed has been automatically restarted by pogod's supervisor loop. Polecats are never restarted (they're ephemeral) — this event applies only to crew.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:**
  - `previous_pid` (int, required): PID of the crashed process
  - `new_pid` (int, required): PID of the new process
  - `restart_count` (int, required): cumulative restart count for this agent since pogod started

```json
{"schema_version":1,"timestamp":"2026-04-25T11:02:50.310000000Z","event_type":"agent_restarted","agent":"crew-arch","details":{"previous_pid":47011,"new_pid":47089,"restart_count":2}}
```

#### `agent_attach_rebound`

pogod repaired an agent's attach socket while the agent process kept running. The
socket had stopped serving connections — see `reason` — so `pogo agent attach`
would have failed against a live, healthy agent. Emitted once per repair; the
agent is not restarted and loses no state. A steady trickle of these for one
agent points at whatever keeps breaking the socket (fd exhaustion, a tmp reaper
if this root fell back to `$TMPDIR`) rather than at the attach mechanism itself.

Before mg-8532 a steady `socket_file_replaced` trickle had one more cause: a
second pogod on a *different* `POGO_HOME` binding the same `$TMPDIR`-derived
socket path, so the two daemons unlinked and rebound each other's live socket
every 30s. Socket paths now derive from `PogoHome()`, so two daemons on distinct
roots can no longer collide. Seeing this reason repeat on a modern pogod means
something outside pogo is replacing the file.

Repairs are rate-limited: a listener that fails again the instant it is rebound
(a recurring permanent `accept(2)` error) backs off from 50ms to a ceiling of
30s, so a persistently broken socket cannot flood this log. The backoff resets
once a listener has stayed healthy for five minutes, so unrelated faults hours
apart each get an immediate repair. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:**
  - `pid` (int, required): PID of the still-running agent process
  - `socket` (string, required): path of the rebound unix socket
  - `reason` (string, required): one of
    - `accept_loop_stopped` — the accept loop exited under a live process. The socket file lingers with nothing accepting, so once the listen backlog fills, attach gets `connection refused`.
    - `no_listener` — no listener was ever bound (e.g. the bind at spawn failed under fd exhaustion).
    - `socket_file_missing` — the socket file was unlinked underneath a live listener.
    - `socket_file_replaced` — a different socket now occupies the path.

```json
{"schema_version":1,"timestamp":"2026-07-10T09:12:03.410000000Z","event_type":"agent_attach_rebound","agent":"crew-mayor","details":{"pid":23884,"socket":"/Users/daniel/.pogo/agents/sockets/mayor.sock","reason":"accept_loop_stopped"}}
```

#### `agent_workspace_freshened`

A crew agent's long-lived checkout at `$POGO_HOME/agents/<name>/repo` was evaluated against its upstream at agent start, **before** the harness process was spawned. Emitted for every verdict except `skipped` — most agents keep no `repo/` at all, and emitting for that would drown the records that matter.

Nothing else keeps these checkouts fresh: the refinery fast-forwards the checkout an MR was *submitted* from, and polecat worktrees are branched from current `origin/main`, but a crew agent's own `repo/` sits outside both paths. One was found 129 commits / ~2 months behind `main`, by accident (mg-d5fc).

The check runs at start specifically because that is the only instant the checkout provably has no reader — so a refresh can never move the ground under an agent mid-edit. It never touches a tree with staged or unstaged changes to tracked files; a dirty stale checkout is declined and reported, never clobbered.

`behind` is derived from `git rev-list HEAD...FETCH_HEAD` **after** a fetch — not from the local remote-tracking ref, which on a stale checkout is exactly as old as its last fetch, and not from path existence at the upstream ref, which cannot distinguish a superseded revision from a current one.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `repo`, `details`
- **`details` fields:**
  - `status` (string, required): one of
    - `updated` — was behind, was fast-forwarded.
    - `already_current` — HEAD already contained the upstream tip.
    - `declined_dirty` — behind, but a tracked file is modified or staged. **Not touched.**
    - `declined_detached` — HEAD is detached; no branch to advance.
    - `declined_no_upstream` — the branch tracks nothing, so "behind" has no referent.
    - `declined_diverged` — behind *and* ahead; no fast-forward exists.
    - `failed` — git itself failed, so freshness is **unknown**. Explicitly not a clean bill of health.
  - `branch` (string, optional): the checked-out branch; absent when detached.
  - `upstream` (string, optional): the tracking ref, e.g. `origin/main`.
  - `behind` (int, required): commits the upstream had that HEAD did not. `-1` means *undetermined* (detached, no upstream, or the fetch failed) — distinct from `0`, which is a positive finding of currency.
  - `ahead` (int, required): commits HEAD had that the upstream did not; `-1` when undetermined.
  - `detail` (string, optional): the decline reason or git error.

A `declined_*` or `failed` verdict with `behind` > 0 also mails the coordinator. That is not a tuned threshold: it is the binary fact "this checkout is known to be rotting and nothing here can stop it", which is precisely what went unnoticed for two months.

```json
{"schema_version":1,"timestamp":"2026-07-21T01:14:02.100000000Z","event_type":"agent_workspace_freshened","agent":"pm-onethird","repo":"/Users/daniel/.pogo/agents/pm-onethird/repo","details":{"status":"declined_dirty","branch":"main","upstream":"origin/main","behind":129,"ahead":0,"detail":"83 tracked path(s) modified or staged; commit or stash, then 'git pull'"}}
```

### Polecat-specific lifecycle

`agent_spawned` and `agent_stopped` already cover polecats. The two events below give polecat lifecycle a dedicated, work-item-scoped narrative for tools that want to filter by polecat events without inferring from `agent_type`.

#### `polecat_spawned`

A polecat has been spawned for a specific work item. Emitted in addition to `agent_spawned` to make polecat-specific queries cheap.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `repo`, `details`
- **`details` fields:**
  - `template` (string, required): name of the polecat template (e.g. `"polecat"`, `"researcher"`)
  - `branch` (string, required): branch name the polecat will work on (e.g. `"polecat-mg-0241"`)
  - `parent` (string, optional): identity of the spawning agent (`"mayor"`, `"human"`)

```json
{"schema_version":1,"timestamp":"2026-04-25T10:00:00.150000000Z","event_type":"polecat_spawned","agent":"cat-mg-0241","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"template":"polecat","branch":"polecat-mg-0241","parent":"mayor"}}
```

#### `polecat_completed`

A polecat reached terminal state — task complete, branch pushed, refinery submission made (or skipped on failure path). Emitted before `agent_stopped`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **`details` fields:**
  - `outcome` (string, required): `"merged"`, `"merge_failed"`, `"abandoned"`, `"errored"`
  - `branch` (string, required)
  - `merge_request_id` (string, optional): refinery MR ID, if submission was attempted
  - `commits` (int, optional): number of commits the polecat made on its branch

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:10.000000000Z","event_type":"polecat_completed","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"outcome":"merged","branch":"polecat-mg-0241","merge_request_id":"mr-9482","commits":1}}
```

### Work item transitions

These are the events `mg` itself emits when a work item changes state. They duplicate information available in macguffin's own state files, but mirroring them into the unified event log lets a single `tail -f` see the full system narrative.

#### `work_item_claimed`

An agent claimed a work item via `mg claim`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **`details` fields:**
  - `title` (string, required): work item title at time of claim
  - `tags` (array of strings, optional)

```json
{"schema_version":1,"timestamp":"2026-04-25T09:59:55.000000000Z","event_type":"work_item_claimed","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"title":"F1: Design event log schema (JSONL at ~/.pogo/events.log)","tags":["pogo","event-log","phase-f"]}}
```

#### `work_item_completed`

An agent marked a work item done via `mg done`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **`details` fields:**
  - `result` (object, optional): the JSON result blob passed to `mg done --result=...`. Free-form per work item; commonly contains `branch`, `mr_id`, summary text.

```json
{"schema_version":1,"timestamp":"2026-04-25T10:22:45.000000000Z","event_type":"work_item_completed","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"result":{"branch":"polecat-mg-0241"}}}
```

#### `work_item_claim_released`

pogod released a stopped polecat's claim, returning its work item to
`available/` (mg-fb13). Emitted by the agent registry, not by `mg` — the two
events below are the only ones in this section whose actor is the daemon rather
than the CLI, and `agent` is the stopped polecat's identity, not pogod's.

Emitted only when a claim was actually held at stop time, so it marks work that
was interrupted rather than finished. A polecat stopped after its own `mg done`
(or after pogod recorded done on its behalf at merge — see `refinery_merged`)
holds no claim and emits nothing here. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **`details` fields:**
  - `pid` (int, required): pid of the stopped polecat — the pid the claim would otherwise have been stranded under
  - `reason` (string, required): why the claim was released. `"agent_stopped"` is the only v1 value.

```json
{"schema_version":1,"timestamp":"2026-07-26T13:40:12.000000000Z","event_type":"work_item_claim_released","agent":"cat-mg-0241","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"pid":48213,"reason":"agent_stopped"}}
```

#### `work_item_claim_release_failed`

The release above was attempted and failed, so the work item **is** stranded in
`claimed/` under a pid that no longer exists. Nothing downstream will recover it
on its own: claimed items are never dispatched, and the stall watcher only scans
`available/`. Treat this as the page-worthy case and run `mg unclaim <id>`.

The stop itself still succeeded — the process is already gone by the time the
release runs, so a failure here does not fail the teardown. Additive — no
`schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **`details` fields:**
  - `pid` (int, required): pid of the stopped polecat
  - `error` (string, required): the failure, including `mg`'s own message when it produced one

```json
{"schema_version":1,"timestamp":"2026-07-26T13:40:12.000000000Z","event_type":"work_item_claim_release_failed","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"pid":48213,"error":"mg unclaim mg-0241: permission denied"}}
```

### Inter-agent communication

#### `mail_sent`

An agent sent macguffin mail (`mg mail send`).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **Optional envelope:** `work_item_id` (if the mail references one)
- **`details` fields:**
  - `to` (string, required): recipient identity (e.g. `"mayor"`, `"crew-arch"`)
  - `subject` (string, required)
  - `mail_id` (string, optional): macguffin mail ID, if assigned

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:00.000000000Z","event_type":"mail_sent","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"to":"mayor","subject":"merge failed for mg-0241","mail_id":"mail-2f81"}}
```

#### `nudge_sent`

`pogo nudge` wrote text to a running agent's PTY (or fell back to mail).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:**
  - `to` (string, required): target agent identity
  - `message` (string, required): the nudge text
  - `delivery` (string, required): `"pty"` (delivered to live session) or `"mail_fallback"` (target not running, queued as mail)
  - `mode` (string, optional): how delivery was established — `"idle"` (waited for quiescence, then wrote and assumed), `"immediate"` (wrote with no precondition), `"ready"` (initial nudge, past the prompt-ready gate), or one of the confirmed-delivery outcomes: `"confirm"` (the message alone drew a submission receipt), `"confirm-bare-return"` (a bare return submitted text the harness had left unsent), `"confirm-resend"` (the message had to be sent twice). The last three are the only values that mean the AGENT reported receiving it; the rest mean pogod wrote bytes. See mg-ebee.
  - `fire_token` (string, optional): correlation id, present when the nudge carries a scheduler fire. Joins to `scheduler_fire_delivered` and `scheduler_fire_completed` on the same value.

```json
{"schema_version":1,"timestamp":"2026-04-25T10:15:30.000000000Z","event_type":"nudge_sent","agent":"mayor","details":{"to":"crew-arch","message":"check your mail","delivery":"pty","mode":"idle"}}
```

#### `nudge_suppressed`

The wake-cycle policy declined an automated WAKE before it reached the PTY
(mg-8184). It is the counterpart of `nudge_sent`: between the two — and
`nudge_unconfirmed` below — every wake pogod decided on leaves exactly one
record, so "no nudge_sent" is never ambiguous between *declined*, *attempted
but unprovable*, and *never attempted*.

Only **wakes** pass through the policy — the stall watcher's fire and a
scheduler fire landing on a live PTY. Spawn kickoffs and operator nudges
(`pogo nudge`) are not governed and never produce this event.

A suppressed wake is not a lost message: callers with a durable channel fall
back to mail. What was suppressed is the terminal write.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:**
  - `to` (string, required): target agent identity
  - `message` (string, required): the wake text that was not written
  - `rule` (string, required): which rule declined —
    `"limit_episode"` (the agent, or the fleet, is inside a known provider
    limit episode) or `"wake_silence_once"` (an earlier wake already spoke into
    this unbroken silence)
  - `reason` (string, required): the human-readable detail behind the rule
  - `fire_token` (string, optional): correlation id, as for `nudge_sent`

```json
{"schema_version":1,"timestamp":"2026-07-29T11:15:30.000000000Z","event_type":"nudge_suppressed","agent":"pogod","details":{"to":"crew-mayor","message":"stall-watch: work piling up","rule":"limit_episode","reason":"usage-limit episode ep-1753-cat-mg-7ffa open since 2026-07-29T10:40:00Z (3 agent(s) rate-limited)"}}
```

#### `nudge_unconfirmed`

A nudge in confirmed-delivery mode that pogod could **not** prove landed. Added
by mg-ebee, and it exists so that an unproven delivery sits in the log next to
the proven ones rather than vanishing into a caller's error string — the same
invisibility that let 771 `nudge_sent` records coexist with a fleet where
nothing consumed them (see "The 2026-07-22 outage" below).

Distinct from `nudge_suppressed`: there, nothing was written and the decision
was pogod's. Here the bytes went out and the *agent* did not report receiving
them.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:**
  - `to` (string, required): target agent identity
  - `message` (string, required): the nudge text
  - `delivery` (string, required): always `"pty"` — the bytes were written
  - `mode` (string, required): always `"confirm"`
  - `outcome` (string, required):
    - `"queued"` — written to a harness that was **mid-turn**. Claude Code takes such a prompt and acts on it but fires no `UserPromptSubmit` for it, so the receipt cannot move: pogod can neither confirm nor deny, and does not retry (a resend would deliver the instruction twice). The scheduler deliberately withholds its mail fallback for this outcome alone. See `docs/investigations/confirmed-nudge-delivery-2026-07-29.md`.
    - `"refused"` — the full escalation ran (the message, a bare return, the message again) and the harness recorded nothing. **Nobody received it.** This is the outcome that used to be reported as success.
  - `fire_token` (string, optional): as for `nudge_sent`.

```json
{"schema_version":1,"timestamp":"2026-07-29T09:00:14.000000000Z","event_type":"nudge_unconfirmed","agent":"pogod","details":{"to":"crew-pm-pogo","message":"run the morning sweep","delivery":"pty","mode":"confirm","outcome":"queued","fire_token":"9f3c1ab2"}}
```

Additive — no `schema_version` bump. A reader that counts only `nudge_sent`
keeps working, but it is now counting *confirmed* deliveries plus the legacy
write-and-hope modes, and `nudge_unconfirmed` is where the rest went.

### Scheduler
### Scheduler

#### `scheduler_fire_delivered`

pogod delivered a scheduled fire to an agent (PTY nudge, or mail fallback).

**Read this event with care.** It records that the BYTES arrived, which is only
half the transaction. During the 23h30m fleet outage of 2026-07-22 it logged
**647 successful deliveries** while every consuming turn failed in ~10ms on an
expired credential. Every record was true; none was useful. The completion
fields below (added by mg-a754) exist so this event can no longer describe a
100%-dead fleet the same way it describes a healthy one.

- **`details` fields:**
  - `schedule_id`, `to`, `delivery`, `original_due`, `fired_at`, `missed_fires`, `replay_policy`, `one_shot`, `cron`
  - `fire_token` (string): the completion nonce issued with this fire. The agent redeems it with `pogo schedule ack`.
  - `fires_delivered` / `fires_completed` (int): lifetime counters for this schedule, persisted in `schedules.json` so they survive a pogod restart.
  - `completion_tracked` (bool): whether this schedule has EVER been acked. When `false`, the recipient is not participating in completion tracking and no conclusion may be drawn from a missing ack — the state is UNKNOWN, not failing.
  - `unacked_streak` (int, present only when `completion_tracked` is true): consecutive delivered-but-unacked fires, **including this one**. A promptly-acking agent reads `1`. A climbing value is the signal: the mayor's would have read `202` by the end of 2026-07-22.

```json
{"schema_version":1,"timestamp":"2026-07-22T12:00:00.000000000Z","event_type":"scheduler_fire_delivered","agent":"pogod","details":{"schedule_id":"sweep-morning","to":"pm-pogo","delivery":"nudge","fired_at":"2026-07-22T12:00:00Z","fire_token":"9f3c1ab2","fires_delivered":143,"fires_completed":0,"completion_tracked":true,"unacked_streak":143}}
```

#### `scheduler_fire_completed`

The agent reported that the work a fire triggered has finished, by running
`pogo schedule ack <id> --agent <a> --token <tok>`.

This is the counterpart signal to `scheduler_fire_delivered`. Producing it
requires a live model turn that executed a tool — which a synthetic
zero-token failure turn (`Login expired`, `You've hit your weekly limit`,
rate-limit 5xx) cannot do. That is the property that makes it useful: it fails
in the same direction as the work it measures. It is also harness-independent,
unlike transcript inspection, which only Claude Code supports today.

- **`details` fields:**
  - `schedule_id`, `to`, `cron`
  - `fire_token` (string): the redeemed nonce. Joins back to the delivery event.
  - `completed_at` (string, RFC3339)
  - `latency_ms` (int): wall time from delivery to acknowledgement.
  - `fires_delivered` / `fires_completed` (int): counters after this completion.

```json
{"schema_version":1,"timestamp":"2026-07-23T09:04:12.000000000Z","event_type":"scheduler_fire_completed","agent":"pogod","details":{"schedule_id":"sweep-morning","to":"pm-pogo","fire_token":"9f3c1ab2","completed_at":"2026-07-23T09:04:12Z","latency_ms":252000,"fires_delivered":144,"fires_completed":144}}
```

**How to read the pair.** A missing completion is not a per-fire verdict — an
agent can simply forget to ack. The signal that matters is fleet-wide and
ratioed: one agent skipping one ack is noise; every tracked schedule in the
fleet going to zero within the same minute is one upstream cause (expired
credential, rate limit, spend cap) and should page a human rather than trigger
N independent restarts. `pogo schedule completion` is the query.

#### `scheduler_fire_failed` / `scheduler_fire_skipped`

Delivery errored, or `ReplaySkip` elided a stale fire. Neither issues a
completion token: no bytes reached the agent, so no turn ran and there is
nothing to complete. This keeps "the fire did not arrive" distinct from "the
fire arrived and accomplished nothing" — the two faults this pair of signals
exists to separate.

### Refinery

These events are the **only** durable record of a completed merge. The refinery's in-memory history is pruned destructively past `MaxHistoryLen` (100) / `MaxHistoryAge` (7d), and because the count cap bites first at any real merge rate, `pogo refinery history` sees under a day. `pogo refinery history --since=<duration|date>` reconstructs merge requests from these events instead (`refinery.HistoryFromLog`), which is how a question about last week gets an answer at all.

That reader states its own bound: it reports whether the log reaches back as far as was asked, and the CLI exits non-zero when it does not. The bound is real — rotation discards the oldest chunk once all `maxRotatedFiles` slots are full — so a reconstruction that claimed completeness unconditionally would be the same defect one layer up (mg-e9ee).

#### `refinery_merge_attempted`

The refinery picked a merge request off the queue and started its pipeline (fetch, rebase, run gates).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `repo`, `details`
- **Optional envelope:** `work_item_id` (the work item the branch is for, if known)
- `agent` is always `"refinery"`.
- **`details` fields:**
  - `merge_request_id` (string, required)
  - `branch` (string, required)
  - `target` (string, required): merge target branch (e.g. `"main"`)
  - `attempt` (int, required): 1-indexed attempt number
  - `author` (string, required): submitting agent (e.g. `"cat-mg-0241"`)

```json
{"schema_version":1,"timestamp":"2026-04-25T10:22:50.000000000Z","event_type":"refinery_merge_attempted","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","attempt":1,"author":"cat-mg-0241"}}
```

#### `refinery_merged`

The refinery successfully merged a branch (gates passed, fast-forward push to target succeeded).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `repo`, `details`
- **Optional envelope:** `work_item_id`
- **`details` fields:**
  - `merge_request_id` (string, required)
  - `branch` (string, required)
  - `target` (string, required)
  - `merge_commit` (string, required): SHA of the merge commit (or fast-forwarded HEAD)
  - `attempt` (int, required): attempt number that succeeded (`0` when no merge attempt ran: restart recovery found the merge already pushed, or the branch was already merged at processing time)
  - `author` (string, required since mg-e9ee): submitting agent (e.g. `"cat-mg-0241"`). Carried on the outcome events, not only on `refinery_merge_attempted`, so a reader reconstructing history from the log can name the author of a merge whose attempt event has rotated out from under it. It is not the same string as `work_item_id`, which is the author with any `cat-` prefix stripped.
  - `duration_seconds` (number, optional): total time from `refinery_merge_attempted` (attempt 1) to merge
  - `already_merged` (bool, optional): `true` when the branch had already landed on the target before processing began (a re-submitted branch, gh #34) — the MR resolved as merged without running gates or pushing, and no `refinery_merge_attempted` event precedes this one

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:09.000000000Z","event_type":"refinery_merged","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","merge_commit":"7f97c8b1a2b3c4d5","attempt":1,"duration_seconds":19.2}}
```

#### `refinery_merge_failed`

A merge attempt failed. Whether this is terminal depends on `attempt` and the configured retry budget — a failed attempt with retries remaining will be followed by another `refinery_merge_attempted`. The final failure is the one whose `terminal` is `true`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `repo`, `details`
- **Optional envelope:** `work_item_id`
- **`details` fields:**
  - `merge_request_id` (string, required)
  - `branch` (string, required)
  - `target` (string, required)
  - `attempt` (int, required)
  - `author` (string, required since mg-e9ee): submitting agent — see `refinery_merged`
  - `stage` (string, required): which pipeline stage failed — `"fetch"`, `"rebase"`, `"closing-ref-check"`, `"build"`, `"test"`, `"push"`, `"unknown"`
  - `reason` (string, required): short error summary, single line, ≤ 200 chars
  - `terminal` (bool, required): `true` if the refinery has given up (no more retries); `false` if another attempt will follow
  - `gate_output_truncated` (string, optional): up to 1 KB of gate stderr/stdout for quick triage. Full output remains in the in-memory MR record (or persisted history once recommendation §1 lands).

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:05.000000000Z","event_type":"refinery_merge_failed","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","attempt":1,"stage":"test","reason":"./test.sh exited with status 1","terminal":false,"gate_output_truncated":"--- FAIL: TestEventEmit ..."}}
```

#### `refinery_merge_cancelled`

An operator stopped a merge with `pogo refinery cancel`, and the pipeline gave up at `stage`. Emitted only for a merge that had already started processing — a queued MR cancelled before it ran never reaches the pipeline and emits nothing.

This is deliberately **not** a `refinery_merge_failed`. A cancelled merge did not fail on its merits, and anything counting merge failures (an author's failure streak, a reliability trend) would otherwise count operator actions as branch defects. There is no `reason` or `terminal` field: the reason is always cancellation, and a cancel is always terminal for the attempt (mg-8595).

Note that a cancel is a request, not a guaranteed outcome. If the merge had already pushed to the target before the cancel landed, the MR resolves as `merged` and a `refinery_merged` event is emitted instead of this one.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `repo`, `details`
- **Optional envelope:** `work_item_id`
- **`details` fields:**
  - `merge_request_id` (string, required)
  - `branch` (string, required)
  - `target` (string, required)
  - `attempt` (int, required): attempt number in flight when the cancel took effect
  - `author` (string, required since mg-e9ee): submitting agent — see `refinery_merged`
  - `stage` (string, required): where the pipeline stopped — the failing-stage vocabulary of `refinery_merge_failed` plus `"before-attempt"`, meaning the cancel landed between attempts rather than inside a gate
  - `gate_output_truncated` (string, optional): up to 1 KB of gate output captured before the kill

```json
{"schema_version":1,"timestamp":"2026-07-29T21:44:02.000000000Z","event_type":"refinery_merge_cancelled","agent":"refinery","work_item_id":"mg-8595","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-8595","target":"main","attempt":1,"stage":"build","gate_output_truncated":"=== Running: ./build.sh ===\n"}}
```

#### `refinery_mr_lost`

Restart recovery could not carry an in-flight merge request forward (branch deleted from origin, remote unreachable, worktree setup failed). The MR moves to the state file's lost list; `refinery show <id>` answers HTTP 410 with `status=lost` so the author can resubmit. See docs/refinery-persistence-design.md (mg-abfd).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `repo`, `details`
- **Optional envelope:** `work_item_id`
- **`details` fields:**
  - `merge_request_id` (string, required)
  - `branch` (string, required)
  - `target` (string, required)
  - `author` (string, required): submitting agent
  - `reason` (string, required): why recovery could not resolve the MR, single line, ≤ 200 chars

```json
{"schema_version":1,"timestamp":"2026-07-02T09:14:02.000000000Z","event_type":"refinery_mr_lost","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","author":"cat-mg-0241","reason":"branch \"polecat-mg-0241\" not found on origin"}}
```

### Daemon watchers

#### `stall_watch_fired`

pogod's stall watcher (gh drellem2/macguffin #12) crossed a work-pile-up threshold for the watched agent (the coordinator, `mayor` by default) and emitted a nudge. One event per offending batch per heartbeat tick, rate-limited by a per-category cooldown. See [stall-watch-design.md](design/stall-watch-design.md).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `category` (string, required): `"unclaimed_items"`, `"unread_mail"`, or `"priority_wake"`
  - `watched_agent` (string, required): the agent that was nudged
  - `nudge_delivery` (string, optional): the channel that carried the nudge — `"pty"` (written to the agent's live terminal), `"mail"` (agent not running, so straight to durable mail), or `"mail_fallback"` (agent running but the PTY nudge failed, so durable mail carried it instead). Absent only when delivery failed outright.
  - `nudge_fallback_reason` (string, optional): present with `"mail_fallback"`; why the PTY channel was not used. **Not an error** — the nudge was delivered.
  - `nudge_error` (string, optional): present only when **every** channel failed and the notice reached nobody; the event is still emitted. Before mg-79dc this field also covered the routine busy-agent case, so historical records carrying it are not all hard failures — see below.

```json
{"schema_version":1,"timestamp":"2026-06-10T16:20:00.000000000Z","event_type":"stall_watch_fired","agent":"pogod","details":{"category":"unclaimed_items","watched_agent":"mayor","item_count":2,"item_ids":["mg-2350","mg-9299"],"age_threshold":"10m0s","oldest_age_seconds":1830.4,"nudge_delivery":"pty"}}
```

  - For `unclaimed_items`: `item_count` (int), `item_ids` ([]string), `age_threshold` (string), `oldest_age_seconds` (float)
  - For `unread_mail`: `unread_count` (int), `max_count` (int), `oldest_age_seconds` (float), `age_threshold` (string), `over_count` (bool), `over_age` (bool)
  - For `priority_wake`: `item_count` (int), `item_ids` ([]string), `wake_delay` (string), `wake_cooldown` (string), `fast_priority` (string), `oldest_age_sec` (float)

**Reading `nudge_error` on records from before 2026-07-17 (mg-79dc):** it meant only "the PTY nudge failed", and the nudge was then **dropped** — there was no mail fallback for a running-but-busy agent. On 2026-07-17, 18 of 47 fires (~38%) carried one, every single instance reading `still producing output after 30s ... context deadline exceeded`. Those fires happened and were never heard, which matters when reasoning backwards from mayor's inbox: **an absent stall notice in that era is not evidence the detector did not fire.** mg-4bd4 concluded the work-item detectors had "never been able to fire on real work" from exactly that absence; the events log falsifies it. Records from mg-79dc onward carry `nudge_delivery`, so a fire that took the durable road is visible as such rather than looking like a failure.

#### `drift_watch_fired`

pogod's drift-check runner (mg-345b) sampled the `[reconcile]` mirrors on its coarse interval and found at least one host artifact drifted from its repo source, so it mailed `human`. It is the DETECTION backstop for the deploy paths the refinery `[deploy]` prevention misses (mg-75f9). **Report-only** — it never reconciles. One event per sample that found drift; the coarse interval rate-limits both the sample and the mail. See [CONFIGURATION.md](CONFIGURATION.md) §"The built-in drift-check runner" and `internal/driftwatch`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `drift_count` (int, required): how many mirrors drifted this sample
  - `mirror_names` ([]string, required): the drifted mirrors, sorted
  - `interval` (string, required): the coarse sample/mail cadence
  - `mail_error` (string, optional): present only when the notice to `human` could not be delivered; the event is still emitted so drift-was-seen is never lost to a down mail channel

```json
{"schema_version":1,"timestamp":"2026-07-17T16:45:00.000000000Z","event_type":"drift_watch_fired","agent":"pogod","details":{"drift_count":1,"mirror_names":["pogod"],"interval":"15m0s"}}
```

#### `gh_teardown_watch_fired`

pogod's gh-issue teardown detector (mg-6e57) sampled the `status=done` gh-issue carriers on its coarse interval and found at least one whose GitHub issue is still open, or whose state could not be established, so it mailed `notify_to` (`pm-pogo` by default — a teardown miss is a fleet workflow failure, not a human decision; mg-b586). It exists because the workflow's last step can silently not run: mg-07ba reached `done, stage: merge` while drellem2/pogo#89 stayed OPEN for four days, and a carrier that completed its teardown is outwardly identical to one that skipped it. **Report-only** — it never closes an issue and never comments. Emitted once per sample that mailed; unchanged findings are re-raised only after `renotify_after`, so this event is not one-per-interval. See [CONFIGURATION.md](CONFIGURATION.md) §"The gh-issue teardown detector" and `internal/ghteardown`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `miss_count` (int, required): done carriers whose issue is still open with no `gh-open:` declaration
  - `indeterminate_count` (int, required): carriers whose issue state could NOT be established (failed `gh` lookup, unresolvable ref). These are **not** clean — an errored lookup and a closed issue are indistinguishable to a careless check, so they are counted separately and reported rather than assumed shut
  - `declared_open_count` (int, required): carriers open on purpose per a `gh-open:` body line; reported but never mailed on their own
  - `scanned` (int, required): how many done carriers were evaluated, so "0 findings" can be told apart from "0 carriers examined"
  - `notified` (string, required): comma-separated mailboxes the notice was sent to, so the routing that actually happened is auditable rather than inferred from config
  - `escalated` (bool, required): true when a finding had gone unresolved past `escalate_after` and `human` was copied in addition to the fleet mailbox
  - `mail_error_<mailbox>` (string, optional): one key per recipient the notice could NOT be delivered to; the event is still emitted so a detected miss is never lost to a down mail channel

```json
{"schema_version":1,"timestamp":"2026-07-21T01:15:00.000000000Z","event_type":"gh_teardown_watch_fired","agent":"pogod","details":{"miss_count":1,"indeterminate_count":0,"declared_open_count":1,"scanned":3,"notified":"pm-pogo","escalated":false}}
```

#### `gh_teardown_watch_error`

The teardown detector could not READ the work-item store, so it audited nothing this sample. Emitted instead of `gh_teardown_watch_fired`, and deliberately not silent: an unreadable store and a clean scan both otherwise render as "no findings", and conflating them is how a detector goes quietly blind — the exact failure shape `internal/ghteardown` exists to catch, reproduced one level up.

- **`details` fields:**
  - `error` (string, required): why the store could not be read

```json
{"schema_version":1,"timestamp":"2026-07-21T01:15:00.000000000Z","event_type":"gh_teardown_watch_error","agent":"pogod","details":{"error":"listing done work items: mg --root ... : command not found"}}
```

#### `ack_watch_fired`

pogod's scheduler-completion deficit detector ([internal/ackwatch](../internal/ackwatch/ackwatch.go), mg-1935) sampled the ack counters `scheduler_fire_completed` maintains and found at least one schedule completing far fewer of its fires than its directly comparable peers, so it mailed `notify_to` (`mayor` by default). It exists because those counters already recorded the fault and nothing consumed them: on 2026-07-29 `mail-check-pm-pogo` read 270/757 against ~751/757 for three peers on an identical cadence, had done so for its entire run, and was found only by a human reading `pogo schedule list` and comparing rows. Every liveness instrument said healthy — a working spinner is itself PTY output, so no output-based check can fire on a spinning agent.

The comparison is **cross-agent**, never against a schedule's own history: the motivating agent was always broken and had no regression to show. A finding needs a rate both far below the peer median and below an absolute floor. **Report-only** — pogod never nudges, restarts, or unregisters anything on this signal. Emitted once per sample that mailed; unchanged findings are re-raised only after `renotify_after`, so this is not one-per-interval. See [CONFIGURATION.md](CONFIGURATION.md) §"The scheduler-completion deficit detector (ack-watch)".

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `deficit_count` (int, required): per-schedule findings — one agent far below its peers
  - `fleet_count` (int, required): whole cohorts below the floor. Everyone at 40% on the same cadence is a **scheduler or fleet** fault (suspect the ack path, an auth outage, or pogod itself), and reporting it as N per-agent alerts would name N innocent agents and bury the one fact that matters
  - `scanned` (int, required): schedules offered to the detector
  - `eligible` (int, required): how many were actually evaluated. The gap between `scanned` and `eligible` is coverage, not health — a schedule with a fresh counter, too few fires, or no comparable peers is **unjudged**
  - `schedules` (array of string, required): the schedule ids named, so the log answers "which one" without opening the mail. Cohort findings appear as `cohort:<kind>/<cadence>`
  - `notified` (string, required): comma-separated mailboxes the notice was sent to
  - `escalated` (bool, required): true when a finding had stood past `escalate_after` and `human` was copied as well. The coordinator is itself a crew agent and can have the very defect reported (mg-d385), so an alert routed only there can reach nobody
  - `mail_error_<mailbox>` (string, optional): one key per recipient the notice could NOT be delivered to; the event is still emitted so a detected deficit is never lost to a down mail channel

```json
{"schema_version":1,"timestamp":"2026-07-29T06:30:00.000000000Z","event_type":"ack_watch_fired","agent":"pogod","details":{"deficit_count":1,"fleet_count":0,"scanned":6,"eligible":4,"schedules":["mail-check-pm-pogo"],"notified":"mayor","escalated":false}}
```

#### `ack_watch_suppressed`

The deficit detector declined to evaluate this sample because the counters are not representative yet: a recent `system_wake` (post-sleep replay makes stale acks expected) or a pogod restart inside the settle window. Both are the same class of known-benign event and share one mechanism rather than getting one each.

Emitted rather than staying silent for the reason this whole package exists: a deliberate silence and a clean scan are otherwise the same absence in the log. Registering a schedule with an existing `--id` also **zeroes its counters**, and every crew agent re-registers on startup — with a nightly redeploy (mg-42ac) that would be a scheduled false-positive storm, so the suppression is load-bearing rather than defensive.

- **`details` fields:**
  - `reason` (string, required): what suppressed it and how long ago
  - `scanned` (int, required): schedules that went unevaluated

```json
{"schema_version":1,"timestamp":"2026-07-29T03:05:00.000000000Z","event_type":"ack_watch_suppressed","agent":"pogod","details":{"reason":"pogod restart 4m0s ago — counters are not representative until 30m0s has elapsed","scanned":6}}
```

#### `ack_watch_clear`

The deficit detector ran, evaluated everything it was able to, and found nothing to report (mg-ddf7). Mail-silent, log-loud: **when a control correctly declines to fire, it says so once, with the circumstances.**

It exists because a silent correct outcome and a control that is not running are the **same observation**. On the 2026-07-29 storm night — 7 polecats against a guideline of 3–5, 15–16 agents firing per hour — the right answer was silence, and this detector would have produced it; nothing anywhere would have recorded that it considered the burst and declined. The only evidence the design worked was that two agents happened to notice the burst and reason about it, which is not a property of the system. At the time of writing this fleet's 60 MB event log contained **zero** `ack_watch_*` events of any kind, and there was no way to tell that from a fleet that had simply been healthy.

The coverage counts are what make the line worth writing, and why they are not optional: `eligible 3` of `scanned 41` and `eligible 41` of `scanned 41` are both no-findings, and only the second is a clean bill of health. Read the skip reasons as **coverage, not health** — an unjudged schedule is unjudged, not well.

Emitted on **every** clear sample rather than only on transitions. `interval` is coarse (30m by default), the event is one line, and a transition-only emit would go quiet during exactly the long calm in which a reader most needs to know the control is still alive — reproducing this package's own bug one level up. Distinct from `ack_watch_suppressed` on purpose: "we declined to look" and "we looked and found nothing" are the two observations this package exists to keep apart.

- **`details` fields:**
  - `scanned` (int, required): schedules offered to the detector
  - `eligible` (int, required): how many were actually judged
  - `skipped_fresh` (int, required): counter reset (registration or re-registration) too recently to describe anything
  - `skipped_few_fires` (int, required): under `min_fires` — a handful of fires is not a sample. This is the gate that silences a storm of freshly-spawned polecats, and it does so by fire count rather than by understanding the mechanism
  - `skipped_not_recurring` (int, required): one-shot or unparseable cron, so no rate is well-defined
  - `skipped_no_peers` (int, required): nothing comparable to compare against, or a cohort in which nobody acks. **Unjudged, not healthy** — and note that an agent alone on its cadence (the mayor, on 30m) is permanently in this bucket

```json
{"schema_version":1,"timestamp":"2026-07-29T22:30:00.000000000Z","event_type":"ack_watch_clear","agent":"pogod","details":{"scanned":41,"eligible":3,"skipped_fresh":6,"skipped_few_fires":29,"skipped_not_recurring":1,"skipped_no_peers":2}}
```

#### `ack_watch_error`

The deficit detector could not READ the scheduler state, so it evaluated nothing this sample. Emitted instead of `ack_watch_fired`, for the same reason `gh_teardown_watch_error` is: an unreadable source and a clean scan otherwise render identically, and conflating them is how a detector goes quietly blind.

- **`details` fields:**
  - `error` (string, required): why scheduler state could not be read

```json
{"schema_version":1,"timestamp":"2026-07-29T06:30:00.000000000Z","event_type":"ack_watch_error","agent":"pogod","details":{"error":"schedule list failed (503): scheduler unavailable"}}
```

#### `deaf_watch_fired`

pogod's missing-mail-loop announcer ([internal/deafwatch](../internal/deafwatch/deafwatch.go), mg-032b) found at least one agent with **no `mail-check-<name>` schedule** that has stayed that way past the hold-down, and mailed `notify_to` (`mayor` by default). Such an agent can be mailed and nothing will ever wake it to read the mail; every coordination path this fleet has runs through mail, so it is unreachable while its process is alive and every liveness instrument reads green.

It exists because the judgement already existed and had exactly one reader. `pogo agent diagnose <name>` has reported `health=no_mail_loop` since mg-de08 and covered the deaf-survivor population since mg-738f — but that is a subcommand taking the agent's **name** as an argument, and not knowing which name to type is precisely what this fault looks like from outside. The verdict here is the SAME one (`agent.Registry.MailLoopReport` runs diagnose's own `mailLoopFor`), asked of the whole registry on a clock. **Report-only** — pogod never registers a schedule, nudges, or restarts on this signal; re-registering the loop would hide *why* it vanished. Emitted once per sample that mailed; an unchanged roster is re-raised only after `renotify_after`. Disjoint from `ack_watch_fired`, which reads schedules that *exist*. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `episode_id` (string, required): stable per-episode id, derived from the first agent in the roster + open time. Matches the `incident_episode_cleared` event emitted when the episode closes
  - `count` (int, required): agents announced in this notice
  - `agents` (array of string, required): the bare agent names, sorted — the argument the operator could not construct. This is the field that makes the log answer "which one" without opening the mail
  - `identities` (array of string, required): the same agents as event-log identities (`crew-<name>`), the shape a notifier matches senders against
  - `scanned` (int, required): agents in the registry
  - `judged` (int, required): how many of them diagnose had standing to judge. The gap is coverage, not health — polecats (mg-e633/mg-6fe0 own their registration path), configured-but-stopped agents, and agents whose prompt tree could not be read are deliberately **unjudged**. `judged: 0` is not an all-clear
  - `notified` (string, required): comma-separated mailboxes the notice was sent to
  - `escalated` (bool, required): true when `human` was copied as well
  - `coordinator` (bool, required): true when the roster names `notify_to` itself, which escalates **immediately** regardless of `escalate_after`. Mailing an agent that has no mail loop about its own missing mail loop is not a weaker alert, it is no alert; the coordinator is itself a crew agent and has had the fleet's defects before its peers (mg-d385)
  - `mail_error_<mailbox>` (string, optional): one key per recipient the notice could NOT be delivered to; the event is still emitted so a detected fault is never lost to a down mail channel

```json
{"schema_version":1,"timestamp":"2026-07-29T06:30:00.000000000Z","event_type":"deaf_watch_fired","agent":"pogod","details":{"episode_id":"ep-1785477600000000000-doctor","count":1,"agents":["doctor"],"identities":["crew-doctor"],"scanned":6,"judged":4,"notified":"mayor","escalated":false,"coordinator":false}}
```

#### `deaf_watch_pending`

An agent was observed with no mail-check schedule and is inside the hold-down window: seen, not yet announced. Spawn and schedule registration are not simultaneous, and a nightly redeploy (mg-42ac) re-runs that gap for the whole fleet — without a hold-down every restart would announce everyone, and a health signal that cries wolf gets ignored (mg-738f's own reasoning, which is how the fleet ended up back where mg-de08 started).

Emitted once per entry into the window, not per sample. It exists so "we saw it and waited" is distinguishable in the log from "we never saw it" — the two are the same absence otherwise, and a loop that repairs itself inside the window produces no other record at all.

- **`details` fields:**
  - `target` (string, required): the bare agent name
  - `identity` (string, required): its event-log identity
  - `hold_down` (string, required): the window it must outlast to be announced
  - `why` (string, required): the fixed explanation of what is being waited out

```json
{"schema_version":1,"timestamp":"2026-07-29T06:05:00.000000000Z","event_type":"deaf_watch_pending","agent":"pogod","details":{"target":"doctor","identity":"crew-doctor","hold_down":"15m0s","why":"no mail-check schedule; waiting out the hold-down before announcing, because spawn and registration are not simultaneous"}}
```

#### `deaf_watch_error`

The announcer could not JUDGE the fleet, so it evaluated nothing this sample — typically a pogod whose scheduler did not load, leaving the registry with no mail-check provider. Emitted instead of staying silent for the same reason `ack_watch_error` is, and with an extra edge here: this detector's entire subject is a fault that is invisible unless something outside reports it, so a version of it that goes quietly blind reproduces its own bug one level up.

Also emitted with `phase: "clear"` when the all-clear mail could not be delivered.

- **`details` fields:**
  - `error` (string, required): why the fleet could not be judged
  - `phase` (string, optional): `"clear"` when the failure was in delivering the all-clear rather than in sampling
  - `to` (string, optional): the mailbox that could not be reached, with `phase: "clear"`

```json
{"schema_version":1,"timestamp":"2026-07-29T06:30:00.000000000Z","event_type":"deaf_watch_error","agent":"pogod","details":{"error":"agent: no mail-check provider installed; diagnose has no basis to judge mail loops"}}
```

#### `synthetic_failure_detected`

pogod's synthetic-failure-turn detector ([internal/synthwatch](../internal/synthwatch/synthwatch.go), mg-8cdb) read the agent's harness session transcript and found it answering turns **locally** and failing them: turns attributed to a synthetic model, with zero tokens in and out, flagged as API errors. The agent is alive and consuming every nudge on time; it accomplishes nothing with them. Detection is structural (synthetic model + zero usage + error flag), never a message string.

This is distinct from `usage_limit_hit`, which reads the PTY modal. This one reads the transcript, so it also sees the members that never render a modal at all — expired credentials above all. Emitted once per agent per episode; the paired `synthetic_failure_cleared` fires on recovery. **A restart cannot fix this class** — see `synthetic_failure_restart_suppressed`. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **Optional envelope:** `work_item_id`
- **`details` fields:**
  - `target` (string, required): the bare agent name
  - `reason` (string, required): `auth_failed` | `rate_limit` | `weekly_limit` | `spend_limit` | `server_error` | `invalid_request` | `unclassified`
  - `failing_turns` (int, required): count inside the detection window
  - `first`, `last` (RFC3339, required): the window's bounds
  - `detail` (string): the harness's own error text, truncated
  - `remediation` (string): always the page-don't-restart directive in v1

```json
{"schema_version":1,"timestamp":"2026-07-22T00:10:26.000000000Z","event_type":"synthetic_failure_detected","agent":"crew-pm-pogo","details":{"target":"pm-pogo","reason":"auth_failed","failing_turns":14,"first":"2026-07-21T23:10:26Z","last":"2026-07-22T00:10:26Z","detail":"Login expired · Please run /login","remediation":"page a human; restart is suppressed and cannot help"}}
```

#### `synthetic_failure_cleared`

The agent left the failing state: its transcript now shows real model turns in the window, or it stopped running. Restart suppression is lifted. Note that a transcript becoming **unreadable** does NOT clear the state — only a positive quiet reading does, because "we stopped being able to look" is not "it recovered". Additive — no `schema_version` bump.

- **`details` fields:** `target` (string, required)

```json
{"schema_version":1,"timestamp":"2026-07-22T22:40:37.000000000Z","event_type":"synthetic_failure_cleared","agent":"pm-pogo","details":{"target":"pm-pogo"}}
```

#### `synthetic_failure_restart_suppressed`

pogod **withheld a restart** it would otherwise have performed, because the agent is in the synthetic-failure class. This is the audit trail for the suppression half of mg-8cdb: a suppression that only ever happened silently would be indistinguishable from one that never shipped. Additive — no `schema_version` bump.

- **`details` fields:**
  - `target` (string, required): the agent whose restart was withheld
  - `reason` (string, required): the class member, as above
  - `failing_turns` (int), `detail` (string)
  - `suppressed_action` (string, required): what was withheld — `"respawn"` in v1
  - `why` (string, required): the rationale, for a reader who finds this event with no other context

```json
{"schema_version":1,"timestamp":"2026-07-22T04:00:00.000000000Z","event_type":"synthetic_failure_restart_suppressed","agent":"crew-pm-pogo","details":{"target":"pm-pogo","reason":"auth_failed","failing_turns":143,"suppressed_action":"respawn","why":"a restart cannot fix a synthetic zero-token failure turn; it discards the session's context and recovers nothing (mg-18d0)"}}
```

#### `usage_limit_hit`

pogod's modal watcher ([modal_hook.go](../internal/claude/modal_hook.go), gh drellem2/pogo #45) declared a **suspected** provider usage-limit hit for an agent: the rate-limit-options modal has been recently visible AND the agent's event log has been stale for longer than the usage-limit staleness gate (~5m, `UsageLimitSuspectStaleness`). This is a heuristic derived entirely from the existing event-staleness tracker — there is no provider quota/API probe. The ~5m gate is deliberately long because the marker text also appears in ordinary transcripts; a shorter gate would false-positive on an agent that merely prints the phrase. Emitted once per wedge; the paired `usage_limit_cleared` fires on recovery. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (the wedged agent, e.g. `"cat-mg-7ffa"`), `details`
- **Optional envelope:** `work_item_id` (present when the agent is tied to a work item)
- **`details` fields:**
  - `matcher` (string, required): always `"rate-limit-options"` in v1

```json
{"schema_version":1,"timestamp":"2026-07-06T18:20:00.000000000Z","event_type":"usage_limit_hit","agent":"cat-mg-7ffa","work_item_id":"mg-7ffa","details":{"matcher":"rate-limit-options"}}
```

#### `usage_limit_cleared`

The agent flagged by a prior `usage_limit_hit` recovered: its event log advanced past the wedge point (the agent is producing events again). This is the recovery signal operators wait on — it means the limit reset and the agent resumed work. Emitted once per hit, paired with the preceding `usage_limit_hit`. (If the agent instead exits while still limited, no `usage_limit_cleared` is emitted — the agent's `agent_stopped`/`agent_crashed` lifecycle event records the death, and the fleet coordinator releases it from the episode.) Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **Optional envelope:** `work_item_id` (present when the agent is tied to a work item)
- **`details` fields:**
  - `matcher` (string, required): always `"rate-limit-options"` in v1

```json
{"schema_version":1,"timestamp":"2026-07-06T22:05:00.000000000Z","event_type":"usage_limit_cleared","agent":"cat-mg-7ffa","work_item_id":"mg-7ffa","details":{"matcher":"rate-limit-options"}}
```

#### `incident_episode_cleared`

pogod's fleet incident coordinators closed a **fleet-wide episode** — the coalesced view the per-agent atoms above cannot reconstruct on their own. This event type is **generic**: it is emitted by every fleet-incident coordinator (usage-limit via [usagelimit.go](../internal/claude/usagelimit.go), auth via the synthetic-failure-turn watcher [synthwatch.go](../internal/synthwatch/synthwatch.go) at every auth-episode close, mg-b8c8; stall to come), discriminated by `details.kind`, so a downstream notifier binds ONE event type and one string to coalesce every incident class without a per-class reader config to keep in sync (mg-8d04 emitted this class-specific as `usage_limit_episode_cleared`; mg-55b2 generalized the name and added `kind` before more consumers bound the class-specific string). Where the per-agent atoms are per-agent and carry no roster or episode window, this one carries the coordinator's OWN notion of the episode: the full affected roster and the open/close window, computed in pogod's memory and otherwise rendered only as prose into the operator clear mail. It exists so a downstream notifier can coalesce incident self-reports without re-deriving coordinator semantics (the flap-gate, the release-not-recovery close) from raw atoms — a reconstruction that is unsafe precisely because those semantics never reach the log as atoms.

Emitted at **every** coordinator episode close, by **any** path — including the release-not-recovery case where the last flagged agent EXITS while still limited (that path emits no per-agent `usage_limit_cleared` atom, yet the episode still closes, so this event still fires). It is NOT emitted for a flap: a hit/clear pair that never outlived the coordinator's hold-down is a non-episode to the coordinator, so it produces neither the clear mail nor this event. One event per genuine episode. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp` (= `closed_at`), `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `kind` (string, required): the incident class this episode belongs to — `"usage_limit"` (usagelimit.go), `"auth"` (synthwatch.go, mg-b8c8), `"deaf_agent"` (deafwatch.go, mg-032b — the last agent with no mail-check schedule got one back), or the stall source's kind. The discriminator a single-type reader keys on.
  - `episode_id` (string, required): stable per-episode id, derived from the opening agent + open time
  - `roster` (array of string, required): the full set of agent identities limited during the episode, sorted
  - `opened_at` (string, required): RFC3339 timestamp of the first agent's hit (episode window start)
  - `closed_at` (string, required): RFC3339 timestamp of the last agent's clear/release (episode window end)

```json
{"schema_version":1,"timestamp":"2026-07-06T22:05:00.000000000Z","event_type":"incident_episode_cleared","agent":"pogod","details":{"kind":"usage_limit","episode_id":"ep-1704566400000000000-cat-mg-7ffa","roster":["cat-mg-7ffa","cat-mg-aaaa"],"opened_at":"2026-07-06T18:20:00.000000000Z","closed_at":"2026-07-06T22:05:00.000000000Z"}}
```

#### `sentinel_drift`

pogod's prompt-ready sentinel drift detector ([sentineldrift.go](../internal/agent/sentineldrift.go), mg-ce4c, fast-follow to pogo#76 / PR #77) declared a **fleet-wide** ready-gate sentinel stale: the fraction of spawns MISSING their prompt-ready sentinel within a rolling window crossed the alert threshold. The gates are hardcoded UI-string sentinels scraped from harness PTY output — the Claude initial-nudge gate (`initial-nudge`) and Cursor's trust-dialog hook (`trust-dialog`) — and when a harness UI change makes one stop matching, the gate silently degrades (a ~60s per-spawn cold-start tax for Claude; unguarded dialog dismissal for Cursor). A single missed spawn is noise; a windowed run of them means the sentinel drifted. The detector aggregates in-process (pogod is the single fleet process), so the count is fleet-wide without reading the log back. Rate-limited to one event per sentinel per drift episode (not per spawn). The paired signal is a mail to the coordinator — a log line alone is not a signal on this host. Additive — no `schema_version` bump.

**Never emitted by a test run.** The record helpers are called from production code that tests drive directly — `internal/{cursor,claude}/trust_hook_race_test.go` run the real trust-dialog hook loop, `internal/agent`'s nudge tests run the real prompt-ready gate to its deadline arm — so a suite can genuinely cross the threshold. `TestMain` in each of those three packages installs a swallowing stub sink (`agent.StubDriftSinkForTesting`) before any test runs, so neither this event nor the coordinator mail can originate in `go test` at any `-count`. Before mg-54f8 the suites were safe only by arithmetic: the cursor trust-dialog key sat at a 0.25 miss fraction against a 0.5 threshold, and `internal/agent`'s initial-nudge key was one fixture short of `driftMinSamples`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `provider` (string, required): harness provider id, e.g. `"claude"`, `"cursor"`
  - `gate` (string, required): `"initial-nudge"` or `"trust-dialog"`
  - `sentinel` (string, required): the primary sentinel string that is probably stale
  - `missed` (int, required): spawns in the window that missed the sentinel
  - `total` (int, required): spawns in the window
  - `fraction` (float, required): `missed / total`
  - `window` (string, required): the window the rate was computed over, e.g. `"1h0m0s"`

```json
{"schema_version":1,"timestamp":"2026-07-13T18:20:00.000000000Z","event_type":"sentinel_drift","agent":"pogod","details":{"provider":"claude","gate":"initial-nudge","sentinel":"? for shortcuts","missed":11,"total":12,"fraction":0.9166666666666666,"window":"1h0m0s"}}
```

#### `auto_renudge`

pogod's post-spawn start-verification watcher ([startverify.go](../internal/agent/startverify.go), mg-feb3, gh drellem2/macguffin#24) re-delivered a bare submit terminator (CR) to a freshly spawned polecat because its mg work item was still unclaimed after the start-verify window. Under a concurrent spawn wave a CPU-starved harness can miss the initial kickoff nudge (the false-idle gate delivers it before Claude Code is listening; it piles in the kernel input buffer and Ink absorbs it as one paste block whose CR never re-tokenizes as a submit — mg-ce61), leaving the agent alive but never claiming its item. The watcher gates on a HARD started-signal — originally the item leaving `available/`, and since mg-7d6d the claim PID moving off pogod's own for dispatches pogod claimed at spawn — never on output quiescence, and retries a bounded number of times; one event is emitted per delivered CR. A run of these on the same spawn wave is the productized-recovery footprint of the init-stall. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `to` (string, required): the renudged agent's identity, e.g. `"cat-feb3"`
  - `work_item_id` (string, required): the mg work item that was still unclaimed, e.g. `"mg-feb3"`. **Empty string** on the `no_ready_composer` path — a spawn with no `--id` is exactly what that signal exists for.
  - `attempt` (int, required): 1-based attempt index for this CR
  - `max_attempts` (int, required): the bounded retry ceiling
  - `reason` (string, required): which started-signal reported the agent unstarted — one of three, and **two of them are hard**:
    - `"claim_pid_not_restamped"` (mg-7d6d): the work item's claim is still stamped with pogod's own PID. The polecat's step 1 re-stamps it to its own, so an unchanged PID is positive evidence that no turn ran. Used for dispatches pogod claimed at spawn, where `work_item_unclaimed` proves nothing.
    - `"work_item_unclaimed"`: the strong claim signal — the item is still in `available/`. Since mg-7254 this applies only where pogod did *not* claim at spawn.
    - `"no_ready_composer"`: the **fallback** — the provider's prompt-ready sentinel has never appeared in this agent's PTY output.

Since mg-c33e a polecat spawned with **no** `--id` is watched on the `no_ready_composer` fallback rather than declined. `--no-worktree` dispatch commonly carries no `--id` (it is optional), and mg-560d proved that gap load-bearing for drellem2/macguffin#25: such a spawn's cwd is a brand-new `~/.pogo/agents/<name>/`, untrusted, so Claude Code raises the workspace-trust dialog every time. The dialog renders no composer, the ready sentinel never matches, and the kickoff nudge is never delivered — and 560d measured that a bare CR, precisely what this watcher sends, dismisses it (dialog → composer at t=0.7s, nudge accepted).

**Reading `reason` is how you tell a hard detection from a heuristic one, and it is worth doing.** The `no_ready_composer` fallback catches a harness whose composer never rendered but *not* the mg-ce61 paste-buffer wedge, where the composer *did* render, `promptReadySeen` latched before the watcher ran, and the agent still never acted. mg-7254 opened that gap by moving the claim to pogod at spawn; mg-7d6d closed it with `claim_pid_not_restamped`.

That arm is **capability-gated**: it needs `mg reclaim` (macguffin mg-bb43), which is additive and may not be installed. On a host without it, a claimed-at-spawn dispatch falls back to `no_ready_composer` and an mg-ce61 wedge draws no `auto_renudge` at all. pogod says which state it is in once at startup — `grep 'claim-pid re-stamp' ~/.pogo/pogod.log` — so the absence of a recovery net is never inferred from silence. Both halves of the mechanism (the verifier and the polecat prompt step) come off that one probe and cannot be enabled separately.

The fallback is a *structural* observation of the screen ("has a composer ever rendered"), not the output-quiescence heuristic the watcher deliberately avoids: quiescence misreads a CPU-starved harness as ready because it is quiet *because* it is starved, whereas a starved process, a loading spinner and the trust dialog all render no composer and so all read correctly as unstarted. The sighting is latched, so a bounded output buffer scrolling the marker away cannot flip a working agent back to unstarted.

```json
{"schema_version":1,"timestamp":"2026-07-14T00:05:00.000000000Z","event_type":"auto_renudge","agent":"pogod","details":{"to":"cat-feb3","work_item_id":"mg-feb3","attempt":1,"max_attempts":3,"reason":"work_item_unclaimed"}}
{"schema_version":1,"timestamp":"2026-07-21T00:05:25.000000000Z","event_type":"auto_renudge","agent":"pogod","details":{"to":"cat-c33e","work_item_id":"","attempt":1,"max_attempts":3,"reason":"no_ready_composer"}}
{"schema_version":1,"timestamp":"2026-07-30T01:14:25.000000000Z","event_type":"auto_renudge","agent":"pogod","details":{"to":"cat-7d6d","work_item_id":"mg-7d6d","attempt":1,"max_attempts":3,"reason":"claim_pid_not_restamped"}}
```

#### `agent_unwatched`

pogod's post-spawn start-verification watcher ([startverify.go](../internal/agent/startverify.go), mg-2437) **declined to watch** a freshly spawned polecat, so that spawn has no `auto_renudge` recovery at all. This event (plus a matching `UNWATCHED` log line naming the agent and the `--id` remedy) makes the absence audible. Its presence is the marker to check first when a polecat sat unstarted and no `auto_renudge` appears in the log.

**mg-c33e narrowed when this fires.** Under mg-2437 the mere absence of `--id` triggered it, which meant the largest class of unwatched spawns was reported but never rescued; mg-560d then proved that class was the cause of the drellem2/macguffin#25 hang. Those spawns are now *watched* on the `no_ready_composer` fallback (see `auto_renudge` above), so `agent_unwatched` no longer fires for a missing `--id` alone. What remains is the honest residue: nothing observable to gate on at all.

Crew agents never carry a work item by design and are exempt, so this event always concerns a polecat. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `to` (string, required): the unwatched agent's identity, e.g. `"cat-2437"`
  - `reason` (string, required): which structural gap applies — `"no_ready_signal"` (this dispatch had no `--id` **and** its provider declares no prompt-ready marker, so neither the claim signal nor the ready-composer fallback can observe anything; re-dispatch with `--id` to get start-verification) or `"no_start_verifier"` (nothing is wired on this daemon, so *no* spawn gets recovery)

```json
{"schema_version":1,"timestamp":"2026-07-21T00:05:00.000000000Z","event_type":"agent_unwatched","agent":"pogod","details":{"to":"cat-2437","reason":"no_ready_signal"}}
```

> `"no_work_item_id"` was this field's value between mg-2437 and mg-c33e. Log lines predating mg-c33e carry it; it is no longer emitted.

#### `pogod_condition`

One annunciation decision by pogod's condition annunciator (mg-342d, `cmd/pogod/conditionnotify.go`)
for a condition enumerated in
[pogod-log-conditions-with-no-reader-2026-07-30.md](investigations/pogod-log-conditions-with-no-reader-2026-07-30.md)
§4 rows A2–A15 — the conditions found to have an actor who could act and no channel to reach them.
Disposition of every row is in
[pogod-condition-annunciation-2026-07-30.md](investigations/pogod-condition-annunciation-2026-07-30.md).

**Emitted on EVERY raise, including the SUPPRESSED ones, and that is the point.** A live condition
produces a steady stream of `reason: "suppressed"`, so "quiet on purpose" is distinguishable from
"the notifier stopped working" — a stopped notifier stops emitting *and* stops suppressing, while a
live condition that is not being suppressed shows up as a run of `reason: "new", notified: false`.
Reading `notified` alone would lose that distinction, which is the defect this whole line of work
descends from, one level up.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (the **addressee**, not
  `"pogod"` — the condition is something that agent has to deal with, so `pogo events --agent <name>`
  shows it in their history), `details`
- **`details` fields:**
  - `condition` (string, required): the stable suppression key, e.g. `"scheduler_load_failed"`, or
    subject-scoped as `"autostart_failed:doctor"` / `"gitgc_sweep_failed:/path/to/repo"`
  - `row` (string, required): the enumeration row it carries, `"A2"`–`"A15"`. This is how the
    investigation and the running daemon get reconciled without grepping code
  - `addressee` (string, required): where it was sent. Empty only with `reason: "unroutable"`
  - `notified` (bool, required): whether a mail actually went out on this occurrence
  - `reason` (string, required): why this occurrence did or did not mail —
    `"new"` (transition INTO the condition), `"changed"` (a materially different failure, so the
    recipient's problem is not the one they were told about), `"unresolved"` (still live past the 24h
    renotify window), `"readdressed"` (the coordinator was renamed, so the previously-notified
    mailbox is now unread by anyone), `"suppressed"` (still live, deliberately quiet),
    `"unroutable"` (**no addressee resolved — never guess a name**; mail to a name no agent reads is
    silently accepted into a phantom mailbox and lost), `"woken"` / `"wake_abandoned"` (see below)
  - `detail` (string, optional): the underlying cause, usually the error string
  - `mail_error` (string, optional): present only when the send failed. A failed send is **not**
    recorded as delivered, so the next occurrence retries — there is no path where a clean-looking
    state file claims an announcement that never happened
  - `tries` (int, optional): present on `"woken"` / `"wake_abandoned"`

**`"woken"` and `"wake_abandoned"` belong to A2 alone.** Every agent's mail-check loop is a
`pogo schedule` entry, so when the scheduler fails to load the coordinator can be *mailed* but never
*prompted to read*. A2 therefore also queues a PTY nudge, retried on the heartbeat — which drives the
scheduler rather than depending on it, so the wake survives the fault it reports. A wake that never
lands within 30 minutes is abandoned as `"wake_abandoned"` and logged as *mailed-but-not-woken*: the
notice is still in the maildir, and the degradation says so rather than going quiet.

```json
{"schema_version":1,"timestamp":"2026-07-30T04:06:00.000000000Z","event_type":"pogod_condition","agent":"mayor","details":{"condition":"scheduler_load_failed","row":"A2","addressee":"mayor","notified":true,"reason":"new","detail":"scheduler.New(/Users/daniel/.pogo/schedules.json): parse: invalid character 't'"}}
{"schema_version":1,"timestamp":"2026-07-30T04:06:00.000000000Z","event_type":"pogod_condition","agent":"mayor","details":{"condition":"scheduler_load_failed","row":"A2","addressee":"mayor","notified":true,"reason":"woken","tries":1}}
{"schema_version":1,"timestamp":"2026-07-30T04:18:00.000000000Z","event_type":"pogod_condition","agent":"mayor","details":{"condition":"scheduler_load_failed","row":"A2","addressee":"mayor","notified":false,"reason":"suppressed"}}
```

#### `pogod_condition_cleared`

A condition annunciated by `pogod_condition` is no longer present, so the annunciator has **forgotten**
it. Forgetting is load-bearing rather than tidiness: without it a condition that broke, was fixed, and
broke again would be suppressed by its own resolved history — a failure that produces no symptom at
all. A recurrence after a clear mails immediately.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (the last addressee),
  `details`
- **`details` fields:** `condition` (string), `row` (string), `first_seen` (RFC3339 string — how long
  the condition was live), `cleared_at` (RFC3339 string)

```json
{"schema_version":1,"timestamp":"2026-07-30T04:20:00.000000000Z","event_type":"pogod_condition_cleared","agent":"mayor","details":{"condition":"scheduler_load_failed","row":"A2","first_seen":"2026-07-30T04:06:00+01:00","cleared_at":"2026-07-30T04:20:00+01:00"}}
```

#### `pogod_condition_summary`

The condition annunciator's per-boot roll-up, emitted **on every boot including the clean ones**.

That is the whole reason it exists, and it is not a convenience. A notifier observable only when it
fires is a notifier whose silence means nothing — so a daemon that boots and emits **no** summary is a
daemon where the annunciator is not running, which is a checkable and different shape from a daemon
with nothing to report. `scripts/pogo-condition-controls.sh`'s negative control asserts this
specifically.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`),
  `details`
- **`details` fields:**
  - `mailed` (int, required), `suppressed` (int, required)
  - `failed` (int, required): notices that could not be sent. **Non-zero means an actionable
    condition reached nobody**, and the matching log line is prefixed `⚠`
  - `live` (int, required) and `conditions` ([]string, required): the conditions still live after this
    boot, as `id(row)`, sorted

```json
{"schema_version":1,"timestamp":"2026-07-30T04:06:00.000000000Z","event_type":"pogod_condition_summary","agent":"pogod","details":{"mailed":2,"suppressed":0,"failed":0,"live":2,"conditions":["ackwatch_not_armed(A3)","scheduler_load_failed(A2)"]}}
{"schema_version":1,"timestamp":"2026-07-30T04:20:00.000000000Z","event_type":"pogod_condition_summary","agent":"pogod","details":{"mailed":0,"suppressed":0,"failed":0,"live":0,"conditions":[]}}
```

#### `worktree_notice_undelivered`

An exited agent's worktree was PRESERVED (it held uncommitted work) or KEPT (`git status` failed, so
dirtiness could not be determined) and **the notice to the coordinator could not be delivered** —
enumeration row A15 (mg-342d).

**This row is an event and not a mail, deliberately.** A15 is not "a condition with no channel"; it is
"the channel itself failed", and mailing about a failed mail is a retry wearing an alarm's clothes —
it fails the same way for the same reason. Before this, `worktreecleanup.go` emitted no events at all,
so a preserved worktree whose notice was lost left nothing but a log line: the tree pinned its branch
indefinitely and no query could find out why. **A non-empty result here means a worktree is being
preserved that nobody was told about**, and it is the query to run when a branch cannot be deleted for
no apparent reason.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (the addressee that did
  **not** hear — the open question is what the coordinator was never told), `details`
- **`details` fields:**
  - `row` (string, required): always `"A15"`
  - `exited_agent` (string, required): whose worktree it was
  - `worktree` (string, required): the path still on disk, pinning its branch
  - `outcome` (string, required): `"preserved"` (there IS uncommitted work here) or `"undetermined"`
    (we could not look — a distinct fact, and reporting it as the first would send someone to rescue
    files that may not exist)
  - `mail_error` (string, required): the underlying send failure. A record that a notice was lost
    without saying why is a record nobody can act on

```json
{"schema_version":1,"timestamp":"2026-07-30T04:06:00.000000000Z","event_type":"worktree_notice_undelivered","agent":"mayor","details":{"row":"A15","exited_agent":"cat-mg-8c66","worktree":"/Users/daniel/.pogo/polecats/8c66","outcome":"preserved","mail_error":"mg mail send failed: no such mailbox"}}
```

## Worked example: a polecat merge cycle

The lines below show the canonical event sequence for a successful polecat run. Times are illustrative.

```json
{"schema_version":1,"timestamp":"2026-04-25T09:59:55.000000000Z","event_type":"work_item_claimed","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"title":"F1: Design event log schema","tags":["pogo","event-log","phase-f"]}}
{"schema_version":1,"timestamp":"2026-04-25T10:00:00.000000000Z","event_type":"agent_spawned","agent":"cat-mg-0241","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"agent_type":"polecat","pid":48213,"prompt_file":"/Users/daniel/.pogo/agents/templates/polecat.md","worktree":"/Users/daniel/.pogo/polecats/pc-0241"}}
{"schema_version":1,"timestamp":"2026-04-25T10:00:00.150000000Z","event_type":"polecat_spawned","agent":"cat-mg-0241","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"template":"polecat","branch":"polecat-mg-0241","parent":"mayor"}}
{"schema_version":1,"timestamp":"2026-04-25T10:22:50.000000000Z","event_type":"refinery_merge_attempted","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","attempt":1,"author":"cat-mg-0241"}}
{"schema_version":1,"timestamp":"2026-04-25T10:23:09.000000000Z","event_type":"refinery_merged","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","merge_commit":"7f97c8b1a2b3c4d5","attempt":1,"duration_seconds":19.2}}
{"schema_version":1,"timestamp":"2026-04-25T10:23:10.000000000Z","event_type":"polecat_completed","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"outcome":"merged","branch":"polecat-mg-0241","merge_request_id":"mr-9482","commits":1}}
{"schema_version":1,"timestamp":"2026-04-25T10:23:12.000000000Z","event_type":"work_item_completed","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"result":{"branch":"polecat-mg-0241"}}}
{"schema_version":1,"timestamp":"2026-04-25T10:23:14.555000000Z","event_type":"agent_stopped","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"pid":48213,"exit_code":0,"reason":"task_complete","duration_seconds":1394.555}}
```

A reader who wants the lifecycle of one work item filters with `jq 'select(.work_item_id == "mg-0241")'`. A reader who wants the refinery narrative filters by `event_type` matching `^refinery_`.

## Relationship to other state

- **macguffin event log (`~/.macguffin/log/`)**: macguffin maintains its own append log for work item state transitions and mail. Pogo's event log is broader (it includes agent lifecycle and refinery merges) and lives in `~/.pogo/`. The work item events (`work_item_claimed`, `work_item_completed`) and `mail_sent` mirror macguffin transitions into the pogo log so a single tail shows the whole system. Phase F4 (mg-4fa7) wires this mirroring via the `pogo events emit` CLI bridge — `mg` shells out to it as a best-effort fire-and-forget call.
- **Refinery in-memory history**: still authoritative for queue/active state. The event log is the durable record. Once F5 (mg-287e) lands, the refinery emits an event for every merge attempt, success, and failure, so post-mortem investigation no longer depends on the in-memory history surviving a restart.
- **PTY ring buffer**: per-agent, 64 KB, lost on agent exit. The event log is system-wide and durable. The two are complementary — the ring buffer captures *what the agent said*, the event log captures *what happened*.

## Non-goals (v1)

- **No event ordering guarantees beyond per-writer order.** Two writers appending concurrently may interleave. Consumers ordering by `timestamp` is good enough.
- **No querying by index.** `grep`, `jq`, and the `pogo events` CLI (F6) are the query surface. No SQL, no full-text search.
- **No retention policy in the schema.** Rotation lives below the schema layer (mg-214a, F7): the live log is rotated to `events.log.1` once it exceeds 100MB, older rotations slide down to `events.log.5`, and anything beyond that is deleted. Readers that want full history must consume events as they happen — rotated tail data is not preserved indefinitely.
- **No event correlation IDs.** `work_item_id` and `merge_request_id` already correlate the events that matter most. A generic correlation ID can be added later as an additive `details` field without bumping `schema_version`.

## Open questions for F2+

These are deliberately deferred — flagged here so the implementation tasks can resolve them:

- **Where is the writer library?** F2 (mg-700a) shipped `internal/events.Emit(ctx, event)`; default path is `~/.pogo/events.log`, overridable for tests via `SetLogPathForTesting`.
- **How does `mg` emit?** F4 (mg-4fa7) chose the shell-out path: `pogo events emit --type=… --details=…` is a thin CLI wrapper over `events.Emit` that mg invokes after each claim/done/mail send. This keeps macguffin free of any pogo Go-module dependency at the cost of a per-event subprocess (acceptable for the low-frequency mg ops). The CLI auto-derives the `agent` field from `POGO_AGENT_NAME`/`POGO_AGENT_TYPE` so the typical caller passes only `--type`, `--work-item-id`, and `--details`.
- **What happens when the disk is full?** Writer drops the event with a stderr warning rather than blocking the calling code path (decided in F2; implemented in `events.Emit`).
- **Truncation policy for `gate_output_truncated` and `last_output`.** 1 KB and 512 B respectively are first guesses; revisit once we see real volumes.
