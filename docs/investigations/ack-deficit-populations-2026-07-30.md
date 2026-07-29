# What produces an ack deficit — three populations, measured (mg-ddf7)

**Date:** 2026-07-30 · **Work item:** mg-ddf7 · **Subject:** `internal/ackwatch`,
`pogo check-acks`

mg-ddf7 required, in this order: (a) fix the denominator to count fires
**delivered** rather than **due**, (b) re-measure the three candidate deficit
populations against the corrected count, (c) only then design a fix, stating
which population it addresses. And: **validate against storm data, not calm
data** — a fix verified on a quiet fleet is verified in the conditions where the
metric was never wrong.

This is the record of (a) and (b), and the design from (c). Everything below is
measured from `~/.pogo/events.log` (60 MB, 2026-04-25 → 2026-07-29, 54 021
`scheduler_fire_delivered` and 3 475 `scheduler_fire_completed` records) using
the instrument that shipped with this ticket:

```
pogo check-acks --populations [--since RFC3339] [--until RFC3339]
```

## Summary of findings

1. **(a) is already true, and landing it would change nothing.** The denominator
   already counts deliveries. Proven, not inspected: 60 830 due periods produced
   54 021 deliveries.
2. **The repair implied by (a) — excluding batched fires — is circular, and would
   pin the signal HEALTHY.** It converts this ticket's failure into mg-7254's.
   Rejected, with the algebra pinned by a test.
3. **Population 2 (token-less fires) does not exist as a live mechanism.** Its
   entire measured mass — 15 741 fires — predates completion tokens. Post-token:
   **zero**.
4. **Populations 1 and 3 are the same quantity seen from two ends, and together
   they explain the metric EXHAUSTIVELY.** `completed/delivered ==
   1/mean_attention_gap − outstanding/delivered`, to zero error, across all 114
   schedules. There is no residual term for diligence to live in.
5. **Validated against storm data, and the storm inverts the metric.** In calm
   the ratio separates a broken agent from a healthy one by 50 points. In storm
   the whole crew compresses into an 11-point band, healthy agents included, and
   the separation is gone.
6. **The control's correct silences were unrecorded.** Fixed (`ack_watch_clear`).

---

## 1. The denominator already counts deliveries (finding, not a fix)

The SME requirement read: *"Count fires DELIVERED, not fires DUE. A batch of
eight is ONE delivery."* `internal/ackwatch` compares acks against
`Entry.FiresDelivered`, and `scheduler.Tick` increments that **once per fire that
actually goes out**: missed cron periods are counted separately into
`missed_fires` and collapsed into a single delivery, and `recordDeliveryLocked`
runs once.

Measured over the whole log:

| | count |
|---|---|
| deliveries (`scheduler_fire_delivered`) | 54 021 |
| due periods (deliveries + `missed_fires`) | 60 830 |
| dues already collapsed into an existing delivery | 6 809 (11.2%) |

So the requirement is satisfied in the shipped code. **The risk this creates is
specific: a future editor "fixes the denominator", observes no change in the
number, and concludes the mechanism is not understood.** The proof is recorded in
`internal/ackwatch/ackwatch.go` for that reason.

The batching that actually hurts is not several dues collapsing into one fire. It
is several *genuinely separate deliveries* landing inside one agent turn, where
`issueFireTokenLocked` supersedes the previous token as each new fire goes out.

## 2. Method

For each schedule, the delivery/completion timeline is reconstructed from the
event log — both events carry `fire_token`, which is exactly enough to recover the
interleaving — and every delivered fire is classified:

| population | definition | remedy it would need |
|---|---|---|
| 1. **batched** | its token was replaced by a later fire's before anything redeemed it | token lifetime, or nothing |
| 2. **token-less** | delivered carrying no token at all | unclosable *by the agent* |
| 3. **boundary** | still outstanding when the window ended | not an agent property at all |

**Why events and not the counters.** A re-registration zeroes a schedule's
counters, and the nightly redeploy (mg-42ac) guarantees one. So a deficit
accumulated during a storm is erased by the restart that follows it, and every
reading taken off the live table is a quiet-afternoon reading. Confirmed while
writing this: `pogo schedule completion` read **96.6%** (1341/1295) on
2026-07-29T23:44Z, hours after the storm below, and pm-pogo's 39% lifetime deficit
had already been zeroed out of existence. **The events log is the only place a
storm survives** — which is also why "the aggregate is at 95%, so this is not
distorting anything" is not a reason to defer.

The event token is a faithful proxy for what the agent saw: `ackField` /
`ackInstruction` and the delivery event all read the same `entry.PendingToken` off
the same clone, so a token present in the event is a token present in the body.

## 3. The population split

**Whole token era** (2026-07-23T18:50Z → 2026-07-29T22:52Z): 4 378 delivered,
3 475 completed (79%), deficit **903**.

| population | count | share of deficit |
|---|---|---|
| 1. batched | 815 | **90.3%** |
| 2. token-less | 0 | **0.0%** |
| 3. boundary | 88 | 9.7% |
| unattributed | 0 | — |

The split is **exhaustive** — no unattributed fires, over any window tried,
including the whole log back to 2026-05-01.

### Population 2 is history, not a mechanism

Every token-less fire in the log predates completion tokens:

- last token-less delivery: **2026-07-23T18:40:34Z**
- first token-carrying delivery: **2026-07-23T18:50:23Z**

Ten minutes — one cadence period — apart. Before that boundary: 15 741 token-less
fires (100% of a 15 741 deficit). After it: **zero**.

Architect's second mechanism was inferred from the mayor's corroboration and is
**refuted as a live mechanism**: mg-a754 shipping the token closed it. The
concern was that it would "persist and be read as ongoing negligence", and it
will not. The instrument nevertheless distinguishes the two cases at runtime
(`PopulationReport.TokenLessIsHistorical`), because a token-less fire appearing
*after* the boundary would be a scheduler defect that no token-lifetime change
would touch — worth catching, just not currently happening.

### Populations 1 and 3 are one quantity, and they are the whole metric

For every schedule, exactly:

```
completed/delivered  ==  1/mean_attention_gap  −  outstanding/delivered
```

where the attention gap is deliveries per ack cycle. Residual measured at
**0.000000000000 across all 114 schedules**, because it is algebra rather than a
fit: the sum of the unacked runs is the count of ackable deliveries and the number
of runs is the count of acks plus the boundary term.

Population 1 *is* the gap term; population 3 *is* the boundary term. 2fcc's
observation and architect's are the same mechanism from opposite ends. Asserted in
`TestSplit_RatioIsTheReciprocalAttentionGap_Exactly`, so a future edit that makes
the ratio mean something else fails a test rather than a mail.

**Consequence.** The completion ratio is the reciprocal of the agent's turn
length in cadence periods. An agent whose turns run longer than its cadence
**cannot** score 100%, however diligent. This is what "the metric measures
DELIVERY, not diligence" means once measured rather than argued — and the
diligence reading is not merely unsafe, it is *arithmetically empty*: there is no
residual left over for it.

### 2fcc's correction stands; the paraphrase it corrected was wrong

The ticket carried a struck claim ("scales inversely with how much an agent is
doing — the quieter the agent, the worse it looks") and 2fcc's correction ("it
scales with the ATTENTION GAP, so it maligns whoever has the LONGEST TURNS").
Over 27 schedules with ≥5 token-carrying fires:

| correlation with completion rate | r |
|---|---|
| mean attention gap | **−0.818** |
| traffic (fire count) | +0.242 |

The correction is right and the paraphrase was wrong. Traffic's weak positive is
a confound — on this fleet the high-traffic schedules are the long-lived crew,
so fire count is a proxy for "lived long enough to look healthy", not a
mechanism.

Corroborated by the live table: the **mayor**, the fleet's busiest agent, read
`mail-check-mayor 32/41` (78%) and `mg-schedule-sweep 23/30` (77%) — the two
worst rows on the box — while acking every token it was handed. And because
cohorts are keyed on cadence and the mayor is alone on 30m and 15m, **it is
permanently `skipped_no_peers`: the agent the metric maligns worst is the one it
can never judge.** Same shape as mg-79dc — the channel fails precisely when the
agent is busy.

## 4. Storm versus calm — the validation the ticket demanded

Two windows, same fleet, same code.

**CALM** — 2026-07-24T17:00Z → 2026-07-25T02:50Z, crew only, 5 agents, 26
fires/hr. 266 delivered, 222 completed (83%).

| schedule | deliv | ack | rate | meanGap | maxGap | batched |
|---|---|---|---|---|---|---|
| pa | 60 | 60 | 100% | 1.00 | 1 | 0 |
| pm-onethird | 60 | 60 | 100% | 1.00 | 1 | 0 |
| architect | 60 | 55 | 92% | 1.09 | 4 | 5 |
| mayor | 20 | 17 | 85% | 1.11 | 2 | 2 |
| **pm-pogo** | 60 | 30 | **50%** | 1.94 | 4 | 29 |

Peer median 100%, pm-pogo 50 points below it and below the 0.75 floor →
**the detector fires, correctly.** This is the regime everyone has in mind when
they say the metric works, and it is the only regime in which it does.

**STORM** — 2026-07-29T15:00Z → 2026-07-29T22:52Z, 7 polecats against a guideline
of 3–5, 15–16 agents/hr, 51–70 fires/hr. 482 delivered, 296 completed (61%).

| schedule | deliv | ack | rate | meanGap | maxGap | batched |
|---|---|---|---|---|---|---|
| pm-onethird | 48 | 45 | 94% | 1.07 | 3 | 3 |
| architect | 48 | 44 | 92% | 1.09 | 5 | 4 |
| f00a | 29 | 25 | 86% | 1.16 | 4 | 4 |
| **pa** | 48 | 40 | **83%** | 1.20 | **9** | 8 |
| **pm-pogo** | 48 | 40 | **83%** | 1.20 | 8 | 8 |
| mayor/sweep | 30 | 23 | 77% | 1.30 | 2 | 7 |
| c76a | 27 | 18 | 67% | 1.50 | 9 | 9 |
| mayor/mail-check | 16 | 8 | 50% | 2.00 | 6 | 8 |
| 86e7 | 16 | 2 | 12% | 5.33 | 13 | 13 |
| d631 | 9 | 1 | 11% | 9.00 | 9 | 8 |
| 8595 | 15 | 0 | **0%** | 15.00 | 15 | 14 |

Read `pa` and `pm-pogo`: **83% each, indistinguishable.** `pa` acked every token
it was handed; nine fires landed in one of its turns and the scheduler superseded
eight of them before it could look. The fleet-wide rate falls from 83% to 61% for
a fleet doing nothing wrong, and the boundary artefact's share of the deficit
rises from 13.6% to 20.4%.

Two things follow, and they are the point of the ticket:

- **The detection margin narrows exactly as the danger grows.** In calm a finding
  needs `rate ≤ median−0.20` and `rate < 0.75`; with a 100% median that is a
  25-point cushion for an innocent agent. In storm the median falls to ~92%, so
  the effective threshold falls to 72% — while innocent agents fall to 83%. The
  cushion shrinks from 25 points to 11, and the population inside it grows from
  5 schedules to ~40.
- **The only thing preventing a storm-night false-positive storm is a fire-count
  gate that knows nothing about the mechanism.** The polecats at 0–12% are
  innocent (short-lived agents with long turns) and are silenced solely by
  `MinFires = 20`. `c76a` at 67% is *below the floor* and cleared 20 fires; it
  escaped being reported only because the `ScaleBand` left it without two
  comparable peers.

Zero alerts on storm night was the **correct** outcome, and now there is a
mechanical account of why. Note the two readings differ in what they measure:
these windows measure the *mechanism* over a fixed interval; the detector reads
*lifetime* counters, which the 03:01 re-registration had reset. Both were
computed; neither produces a finding for that night.

## 5. The design (c) — and what it addresses

**Not** a threshold change. Raising `Floor` or `MinGap` to quiet a storm converts
noise into silence without restoring information, which is the move that makes a
detector useless later. The repair must restore the signal's ability to **vary**.

**Rejected: exclude superseded fires from the denominator** (the repair implied by
the SME requirement). It is circular — a fire is superseded precisely when nothing
acked it in time — so it subtracts the deficit from its own denominator, leaving
`completed/(completed+outstanding)`: a function of the **ack count alone**. Any
schedule with 20 acks reads ≥95% whether the agent is perfect or wedged. Applied
to the one true positive in this fleet's history it lifts pm-pogo's calm reading
from 50% to 97% and its peer gap from 50 points to 1 — the finding vanishes. That
is mg-7254's failure (pinned HEALTHY → false calm) substituted for this one
(pinned UNHEALTHY → false noise); both end with the detector ignored. Pinned by
`TestSplit_ExcludingBatchedFires_IsCircular_AndPinsTheSignalHealthy`.

**Recommended: divide the cadence out — judge the ack INTERVAL in time, not the
ack ratio in fires.** Since `rate == cadence / mean-ack-interval`, the ratio is
cadence-normalised by construction and therefore saturates; the interval itself
does not. `pa` becomes "acks every 12 minutes", a number free to vary and
comparable across cadences, and a wedged agent becomes "no ack in 6 hours", which
grows without bound. Concretely this promotes `UnackedStreak` / time-since-last-
completion from a rendered footnote to the judged statistic. In the storm table
above, `maxGap` is the only column that still separates `pa` (9) from `8595` (15)
from a genuinely dead agent (unbounded) — it is the column that does not
saturate.

**Which populations that addresses, and which it does not:**

| population | addressed? |
|---|---|
| 1. batched | **Yes** — batching changes fires-per-ack, not time-per-ack. A batch of 8 in 80 minutes is an 80-minute interval either way. |
| 2. token-less | **N/A** — measured at zero post-token. Nothing to address. |
| 3. boundary | **Partly** — a time-based statistic measures *between* completions, so the tail becomes "time since last completion", which is the right thing to report rather than an artefact. |
| cadence-cohort exclusion (the mayor) | **Yes, incidentally** — a cadence-independent statistic does not need cadence in the cohort key, so an agent alone on its cadence stops being permanently unjudgeable. |
| short-lived schedules | **No.** A polecat that lives 40 minutes still has too few completions for a stable interval. That stays a coverage problem, honestly reported rather than fixed. |

**Not landed here.** This ticket's mandate was to fix the count, re-measure, and
*then* design; swapping the judged statistic is a change to what the detector
alerts on and deserves its own ticket, its own storm validation, and its own
false-positive budget. What landed is the measurement, the two rejected
alternatives recorded with their reasons, and `ack_watch_clear`.

## 6. `ack_watch_clear` — the control's correct silences are now recorded

> A silent correct outcome and a control that is not running are the same
> observation.

`Watcher.sample` emitted `ack_watch_error`, `ack_watch_suppressed` and
`ack_watch_fired` — and **nothing at all** on the found-nothing path. So the storm
night's correct silence was indistinguishable from a detector that had never been
wired up. That was not hypothetical: this fleet's event log contained **zero**
`ack_watch_*` events of any kind (the installed `pogo 0.5.0` predates
`check-acks`), and no reader could have told which of the two they were looking
at.

It now emits `ack_watch_clear` on every clear sample, carrying `scanned`,
`eligible`, and the four skip reasons — because `eligible 3 of 41` and
`eligible 41 of 41` are both no-findings and only one is a clean bill of health.
Every sample rather than every transition: a transition-only emit goes quiet
during exactly the long calm in which a reader most needs to know the control is
alive.

## 7. What shipped

- `internal/ackwatch/populations.go` — the instrument. Pure classification over a
  fire timeline; `ReadFireTimeline` is the only filesystem touch and takes an
  explicit path.
- `pogo check-acks --populations [--since] [--until]` — re-runnable, so the split
  is a measurement rather than a claim that rots. Never exits non-zero.
- `ack_watch_clear`.
- The two rejected alternatives, with their reasons, in `ackwatch`'s own package
  notes — where a later editor will meet them.

## Numbers, for anyone re-running this

```
pogo check-acks --populations --since 2026-07-23T18:50:24Z                            # token era
pogo check-acks --populations --since 2026-07-29T15:00:00Z --until 2026-07-29T23:00:00Z  # storm
pogo check-acks --populations --since 2026-07-24T17:00:00Z --until 2026-07-25T03:00:00Z  # calm
pogo check-acks --populations --since 2026-07-01T00:00:00Z --until 2026-07-23T19:00:00Z  # pre-token
```

The event log is append-only, so these windows are reproducible as long as it is
not rotated. The pre-token window is the control: it should read 100%
token-less and label itself HISTORICAL.
