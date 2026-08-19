- **first-turn measured each agent's evidence from the OLDEST spawn in the
  crew, so a coordinator respawned alone was reported as having completed
  nothing while it was completing a turn every few minutes (mg-21ad).**
  `firstturn.ReadEvidence` took one `since` for the whole population and pogod
  passed it `EarliestStart` — the oldest running crew agent's spawn. That is the
  right lower bound for the READ and the wrong one for the JOIN: the evidence it
  returns is folded straight onto each agent and compared against **that
  agent's** `StartedAt`.

  **The bill, measured on this box.** `pa` had been up since
  2026-08-19T06:54:22Z when `mayor` was respawned alone at 15:20:07Z. mayor
  completed a fire at 15:32:21Z and three more after it; at 16:11:16Z first-turn
  mailed *"mayor has completed nothing since it spawned 51m0s ago"*, and again
  at 17:16:08Z, by which time the turnlog held 31 completed turns. Replaying the
  live `~/.pogo/events.log` through the old call shape reproduces the notice
  exactly: `FirstCompletion` comes back as **07:02:48Z — 8h17m before the spawn
  it is compared against** — and `Delivered` as **56**, which is the notice's own
  "delivered 56 fires since" and the count from *pa's* spawn, not mayor's. The
  same replay through the fixed reader returns 15:32:21Z and 5, and the sample
  is calm.

  **Why it mattered more than an ordinary false positive.** This arm is the only
  instrument that can report a coordinator which never came up, because every
  other fleet-wide check on this box routes through that coordinator. Crying
  wolf here is not noise in a redundant channel; it is noise in the sole
  channel, and its cost is that the next reader discounts the one notice nobody
  else can send.

  **What changed.** `ReadEvidence(logPath, agents, now, lookback)` now anchors
  every agent at its own `StartedAt` and computes the window itself, so the read
  bound and the join anchor are decided in one function and cannot drift apart
  again. `EarliestStart` is deleted rather than left exported — a helper whose
  doc comment describes the wrong anchor is an invitation to rewire it. Agents
  with no known spawn, or one older than the lookback, get no anchor and
  accumulate no counts: they are unjudgeable either way, and a number measured
  from an arbitrary floor is worse than no number.

  **The remedy is the same kind of artifact as the defect, so it does not assume
  the caller gets it right.** `Detect` no longer turns "completed only before it
  spawned" into a finding. Under the fixed reader that reading is
  unconstructible — an agent that has completed nothing since spawn arrives with
  a ZERO `FirstCompletion`, which still fires, and still fires on the replayed
  2026-08-11 outage. A non-zero completion *before* the spawn can now only mean
  the window was measured from somewhere else, so it is named in the new
  `Report.Misanchored` population, printed in the notice and carried on the
  `first_turn_watch_*` events, and judged neither dark nor clear. An impossible
  reading degrades to "cannot judge", never to an alarm.

  **New fixture** `internal/firstturn/testdata/staggered-respawn-2026-08-19.jsonl`
  — a verbatim slice of the real traffic, in two windows so it carries the trap:
  the completions from the incarnation mayor was about to replace. The suite had
  no staggered population at all before this; every test spawned the whole crew
  at one instant, which is the one shape in which the population-wide anchor and
  the per-agent anchor agree.
