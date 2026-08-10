# Why scheduler fires "arrive batched" — 2026-08-10 (mg-772f)

**Answer: they don't.** The fires were delivered on time, one per due mark, and accepted by the
receiving harness on time. What was observed as a batch of 27 was 27 individually-delivered fires
whose consuming turns had all died on an API error, seen at once by the first turn that survived.

The phenomenon had already been detected, named and logged — by `internal/synthwatch`, in the same
`events.log` the ticket pointed at — four hours before either observer wrote it up.

---

## The question

mg-772f asked one narrow thing: is the batching on the **delivery** side (pogod holding fires and
flushing them together — timer coalescing, a blocked write, a lock, macOS timer slack) or on the
**consumption** side (fires delivered on time, the agent's turn boundary being where they surface)?

It suggested comparing `fired_at` against `original_due` and noted that whoever took it should first
check whether those timestamps actually discriminate. They do, and they exonerate delivery
completely.

## What was measured

Source: `~/.pogo/events.log` (79 MB), the Claude Code session transcripts under
`~/.claude/projects/`, and `~/.pogo/agents/receipts/`.

### 1. Delivery was punctual — every day, on every arm

`scheduler_fire_delivered` carries `original_due` and `fired_at`. Across the whole log:

| deliveries | with both stamps | p50 lag | p90 lag | within 60s |
|---|---|---|---|---|
| 65,717 | 65,717 (100%) | 14s | 27s | 98.86% |

The 1.14% outside 60s are sleep-replay catch-ups (`missed_fires > 0`), which is the replay policy
working as designed rather than a defect.

Restricting to the fires that actually produced the deficit — the 7,106 whose token was superseded
before anything redeemed it:

| | count | delivered within 60s of due |
|---|---|---|
| all deliveries | 65,717 | 98.86% |
| **superseded ("batched") fires** | **7,106** | **99.78%** |

Per day, over the 16 days with enough traffic to measure, the superseded fires were delivered on
time **100.0%** of the time on 14 days and 97.6% / 99.8% on the other two. There is no day on which
delivery looks even slightly responsible.

> **pogod does not hold fires and flush them together.** Whatever bunches them is downstream of
> delivery, on every day measured.

### 2. The harness accepted them on time too

`nudge_sent (delivery: pty)` for architect on 2026-08-09 fired 27 times between 13:01:09Z and
17:20:29Z — one per 10-minute mark, none missing, none doubled. pm-pogo's 27 are the same 27 marks
to the second. Both agents' `fired` tracked `due` within 30s throughout.

Every one of the 27 emitted `nudge_sent`, not `nudge_unconfirmed`, meaning the harness returned a
submission receipt for each — so the prompts were not sitting unsent in a composer either.

The session transcript settles it directly. Each of the 27 appears as its own `type:"user"` record
with its own timestamp, and those timestamps match pogod's `fired` to within 100ms:

```
transcript ts            due (local)                pogod fired
2026-08-09T13:01:09.289Z 2026-08-09T14:00:00+01:00  2026-08-09T14:00:18+01:00
2026-08-09T13:10:19.029Z 2026-08-09T14:10:00+01:00  2026-08-09T14:10:18+01:00
2026-08-09T13:20:19.715Z 2026-08-09T14:20:00+01:00  2026-08-09T14:20:18+01:00
...  (27 rows, 10 minutes apart, none coalesced)
```

No delivery-side batching, and no turn-boundary queueing either. Both halves of the ticket's
dichotomy are false.

### 3. What actually happened: the turns died

Reading the transcript record *after* each of those user messages:

```
451  13:01:09  user       [scheduler ... due=14:00:00+01:00 ack=293035bc]
457  13:04:08  assistant  API Error: Unable to connect to API (ENOTFOUND)
460  13:10:19  user       [scheduler ... due=14:10:00+01:00 ack=c6edf7de]
461  13:13:19  assistant  API Error: Unable to connect to API (ENOTFOUND)
464  13:20:19  user       [scheduler ... due=14:20:00+01:00 ack=7a9b68db]
466  13:23:20  assistant  API Error: Unable to connect to API (ENOTFOUND)
...
```

Classifying all 27 for each agent, in the window 13:00Z–17:25Z:

| agent | scheduler prompts | turn died on an API error | turn ran |
|---|---|---|---|
| architect | 27 | **26** | 1 |
| pm-pogo | 27 | **26** | 1 |

The single surviving turn is the **last** one in each case — 17:20:29Z, the first fire to reach the
API after the outage. Its context contained all 27 prompts, 26 of them with a dead turn above them.

**That is what "27 fires landed at once" described.** The batching was in the reading.

This also explains the ticket's most puzzling observation — two independent mailboxes showing the
identical window. They were not independent: a shared network failure ended both agents' turns on
the same schedule, so the two "batches" are one event seen twice.

The ticket's other observations all hold and are consistent with this: the agent *was* alive,
responsive, and 7h39m into an unbroken uptime. Nothing crashed. Only the API calls failed.

### 4. It was already detected

`synthwatch` (`internal/synthwatch`) exists for exactly this class — "every member of this class
leaves the agent ALIVE and RESPONSIVE". On 2026-08-09 it fired:

```
2026-08-09T13:04:48Z  synthetic_failure_detected  target=architect   ("server_error")
2026-08-09T13:07:18Z  synthetic_failure_detected  ...
2026-08-09T17:22:29Z  synthetic_failure_cleared   target=architect
```

Detected 3m39s after the first dead fire, cleared 2 minutes after the last one — bracketing the
"batch" window on both sides, in the same file, under an event name that says what it is. Both
observers read `nudge_sent` and `scheduler_fire_completed` and neither read
`synthetic_failure_detected`.

### 5. How much of the fleet's deficit is this?

Joining the two event families across the whole token era (agent name and time only —
`scheduler_fire_delivered.to` and `synthetic_failure_*.target` are both the bare name):

| | count | share of superseded |
|---|---|---|
| superseded ("batched") fires | 7,106 | — |
| ...delivered late by pogod | 18 | **0.25%** |
| ...landed while synthwatch had that agent FAILING | 3,661 | **51.5%** |

Independently reproduced: a Python prototype over the raw log and the Go implementation in
`internal/ackwatch` agree to the unit on all three numbers.

Fleet-wide across all agent transcripts, **18.6%** of the 19,077 scheduler prompts with a
classifiable turn died on an API error. Per day that is bimodal, and the split matters:

| day | % of scheduler prompts whose turn died | % of token-carrying fires superseded |
|---|---|---|
| 2026-07-10 … 07-31 | ~0% (one day at 8%) | 4–45% |
| 2026-08-04 | 97.9% | 91.3% |
| 2026-08-05 | 85.5% | 77.8% |
| 2026-08-08 | 63.5% | 95.1% |
| 2026-08-09 | 66.4% | 71.5% |
| 2026-08-10 | 37.0% | 36.3% |

So there are **two** mechanisms behind population 1, and they dominate in different regimes.
July's supersessions happened with essentially zero dead turns — those are genuine long-turn
batching. August's are mostly dead turns. Neither is a delivery-side defect.

## What this means for the two downstream tickets

### mg-a14c — "only the most recent fire is ackable"

The mechanism is real and `issueFireTokenLocked` does abandon the previous token. But the ticket's
conclusion — that the 18%/22% ack rate is *"largely arithmetic rather than diligence"* — needs a
third category. **Half the supersession is neither arithmetic nor diligence: it is fires landing on
turns that never ran.** No token-lifetime change recovers one of them, because there was no turn to
do the work in.

The ack instrument comes out of this **better** than the ticket assumed. It correctly reported a
4.5-hour dead fleet that `nudge_sent` and `scheduler_fire_delivered` both scored as perfectly
healthy — which is precisely the 2026-07-22 failure mode `internal/scheduler/completion.go` was
written for, recurring. Attributing that reading to "batching" would have explained away a live
outage as a measurement artefact.

### mg-1935 step 2 — the streak-length distribution

The ticket's worry is correct but misidentifies the contaminant. The distribution of terminated
streaks (runs of unacked fires ending in one ack) has a long, non-geometric tail:

```
len 2    659 runs (54.1% cum)      len 43    11 runs
len 3    276 runs (76.8% cum)      len 83     7 runs
len 27     6 runs (96.0% cum)      len 199    4 runs
```

Those tails are not busy agents. They are outage windows. A threshold fitted to this distribution
would alarm on API outages while calling them inattentive agents — a correct alarm with the wrong
name on it, which is the failure mode that makes a metric distrusted.

**Recommendation for mg-1935:** fit the threshold to streaks *outside* synthetic-failure episodes.
`SplitWithEpisodes` now reports `BatchedInFailureEpisode` so that subset can be excluded without
re-deriving any of this.

## What shipped with this investigation

Additive annotations on population 1 in `internal/ackwatch`. **None of them change the deficit
arithmetic** — the ratio, the mean gap, the boundary term and the `Identity()` residual all read
exactly as before, asserted in `TestSplit_EpisodeJoin_ChangesNoDeficitArithmetic`. Re-scoring the
deficit is mg-a14c's question and this ticket deliberately does not answer it.

- `FireEvent.Due` / `.Fired` — the scheduler's `original_due` / `fired_at`, written since the first
  fire and **read by nothing until now**. This was the discriminator the ticket asked for, sitting
  unread in every event.
- `FireEvent.Late()` and `LateDeliveryThreshold` (60s — an order of magnitude above the p90 jitter
  of 27s, an order of magnitude below the 5-minute shortest cadence).
- `SchedulePopulation.LateDelivery` — the delivery-side arm. The only counter in the report that
  can implicate pogod.
- `FailureEpisode`, `ReadFailureEpisodes` and `SplitWithEpisodes` — synthwatch's episodes joined in,
  producing `BatchedInFailureEpisode`.
- `pogo check-acks --populations` reports both, and says "Delivery is EXONERATED for this window"
  when `LateDelivery` is 0.

A zero `BatchedInFailureEpisode` is deliberately **not** rendered, because "no episodes overlapped"
and "the caller supplied none" are indistinguishable from inside the split, and a silent 0 would
read as an acquittal the data cannot support.

## Status of the claims here

*Measured:* delivery punctuality (65,717 deliveries, whole log); the 27-fire windows for architect
and pm-pogo and their 26/27 dead-turn split; the transcript timestamps; synthwatch's bracketing
episode; the 51.5% / 0.25% fleet split, cross-checked by two independent implementations; the
per-day tables; the streak-length distribution.

*Not established:*

- **Why the network failed.** `ENOTFOUND` is a DNS resolution failure and this investigation did not
  chase it past the transcript. The August dead-turn rates (97.9% on 08-04, 37% today) say this is
  ongoing and is a much larger problem than the one this ticket was filed about. **Not in scope
  here, and worth its own ticket.**
- **Whether the pre-token era batched.** Fires before 2026-07-23 carry no token, so supersession is
  undefined for them; the delivery-lag arm still applies and shows the same punctuality, but nothing
  here says how those fires were consumed.
- **July's residual batching.** ~20–45% of July's fires superseded with near-zero dead turns. That
  is presumably genuine long-turn batching, but this investigation measured the *absence* of the two
  named causes rather than the presence of that one.

  One direct observation, taken while writing this: the investigating polecat's own mail-check
  schedule delivered 5 fires (12:00–12:40 local) into a single 531-second `./build.sh` turn. All 5
  were punctual — `fired = due + 4s`, every one — the agent was healthy throughout with no
  synthetic-failure episode, and the ack came back `2/6 fires completed`. That is the long-turn arm
  happening in isolation, with both other causes excluded. It is one instance and not a measurement
  of the population, but it does confirm the mechanism exists independently of the other two.
- **Whether synthwatch's paging reached anyone.** It detected and logged. Whether the episode mail
  was read is a different question, and given two agents wrote up "batching" four hours into an
  episode synthwatch had already named, it is a question worth asking.
