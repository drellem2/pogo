# Priority-aware fast wake (coordinator idle-latency)

**Status:** Shipped — gh drellem2/pogo #61, `internal/stallwatch/`.
**Scope of this doc:** the platform half (W-1 config + branch, W-2 knobs, W-4
tests/docs). The coordinator prompt's bounded idle-backoff (W-3, `mayor.md`) is a
separate, prompt-only change tracked independently.

## The problem

The coordinator runs two timers: a fast in-harness self-wake (30–60s while
busy) and a ~30-min pogo-schedule backstop. When the queue is quiet it lengthens
its own self-wake to conserve tokens and coasts near the backstop cadence. That
is reasonable — *except* that a `priority = high` work item which arrives with no
accompanying mail has no fast path: it waits out whichever timer fires next,
up to ~30 minutes, and worst exactly when the system is idle. Observed with a
high-priority voice-pr dispatch.

`pogo agent diagnose` does **not** reveal this: it measures coordinator health
against the ~30-min backstop cron, so an item picked up "within one cron
interval" looks healthy while a human waited half an hour.

## Why not a new IPC channel

pogod cannot cheaply wake an agent parked on its in-harness self-wake without a
PTY write — a file flag or a mail the coordinator only reads on its *next* cycle
cannot reduce idle latency, because the parked coordinator won't look until it
wakes on its own anyway. The one inbound channel that already collapses idle
latency *without* interrupting in-flight work is the existing **wait-idle
nudge**: for an idle agent the PTY is quiescent so it fires promptly; for a busy
agent it blocks until the current turn ends, then injects. So the missing piece
is not a new channel — it is a **priority-aware trigger** on the delivery pogod
already performs.

pogod's stall watcher already lists `~/.macguffin/work/available/` every 30s
heartbeat tick and already has `WorkItem.Priority` in hand; it was simply
priority-blind and 10-min-gated. The fix is a branch, not new plumbing. The wake
policy stays entirely in pogod, keyed off the generic `Priority` field, so `mg`
needs no `--wake` flag and no mg→pogod event — it remains a decoupled work queue.

## The design (Lever A)

In `stallwatch.checkUnclaimedItems`, over the same `available/` listing, add a
priority pass (`checkPriorityWake`):

- An item qualifies when it is **assigned to the watched agent** (or unassigned)
  — **SUPERSEDED by mg-4bd4:** it now qualifies unless its assignee is an
  execution gate (`non_dispatchable_assignees`, default `["human", "parked"]`
  since mg-a3a2), so
  PM-owned items are visible; see "Ownership vs execution" in
  docs/CONFIGURATION.md —
  its priority is in `fast_priorities` (default `["high"]`), and it has aged past
  the short **`high_priority_wake_delay`** (default 30s) rather than the 10-min
  `unclaimed_item_age_threshold`. The small delay lets a burst of enqueues settle
  so a batch is one nudge, not one per item.
- Delivery reuses the **same wait-idle nudger** the standard checks use (see
  `newStallNudger` in `cmd/pogod/main.go`) — so a **busy agent is never
  interrupted** and an idle one is woken at once.
- A dedicated **`high_priority_wake_cooldown`** (default 3m, separate from the
  standard `nudge_cooldown`) gates repeats.
- On a fire, pogod's `heartbeat.Nudge` is invoked (via the optional `FastPoll`
  hook) to collapse the next ~30s poll for a prompt follow-up sweep. It cannot
  storm the loop: `FastPoll` runs only on an actual fire, and the cooldown
  suppresses the immediately-following check, so at most one extra tick follows
  each wake.

The standard 10-min pass skips fast-priority items *only while the wake is
enabled* — they are owned by the fast path, so a stuck high-priority item draws
one fast nudge, not a second slow one. Disable the wake and those items fall
straight back to the 10-min gate; the feature never silences them.

## Why it cannot loop-nudge a stuck item

Two structural guarantees, both asserted by tests:

1. **Only `available/` is scanned.** An item with unmet deps sits in `pending/`
   and an already-claimed item in `claimed/`; `workitem.ListFrom(root,
   "available")` returns neither, so a blocked or claimed high-priority item is
   never even seen — it cannot wake anything.
2. **The dedicated cooldown** caps a ready-but-undispatchable item to one nudge
   per `high_priority_wake_cooldown`, not one per heartbeat tick.

## Tests (W-4)

`internal/stallwatch/stallwatch_test.go`:

- `TestPriorityWakeBypassesTenMinuteGate` — high-priority item past the wake
  delay but far under the 10m gate fires a `priority_wake` nudge.
- `TestPriorityWakeRespectsWakeDelay` — younger than the delay does not fire.
- `TestPriorityWakeCooldownPreventsLoopNudge` — a stuck available item nudges
  once per cooldown across many ticks (review point **b**).
- `TestBlockedOrClaimedHighPriorityDoesNotWake` — `pending/` and `claimed/`
  high-priority items never wake (review point **b**).
- `TestTenMinuteGateStillAppliesToNonHighPriority` — non-high items keep the 10m
  gate (review point **c**).
- `TestPriorityWakeDisabledFallsBackToStandardGate` — disabling never silences a
  high-priority item.
- `TestPriorityWakeAndStandardHaveIndependentCooldowns`,
  `TestPriorityWakeFastPollInvokedOnFire`,
  `TestPriorityWakeIgnoresItemsAssignedElsewhere`,
  `TestPriorityWakeCaseInsensitivePriority`, and the zero-config default check.

`cmd/pogod/main_stallnudger_test.go`:

- `TestStallNudgerNeverInterruptsBusyAgent` — end-to-end proof of review point
  **a**: through the exact nudger the wake uses, a perpetually-busy agent never
  receives the wake in its PTY.
- `TestStallNudgerFallsBackToMailWhenOffline` — durable mail delivery for an
  offline agent.

The wait-idle primitive itself is proven in
`internal/agent/nudge_test.go:TestNudgeWithModeWaitIdleTimeoutOnBusy`.

## Configuration

See [../CONFIGURATION.md](../CONFIGURATION.md) §"Priority wake". All knobs live
under `[stall_watch]`; the wake is default-on for the watched coordinator.
