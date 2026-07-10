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

## Envelope

Every line is a JSON object with the same envelope:

| Field           | Type   | Required | Description |
|-----------------|--------|----------|-------------|
| `schema_version`| int    | yes      | Schema version. `1` for the initial release. Bump on incompatible changes. |
| `timestamp`     | string | yes      | RFC3339 with nanosecond precision, UTC. Example: `"2026-04-25T17:42:09.123456789Z"`. |
| `event_type`    | string | yes      | One of the event types in the catalog below. Dot-separated namespace is reserved for future expansion (e.g. `agent.spawned`); v1 uses flat names listed below. |
| `agent`         | string | see note | The acting agent's identity. `"ringmaster"`, `"crew-arch"`, `"cat-mg-0241"`, `"refinery"`, `"mg"`, `"human"`, etc. Required for every event except those with no clear actor (none in v1, so effectively always required). |
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

- **Crew agents:** `crew-<name>` (matches process name `pogo-crew-<name>` minus the `pogo-` prefix). Examples: `crew-arch`, `crew-ops`. Exception: the coordinator uses its bare configured name (`[agents]` coordinator, default `ringmaster`) with no `crew-` prefix.
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

### Polecat-specific lifecycle

`agent_spawned` and `agent_stopped` already cover polecats. The two events below give polecat lifecycle a dedicated, work-item-scoped narrative for tools that want to filter by polecat events without inferring from `agent_type`.

#### `polecat_spawned`

A polecat has been spawned for a specific work item. Emitted in addition to `agent_spawned` to make polecat-specific queries cheap.

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent`, `work_item_id`, `repo`, `details`
- **`details` fields:**
  - `template` (string, required): name of the polecat template (e.g. `"polecat"`, `"researcher"`)
  - `branch` (string, required): branch name the polecat will work on (e.g. `"polecat-mg-0241"`)
  - `parent` (string, optional): identity of the spawning agent (`"ringmaster"`, `"human"`)

```json
{"schema_version":1,"timestamp":"2026-04-25T10:00:00.150000000Z","event_type":"polecat_spawned","agent":"cat-mg-0241","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"template":"polecat","branch":"polecat-mg-0241","parent":"ringmaster"}}
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
  - `to` (string, required): recipient identity (e.g. `"ringmaster"`, `"crew-arch"`)
  - `subject` (string, required)
  - `mail_id` (string, optional): macguffin mail ID, if assigned

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:00.000000000Z","event_type":"mail_sent","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"to":"ringmaster","subject":"merge failed for mg-0241","mail_id":"mail-2f81"}}
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
{"schema_version":1,"timestamp":"2026-04-25T10:15:30.000000000Z","event_type":"nudge_sent","agent":"ringmaster","details":{"to":"crew-arch","message":"check your mail","delivery":"pty","mode":"idle"}}
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
  - `attempt` (int, required): attempt number that succeeded (`0` when no merge attempt ran: restart recovery found the merge already pushed, or the branch was already merged at processing time)
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
  - `stage` (string, required): which pipeline stage failed — `"fetch"`, `"rebase"`, `"build"`, `"test"`, `"push"`, `"unknown"`
  - `reason` (string, required): short error summary, single line, ≤ 200 chars
  - `terminal` (bool, required): `true` if the refinery has given up (no more retries); `false` if another attempt will follow
  - `gate_output_truncated` (string, optional): up to 1 KB of gate stderr/stdout for quick triage. Full output remains in the in-memory MR record (or persisted history once recommendation §1 lands).

```json
{"schema_version":1,"timestamp":"2026-04-25T10:23:05.000000000Z","event_type":"refinery_merge_failed","agent":"refinery","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"merge_request_id":"mr-9482","branch":"polecat-mg-0241","target":"main","attempt":1,"stage":"test","reason":"./test.sh exited with status 1","terminal":false,"gate_output_truncated":"--- FAIL: TestEventEmit ..."}}
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

pogod's stall watcher (gh drellem2/macguffin #12) crossed a work-pile-up threshold for the watched agent (the coordinator, `ringmaster` by default) and emitted a nudge. One event per offending batch per heartbeat tick, rate-limited by a per-category cooldown. See [stall-watch-design.md](design/stall-watch-design.md).

- **Required envelope:** `schema_version`, `timestamp`, `event_type`, `agent` (always `"pogod"`), `details`
- **`details` fields:**
  - `category` (string, required): `"unclaimed_items"` or `"unread_mail"`
  - `watched_agent` (string, required): the agent that was nudged
  - `nudge_error` (string, optional): present only when delivery failed; the event is still emitted
  - For `unclaimed_items`: `item_count` (int), `item_ids` ([]string), `age_threshold` (string), `oldest_age_seconds` (float)
  - For `unread_mail`: `unread_count` (int), `max_count` (int), `oldest_age_seconds` (float), `age_threshold` (string), `over_count` (bool), `over_age` (bool)

```json
{"schema_version":1,"timestamp":"2026-06-10T16:20:00.000000000Z","event_type":"stall_watch_fired","agent":"pogod","details":{"category":"unclaimed_items","watched_agent":"ringmaster","item_count":2,"item_ids":["mg-2350","mg-9299"],"age_threshold":"10m0s","oldest_age_seconds":1830.4}}
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

## Worked example: a polecat merge cycle

The lines below show the canonical event sequence for a successful polecat run. Times are illustrative.

```json
{"schema_version":1,"timestamp":"2026-04-25T09:59:55.000000000Z","event_type":"work_item_claimed","agent":"cat-mg-0241","work_item_id":"mg-0241","details":{"title":"F1: Design event log schema","tags":["pogo","event-log","phase-f"]}}
{"schema_version":1,"timestamp":"2026-04-25T10:00:00.000000000Z","event_type":"agent_spawned","agent":"cat-mg-0241","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"agent_type":"polecat","pid":48213,"prompt_file":"/Users/daniel/.pogo/agents/templates/polecat.md","worktree":"/Users/daniel/.pogo/polecats/pc-0241"}}
{"schema_version":1,"timestamp":"2026-04-25T10:00:00.150000000Z","event_type":"polecat_spawned","agent":"cat-mg-0241","work_item_id":"mg-0241","repo":"/Users/daniel/dev/pogo","details":{"template":"polecat","branch":"polecat-mg-0241","parent":"ringmaster"}}
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
