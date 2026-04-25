# Event Log Schema

Append-only event log for pogo. Captures agent lifecycle, work item transitions, mail, nudges, and refinery merges so the system is observable without coordination overhead.

This document is the design contract for phase F (work items mg-0241, mg-700a, mg-22ed, mg-4fa7, mg-287e, mg-156b, mg-214a). F1 (this doc) defines the schema. F2 onward wires emission into pogod, agent lifecycle, mail, nudge, and the refinery.

## File

- **Path:** `~/.pogo/events.log`
- **Format:** JSONL (one JSON object per line, UTF-8, terminated by `\n`)
- **Mode:** append-only. Writers must `O_APPEND | O_WRONLY | O_CREAT` and emit a single line per event. No edits, no deletes (rotation is a separate concern — see F7).
- **Concurrency:** multiple writers (pogod, mg, mail, refinery, polecats) may append concurrently. POSIX `write(2)` of a single line ≤ `PIPE_BUF` (4096 on Linux, 512 on macOS) is atomic against other appenders. Events larger than `PIPE_BUF` must use a process-level mutex or `flock(2)` on the file. Implementations should keep the JSON object well under 512 bytes whenever possible; lines that exceed it must take an advisory lock.
- **Persistence:** survives pogod restarts (unlike the in-memory refinery history). This is the durable observability spine.
- **Not coordination:** the log is purely observational. It is not used to drive state transitions. macguffin remains the source of truth for work item state.

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

- **Crew agents:** `crew-<name>` (matches process name `pogo-crew-<name>` minus the `pogo-` prefix). Examples: `crew-arch`, `crew-ops`, `mayor`.
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
  - `mode` (string, optional): `"idle"` (default) or `"immediate"`

```json
{"schema_version":1,"timestamp":"2026-04-25T10:15:30.000000000Z","event_type":"nudge_sent","agent":"mayor","details":{"to":"crew-arch","message":"check your mail","delivery":"pty","mode":"idle"}}
```

### Refinery

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
  - `attempt` (int, required): attempt number that succeeded
  - `duration_seconds` (number, optional): total time from `refinery_merge_attempted` (attempt 1) to merge

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
  - `stage` (string, required): which pipeline stage failed — `"fetch"`, `"rebase"`, `"build"`, `"test"`, `"push"`, `"unknown"`
  - `reason` (string, required): short error summary, single line, ≤ 200 chars
  - `terminal` (bool, required): `true` if the refinery has given up (no more retries); `false` if another attempt will follow
  - `gate_output_truncated` (string, optional): up to 1 KB of gate stderr/stdout for quick triage. Full output remains in the in-memory MR record (or persisted history once recommendation §1 lands).

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:05.000000000Z","event_type":"refinery_merge_failed","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","attempt":1,"stage":"test","reason":"./test.sh exited with status 1","terminal":false,"gate_output_truncated":"--- FAIL: TestEventEmit ..."}}
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

- **macguffin event log (`~/.macguffin/log/`)**: macguffin maintains its own append log for work item state transitions and mail. Pogo's event log is broader (it includes agent lifecycle and refinery merges) and lives in `~/.pogo/`. The work item events (`work_item_claimed`, `work_item_completed`) and `mail_sent` mirror macguffin transitions into the pogo log so a single tail shows the whole system. Phase F4 (mg-4fa7) wires this mirroring; until then, pogod components write directly and `mg` writes nothing into `~/.pogo/events.log`.
- **Refinery in-memory history**: still authoritative for queue/active state. The event log is the durable record. Once F5 (mg-287e) lands, the refinery emits an event for every merge attempt, success, and failure, so post-mortem investigation no longer depends on the in-memory history surviving a restart.
- **PTY ring buffer**: per-agent, 64 KB, lost on agent exit. The event log is system-wide and durable. The two are complementary — the ring buffer captures *what the agent said*, the event log captures *what happened*.

## Non-goals (v1)

- **No event ordering guarantees beyond per-writer order.** Two writers appending concurrently may interleave. Consumers ordering by `timestamp` is good enough.
- **No querying by index.** `grep`, `jq`, and the `pogo events` CLI (F6) are the query surface. No SQL, no full-text search.
- **No retention policy in the schema.** Rotation is F7's problem (mg-214a). Schema-wise, the file grows unboundedly until rotated.
- **No event correlation IDs.** `work_item_id` and `merge_request_id` already correlate the events that matter most. A generic correlation ID can be added later as an additive `details` field without bumping `schema_version`.

## Open questions for F2+

These are deliberately deferred — flagged here so the implementation tasks can resolve them:

- **Where is the writer library?** Proposed in F2 (mg-700a): `internal/events.Emit(ctx, event)` with the file path resolved from config (default `~/.pogo/events.log`).
- **How does `mg` emit?** Either (a) `mg` shells out to the writer library directly (requires `mg` to know about pogo) or (b) pogod exposes an HTTP endpoint and `mg` posts to it. Decide in F4 (mg-4fa7).
- **What happens when the disk is full?** Writer should drop the event with a stderr warning rather than blocking the calling code path. Decide in F2.
- **Truncation policy for `gate_output_truncated` and `last_output`.** 1 KB and 512 B respectively are first guesses; revisit once we see real volumes.
