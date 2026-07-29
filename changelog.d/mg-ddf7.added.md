- **`pogo check-acks --populations` splits an ack deficit by the MECHANISM that
  produced it — and the measurement rejects both repairs the ticket arrived with
  (mg-ddf7).**
  mg-ddf7 required, in order: fix the denominator to count fires **delivered**
  rather than **due**, re-measure the three candidate deficit populations against
  the corrected count, and only then design a fix — validated against **storm**
  data, not calm. Full record in
  [`docs/investigations/ack-deficit-populations-2026-07-30.md`](docs/investigations/ack-deficit-populations-2026-07-30.md).

  **The denominator already counted deliveries.** `scheduler.Tick` collapses every
  missed cron period into a single fire and calls `recordDeliveryLocked` once, so
  across this fleet's log 60 830 due periods produced 54 021 deliveries — 6 809
  dues, 11%, already collapsed. Landing "count deliveries" is a no-op, and the
  risk is specific: an editor lands it, sees the number not move, and concludes
  the mechanism is not understood. The proof is now in `internal/ackwatch`.

  **The three populations, measured over the token era** (4 378 delivered, 3 475
  completed, deficit 903): **batched 815 (90.3%), token-less 0 (0.0%), boundary 88
  (9.7%), unattributed 0.** The split is exhaustive over every window tried.
  Architect's token-less population is **refuted as a live mechanism** — all
  15 741 of them predate completion tokens, the last one ten minutes (one cadence
  period) before the first token-carrying fire. mg-a754 shipping the token closed
  it; the instrument still distinguishes the two cases at runtime, because a
  token-less fire *after* that boundary would be a scheduler defect no
  token-lifetime change would touch.

  **Populations 1 and 3 turned out to be one quantity, and it is the whole
  metric.** For every schedule, exactly:

      completed/delivered  ==  1/mean_attention_gap  -  outstanding/delivered

  Residual **0.000000000000 across all 114 schedules** — algebra, not a fit. So
  the completion ratio *is* the reciprocal of the agent's turn length in cadence
  periods, and there is **no residual term for diligence to live in**. An agent
  whose turns run longer than its cadence cannot score 100% however diligent: on
  the 2026-07-29 storm night `pa` acked every token it was handed and read 83%,
  nine fires having landed in one turn with eight tokens superseded before it
  could look. 2fcc's correction is confirmed (r = **−0.818** against the attention
  gap, +0.242 against traffic) and the paraphrase it corrected is refuted — and
  the **mayor**, the busiest agent on the box, holds the two worst rows while
  being permanently `skipped_no_peers` because it is alone on its cadence.

  **Storm validation, which is what the ticket insisted on.** In **calm** the
  metric works: pm-pogo at 50% against a 100% peer median, 50 points clear, and
  the detector fires correctly. In **storm** it inverts: the crew compresses into
  an 11-point band and `pa` (healthy) and `pm-pogo` are indistinguishable at
  83%/83%, while innocent short-lived polecats pin at 0–12% and are silenced only
  by `MinFires` — a gate that knows nothing about the mechanism. `c76a` (67%,
  below the floor) escaped a false positive solely because `ScaleBand` left it a
  peer short. The detection cushion for an innocent agent shrinks from 25 points
  to 11 exactly as the population inside it grows from 5 schedules to ~40.

  **Both candidate repairs rejected, with reasons recorded in ackwatch's own
  notes** rather than only in the ticket. Excluding superseded fires from the
  denominator is **circular** — a fire is superseded precisely when nothing acked
  it in time — leaving `completed/(completed+outstanding)`, a function of the ack
  count alone: any schedule with 20 acks reads ≥95% whether the agent is perfect
  or wedged, and pm-pogo's calm finding goes from a 50-point gap to 1. That
  substitutes mg-7254's failure (pinned HEALTHY, false calm) for this one (pinned
  UNHEALTHY, false noise); both end with the detector ignored. Raising `Floor` or
  `MinGap` is rejected for the same reason: it converts noise into silence without
  restoring information. Pinned by
  `TestSplit_ExcludingBatchedFires_IsCircular_AndPinsTheSignalHealthy`.

  **Designed and deliberately not landed:** judge the ack **interval in time**
  rather than the ratio in fires. Since `rate == cadence / mean-ack-interval`, the
  ratio is cadence-normalised by construction and saturates; the interval does
  not. That un-pins the signal instead of moving the threshold it is read at, and
  it incidentally frees an agent alone on its cadence from being unjudgeable.
  Swapping the judged statistic changes what the detector alerts on, so it gets
  its own ticket and its own storm validation.

  **Reads events.log, not the counters, and that is load-bearing.** A
  re-registration zeroes a schedule's counters and the nightly redeploy guarantees
  one, so a deficit accumulated during a storm is erased by the restart that
  follows it — `pogo schedule completion` read 96.6% hours after the storm above,
  with pm-pogo's deficit zeroed out of existence. Every reading off the live table
  is a quiet-afternoon reading, which is the one regime in which this metric was
  never wrong.

- **The ack-watch detector now records the samples where it correctly found
  nothing (mg-ddf7).**
  `Watcher.sample` emitted `ack_watch_error`, `ack_watch_suppressed` and
  `ack_watch_fired` — and nothing at all on the found-nothing path, so a silent
  correct outcome and a control that is not running were the same observation. Not
  hypothetical: on the 2026-07-29 storm night silence was the right answer and
  this detector would have produced it, with nothing anywhere recording that it
  considered the burst and declined; the only evidence the design worked was that
  two agents happened to notice, which is not a property of the system. The live
  60 MB event log contained **zero** `ack_watch_*` events of any kind.

  It now emits **`ack_watch_clear`** carrying `scanned`, `eligible` and the four
  skip reasons, because `eligible 3 of 41` and `eligible 41 of 41` are both
  no-findings and only the second is a clean bill of health. On every clear sample
  rather than every transition: the interval is coarse (30m), the event is one
  line, and a transition-only emit would go quiet during exactly the long calm in
  which a reader most needs to know the control is still alive. Kept distinct from
  `ack_watch_suppressed` — "we declined to look" and "we looked and found nothing"
  are the two observations this package exists to keep apart.
