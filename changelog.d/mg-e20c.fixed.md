- **An absent witness store was reported as zero orphaned polecats
  (drellem2/pogo#197, mg-e20c).** With no witness file on disk,
  `OrphanedPolecats` returned no survivors and no error, so the heartbeat's
  orphan sweep logged nothing and `/agents/drain` reported "none unreachable" —
  the same answer as a clean fleet. `OrphanedPolecats` now returns
  `ErrWitnessAbsent` when the store does not exist. The sweep returns -2 for
  that state and logs it when it starts and when it ends. It does not log on
  every heartbeat. `/agents/drain` sets `unreachable_err`, which
  `pogo-self-deploy` already prints as "cannot tell whether any polecat
  survived". A present but empty store still reads as zero. One limit remains:
  the store is created by the first agent start, so once it exists, polecats
  left by a pogod that never wrote a witness are still not visible.
