# Stall-watch design — pogod-side work-pile-up nudges

Status: implemented (mg-b971). Source: `internal/stallwatch/`, wired in
`cmd/pogod/main.go`. Origin: cross-droplet feature request gh
[drellem2/macguffin #12](https://github.com/drellem2/macguffin/issues/12)
(CloverRoss, 2026-06-02).

## Problem

The mayor runs an LLM-driven loop that is supposed to, every cycle, check its
mail and check for available work to dispatch. Under prompt drift or LLM
cycle-skipping, the loop can keep *running* (process healthy, `health=ok`)
while silently dropping those steps. Work then piles up: items sit unclaimed in
`available/`, mail accumulates unread in `new/`. Health-based watchers don't
catch this — the mayor isn't stalled, it's *behaviorally* stalled.

This is the third leg of the wedge-response triad:

- **Leg 1 (Ocean, Mayor §3c)** — catches Director / Architect / Doctor *process*
  stalls.
- **Leg 2 (Ocean, Director Crew Wedge-Watch)** — catches Mayor / Architect /
  Doctor *process* stalls.
- **Leg 3 (pogod, this doc)** — catches Mayor *behavioral* stalls (process
  healthy, work piling up).

**Why pogod, not an Ocean-side watcher.** If the mayor's own loop is the thing
dropping steps, a watcher that lives in that same loop drifts with it — watcher
and watched skip together. pogod's heartbeat is the only watcher in the system
with a guaranteed-independent cadence (it's a Go ticker, not an LLM cycle), so
it's the only place this check belongs.

## Design

A `stallwatch.Watcher` is built at pogod startup and driven from the heartbeat
`OnTick` callback — the same loop the scheduler and `system_wake` detection ride
(see `docs/sleep-resilience-design.md`). Piggybacking means the check inherits
the heartbeat's clock-jump resilience for free and adds no new goroutine
lifecycle to reason about.

On each tick the watcher runs two independent checks:

### Threshold A — unclaimed items

Scan `~/.macguffin/work/available/` via `workitem.ListFrom`. An item counts as
the mayor's responsibility when its `assignee` is the watched agent **or** empty
(unassigned available work is the mayor's to dispatch).

> **SUPERSEDED (mg-4bd4, 2026-07-17).** The rule above is what shipped, and it
> was wrong: it allowlisted the values a *dispatcher* carries, so it skipped
> every item naming an *owner* — 13 of 14 available items, because PMs file with
> `--assignee=pm-<name>`. An item now counts as the mayor's responsibility unless
> its assignee is an execution gate (`non_dispatchable_assignees`, default
> `["human"]`); ownership no longer affects visibility. See "Ownership vs
> execution" in docs/CONFIGURATION.md.

Age is the work-item
file's mtime — the best available proxy for "time sitting in the available
queue," since mg rewrites/moves the file on status transitions. Any qualifying
item older than `unclaimed_item_age_threshold` triggers a single batched nudge
listing the offending IDs.

### Threshold B — unread mail

Scan the watched agent's `new/` maildir. Fire when either the oldest message is
older than `unread_mail_age_threshold`, **or** the unread count exceeds
`max_unread_mail_count`. A missing maildir (agent never received mail) is benign
and silent.

### Nudge + event

On a cross the watcher calls its injected `Nudger`, which pogod wires to the
same PTY-then-mail fallback the scheduler's `PogodDeliverer` uses: nudge the
mayor's PTY when it's running, fall back to `mg` mail when it's offline so the
signal is durable. It then appends a `stall_watch_fired` event to the event log
with the category, counts, ages, and (on failure) the nudge error — so the log
records a threshold cross even when delivery fails.

### Cooldown

Each category (`unclaimed_items`, `unread_mail`) has its own cooldown keyed in a
mutex-guarded map. A persistent backlog therefore produces one nudge per
`nudge_cooldown` window per category, not one per heartbeat tick. The fire time
is recorded *before* the nudge attempt, so a failed delivery still consumes the
cooldown (retry next window beats hammering a wedged recipient every tick).

The check runs in a goroutine off `OnTick`: a wait-idle nudge can block up to
`DefaultNudgeTimeout` (30s), and the heartbeat goroutine must not stall the
scheduler sweep. The per-category cooldown + mutex make overlapping checks safe.

## Configuration

`[stall_watch]` in `~/.config/pogo/config.toml`. Defaults (in
`internal/config`): enabled, agent `mayor`, both age thresholds 10m, max unread
5, cooldown 5m — matching the gh #12 spec's 600s/5/300s.

```toml
[stall_watch]
enabled = true
agent = "mayor"
unclaimed_item_age_threshold = "10m"
unread_mail_age_threshold = "10m"
max_unread_mail_count = 5
nudge_cooldown = "5m"
```

### Deviation from the gh #12 spec shape

The issue sketched the config as a nested JSON
`stall_watch.agents.mayor.*_seconds` block in `~/.pogo/config.json`. pogo has no
JSON config — it uses a flat, single-line TOML reader (`config.loadConfigFile`),
and the mayor is the only behavioral-stall target today. So this ships as a
single flat `[stall_watch]` section with a configurable `agent` key rather than
a per-agent map, and Go-duration strings (`"10m"`) in place of `*_seconds`
integers. The semantics are identical; the shape matches the rest of pogo's
config. If a second watched agent is ever needed, this is the seam to revisit.
