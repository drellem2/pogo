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
  - `stop_cause` (string, optional): which stop path ran. Present only when `reason` is `"requested"` — an agent that finished its own work was not stopped by anybody, and an empty value there would read as an unattributed stop rather than as no stop at all. One of:
    - `"request"` — a single explicit stop: `DELETE /agents/{name}`, i.e. `pogo agent stop`
    - `"stop_all"` — the fleet-wide drain, whose only live caller is the transition to index-only mode. Pair it with the `server_mode_changed` at the same instant, which names the HTTP caller that asked
    - `"park"` — `pogo agent park`
    - `"merge_reap"` — pogod reaping a polecat whose branch merged
    - `"merge_backstop"` — the defer-done backstop reaping a polecat that merged but lingered past its deadline
    - `"done_reap"` — pogod reaping a polecat whose work item is done and which has gone idle
  - `duration_seconds` (number, optional): wall-clock seconds since `agent_spawned`

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:14.555000000Z","event_type":"agent_stopped","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"pid":48213,"exit_code":0,"reason":"task_complete","duration_seconds":1394.555}}
{"schema_version":1,"timestamp":"2026-08-08T00:44:20.124971000Z","event_type":"agent_stopped","agent":"crew-architect","details":{"pid":32439,"exit_code":0,"reason":"requested","stop_cause":"stop_all","duration_seconds":25611.635}}
```

`stop_cause` was added by mg-a95f and is absent from every record written before it. Its
absence in an old record means the field did not exist, **not** that the stop was
unattributed — the same distinction `server_mode_changed` carries for transitions before
2026-08-09T09:41Z. Records written by a daemon that has not yet restarted onto a build
containing it are likewise silent.

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

#### `work_item_stranded_push`

A stopped polecat's work item went back to `available/` **with pushed work
behind it** (mg-b468). The item now describes itself as unstarted while its
output sits on a branch, and the next dispatch will re-derive it.

Emitted alongside `work_item_claim_released`, never instead of it: the claim
release still happens, because refusing it would strand the item in `claimed/`
under a dead pid — a worse failure (see `work_item_claim_release_failed`). This
event is the record that the return to `available/` is a lie about the item's
state.

**Absence is not a clean bill of health.** The check runs `git cherry` against
the repo's target ref, and a repo it cannot read produces a log line rather than
an event. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `repo`, `details`
- **`details` fields:**
  - `branch` (string, required): the polecat branch carrying the work
  - `ref` (string, required): the ref the commits were read from — `refs/remotes/origin/<branch>` when pushed, `refs/heads/<branch>` when the polecat committed but never pushed
  - `pushed` (bool, required): whether `ref` was a remote-tracking ref. `false` is the **more** urgent case: git-gc reaps the worktree holding the only copy
  - `target` (string, required): the ref the branch was compared against
  - `disposition` (string, required): `"resubmit"` or `"pre_registration"`. The second means an unmerged commit whose subject begins `predictions:` — a re-dispatch that branches from the target destroys the control it records, and the resulting artifact looks valid
  - `unmerged` (int, required): how many commits the target does not have
  - `reason` (string, required): the claim-release reason this accompanies, e.g. `"agent_stopped"`
  - `summary` (string, required): the one-line finding, including the remedy

```json
{"schema_version":1,"timestamp":"2026-08-05T09:55:02.000000000Z","event_type":"work_item_stranded_push","agent":"cat-9a19","work_item_id":"mg-9a19","repo":"/Users/daniel/dev/pogo","details":{"branch":"polecat-9a19","ref":"refs/remotes/origin/polecat-9a19","pushed":true,"target":"refs/remotes/origin/main","disposition":"resubmit","unmerged":1,"reason":"agent_stopped","summary":"polecat-9a19 has 1 unmerged commit(s) on refs/remotes/origin/main ..."}}
```

#### `dispatch_stranded_work_overridden`

A dispatch went ahead over the stranded-work gate's refusal, with a stated
reason (`pogo agent spawn-polecat --stranded-override="<why>"`). The gate
attributes a branch to a work item heuristically — a commit-subject id, or the
item's id-suffix in the branch name — so it can be wrong, and a refusal with no
way past it becomes a wedge that gets resolved by disarming the gate. This event
is what keeps the override from being silent. Additive — no `schema_version`
bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **Optional envelope:** `repo`
- **`details` fields:**
  - `agent_type` (string, required): always `"polecat"` in v1
  - `agent_name` (string, required): the name of the polecat that was dispatched anyway
  - `reason` (string, required): the operator's stated why — what they knew that the gate did not
  - `refusal` (string, required): the bypassed refusal verbatim, so a reader can tell a bad attribution from a real duplication

```json
{"schema_version":1,"timestamp":"2026-08-05T09:58:11.000000000Z","event_type":"dispatch_stranded_work_overridden","agent":"cat-a9a19","work_item_id":"mg-9a19","repo":"/Users/daniel/dev/pogo","details":{"agent_type":"polecat","agent_name":"a9a19","reason":"branch is a stale duplicate; the real work merged as 9072f34","refusal":"work item mg-9a19 already has PUSHED, UNMERGED work: ..."}}
```

#### `dispatch_preserved_worktree_overridden`

A dispatch went ahead over the **preserved-worktree** gate's refusal, with a
stated reason (`pogo agent spawn-polecat --preserved-override="<why>"`). The
gate refuses when an item's work is already written and UNCOMMITTED in a
retained worktree — see `worktree_preserved` below for the state, and mg-836c for
why detection alone was not enough. Attribution is a name match between the tree's
directory and the item, so it can be wrong; the override exists so a wrong
refusal is not a wedge whose only exit is deleting the tree.

**This is the override whose consequence is not recoverable, and that is why the
event matters more than its two siblings.** A stranded branch survives a wrong
call — it is on origin. A preserved tree is reaped by the next `gc` sweep once its
item concludes, so this event is the only durable record that somebody chose to
proceed over the last copy of that work, and the only thing that gives a
re-derivation discovered later a named cause instead of looking like a mystery.
Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **Optional envelope:** `repo`
- **`details` fields:**
  - `agent_type` (string, required): always `"polecat"` in v1
  - `agent_name` (string, required): the name of the polecat that was dispatched anyway
  - `reason` (string, required): the operator's stated why — specifically, what they found when they READ the tree
  - `refusal` (string, required): the bypassed refusal verbatim, which names the tree, its branch and its modified/untracked split

```json
{"schema_version":1,"timestamp":"2026-08-19T07:31:04.000000000Z","event_type":"dispatch_preserved_worktree_overridden","agent":"cat-q516e","work_item_id":"mg-516e","repo":"/Users/daniel/dev/pogo","details":{"agent_type":"polecat","agent_name":"q516e","reason":"read all 16 files; every one is regenerated suite output","refusal":"work item mg-516e already has UNCOMMITTED work in a RETAINED WORKTREE: /Users/daniel/.pogo/polecats/p516e holds 16 uncommitted path(s) — 14 modified, 2 untracked ..."}}
```

#### `work_item_completion_notice`

pogod decided what to tell the agent that FILED a work item, at the moment the
item closed (mg-f120). Emitted by the daemon, not by `mg`.

**It is emitted for the SKIPS as well as the sends, and that is the point.**
Until mg-f120 nothing told a commissioning agent that its item had finished —
the refinery mails the coordinator, pogod closes the item, the coordinator
archives it — and the failure mode was silence, where the absence erases its own
evidence. A notifier that recorded only its sends would rebuild that: a decision
not to mail and a notifier that never ran would look identical. `sent` is the
field that separates them, and `skipped` says why. Additive — no
`schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **`details` fields:**
  - `route` (string, required): `"merge"` when pogod closed the item itself at a merge, `"self-close"` when the worker closed it with `mg done` and the done-item reaper observed it
  - `creator` (string, required): the item's recorded filer; `""` when the item named none or it could not be read
  - `to` (string, required): the mailbox actually written; `""` when nothing was sent
  - `redirected` (bool, required): `true` when `to` is the coordinator standing in for a creator that no longer exists, or relaying a mail the creator's box refused
  - `sent` (bool, required): whether a mail was written
  - `closed` (bool, required): whether the work item actually reached a terminal state. `false` on the merge route means the branch landed and the item is **still open** — the notice sent was "MERGED BUT NOT CLOSED", not a completion (mg-2b71)
  - `not_closed_reason` (string, optional): why the item is still open, in words — present only when `closed` is `false`
  - `skipped` (string, optional): why nothing was sent, in words — e.g. the filer is the worker, or the coordinator already had the refinery's merge mail
  - `error` (string, optional): the delivery failure, when one occurred
  - `branch`, `merged_sha` (string, optional): present on the merge route

```json
{"schema_version":1,"timestamp":"2026-08-12T15:04:11.000000000Z","event_type":"work_item_completion_notice","agent":"pogod","work_item_id":"mg-145f","details":{"route":"merge","creator":"pm-onethird","to":"pm-onethird","redirected":false,"sent":true,"closed":true,"branch":"polecat-p145f","merged_sha":"8eec6d2"}}
```

**`closed` is required rather than assumed, and it was added after a notice went
out that nobody had the standing to send.** On 2026-08-13 mg-479c's branch merged,
`mg done` was refused (exit 4, the item was unclaimed), and the filer was told
COMPLETED anyway — 45 seconds after pogod's own log recorded the refusal. The two
skips above (`the filer is the worker`, `the coordinator already had the merge
mail`) are also conditional on `closed`: each says the recipient already knows,
and neither is true of a merge that left the item open.

```json
{"schema_version":1,"timestamp":"2026-08-13T02:27:07.000000000Z","event_type":"work_item_completion_notice","agent":"pogod","work_item_id":"mg-479c","details":{"route":"merge","creator":"pm-onethird","to":"pm-onethird","redirected":false,"sent":true,"closed":false,"not_closed_reason":"pogod declined to close it: work item is gated and was deliberately not closed: mg-479c is unclaimed and assigned to \"parked\"","branch":"polecat-c479c","merged_sha":"1a0240a"}}
```

#### `work_item_closed_without_verdict`

pogod's auto-done path closed a work item whose merge request carried no author
verdict (mg-c456). Emitted by the daemon at the close, not reconstructed
afterwards.

**It exists because the moment of loss was the one moment nothing watched.**
doctor's census filed with mg-c456 — `pogo check-verdicts`, unfiltered over every
filer on 2026-08-19 — reported **385 ROUTING + 1014 LOST** rows in the live store,
with a per-landing-date table putting the accrual at roughly 10–80 rows per working
day, dropping to exactly **zero** across the 2026-08-15..18 fleet outage and
resuming when throughput resumed. Those figures are doctor's and are not re-derived
here. That outage is a natural control arm, and it is what makes the gap a product
of NORMAL operation rather than one incident's backlog. `check-verdicts` finds
those rows later by reconstruction and is deliberately report-only; nothing counted
them as they were made.

**And the reason nothing counted them is not a missing counter, which is why this
is an event at the close rather than a metric on an existing path.** At the instant
a verdict is lost the system's own answer is SUCCESS: the branch merged, the item
closed, and the worker's later `mg done --result` was refused *because the item is
already terminal* — a refusal the protocol scores as normal completion. Every
signal on that path reads healthy, so a wider or better-aimed version of any
existing check reports more success, not the loss. So the loss is recorded HERE,
at the instant it is created, rather than counted on a path whose own answer is
"fine". Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `details`
- **`details` fields:**
  - `route` (string, required): `"merge"`. There is only one, and it is stated rather than assumed — this writer is reached only on the auto-done path
  - `worker_live_at_close` (bool, required): whether a polecat was registered for the item at the close and about to be stopped. `true` means a worker's submit-time window is now shut; `false` means a hand-submitted or stranded branch whose submitter carried no verdict either. Different findings, and folding them together is how a scope becomes wrong
  - `branch`, `mr`, `target` (string, required): what merged, under which merge request, onto what
  - `worker` (string, optional): the polecat's registry name; absent when none was running
  - `merged_sha` (string, optional): the commit the merge landed as

The shape, as the writer emits it (an illustration — the daemon carrying this event
had not been restarted onto it when mg-c456 landed, so no live instance existed to
quote):

```json
{"schema_version":1,"timestamp":"2026-08-19T10:14:02.000000000Z","event_type":"work_item_closed_without_verdict","agent":"pogod","work_item_id":"mg-1234","details":{"route":"merge","worker_live_at_close":true,"worker":"1234","branch":"polecat-mg-1234","mr":"mr-42","target":"main","merged_sha":"45b4421a"}}
```

The nearest real instance predates the event and is what the ticket was written
around: on 2026-08-19 `polecat-pf867` was submitted on the worker's behalf with no
`--verdict-file`, and mg-f867's stored result is merge bookkeeping and nothing
else. Its verdict survived only because the worker noticed and the text was
recovered from archived mail and appended to the item's **body** by hand — an
accident, not a mechanism. This event is what that close would have said about
itself.

**Coverage, stated because an unstated limit is how this lineage keeps getting
re-measured wrong.** This records closes *pogod itself* performed. A polecat that
wins the race and writes a verdict-free result of its own is a real loss and is
**not** counted here — the reap does not read the store back and cannot see it.
`pogo check-verdicts` remains the instrument for that population, and the two
answer different questions: this one counts at loss time and cannot see every
loss; that one sees the whole store and only afterwards.

**What the record asserts is two facts and deliberately not a third.** It asserts
that the merge request carried no author verdict and that this close is the one
that landed, so the single submit-time attachment point is shut. It does **not**
assert that the worker had a conclusion it failed to record: a trivial item may
genuinely have nothing to say, and an event that called every such close a
destroyed verdict would be a detector reporting a loss it never measured.

**Why this is an event and not a key on the work item, which was tried first.**
The archive keeps a result sidecar forever and this log rotates, so marking the
absence on the item looks like the better home. It is not: mg-bf3f's `d2_cause.py`
D2.5 predicate for "did anybody write down an outcome" is **any field beyond
`branch`/`mr`/`target`**, so a key stating the absence trips it and a verdict-free
close reads as *answered* — the remedy exhibiting the defect it remedies. That
proxy is also already stale, which is the reason to route around it rather than
widen it: over the 578 result sidecars in `~/.macguffin/work/archive/2026-08` on
2026-08-19 it calls 452 answered while only 401 record a verdict — **51 false
positives**, produced by `merged_sha` and `post_merge_tag`, which are the
refinery's own bookkeeping and not outcomes. A future fix that wants the durable
on-item marker has to deal with that predicate first.

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
  - `consecutive` (number, required): how many wakes the current unbroken run of
    suppressions has declined, this one included. Reset by a DELIVERED wake, so
    a value that keeps climbing is an agent nothing has reached since the run
    began (mg-3a8a)
  - `suppressed_for_seconds` (number, required): how long that run has lasted.
    This is the number the bound is compared against — see
    `wake_suppression_released` below
  - `fire_token` (string, optional): correlation id, as for `nudge_sent`

```json
{"schema_version":1,"timestamp":"2026-07-29T11:15:30.000000000Z","event_type":"nudge_suppressed","agent":"pogod","details":{"to":"crew-mayor","message":"stall-watch: work piling up","rule":"limit_episode","reason":"usage-limit episode ep-1753-cat-mg-7ffa open since 2026-07-29T10:40:00Z (3 agent(s) rate-limited)","consecutive":2,"suppressed_for_seconds":63.0}}
```

`consecutive` and `suppressed_for_seconds` are structured because the run length
was the load-bearing number in mg-3a8a and reading it meant regexing an age out
of an English sentence. To find a latched suppression:

```bash
pogo events list --since=24h --json |
  jq -r 'select(.event_type=="nudge_suppressed") |
         [.details.to, .details.rule, .details.consecutive, .details.suppressed_for_seconds] | @tsv' |
  sort -k4 -nr | head
```

#### `wake_suppression_released`

The wake-cycle policy's BOUND fired: a rule declined a wake, and it was
delivered anyway because the run of consecutive suppressions had outlived
`agent.DefaultWakeSuppressionBound` (15 minutes). Added by mg-3a8a.

It exists because both suppression rules were unbounded, and rule 1's condition
— "the agent has produced no PTY output since we woke it" — is exactly the
condition a wake exists to break. Measured over 2026-08-14..19: 143 consecutive
wakes to `crew-pa` declined across 106 hours, the age in `reason` climbing
monotonically and never resetting, with the only exit an operator running `pogo
agent stop`/`start` out of band. Over the same window `crew-mayor`'s
suppressions all read `already woken 0s ago` — the same rule, working as
intended, in the same daemon.

The run ends only when a wake is DELIVERED, so past the bound every attempt is
released until one lands; a release that failed on a busy PTY is re-offered
rather than spending the period. After a delivery the debounce starts again from
zero, which caps a permanently silent agent at one wake per bound period.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:** the same fields as `nudge_suppressed` (`to`, `message`,
  `rule`, `reason`, `consecutive`, `suppressed_for_seconds`, optional
  `fire_token`), where `rule` names the rule that was OVERRIDDEN, plus:
  - `bound_seconds` (number, required): the ceiling that was crossed

```json
{"schema_version":1,"timestamp":"2026-08-19T16:40:00.000000000Z","event_type":"wake_suppression_released","agent":"pogod","details":{"to":"crew-pa","message":"check your mail","rule":"wake_silence_once","reason":"agent pa was already woken 106h29m49s ago and has produced no PTY output since that wake settled (2s) — released by the wake-suppression bound after 16m0s and 2 consecutive suppression(s) (bound 15m0s)","consecutive":2,"suppressed_for_seconds":960.0,"bound_seconds":900.0}}
```

A `wake_suppression_released` next to a `nudge_sent` with no answering agent
output is the fleet's signal that an agent is silent *and* unreachable by the
scheduler — the state that needs `pogo agent stop`/`start`, not another wake.

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
  - `fires_delivered` / `fires_completed` (int): lifetime counters for this schedule, persisted in `schedules.json` so they survive a pogod restart. They are deliberately **zeroed by a re-registration** (`pogo schedule` with an existing `--id`, which every crew agent does at boot), so a low pair here right after a bounce means "counting restarted", not "nothing is completing".
  - `completion_tracked` (bool): whether this schedule has EVER been acked. When `false`, the recipient is not participating in completion tracking and no conclusion may be drawn from a missing ack — the state is UNKNOWN, not failing. "Ever" spans re-registrations: it is backed by the `ever_acked` bit on the entry, which survives the reset above (mg-00d6). Before that bit, this field went `false` for the whole crew after every nightly bounce, and stayed false for any agent that never came back — so the schedules most in need of a verdict were the ones excluded from getting one.
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

#### `schedule_removed`

An entry left the live set. Emitted at **every** delete site so an operator can
answer "why did this schedule disappear?" from the log alone (mg-8e5d).

- **`details` fields:**
  - `schedule_id`, `to`, `delivery`, `replay_policy`, `one_shot`, `cron`, `next_fire`
  - `removed_at` (string, RFC3339)
  - `reason` (string): one of the values below
  - `error` (string, optional): present when the removal was caused by a failure
  - `kind`, `message` (string, **one-shots only**, mg-8011): what the one-shot
    WAS. A one-shot's removal record is the only surviving trace of it — the
    entry is gone from `schedules.json` by the time anything reads the log — and
    a `--id`-less `pogo schedule --once` is called `sch-<hex>`, which names
    nothing, so without the message no reader can say what obligation was
    missed. `message` is whitespace-collapsed and truncated to 200 runes.
    Recurring schedules carry neither: their message is boilerplate repeated on
    every removal, and their id plus cron already identify them.

| `reason` | Meaning |
|---|---|
| `explicit_rm` / `explicit_rm_by_id` | `pogo schedule rm` |
| `agent_gone` | the mail-check's target agent is no longer alive |
| `cron_unparseable` / `no_future_fire` | the cron stopped yielding a fire |
| `rollback_persist_failure` | `Add` could not persist and was rolled back |
| `one_shot_acked` | a one-shot's fire was acknowledged; its work was reported done |
| `one_shot_unacked` | a one-shot's ack window (`AckStaleWindow`, 24h) closed with no ack |
| `one_shot_undelivered` | a one-shot's delivery failed, so no turn ran |
| `one_shot_skipped` | `ReplaySkip` elided a stale one-shot fire |

**One-shots are completable (mg-64e6).** Until this fix a one-shot was deleted
in the same `Tick` pass that delivered it, tagged `one_shot_complete` — while
the body it had just delivered told the agent to run
`pogo schedule ack <id> ...`. That command could never work: the entry was gone
before any agent could read the nudge, so the ack was refused with
`schedule not found`. Not a race; the delete was in the same pass as the
delivery. And because `Completion()` iterates the LIVE entries, the deleted
one-shot then contributed to no counter at all — a silent hole rather than a
false red, so nothing would ever start looking wrong. Worse, the label asserted
at fire time what only the agent can know: a one-shot delivered into a dead,
wedged or zero-token agent produced a record byte-identical to one whose work
was done.

A fired one-shot now stays in the live set — marked spent, so it never fires
again — until its ack lands or its window closes, and `one_shot_acked` vs
`one_shot_unacked` are the two records that used to be one. `one_shot_complete`
is retired and must not come back; `internal/scheduler/oneshotack_test.go`
guards the label.

`pogo schedule list` shows a fired-and-unacked one-shot with
`— (fired, awaiting ack)` in the NEXT FIRE column.

The usual caveat still applies: an agent can forget to ack, so
`one_shot_unacked` means nobody answered, not that the work failed.

**Who reads it (mg-8011).** `pogo check-oneshots` is the consumer, and the
`one-shot acks` row in `pogo doctor --check` is its summary. Until that shipped
the split existed and nothing looked at it, which from a human's seat is the
original defect unchanged: a one-shot firing into a dead agent produced a
correctly-labelled record, and no alarm, no row and no digest line. The reader
lives in `internal/scheduler/oneshotoutcomes.go` — the same package as the
writer, so both ends share one set of reason constants and a rename cannot leave
a reader quietly matching nothing.

It joins `scheduler_fire_delivered` onto each removal to say how long the fire
sat, and that join is deliberately **not** windowed: a one-shot is reaped 24h
after firing, so in any window shorter than a day the delivery record is outside
it and a windowed join would report a zero wait.

**A window written by an older pogod is reported as unmeasurable, never as
clean.** These four labels ship in `d71e1e2` and are inert until pogod is rebuilt
onto it; before that every one-shot leaves as the retired `one_shot_complete`.
Finding that label in the window means an unanswered one-shot would be invisible,
so the reader says so instead of printing a zero it cannot stand behind — see
mg-afd0 / mg-3141 for the confusion class this avoids joining.

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
  - `class` (string, since mg-e5c2): `"infrastructure"` (establishes nothing about the branch — network, credentials, or the refinery's own checkout), `"contention"` (lost a race with another merge), `"defect"` (establishes a fact about the branch — a gate verdict, a conflict), `"host"`, `"indeterminate"`, or `"unclassified"`. Only `defect` invites dispatching a fix. Since mg-67c9 a gate that failed because ITS OWN network I/O failed is `"infrastructure"` rather than `"defect"`, with `signal` reading `gate-network "<wording>" with "<marker>" on the same line`; the classification is made from the FULL gate output before it is capped, and only when both wordings sit on one line.
  - `retried` (bool, since mg-e5c2): whether another attempt followed this one
  - `not_retried_reason` (string, since mg-e5c2): present when `retried` is false — why re-running would give the same answer, or which budget was spent. An absent retry that says nothing is indistinguishable from a policy that does not exist.
  - `backoff_seconds` (float, since mg-e5c2): the delay slept before the next attempt
  - `transport` (string, since mg-e5c2): `"ssh"`, `"https"`, `"file"`, `"git"` or `"unknown"` — measured from the clone's origin URL, falling back to the error's own wording
  - `remote` (string, since mg-e5c2): the origin URL as configured at the moment of failure
  - `git_command` (string, since mg-e5c2): the git invocation that failed, as invoked
  - `signal` (string, since mg-e5c2): the evidence that decided `class` — the matched wording, or the stage — so a classification can be audited rather than trusted
  - `raw_error` (string, since mg-e5c2): git's combined output **verbatim**, up to 768 bytes and not reduced to one line

`raw_error` and `transport` exist because `reason` is a single truncated line, and on 2026-08-05 a set of 31 failures read one line at a time, across one transport, produced two confident wrong mechanisms over several hours (mg-e5c2). 20 of those failures were ssh reporting `Undefined error: 0` — a wording that names no cause — and 11 were HTTPS reporting `Could not resolve host: github.com`, which named it outright. The event log is the only view that spans merge requests, so it is where both halves have to be legible:

```bash
pogo refinery history --since=6h --json |
  jq -r '.[].attempts[] | "\(.transport)\t\(.raw_error)"' | sort -u
```

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:05.000000000Z","event_type":"refinery_merge_failed","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","attempt":1,"stage":"test","reason":"./test.sh exited with status 1","terminal":false,"class":"defect","retried":false,"not_retried_reason":"not retryable: the test gate ran on this tree and returned a verdict — re-running establishes the same fact","gate_output_truncated":"--- FAIL: TestEventEmit ..."}}
{"schema_version":1,"timestamp":"2026-08-05T20:33:21.000000000Z","event_type":"refinery_merge_failed","agent":"refinery","work_item_id":"mg-fc8d","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9483","branch":"polecat-mg-fc8d","target":"main","attempt":1,"stage":"fetch","reason":"fetch: ssh: connect to host github.com port 22: Undefined error: 0","terminal":false,"class":"infrastructure","retried":true,"backoff_seconds":2,"transport":"ssh","remote":"git@github.com:drellem2/pogo.git","git_command":"git fetch origin","signal":"connect to host","raw_error":"ssh: connect to host github.com port 22: Undefined error: 0\nfatal: Could not read from remote repository."}}
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

pogod's stall watcher (gh drellem2/macguffin #12) crossed a work-pile-up threshold for the watched agent (the coordinator, `mayor` by default) and emitted a nudge. One event per offending batch per heartbeat tick. Rate-limited by a **per-item** escalating cooldown for the two work-item categories and a flat per-category cooldown for `unread_mail` (mg-1693). See [stall-watch-design.md](design/stall-watch-design.md).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `category` (string, required): `"unclaimed_items"`, `"unread_mail"`, `"priority_wake"`, `"worked_unclaimed"`, `"preserved_worktree"`, `"blocked_reminder"`, or `"indefinite_hold"`
  - `watched_agent` (string, required): the agent that was nudged
  - `nudge_delivery` (string, optional): the channel that carried the nudge — `"pty"` (written to the agent's live terminal), `"mail"` (agent not running, so straight to durable mail), or `"mail_fallback"` (agent running but the PTY nudge failed, so durable mail carried it instead). Absent only when delivery failed outright.
  - `nudge_fallback_reason` (string, optional): present with `"mail_fallback"`; why the PTY channel was not used. **Not an error** — the nudge was delivered.
  - `nudge_error` (string, optional): present only when **every** channel failed and the notice reached nobody; the event is still emitted. Before mg-79dc this field also covered the routine busy-agent case, so historical records carrying it are not all hard failures — see below.
  - `nudge_subject` (string, optional, mg-b6f8): the mail subject this notice was delivered under. Present on every fire from mg-b6f8 onward; absent on older records, where there was nothing to record because **every** stall-watch mail carried the one string `stall-watch: work piling up`. It is stamped so "which notices were indistinguishable in the recipient's notification list" is answerable from this log rather than by hand-reading a maildir:

    ```bash
    jq -r 'select(.event_type=="stall_watch_fired") | .details.nudge_subject // empty' \
      ~/.pogo/events.log | sort | uniq -c | sort -rn | head
    ```

    A count above 1 is a repeat the reader could not tell from its predecessor. That is the defect mg-b6f8 fixed, and this is how a regression of it gets counted.

```json
{"schema_version":1,"timestamp":"2026-06-10T16:20:00.000000000Z","event_type":"stall_watch_fired","agent":"pogod","details":{"category":"unclaimed_items","watched_agent":"mayor","item_count":2,"item_ids":["mg-2350","mg-9299"],"age_threshold":"10m0s","oldest_age_seconds":1830.4,"nudge_delivery":"pty"}}
```

  - For `unclaimed_items`: `item_count` (int), `item_ids` ([]string), `age_threshold` (string), `oldest_age_seconds` (float)
  - For `unread_mail`: `unread_count` (int), `max_count` (int), `oldest_age_seconds` (float), `age_threshold` (string), `over_count` (bool), `over_age` (bool)
  - For `priority_wake`: `item_count` (int), `item_ids` ([]string), `wake_delay` (string), `wake_cooldown` (string), `fast_priority` (string), `oldest_age_sec` (float)
  - For `worked_unclaimed` (mg-1a8a): `item_count` (int), `item_ids` ([]string), `workers` ([]object — `item_id`, `polecat`, `evidence` (`"registry"` or the weaker `"witness"`), `pid` when known), `oldest_age_seconds` (float)
  - For `preserved_worktree` (mg-836c): `item_count` (int), `item_ids` ([]string), `worktrees` ([]object — `item_id`, `worktree`, `branch`, `modified_paths` (int), `untracked_paths` (int), `unread` (bool: the tree was found but `git status` could not read it, so the two counts are not a claim about its contents)), `oldest_age_seconds` (float). The **modified/untracked split** is carried rather than a total because the halves are different facts: a modified tracked file still has its committed version in the object store, while an untracked path is on no branch, in no stash and on no remote — that tree is its only copy on the machine.
  - For `indefinite_hold` (mg-f398): `item_count` (int), `item_ids` ([]string), `hold_gates` (map gate value → count, e.g. `{"parked":4,"human":1}`), `age_threshold` (string), `cooldown` (string), `oldest_age_seconds` (float, absent when every held item is unaged), `unaged_ids` ([]string, optional — items whose file could not be stat'd, so their hold is real but its age is unknown)
  - For both work-item categories (mg-1693, all optional — absent means "nothing to report"): `repeat_counts` (map item id → notice number, present only for items on their 2nd or later notice), `backoff_suppressed_ids` ([]string) and `backoff_suppressed_count` (int) for candidates that were detected but held back by their own backoff, and `next_backoff` (string), the longest gap now applied to any item in this fire.

**Counting stall notices PER ITEM, not per fire (mg-1693).** `item_ids` is the field that matters when asking whether the watcher is over-firing, and until mg-1693 nothing in this event let you tell the two failure modes apart. The cooldown was keyed per *category*, so an item the coordinator was deliberately holding got re-detected and re-notified every cooldown forever: on 2026-07-30, 87 fires carried **212 item-notices across 29 items**, with `mg-61f4` appearing 22 times and `mg-0e24` 27. Every one of those detections was **correct** — the items really were available, high-priority and undispatched. Reading it as an over-firing detector (fire count) rather than a broken repeat-suppressor (per-item count) would have led to tightening detection and losing true positives. **A correct detector with a broken repeat-suppressor is indistinguishable from an over-firing detector unless you count per item.** To count that way:

```bash
jq -r 'select(.event_type=="stall_watch_fired") | .details.item_ids // [] | .[]' \
  ~/.pogo/events.log | sort | uniq -c | sort -rn | head
```

Records from mg-1693 onward carry `repeat_counts`, so the same question is answerable from a single event rather than by correlating ids across fires.

**Reading `nudge_error` on records from before 2026-07-17 (mg-79dc):** it meant only "the PTY nudge failed", and the nudge was then **dropped** — there was no mail fallback for a running-but-busy agent. On 2026-07-17, 18 of 47 fires (~38%) carried one, every single instance reading `still producing output after 30s ... context deadline exceeded`. Those fires happened and were never heard, which matters when reasoning backwards from mayor's inbox: **an absent stall notice in that era is not evidence the detector did not fire.** mg-4bd4 concluded the work-item detectors had "never been able to fire on real work" from exactly that absence; the events log falsifies it. Records from mg-79dc onward carry `nudge_delivery`, so a fire that took the durable road is visible as such rather than looking like a failure.

**Counting holds nothing will ever release (`indefinite_hold`, mg-f398).** `parked` and `human` are gates with no driver: nothing scheduled evaluates their release condition, so an indefinite hold persists until a person happens to look. This category is the reader that closes that — it releases nothing, writes no field, and reads no item text, so `hold_gates` and the ages are the whole of what it knows. What has been held longest, across the log:

```bash
jq -r 'select(.event_type=="stall_watch_fired" and .details.category=="indefinite_hold")
       | "\(.details.oldest_age_seconds // 0) \(.details.item_ids | join(","))"' \
  ~/.pogo/events.log | sort -rn | head
```

Two reading notes. **Absence of these events is ambiguous**: the report fires only when something is held, so a log with none of them is equally consistent with "nothing is parked" and "the report is disarmed" — pogod's startup line prints `indefinite_hold=` for exactly that reason, and it is the thing to check first. And `unaged_ids` is not a subset of the aged population: those items are held, but their file could not be stat'd, so they are counted in `item_count` while contributing nothing to `oldest_age_seconds`.

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

#### `revision_stale`

pogod's revision-staleness check (mg-5bd2) sampled its own build stamp and found the commit it was built from is older than `self_stale_after` (default 7 days). It is the POSITIVE answer to "is the daemon current?", and it deliberately does not route through the nightly deploy job: every prior alarm on that question was indexed to the job's own exit code, so a night the job never fired produced no exit code and therefore no alarm. Four such nights passed in a row (2026-08-01..08-04) and pogod ended up 85 commits behind main with nothing raised. **Report-only** — it never redeploys and has no seam through which it could.

**Emitted on EVERY stale sample, mailed or not.** The mail is capped at four notices per revision (detection, +1d, +3d, +7d — see `notice`/`mailed` below); this record is not, so a spent mail budget never turns the condition invisible. To ask whether the condition is live right now, read the newest event rather than the newest mail:

```bash
jq -r 'select(.event_type=="revision_stale") | "\(.timestamp) \(.details.revision[0:8]) age=\(.details.age_days)d mailed=\(.details.mailed)"' \
  ~/.pogo/events.log | tail -5
```

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `revision` (string, required): the full `vcs.revision` of the running binary — the same value `GET /version` reports. Read in-process from the build stamp rather than over the loopback, because a loopback probe can be answered by a process that is not pogod (mg-e314)
  - `commit_time` (string, required): `vcs.time`, the time of the **commit** the binary was built from. Not the build time, and emphatically not the process start time — on 2026-08-04 pogod restarted onto the *same* 2026-07-30 binary, so neither uptime nor a recent restart says anything about which code is running
  - `age_days` / `age_hours` (int, required): how old that commit is at sample time
  - `threshold` (string, required): N, the age at which the daemon is reported stale
  - `notice` (int, required) and `max_notices` (int, required): position in the mail budget for this revision
  - `mailed` (bool, required): whether THIS sample sent mail. False means the sample was suppressed by the backoff or the cap, **not** that the condition eased
  - `behind_main` (int, optional): commits `origin/main` is ahead, when a `[drift_watch] self_repo` is configured and the lookup succeeded. **Context only — it never gates the alarm.** `origin/main` is a remote-tracking ref that only a fetch refreshes, so a suppressor keyed on it would go dark on an unfetched repo, which is the same proxy-goes-dark failure this detector exists to remove
  - `revision_foreign` (bool, optional): the running revision is not in the configured repo **at all**, so the age is measured against some other project's history. A real cause on this host: `~/.pogo` is itself a git repo (mg-3610), so `go build` run inside a polecat worktree walks up and stamps `~/.pogo`'s HEAD
  - `build_modified` (bool, optional): `vcs.modified` — the tree was dirty at build time, so the running code is not exactly `revision`
  - `mail_error` (string, optional): present only when the notice could not be delivered; the event is still emitted so the condition is never lost to a down mail channel

```json
{"schema_version":1,"timestamp":"2026-08-07T12:39:00.000000000Z","event_type":"revision_stale","agent":"pogod","details":{"revision":"d31297f493cdd757fc46654351e0a2c93e66f49b","commit_time":"2026-07-30T00:34:07Z","age_days":8,"age_hours":204,"threshold":"7d","notice":1,"max_notices":4,"mailed":true,"behind_main":85}}
```

#### `revision_stale_disarmed`

The running binary carries no `vcs.revision`/`vcs.time` stamp, so its age cannot be established and the staleness check above is **blind**. Emitted at most once per process, alongside a log line. Every `go test` binary and every `go run` hits this, which is why it does not mail.

It exists so the silence is *declared*. A blind detector and a healthy daemon both produce no `revision_stale` events, and treating that ambiguity as health is the exact mistake mg-5bd2 was filed about.

- **`details` fields:** `reason` (string, required)

```json
{"schema_version":1,"timestamp":"2026-08-07T12:39:00.000000000Z","event_type":"revision_stale_disarmed","agent":"pogod","details":{"reason":"binary carries no vcs.revision/vcs.time stamp"}}
```

#### `gh_teardown_watch_fired`

pogod's gh-issue teardown detector (mg-6e57) sampled the `status=done` gh-issue carriers on its coarse interval and found at least one whose GitHub issue is still open, or whose state could not be established, so it mailed `notify_to` (`pm-pogo` by default — a teardown miss is a fleet workflow failure, not a human decision; mg-b586). It exists because the workflow's last step can silently not run: mg-07ba reached `done, stage: merge` while drellem2/pogo#89 stayed OPEN for four days, and a carrier that completed its teardown is outwardly identical to one that skipped it. **Report-only** — it never closes an issue and never comments. Emitted once per sample that mailed; unchanged findings are re-raised only after `renotify_after`, so this event is not one-per-interval. See [CONFIGURATION.md](CONFIGURATION.md) §"The gh-issue teardown detector" and `internal/ghteardown`.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `miss_count` (int, required): done carriers whose issue is still open with no `gh-open:` declaration
  - `indeterminate_count` (int, required): carriers whose issue state could NOT be established **even though the lookup worked** — the ref no longer resolves, or GitHub reports a state the detector does not model. These are **not** clean; an unreadable answer is reported rather than assumed shut. They are determinations, and re-running reproduces them
  - `blocked_count` (int, required, mg-dd22): carriers that were **never checked** because the instrument failed — no network, no credential, a rate limit. Counted apart from `indeterminate_count` because a failure to measure is not a measurement. On 2026-08-04 one network blip made all 12 carriers in a batch report indeterminate; 6 of them were real teardown misses, and nothing in the output distinguished a masked finding from a real one. Network-class failures are retried with backoff before landing here
  - `instrument_failure` (bool, required, mg-dd22): true when **no** carrier in the run reached a verdict. This is the signature of a broken detector, not of N broken carriers, and it makes "how often does this go blind?" a query rather than a re-read of old mail — the recurrence in mg-dd22 had to be established by hand. Needs at least 2 scanned carriers: from a sample of one, a blind run and a blind carrier are the same observation
  - `failure_classes` (string, optional, mg-dd22): comma-separated distinct causes behind the no-verdict findings — `network`, `auth`, `rate_limit`, `subject`, `unclassified`. Exists so today's network outage never again has to be hand-separated from mg-03ea's auth gap by reading `gh`'s error prose
  - `declared_open_count` (int, required): carriers open on purpose per a `gh-open:` body line; reported but never mailed on their own
  - `scanned` (int, required): how many done carriers were evaluated, so "0 findings" can be told apart from "0 carriers examined"
  - `notified` (string, required): comma-separated mailboxes the notice was sent to, so the routing that actually happened is auditable rather than inferred from config
  - `escalated` (bool, required): true when a finding had gone unresolved past `escalate_after` and `human` was copied in addition to the fleet mailbox
  - `mail_error_<mailbox>` (string, optional): one key per recipient the notice could NOT be delivered to; the event is still emitted so a detected miss is never lost to a down mail channel

```json
{"schema_version":1,"timestamp":"2026-07-21T01:15:00.000000000Z","event_type":"gh_teardown_watch_fired","agent":"pogod","details":{"miss_count":1,"indeterminate_count":0,"blocked_count":0,"instrument_failure":false,"declared_open_count":1,"scanned":3,"notified":"pm-pogo","escalated":false}}
{"schema_version":1,"timestamp":"2026-08-05T10:56:21.000000000Z","event_type":"gh_teardown_watch_fired","agent":"pogod","details":{"miss_count":0,"indeterminate_count":0,"blocked_count":13,"instrument_failure":true,"failure_classes":"network","declared_open_count":0,"scanned":13,"notified":"pm-pogo","escalated":false}}
```

A run whose `instrument_failure` is true **measured nothing**. It is not evidence that a previously reported miss has cleared, and the watcher's escalation clocks are deliberately preserved across it — otherwise a blip every few days would keep an un-actioned finding permanently un-escalated.

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
  - `fleet_count` (int, required): whole cohorts **dark right now** — delivered fires, nothing completing them, over the trailing `cohort_window`. Everyone at 0% on the same cadence is a **scheduler or fleet** fault (suspect the ack path, an auth outage, or pogod itself), and reporting it as N per-agent alerts would name N innocent agents and bury the one fact that matters. This used to be judged on the LIFETIME median and could not clear: two ended outages on 2026-08-10 held one cohort finding escalated for 61 hours while the fleet was healthy, because a cumulative ratio is monotone in past damage (mg-c232). Its only exits were a counter reset — which hides the signal rather than correcting it — and being ignored
  - `cohort_window`, `cohort_delivered`, `cohort_completed`, `cohort_rate` (optional): the windowed measurement behind `fleet_count`. Present whenever `fleet_count > 0`. `cohort_rate` is absolute: no median, no peers
  - `cohort_judged` (array of string, optional): the schedules the cohort rate was measured over — those whose agent was RUNNING for the whole window. "Fires delivered, nothing completed" is also what an ABSENT fleet looks like, so a cohort with too few judged schedules is reported as unmeasured rather than dark
  - `cohort_blind` (string, optional): why the cohort arm could not judge at all. It fails **closed** into this key rather than falling back to the lifetime median, because a fallback would reinstate the removed trigger on exactly the days the events log is unreadable
  - `retired_recovered` (int, optional): per-schedule findings the LIFETIME rule raised and the recent window retired — the schedule is below its peers over its history and is completing its fires now. The window is a one-way veto here: it can retire a finding and can never raise one
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
  - `blackout_judged` (bool, required) / `blackout_blind` (string, optional): whether the ABSOLUTE fleet arm looked. A clear from the peer arm alone is the reading a dead fleet produces
  - `cohort_judged` (bool, required) / `cohort_blind` (string, optional): the same pair for the cohort arm, which judges the same window since mg-c232 and therefore goes blind in the same circumstances
  - `cohort_not_measured` (array of string, optional): cohorts with enough ack-aware members to judge but too little traffic inside the window — a daily-cadence cohort inside a 3-hour window, most obviously. **Unjudged, not healthy**
  - `retired_recovered` (int, optional): lifetime findings the recent window retired. This is the number that makes a NEWLY quiet report readable as "the fleet recovered" rather than as "the detector stopped looking"

```json
{"schema_version":1,"timestamp":"2026-07-29T22:30:00.000000000Z","event_type":"ack_watch_clear","agent":"pogod","details":{"scanned":41,"eligible":3,"skipped_fresh":6,"skipped_few_fires":29,"skipped_not_recurring":1,"skipped_no_peers":2,"blackout_judged":true,"cohort_judged":true}}
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

#### `first_turn_watch_dark`

pogod's first-completed-turn floor ([internal/firstturn](../internal/firstturn/firstturn.go), mg-3cbb) found at least one **crew** agent that it spawned and that has never completed a single scheduled fire since — past the grace, with fires demonstrably delivered to it the whole time. **A spawn is not a success**: on 2026-08-11 this daemon logged `autostart: started X (pid=N)` five times, re-registered every schedule, and passed its own post-check ninety seconds later, over a fleet that then completed zero turns for seventeen hours.

Distinct from `ack_watch_fired` with `blackout: true`, which is the *other* side of a spawn. That arm judges a completion **ratio** over a trailing 3h window and therefore cannot speak about an agent until it has been up that long — on the outage above its first post-bounce firing was 05:03:36Z, one full window after the 02:01:33Z spawn, and it then fired 33 times, correctly. This one is red at 02:46:33Z. `ack_watch` blackout means *was alive, went dark*; this means *was never alive*. **Report-only** — pogod never restarts, nudges, or respawns on this signal. Emitted once per mailed notice; repeats climb a doubling ladder so each carries a strictly larger `dark_for`. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `episode_id` (string, required): stable per-episode id, matching the `incident_episode_cleared` event emitted on close
  - `state` (string, required): always `"dark"` on this event type
  - `fleet` (bool, required): true when EVERY judged agent is a finding and there are at least two of them. It changes the routing, not the severity: a fleet that has never come up cannot be asked to fix itself, so this escalates on its first sample rather than on an age gate
  - `agents` (array of string, required): the bare names, sorted
  - `identities` (array of string, required): the same agents as event-log identities
  - `dark_for` (string, required): how long the **most recently spawned** dark agent has been dark — the conservative answer to "how long has the fleet been like this", because rounding it up would overstate the alarm's own evidence
  - `episode_age` (string, required): how long this episode has been open
  - `judged` (array of string, required): the agents actually judged this sample
  - `scanned` (int, required): agents in the registry before filtering
  - `too_fresh` / `beyond_lookback` / `never_addressed` / `misanchored` (arrays of string, required): the populations declined and why. `never_addressed` is an agent fewer than 2 fires reached — that is `deaf_watch`'s finding and a different remedy, and blaming this agent for it would point the operator at the wrong component. `misanchored` is an agent whose only observed completion **predates its own spawn**: an impossible reading rather than a negative one, since the evidence reader anchors every agent at its own `StartedAt`, so it means the window came from somewhere else and the agent is judged neither dark nor clear (mg-21ad — a mayor with 31 completed turns was mailed about twice as having "completed nothing since it spawned", because its evidence was measured from a crew-mate's spawn 8h17m earlier). A report that states its own denominator cannot be misread as coverage it did not have (mg-7a20)
  - `grace` (string, required): the threshold in force
  - `notified` (string, required): comma-separated recipients
  - `escalated` (bool, required): true when the escalation box was copied
  - `notify_to_dark` / `escalate_to_dark` (bool, required): whether the recipient is ITSELF one of the dark agents. `escalate_to_dark: true` means the notice had no recipient outside the outage at all
  - `mail_error_<mailbox>` (string, optional): one key per recipient that refused it

```json
{"schema_version":1,"timestamp":"2026-08-11T02:46:33.000000000Z","event_type":"first_turn_watch_dark","agent":"pogod","details":{"episode_id":"ep-1786502793000000000-architect","state":"dark","fleet":true,"agents":["architect","mayor","pa","pm-onethird","pm-pogo"],"identities":["crew-architect","crew-mayor","crew-pa","crew-pm-onethird","crew-pm-pogo"],"dark_for":"45m0s","episode_age":"0s","judged":["architect","mayor","pa","pm-onethird","pm-pogo"],"scanned":5,"grace":"45m0s","notified":"mayor,human","escalated":true,"notify_to_dark":true,"escalate_to_dark":false}}
```

#### `first_turn_watch_clear`

One sample of the floor with nothing to report — every judged agent has completed at least one fire since it spawned — carrying the coverage counts. Also emitted with `suppressed: true` while pogod is inside its own settle window after a restart, since a bounce spawns the whole crew at once and none of them can have acked yet.

It exists for the reason this whole ticket exists: a silent correct outcome and a control that is not running are the same observation. The synthetic-failure-turn detector ran ~204 checks across 17h of total fleet silence and emitted nothing at all, and that quiet was read as the fleet's health.

- **`details` fields:** `judged`, `scanned`, `too_fresh`, `beyond_lookback`, `never_addressed`, `misanchored`, `grace` — as on `first_turn_watch_dark`; plus `suppressed` (bool, optional), `reason` (string, optional) and `would_have_reported` (array of string, optional) on the settle-window path

#### `first_turn_watch_blind`

The floor could not judge this sample: no agent registry, an unreadable scheduler event log, or a source error. It judged **nothing** — this is not a health claim in either direction, and a detector that reads green because it could not look is the founding bug of this whole lineage one level up.

- **`details` fields:** `reason` (string, required), `scanned` (int, required), `why` (string, required); plus `phase: "clear"` and `to` when the failure was in delivering the all-clear

#### `first_turn_watch_unreported`

Every recipient of a finding refused the mail. The one state worse than the bug this arm fixes: the fleet never came up, pogod noticed, and the notice did not leave the machine. Mirrors `ack_watch_blackout_unreported`.

- **`details` fields:** `recipients` (string, required), `agents` (array of string, required), `dark_for` (string, required)
#### `turn_watch_finding`

pogod's turn-completion reader ([internal/turnwatch](../internal/turnwatch/turnwatch.go), mg-a270) found at least one agent that is PRESENT and has completed no recent turn, and mailed. The evidence is `~/.pogo/agents/turnlog/<name>.log` — one line per completed turn, appended by the agent itself via `pogo turn-done` — which is the only artifact on this machine that **nothing but a completed turn produces**. Every other liveness-adjacent signal here describes something pogod did, and on 2026-08-10/11 all of those were green and truthful for 22 hours while the fleet did no work.

**The recipient is the interesting field.** A finding about the COORDINATOR goes to the escalation box and never to the coordinator. Every fleet-wide scheduled check on this machine is coordinator-owned, so a detector that routes through the coordinator cannot report the coordinator being down — that circularity, not a mistuned threshold, is why the outage read green. `reader_is_pogod` and `routed_via_mayor` are carried on every event so the property is auditable from the log rather than only from the source.

**Report-only** — no nudge, restart or stop seam. Emitted once per sample that mailed; an unchanged roster is re-raised only after `renotify_after`. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `findings` (array of string, required): `<agent>:<verdict>`, coordinator first. Verdicts are `stale` (completed turns before, none recently), `silent` (never wrote one), `unreadable` (artifact could not be parsed)
  - `coordinator_hit` (bool, required): whether the coordinator is among the findings
  - `population` (int, required): how many present agents were examined — zero findings over a zero population is not a clean fleet
  - `notified` (array of string, required): the mailboxes told
  - `reader_is_pogod` (bool, required): always true; present so a future crew-resident reader is distinguishable in the log
  - `routed_via_mayor` (bool, required): always false

```json
{"schema_version":1,"timestamp":"2026-08-11T20:30:00.000000000Z","event_type":"turn_watch_finding","agent":"pogod","details":{"findings":["mayor:stale","architect:silent"],"coordinator_hit":true,"population":5,"notified":["human"],"reader_is_pogod":true,"routed_via_mayor":false}}
```

#### `turn_watch_skipped`

An agent was red but was NOT judged, because it is still inside its post-start grace window (45 min by default). An agent thirty seconds old with no completed turn has not failed to complete one; it has not had time. Emitted rather than swallowed so "declined to judge" and "judged and found nothing" stay apart — the distinction this whole lineage keeps rediscovering.

- **`details` fields:**
  - `target` (string, required): the bare agent name
  - `verdict` (string, required): the reading that was not acted on
  - `why` (string, required): the grace window and its length

#### `turn_watch_clear`

The last finding cleared: every present agent has completed a turn inside the window. Emitted on the transition only, unlike `ack_watch_clear`, because `turn_watch_finding` already carries the population count on every mailing sample and the coarse interval here is 15m.

- **`details` fields:**
  - `population` (int, required): present agents examined
  - `live` (int, required): how many completed a turn inside the window

#### `turn_watch_error`

The reader could not produce a reading, so it evaluated nothing this sample — an unreachable agent registry, most plausibly. It is emitted rather than passed over in silence because without the population the only remaining list would be the turnlog directory, and an agent that has never written a line is absent from that directory: a scan built the other way is structurally blind to exactly the agents this detector exists to find. Also emitted when a notice could not be delivered.

- **`details` fields:**
  - `error` (string, required): why the fleet could not be judged, or why the mail failed
  - `to` (string, optional): the mailbox that could not be reached

```json
{"schema_version":1,"timestamp":"2026-08-11T20:30:00.000000000Z","event_type":"turn_watch_error","agent":"pogod","details":{"error":"turnlog: could not determine which agents are present: connection refused"}}
```

#### `synthetic_failure_detected`

pogod's synthetic-failure-turn detector ([internal/synthwatch](../internal/synthwatch/synthwatch.go), mg-8cdb) read the agent's harness session transcript and found it answering turns **locally** and failing them: turns attributed to a synthetic model, with zero tokens in and out, flagged as API errors. The agent is alive and consuming every nudge on time; it accomplishes nothing with them. Detection is structural (synthetic model + zero usage + error flag), never a message string.

This is distinct from `usage_limit_hit`, which reads the PTY modal. This one reads the transcript, so it also sees the members that never render a modal at all — expired credentials above all. Emitted once per agent per episode; the paired `synthetic_failure_cleared` fires on recovery. **A restart cannot fix this class** — see `synthetic_failure_restart_suppressed`. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **Optional envelope:** `work_item_id`
- **`details` fields:**
  - `target` (string, required): the bare agent name
  - `reason` (string, required): `auth_failed` | `rate_limit` | `weekly_limit` | `spend_limit` | `server_error` | `invalid_request` | `unclassified`
  - `failing_turns` (int, required): count inside the detection window. **A count, not a rate** — the threshold is 2, so this fires alongside any number of successful turns. Never read it as "the agent is failing every turn" (mg-c058).
  - `window_seconds` (int): the size of the trailing window `failing_turns` was counted over. Added mg-c058: without it a reader supplies their own window, and the counter's window is narrower than the fault at both ends.
  - `first`, `last` (RFC3339, required): the bounds of the counted errors — **not** the bounds of the fault, which can start before `first` and continue past `last`.
  - `detail` (string): the harness's own error text, truncated
  - `remediation` (string): always the page-don't-restart directive in v1

```json
{"schema_version":1,"timestamp":"2026-07-22T00:10:26.000000000Z","event_type":"synthetic_failure_detected","agent":"crew-pm-pogo","details":{"target":"pm-pogo","reason":"auth_failed","failing_turns":14,"window_seconds":1800,"first":"2026-07-21T23:10:26Z","last":"2026-07-22T00:10:26Z","detail":"Login expired · Please run /login","remediation":"page a human; restart is suppressed and cannot help"}}
```

#### `synthetic_failure_cleared`

The agent left the failing state: its transcript now shows real model turns in the window, or it stopped running. Restart suppression is lifted. Note that a transcript becoming **unreadable** does NOT clear the state — only a positive quiet reading does, because "we stopped being able to look" is not "it recovered". Additive — no `schema_version` bump.

**This is a per-agent transition, not an episode boundary, and since mg-70f3 the two are far apart.** The episode — and its `human` mail — stays open for a further `synthwatch.DefaultClearHold` (60m) of continuous quiet, so a `synthetic_failure_cleared` here is routinely followed by a fresh `synthetic_failure_detected` for the same episode with no mail in between. Count `incident_episode_cleared{kind:auth}` for episodes; count this for agents.

- **`details` fields:** `target` (string, required)

```json
{"schema_version":1,"timestamp":"2026-07-22T22:40:37.000000000Z","event_type":"synthetic_failure_cleared","agent":"pm-pogo","details":{"target":"pm-pogo"}}
```

#### `synthetic_failure_episode_held`

The class **recurred inside an open episode's quiet hold**, so pogod extended the episode instead of closing it and re-opening — withholding one clear mail and one re-open page (mg-70f3). Each of these is a flap that did not reach a human, and counting them is the only way to answer "is this alarm still flapping" without re-deriving it from mail. Additive — no `schema_version` bump.

The founding population: 49 open pages and 44 clear notices in `~/.pogo/reminders/deadman.log` as of 2026-08-14T08:16Z, including **five** clear→re-alarm cycles on 2026-08-14 with gaps of 2m50s, 2m29s, 10m09s, 3m31s and 31m32s — one intermittent github.com reachability fault reported as six short ones. Every gap here is from the mail's maildir SEND stamp; the log line records when the delivering daemon noticed it, a lag of 16m26s on the anchor page.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **Optional envelope:** `work_item_id`
- **`details` fields:**
  - `target` (string, required): the agent whose recurrence extended the episode. **Incidental** — it is whoever took a turn during the burst, not an identification of the affected agent.
  - `reason` (string, required): the class member, as above
  - `episode_id` (string, required): the episode this was folded into — the same id its eventual `incident_episode_cleared` carries
  - `quiet_seconds` (int, required): how long the episode had been quiet when the class came back. This is the flap gap.
  - `hold_seconds` (int, required): the hold in force, so a suppression can be checked against the bound that produced it
  - `recurrence` (int, required): 1-based index of this recurrence within the episode
  - `why`, `withheld` (string): the rationale and what was not sent

```json
{"schema_version":1,"timestamp":"2026-08-14T03:07:23.000000000Z","event_type":"synthetic_failure_episode_held","agent":"cat-p82a6","details":{"target":"p82a6","reason":"server_error","episode_id":"ep-1786789692000000000-crew-mayor","quiet_seconds":171,"hold_seconds":3600,"recurrence":1,"why":"the class recurred inside the episode's quiet hold; the episode was extended instead of closed and re-opened (mg-70f3)","withheld":"one clear mail and one re-open page"}}
```

#### `synthetic_failure_page_suppressed`

The **paging floor** withheld an episode-open page: an open page for the *same reason* went out less than `synthwatch.DefaultMinPageInterval` (30m) ago. The floor is a backstop independent of the episode machinery, and it has one unconditional escape — **a page for a different reason is never withheld**, because a reason change is new information about a different fix. Additive — no `schema_version` bump.

Under the shipped configuration this event should not appear at all: `DefaultMinPageInterval` (30m) is below `DefaultClearHold` (60m), so two episode-opens can never be closer together than a full hold. Seeing one means the hold was shortened or disabled.

- **`details` fields:**
  - `target`, `reason` (string, required): as above
  - `subject` (string, required): the page that was not sent, verbatim
  - `since_last_page_sec` (int, required): how long since the last page of this reason
  - `floor_sec` (int, required): the floor in force
  - `why` (string, required): the rationale

```json
{"schema_version":1,"timestamp":"2026-08-10T20:01:24.000000000Z","event_type":"synthetic_failure_page_suppressed","agent":"crew-mayor","details":{"target":"mayor","reason":"spend_limit","subject":"AGENTS FAILING TURNS — mayor (spend_limit): 2 errors in 30m, 2026-08-10T19:31:02Z–19:59:40Z","since_last_page_sec":41,"floor_sec":1800,"why":"an episode-open page for the same reason went out less than the paging floor ago (mg-70f3); a DIFFERENT reason is never floored"}}
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
- **Optional envelope:** `work_item_id`, `repo` — present whenever the exited agent had them
  (mg-32e3). A lost notice is exactly when this event is the only surviving trace, so it has to
  answer the same question the notice would have: which item just became unsafe to dispatch at
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
{"schema_version":1,"timestamp":"2026-07-30T04:06:00.000000000Z","event_type":"worktree_notice_undelivered","agent":"mayor","work_item_id":"mg-8c66","repo":"/Users/daniel/dev/pogo","details":{"row":"A15","exited_agent":"cat-mg-8c66","worktree":"/Users/daniel/.pogo/polecats/8c66","outcome":"preserved","mail_error":"mg mail send failed: no such mailbox"}}
```

#### `worktree_preserved`

An exited agent's worktree was RETAINED rather than reaped, and **the work item it belongs to now has
work that no branch and no push can see** (mg-32e3).

**Every guard defined over COMMITS is blind to this, by construction.** The
spawn-time stranded-work refusal, `git cherry`, `strandedwork.Inspect`, `pogo check-stranded` and both
`work_item_stranded_push` reporters are all defined over PUSHED commits, and a polecat commits at the
END of its life — so this is not an edge case, it is the normal mid-flight state of every worker and
exactly what a crash, a stop or an outage leaves behind. Since mg-836c one guard is NOT blind to it:
the spawn-time **preserved-worktree** gate reads the trees directly (see
`dispatch_preserved_worktree_overridden` above). `~/.pogo/polecats/qbe37` was
preserved on 2026-08-10 with 16 uncommitted paths, including a 1450-line package that existed in no
other location on the machine; `pogo gc` would eventually have reclaimed the tree.

**It is the record half of a path that only ever had a mail half** — the exact mirror of
`work_item_stranded_push`, whose event half worked and whose mail half was missing until mg-be37. The
preservation notice worked (22 delivered notices over three days) but emitted nothing structured, so
three days of preservations had to be reconstructed by grepping `PRESERVED worktree` out of
`pogod.log` — which pogod writes to inherited stderr and which is therefore not durable at all.

**One event type covers both retention outcomes**, discriminated by `outcome` and using
`worktree_notice_undelivered`'s vocabulary so the two join on that field. A consumer asking "does this
item have work nobody pushed?" wants both: `preserved` is a positive finding and `undetermined` is a
tree that could not be ruled out.

**It reports; it blocks nothing — and for eight days that was the whole defect.** No tree is reclaimed
on the strength of it, and no dispatch was refused on it either. The mail beside it fires ONCE, says so,
and goes to ONE addressee; on 2026-08-19 that addressee was among the agents down in the outage the
notice was reporting on, and the item it named stayed `available` with priority-wake advertising it as
ready. Dispatch is now gated on the trees themselves rather than on this event or its mail (mg-836c) —
which is the difference between a notice and a guard, and leaves this event doing what it is good at:
being the durable record. Additive — no `schema_version` bump.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (the exited agent whose
  tree it is — the opposite attribution from `worktree_notice_undelivered` above, and for the same
  reason: there the open question is what the addressee was never told, here it is what this agent
  left behind), `details`
- **Optional envelope:** `work_item_id` (absent for a crew agent or a polecat spawned without `--id`),
  `repo`
- **`details` fields:**
  - `worktree` (string, required): the retained tree
  - `outcome` (string, required): `"preserved"` or `"undetermined"`, as above
  - `pushed` (bool, required): always `false`, stated rather than implied — this is the population
    every pushed-commit guard misses, and a consumer should not have to infer that from a type name
  - `detail` (string, required): the underlying refusal, including the dirty paths or the `git status`
    failure
  - `dirty_paths` (int), `modified_paths` (int), `untracked_paths` (int) and `files` ([]string):
    present only when `outcome` is `"preserved"`, because a count is meaningful only when the tree
    was actually read — a `0` on an unreadable tree would assert it was clean, which is a claim
    nobody established. `files` is capped at 10 entries; the three counts are computed over the
    **full** porcelain output, so `modified_paths + untracked_paths == dirty_paths` holds even when
    `files` is truncated
  - `branch` (string) **or** `branch_error` (string), never both (mg-d45b): the branch checked out in
    the retained tree, or why it could not be read. They are alternatives so that a consumer reading
    `branch` can trust it was actually observed — a key that merely disappears on failure is
    indistinguishable from one nobody implemented, which is this event's own defect one layer down.
    A detached HEAD reports the literal `"HEAD"`, git's own answer, because a preserved tree with no
    branch name to hand a rescuer is a worse situation and must stay distinguishable from a failed
    read. `branch_error` is the norm on `outcome: "undetermined"`, where `git status` has already
    failed and `rev-parse` usually fails for the same reason

**Why the modified/untracked split is not cosmetic (mg-d45b).** The two halves have different
consequences. A modified tracked path still has its committed version in the object store, so the
exposure is a lost edit. An untracked path is on no branch, in no stash and on no remote, and the
preserved tree is its **only copy on the machine** — that is how `~/.pogo/polecats/qbe37` came to hold
the sole copy of a 1450-line `internal/strandwatch/` package. A single `dirty_paths: 16` cannot
distinguish sixteen tweaks from sixteen irreplaceable files, so a consumer deciding whether a
preservation is urgent had to open the tree and look, which is exactly the by-hand reconstruction this
event exists to replace.

```json
{"schema_version":1,"timestamp":"2026-08-10T01:51:59.000000000Z","event_type":"worktree_preserved","agent":"cat-qbe37","work_item_id":"mg-be37","repo":"/Users/daniel/dev/pogo","details":{"worktree":"/Users/daniel/.pogo/polecats/qbe37","outcome":"preserved","branch":"polecat-qbe37","pushed":false,"dirty_paths":16,"modified_paths":2,"untracked_paths":14,"files":["?? internal/strandwatch/"],"detail":"worktree /Users/daniel/.pogo/polecats/qbe37 has 16 uncommitted change(s), refusing to remove: ..."}}
```

### Server run mode

pogod runs in one of two modes. `full` permits agent, refinery and scheduler work; `index-only`
keeps indexing alive and answers **503 on every `/agents/`, `/refinery/` and `/scheduler/`
endpoint** (`RequireOrchestration`). The mode is readable on demand at `GET /server/mode`, but a
reading taken now says nothing about a transition that happened six hours ago — and in index-only
mode a daemon that dispatches nothing still answers `/version`, still lists its crew, and is
healthy by every instrument anyone reaches for. The two events below exist so the *transition* is
an artifact rather than something inferred (mg-293c).

Both events are also written to `pogod.log` as `server: run mode ...` lines. That is deliberate
duplication, not an oversight: the log line is what an operator tailing the daemon sees, and the
event is the artifact of record because it does not depend on where the process's stderr was
pointed. That distinction has already cost a fleet: the transition sites have logged since
mg-ce65 (2026-03), and four months of `~/Library/Logs/pogo/pogod.log*` contain **zero** such
lines — including across the 2026-08-07 03:00 window in which `POST /agents/drain` was
demonstrably answered 503 by `RequireOrchestration`.

#### `server_mode_changed`

pogod's run mode actually changed. Emitted **only on a real transition** — a stop against an
already-stopped daemon, or a start against an already-full one, changes nothing and records
nothing. That is what makes the absence of this event evidence: an idle daemon and a stopped one
are otherwise indistinguishable.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `from` (string, required): the mode before the change — `"full"` or `"index-only"`
  - `to` (string, required): the mode after the change
  - `attributed` (bool, required): whether the caller identified itself at all. `false` means the
    transition happened and nobody can be named for it — a finding, not a gap in the schema
  - `trigger` (string, required): `"http"` (a mode endpoint), or `"unattributed"` (an in-process
    `SetMode` with no caller context)
  - `detail` (string, required): the route or reason, e.g. `"POST /server/stop-orchestration"`
  - `actor_agent` (string, optional): the caller's `POGO_AGENT_NAME`. Omitted, never blank, when
    the caller is not an agent
  - `actor_client` (string, optional): the caller's command, e.g. `"pogo service install"`. Only
    leading non-flag argv words are captured, so flag values never reach the log
  - `actor_pid` (int-as-string, optional): the caller's process id
  - `remote_addr` (string, optional), `user_agent` (string, optional): the HTTP peer

```json
{"schema_version":1,"timestamp":"2026-08-07T02:00:10.000000000Z","event_type":"server_mode_changed","agent":"pogod","details":{"from":"full","to":"index-only","attributed":true,"trigger":"http","detail":"POST /server/stop-orchestration","actor_agent":"mayor","actor_client":"pogo service install","actor_pid":"4711","remote_addr":"127.0.0.1:52144","user_agent":"Go-http-client/1.1"}}
```

**Attribution comes from three headers** (`X-Pogo-Agent`, `X-Pogo-Client`, `X-Pogo-Pid`) stamped by
`internal/client` on the two mode-transition requests. A caller that does not send them — `curl`, or
a future path that bypasses the client — still produces a record, marked `attributed: false` and
`[UNATTRIBUTED]` in the log line. The headers exist because Go's default client identifies itself as
`Go-http-client/1.1` and nothing else; tracing "who stopped orchestration at 02:00Z" on 2026-08-07
consumed two days across three agents and ended on a leading candidate it could not demonstrate.

Note the call sites are open-ended and the record is written at the **transition**, not at the call.
pm-pogo enumerated the callers as "the two HTTP handlers" and was corrected the same day by an
in-tree one nobody had grepped for: `internal/service`'s `quiesceCrew`, which stops fleet-wide
dispatch as a side effect of installing a launchd job (mg-6515).

To ask who last darked the fleet:

```bash
pogo events list --type=server_mode_changed --since=24h
```

Or, for just the transition and the caller:

```bash
jq -r 'select(.event_type=="server_mode_changed") |
       "\(.timestamp) \(.details.from) -> \(.details.to)  \(.details.actor_client // "?") \(.details.actor_agent // "")"' \
  ~/.pogo/events.log
```

#### `server_mode_boot`

The mode a pogod process started in, emitted unconditionally at server construction. `ModeFull` is
`iota` = 0 so a fresh process always boots `full` — this event is not there to reveal a surprise but
so that "which mode did it boot into" is answerable from a record rather than inferred from the
absence of a transition, which is the inference this pair exists to retire.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `mode` (string, required): `"full"` or `"index-only"`
  - `trigger` (string, required): always `"startup"`
  - `detail` (string, required): always `"process start"`
  - `actor_pid` (int-as-string, required): the daemon's own pid

```json
{"schema_version":1,"timestamp":"2026-08-07T18:37:28.000000000Z","event_type":"server_mode_boot","agent":"pogod","details":{"mode":"full","trigger":"startup","detail":"process start","actor_pid":"32415"}}
```

### Command line

Every other event type in this catalog is written by a daemon. This section holds the one exception, and it is deliberately one exception rather than a category.

#### `investigation_search`

Somebody ran `pogo investigations` — the search over `docs/investigations/` file contents (mg-22c7). Emitted **once per invocation, on every path out of the command**, including searches that matched nothing and invocations that failed before searching.

That completeness is the reason the event exists. The command is phase 1 of a gated decision: phase 2 (suggesting matching investigations at `mg new`, or carrying the search in the polecat dispatch template) is justified only by phase 1 being built and going **unused**, since that is what would distinguish a recall problem from a friction problem. Nothing else on this box records a CLI invocation, so without this line the branch that justifies phase 2 would produce no artifact at all, and "nobody ran it" would be indistinguishable from "nobody needed it". A `no_match` is the most informative record the command can leave — someone had a question and the corpus did not answer it — so it is emitted with the same weight as a hit.

**This is not a general `cli_invoked`.** One event, one subcommand. A general CLI-invocation event is a separate decision with its own volume and privacy questions, and nobody has made it.

**Reading it at the gate:** the count alone cannot settle the question, because a zero could mean nobody remembered (recall failure → build phase 2) or that no question arose the corpus could answer (no problem → do nothing). The deciding measurement is how many incidents and investigations in the window had an answer in the corpus that went unfound; this count is the cheap half. `agent` and `corpus_dir` are there so build-time invocations from the polecat worktree that produced the command can be excluded from the count.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `details`
- **`details` fields:**
  - `query` (string, required): the search terms as given, space-joined. Empty for the listing mode (`pogo investigations` with no terms), which is still an invocation
  - `terms` (int, required): number of terms
  - `outcome` (string, required): `"matched"`, `"no_match"`, or `"error"`
  - `corpus_dir` (string, required): the directory searched; empty only when the corpus could not be located
  - `files_searched` (int, optional): the denominator — absent only on the `error` outcome, where no search ran
  - `matches` (int, optional): documents matched
  - `unindexed` (int, optional): how many of `files_searched` are absent from that directory's `README.md`. A diagnostic on the index's staleness, never a filter on the search
  - `skipped` (int, optional): entries in the directory that were **not** searched (binary, hidden, unreadable)
  - `error` (string, optional): present only on the `error` outcome

```json
{"schema_version":1,"timestamp":"2026-08-12T07:04:01.909657000Z","event_type":"investigation_search","agent":"cat-p22c7","details":{"query":"drain stall","terms":2,"outcome":"no_match","corpus_dir":"/Users/daniel/dev/pogo/docs/investigations","files_searched":46,"matches":0,"unindexed":10,"skipped":0}}
```

Read them back with:

```bash
pogo events list --since=720h --type=investigation_search
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
