- **Dispatch and the refinery now measure how much of the host the fleet is
  actually holding, instead of counting agents (mg-1b8c).** New
  `pogo host load` and `GET /hostload`; `pogo agent spawn-polecat` answers
  **503** when the fleet already holds half the host; a running gate samples
  the host and `pogo refinery show` prints a `Host:` line; and a gate timeout
  reached on a saturated host says so, with the numbers, in its error text.

  **Measured, both arms.** A fixed CPU workload, identical every run, on the
  fleet's 10-core host: **11.5s** uncontended, and **20.2s / 47.1s / 56.5s /
  78.5s** against 6 / 14 / 24 / 40 competing processes — a **1.76x to 6.83x**
  inflation of the same work, rising monotonically. That is enough to push a
  gate through a fixed timeout, and a merge failure attributed to a gate reads
  as a defect in the change under test. The branch was fine. Full write-up,
  including what the numbers do *not* establish, in
  `docs/investigations/gate-contention-inflates-wall-clock-2026-07-30.md`.

  **It counts the resource, not the agents.** The rule this replaces is "a
  reasonable limit is 3-5 concurrent workers", and a slot count cannot see what
  is in the slots. The live instance was **ONE** worker that had
  self-parallelised into three compute processes holding ~5.7 of 10 cores —
  which any count of agents reads as an idle box. Attribution is by process
  subtree from pogod, so an agent's compute children count and an idle agent
  costs nothing.

  **It does not gate on the load average, deliberately.** Measured on the same
  host the same night: a load average of **214** against roughly **7.5 of 10
  cores** actually in use, because Darwin counts uninterruptible-sleep tasks in
  that figure. A guard keyed on it refuses to dispatch while cores sit idle.
  The load average is still printed — it is what made a coordinator look in the
  first place — labelled `CONTEXT ONLY` and used by nothing.

  **Nor on total host CPU.** A VPN extension at ~0.9 cores and the system
  indexer at ~0.3 were not the fleet's, and pausing fleet work would not give
  those cores back. Gating on total load hands an unrelated process a veto over
  the fleet's own dispatch, so the guard only ever reads the fleet's share.

  **The 503 is a later, not a no**, and says so: the work item is fine, the
  host is busy, hold it and re-check. It fails **open** on an unreadable or
  unattributable sample — refusing work on missing information stalls the queue
  for a reason nobody downstream can check or clear.

  **No cost prediction, and no scheduler.** Knowing a work item is
  compute-heavy before dispatch is not implementable: nothing on an item
  declares cost, per-repo history is wrong in exactly the expensive cases, and
  a filer-set marker depends on somebody remembering — mg-ddf4 established the
  store cannot even say who filed anything (`creator` is the unix user and
  reads `daniel` for every item). So the gate does not predict; it observes
  what the fleet holds right now, which is knowable exactly.

  **The timeout still kills and the merge still fails.** A bound that could be
  silenced by loading the host would not be a bound — that is mg-2789's defect,
  a control that cannot fail. What changed is the reading: an error that says
  only "exceeded its timeout" implies a verdict on the branch, and on a
  contended host the evidence does not support one. The message now states that
  it establishes neither that the change is broken nor that it is fine, and
  says to re-run on a quieter host.

  **What this does not close.** A load-aware dispatcher does not help a worker
  that was already running when the host filled up. A contention-induced
  timeout is still indistinguishable from a red gate to the actor with the
  least context, and that actor has to decide. The coordinator prompt now says
  a timeout on a saturated host is UNKNOWN and to pass that on; the general
  interpretation problem is tracked separately.

  **Limits, stated rather than left to be discovered.** Saturation is computed
  from CPU consumed, which is bounded by the core count — it detects a full
  host and cannot say how far past full it is. It is blind to a step slowed by
  I/O rather than CPU. And the half-the-host threshold is a judgement about a
  decision made without knowing the new work's cost; the refusal prints the
  measurement it acted on, so a wrong value is diagnosable rather than
  mysterious. It was 60% first, calibrated straight to the measured instance —
  a live check of the guard then read seven compute processes at 58% and did
  not fire, which is how a threshold tuned to sit just under one measurement
  fails. It now follows from an argument: below half the host, holding a
  dispatch would be gating on capacity that pausing our own work cannot
  recover.
