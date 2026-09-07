- **A gh-issue triage polecat is no longer left holding a worker slot for the
  whole human gate (mg-9af1).** pogod's done-item reaper stops a polecat whose
  work item is parked at `stage: gated` once it has been quiet for two minutes,
  the same window it already applies to a polecat whose item reached `done`.

  **Neither half was wrong and nothing joined them.** A triage polecat is
  correctly instructed *not* to call `mg done` — no successor exists until the
  coordinator files the build ticket after the gate, and `mg done` refuses a
  `declares-remainder` item that names none. That instruction stays. Its
  consequence was that the item never reached a terminal state, so the reaper's
  condition could never be satisfied, and a polecat whose work was finished and
  whose packet was already durable on the ticket body sat `running` and
  `claimed` until a person answered. On a gated ticket that wait is unbounded by
  design: the gate semantics are "silence = HOLD, however long that takes".

  **Measured four times, each stopped by hand.** `p634e` delivered its gh#135
  packet at 14:28Z on 2026-08-12 and was still running at 14:38Z with `mg-0a43`
  (high) and five other pogo items undispatchable behind that repo's cap for the
  whole window; `p1539` and `pc4a9` the same day; `t7476` delivered its gh#161
  packet on 2026-09-07 with ten issues queued behind a three-slot cap. Every one
  of them read `healthy`, `last-activity: just now` while it sat there, which is
  why no health check would ever have reported it.

  **Why releasing the claim is safe, and why the fix keys on the gate's own
  predicate.** The stop releases the claim, so the ticket returns to
  `available/` — and a second triage polecat's first instructed act is an
  acknowledgement comment on a stranger's open GitHub issue. `config.IsStageGated`
  is what prevents that, at the spawn point (mg-69b1), and it was confirmed both
  merged and live in the running daemon before this was built. The reap calls
  that same predicate rather than re-implementing "does this stage gate", so what
  it reaps and what the dispatcher refuses are the same set by construction. If
  that gate is ever narrowed, this reap has to be re-argued with it.

  **It cannot reach a builder mid-review.** The ticket also reports a build
  polecat idling after PR-open and warns that stopping *that* one is unsafe — it
  owns an open PR, an unmerged branch and the modify side of a live review loop,
  with no equivalent of "the packet is on the ticket". `stage: gated` is carried
  only by the triage ticket (the build ticket carries `build → review → merge`),
  so a builder's item never reads `gated`. The narrowing is the vocabulary's, not
  a remembered exclusion.

  **It closes nothing.** The item stays where the coordinator parked it, claimed
  by nobody, awaiting the decision: no `mg done`, no stage change, and no
  completion notice to the agent that filed it — telling a commissioner the work
  finished would be a false statement about the one thing they are waiting for.
  The stop carries its own `stop_cause`, `gate_reap`, so an operator holding an
  `agent_stopped` record can tell "the worker was released" from "the ticket is
  closed". A stage the probe cannot read leaves the polecat running: that is the
  same mg-27d4 error the dispatch gate reads the opposite way, and both fail
  toward the recoverable outcome.

  **The coordinator-side half landed too, and the ordering rule survives it.**
  `mayor.md` says the reaper now frees the worker so the stop need not be done by
  hand — but `stage: gated` must still be set *before* the polecat ends by any
  route, because while the polecat lives its claim is what holds the ticket, and
  stopping first opens exactly the re-dispatch window the gate exists to close.
