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
> `["human", "parked"]` — `parked` added by mg-a3a2 so a deliberately-parked
> item can go quiet without falsely claiming a human owns it); ownership no
> longer affects visibility. See "Ownership vs execution" in
> docs/CONFIGURATION.md.

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

On a cross the watcher calls its injected `Nudger`, which pogod wires to a
PTY-then-mail fallback: nudge the mayor's PTY in wait-idle mode when it's
running, and fall back to durable `mg` mail whenever the PTY cannot carry the
message — **both** when the mayor is offline and when the PTY nudge *fails*. It
then appends a `stall_watch_fired` event recording the category, counts, ages,
the delivery channel (`nudge_delivery`), and — only if every channel failed —
the nudge error.

#### Why mail backstops a *running* agent (mg-79dc)

The fallback originally covered only an offline mayor, on the reasoning that a
running agent gets the PTY and mailing it too would double-deliver. That
conflates *running* with *reached*. Wait-idle can only deliver to an agent that
goes quiet, and **a working agent never goes quiet** — so the channel failed
exactly when the mayor was busy, which is precisely when a dispatch stall is
most likely and the notice most needed. A watcher whose reporting channel goes
dark under the very condition it watches for is not lossy; it is blind, and the
correlation is the whole problem.

Measured on 2026-07-17: 18 of 47 fires (~38%) died with `still producing output
after 30s ... context deadline exceeded`, including both work-item fires.

**Not a timeout-tuning problem.** Every dropped fire recorded a "last PTY write"
of 2–305ms: the mayor was writing *continuously*, not almost-quiet. No deadline
survives that, so lengthening the timeout would only trade a visible failure for
a slower one. `mg mail` is the right shape because it does not require an idle
recipient at all. The fallback also lands in a channel stall-watch itself
watches (`unread_mail`), so an ignored notice escalates rather than vanishing.

This does not weaken the never-interrupt-a-busy-agent guarantee (gh #61): the
PTY is still never written to while busy. The guarantee was "do not interrupt a
busy agent", not "do not inform it". Nor does it double-deliver — mail is sent
only when the PTY nudge returned an error, i.e. only when nothing was written.

### Cooldown

Each category (`unclaimed_items`, `unread_mail`, `priority_wake`) has its own
cooldown keyed in a mutex-guarded map. A persistent backlog therefore produces
one nudge per `nudge_cooldown` window per category, not one per heartbeat tick.
The fire time is recorded *before* the nudge attempt, so a failed delivery still
consumes the cooldown rather than hammering a wedged recipient every tick.

**The cooldown is a rate limiter, not a retry queue** — the distinction is
load-bearing and easy to get backwards. A failed nudge is never queued or
re-sent. What happens after the cooldown is that the *condition* is sampled
afresh, and only if it still holds is a *new* message composed. So a stall that
resolves inside the cooldown window takes its undelivered notice with it,
silently; a stall that resolves-then-recurs reports the recurrence as if it were
the first. This is why delivery must succeed on the **first** attempt, and why
the mail fallback — not a retry — is the fix.

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
