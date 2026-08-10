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
  standard `nudge_cooldown`) gates repeats — **per item**, and doubling up to
  `repeat_backoff_cap` (mg-1693; see below).
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

### Correction (mg-1693): guarantee 2 was the wrong bound

Guarantee 2 as originally written is true and was not enough, and the gap is
worth keeping visible because the reasoning is the kind that repeats. It bounds
nudges *per tick* and says nothing about the total, so "cannot loop-nudge" was
argued from a rate limit that a **permanently** held item simply outlasts: one
nudge per 3m, forever, is a loop. Worse, the cooldown was keyed on the category
rather than the item, so the bound applied to the *alert kind*, and nothing
recorded that the coordinator had already been told about a specific item.

Measured on the live host on 2026-07-30: `mg-61f4` drew 22 notices in about four
hours, `mg-0e24` 27, `mg-7c95` 22 — every one a correct detection of a genuinely
ready, high-priority, undispatched item the mayor was holding behind its
five-polecat cap. This is the wake traffic that read as a false-positive problem
and is not one.

The repaired bound is per **item** and *escalating*: first notice immediate,
each repeat about that item doubling out to `repeat_backoff_cap`. That bounds
the total, not just the rate, which is what "cannot loop-nudge" needed to mean.
Structural guarantee 1 is unaffected and still holds. See
[stall-watch-design.md](stall-watch-design.md) §Cooldown.

### Correction (mg-dd77): "ready and unclaimed" is not "dispatchable"

The bound above is about how OFTEN the wake fires. This is about what it says
when it does. `priority-wake: N high-priority work item(s) are ready and
unclaimed — claim or dispatch now` named a remedy the daemon refuses whenever
the item's repo is at its per-repo worker cap, and on 2026-08-10 it fired twice
for `mg-aab5` (02:46Z, 02:52Z) while `/Users/daniel/dev/pogo` held 3 polecats
against a cap of 3.

Of the two surfaces with this blind spot, **this is the worse one**: the wording
is the most imperative the component emits, it lands on the items a coordinator
is least willing to conclude "ignore this" about, and its cooldown is the
shortest — so the unactionable alarm on the highest-value work repeated the
fastest. It also pushed toward a genuinely destructive remedy, because the only
two ways to satisfy "dispatch now" at cap are to preempt a working polecat
(stranding its pushed branch — mg-be37) or to snooze the item (hiding ready
high-priority work to silence a detector). An alarm should never make those the
available responses, and the at-cap text now rules both out by name.

The fix is the repo-occupancy check shared with the standard stall notice — see
[stall-watch-design.md](stall-watch-design.md) §"The remedy is checked against
the per-repo cap". The priority information is kept rather than dropped: a
coordinator still wants to know a high-priority item is waiting on a slot, and
naming the occupying workers lets it ask whether one of them is wedged.

## Tests (W-4)

`internal/stallwatch/stallwatch_test.go`:

- `TestPriorityWakeBypassesTenMinuteGate` — high-priority item past the wake
  delay but far under the 10m gate fires a `priority_wake` nudge.
- `TestPriorityWakeRespectsWakeDelay` — younger than the delay does not fire.
- `TestPriorityWakeCooldownPreventsLoopNudge` — a stuck available item nudges
  once per cooldown across many ticks (review point **b**). See
  `internal/stallwatch/repeatbackoff_test.go` for the mg-1693 half this test does
  not cover: `TestHeldItemStopsRenotifyingForever` bounds the TOTAL over a long
  window, which is what this per-tick assertion left open.
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
