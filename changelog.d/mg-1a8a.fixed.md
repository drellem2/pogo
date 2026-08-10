- **stall-watch asks who is WORKING an item before calling it neglected
  (mg-1a8a).** A polecat spawn whose claim-at-spawn fails open leaves its work
  item in `available/`, and every stall-watch check inferred ownership from that
  status — so the standard notice reported the item as neglected and
  priority-wake told the coordinator to "claim or dispatch **now**", while a
  polecat was working it. Both checks now consult a live-worker probe first, and
  the items they drop are re-reported by a new **worked-but-unclaimed** notice
  that says the opposite: do not dispatch, here is the worker, its pid and the
  evidence.

  **THE LOG LINE PREDICTED THE HARM AND NOTHING ACTED ON IT.** The spawn point
  said it in its own words, observed verbatim in a refinery gate log on
  2026-08-07: *"dispatching ANYWAY (claim failures fail open) … If wi-1 is a real
  item still in available/, stall-watch will report it as neglected while this
  polecat works it."* The second-order effect is the damage: the item is nagged
  indefinitely, priority-wake names it as urgent and unclaimed, and a coordinator
  acting on that nag spawns a **second** polecat onto work already in progress —
  two branches touching the same files, the concurrent-edit shape that cost
  mg-0155 an attempt in rebase conflicts. It also inverts the predicate a
  coordinator uses all evening to decide what is safe to dispatch: an item being
  worked reads available, and nothing about it looks wrong.

  **THE CLAIM FIELD CANNOT CARRY THE DISTINCTION, SO THE FIX IS A SECOND
  SOURCE.** The claim already means "in progress", "finished, awaiting a human"
  (mg-ed7b) and now "in progress but unclaimable"; a better claim was not
  available. pogod already knows which polecats are alive and which item each was
  dispatched at, independently of whether the claim stuck.
  `agent.Registry.WorkItemsInFlight` unions the in-memory registry with the
  persisted polecat witness — the registry alone is permanently empty after a
  restart (mg-13a3), which is exactly when survivors exist — and `cmd/pogod`
  wires it to the watcher as a `stallwatch.Workers` probe, sampled **once per
  tick** so three checks cannot disagree about who is alive within one sample.

  **THE SUPPRESSION IS PAIRED WITH A RE-REPORT.** Dropping worked items silently
  would fix the double-dispatch and hide the anomaly — and the missing claim is
  what `mg done` needs at the END of the work, so the polecat discovers it after
  doing everything. So the finding survives and only the remedy changes, the same
  move mg-dd77 made for at-cap items: the new notice names each worker, its pid
  and whether the evidence is the live registry or the witness, prints the exact
  `mg claim <id> --pid <worker pid>` that restores the invariant, and says that
  if the worker is GONE the item really is free and dispatching it is correct. It
  has no age threshold — a worked-but-unclaimed item is an anomaly the instant it
  exists — but shares the per-item backoff, and stamps `workers` on
  `stall_watch_fired` so "aging because nobody dispatched it" and "aging because
  its claim failed open" are countable apart.

  **THE FIX IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT, so its own ways of
  repeating it are closed.** (1) *An inference presented as fact* is the defect's
  shape: a probe that cannot answer at all leaves every check exactly as it was —
  reported as neglected — because a false "dispatch this" is self-correcting
  while a false silence looks like a healthy queue, the mistake this component
  made in mg-4bd4 and mg-1693. (2) An *incomplete* answer (unreadable witness,
  live registry) is uncertainty, not ignorance: the registry half is kept, the
  dispatch notices still fire, and the caveat rides along — discarding the live
  half would put the notices back to guessing from item status, this defect one
  layer down. (3) The filter cannot latch: when the worker goes, the item returns
  to the dispatch population, proven by test. (4) The spawn point's log line,
  which predicted the old behaviour in prose, was rewritten rather than left to
  rot into a false claim — it now states the conditional that survives.

  **What this does NOT fix, stated rather than implied.** The item still reads
  `available` to anything consulting mg directly, including a human at the board.
  Nothing at the spawn point refuses a second polecat on an item a live worker
  holds — the claim-at-spawn conflict is that guard, and it is the guard that
  failed open here. This removes the nag that induces a second dispatch, not the
  ability to make one. Documented in
  [docs/design/stall-watch-design.md](../docs/design/stall-watch-design.md).
