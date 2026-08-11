- **A spawn is no longer a success: pogod alarms when a crew agent it started
  never completes a first turn — and a fire the agent is still holding survives
  the boot-time re-registration that used to retract it (mg-3cbb).** Two changes
  from one outage, plus one correction to the record that matters more than
  either.

  **The correction first, because the ticket's premise is wrong.** mg-3cbb was
  filed as "the 22h outage of 2026-08-10/11, in which every instrument we own
  read green". One instrument was red the entire time. ack-watch's FLEET BLACKOUT
  arm (mg-e2a4) fired **33 consecutive times**, from 00:33Z on 08-11 through
  19:10Z, each firing naming all five crew agents, each escalated to `human`, and
  the out-of-process `com.pogo.deadman` notifier surfaced ~35 of them as system
  notifications. Detection, routing, and out-of-fleet delivery all worked as
  designed. The outage still ran 22 hours and ended when a human restarted pogod
  by hand. Whatever the remaining problem is, "nothing saw it" is not it, and the
  next person to read that ticket should not inherit the wrong premise.

  **What the blackout arm genuinely cannot do is speak about a FRESH agent**, and
  that is arithmetic rather than a bug. It judges an absolute completion *ratio*
  over a trailing 3h window, so an agent is ineligible until it has been up for
  the whole window — a gate that is load-bearing, because "fires delivered,
  nothing completed" is also what an *empty* fleet looks like every night. pogod
  restarted at 02:01:33Z and spawned five agents that completed nothing for 17
  hours; that arm's first post-bounce firing was 05:03:36Z, exactly one window
  later.

  **`internal/firstturn` is the rung on the other side of a spawn.** It asks a
  question with no denominator — *has this agent completed a single fire since it
  was spawned?* — and alarms at 45 minutes. On that outage it is red at 02:46:33Z,
  **2h17m earlier**, from the same event log, with no new instrumentation. The
  two arms partition the failure rather than overlapping: was-alive-and-went-dark
  against was-never-alive.

  **The grace is measured, not chosen.** Every crew `agent_spawned` on this box
  since completion tracking existed (2026-07-23), paired with the first
  `scheduler_fire_completed` addressed to it at or after the spawn — 87 spawns,
  and the distribution is bimodal with **nothing whatever in the middle**:

  | population | n | spawn → first ack |
  |---|---|---|
  | healthy | 67 | max **33.7 min** (p50 12.6 min) |
  | outage | 20 | min **150.8 min** (max 1139 min = 19h) |

  The 20 in the upper mode *are* the three outages — 2026-08-10's spend-limit
  episode at 150–181 min, 2026-08-11's five inert spawns at 1044–1064 min, and
  2026-08-08's hung deploy at 1139 min. 45 minutes sits in the empty band: 1.33×
  the healthy maximum, 3.35× below the smallest real outage, zero false positives
  against all 67 healthy spawns. `TestGrace_SitsInsideTheMeasuredEmptyBand` pins
  both edges so a later edit has to confront the measurement rather than the
  number.

  **Shown to fire, against the recorded condition.** `testdata/outage-2026-08-11.jsonl`
  is a verbatim slice of this box's events log — 61 fires delivered to the five
  crew agents between 02:00 and 04:00Z with zero completed, and the recovery
  window at 19:15–20:00Z with 16 completions. The replay tests drive the real
  reader over it and assert the arm goes RED on the outage window and CALM on the
  recovery window. A detector that has only been shown to compile is what already
  existed: the synthetic-failure-turn detector ran ~204 checks over 17h of total
  silence and emitted nothing, because an agent at zero turns produces zero
  *failing* turns and is arithmetically indistinguishable from a healthy one.

  **The subject line carries a number that grows.** Repeats climb a doubling
  ladder — pages at +1h, +3h, +7h, +13h across a 17h outage, each stating how long
  this has been going on. That is deliberate contrast: the blackout arm's 33
  notices all carried the byte-identical subject "90 fires delivered in the last
  3h0m0s, NONE completed", so the 33rd held no more information than the 1st.
  Paging once (synthwatch's shape) buries the notice; paging identically forever
  is noise; only the duration distinguishes one notice from the next.

  **The fleet-wide case escalates on its first sample, structurally.** Not on an
  age gate. A single dark agent goes to `mayor` — restarting one agent is
  coordination work — but a fleet that has never come up cannot be the thing that
  fixes it, and the mayor is inside every outage in this system's history.

  **Second change: re-registering a schedule no longer retracts a fire the agent
  is still holding.** Registering with an existing `--id` replaces the entry, and
  the replacement arrived with an empty `PendingToken` — so an agent that booted,
  re-registered by procedure, then did the work of a catch-up fire and ran the
  exact `pogo schedule ack` command the fire handed it was refused with `no fire
  outstanding to acknowledge`. Demonstrated live by pm-pogo while closing this
  very outage: **two fires delivered, zero acked, both actually handled** — a
  record byte-identical to an agent that received them and died, which is exactly
  the discrimination completion tracking was added to provide after 2026-07-22.
  It happened in the recovery path, where fires are most likely to be catch-ups
  carrying real missed work and where every crew agent re-registers by procedure.

  `Add` now carries the outstanding token, its issue time, and the single
  delivery it represents (so a redeemed carry reads 1/1, not an incoherent 1/0).
  The **lifetime counters still reset** — `internal/ackwatch` depends on that, and
  a preserved ratio would mix fires from before and after a cadence change and
  describe no single regime. A token already past `AckStaleWindow` is not carried,
  and the next fire still supersedes a carried one, so a late ack for fire N-1
  cannot mask that fire N also went unanswered.

  New config section `[first_turn]` (`enabled`, `interval`, `grace`,
  `notify_to`), documented in `docs/CONFIGURATION.md`. Report-only: it mails and
  has no seam through which it could restart, nudge or respawn anything — no
  member of the synthetic-failure class is fixable by a restart (mg-18d0), and
  pogod may already be suppressing respawns for that reason when this fires.
