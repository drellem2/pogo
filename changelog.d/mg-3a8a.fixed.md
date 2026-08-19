- **A wake suppression can no longer last forever: any run of consecutive
  suppressions is bounded at 15 minutes, after which the next wake is DELIVERED
  over the rule that declined it (mg-3a8a).** pogod's `wake_silence_once` rule
  declines a wake when the target "was already woken N ago and has produced no
  PTY output since that wake settled". As a debounce that is right. It had no
  upper bound on N — and the condition that triggers it is exactly the condition
  a wake exists to break, so the first suppression of a silent agent was
  permanent. Measured on this box over 2026-08-14..19: **143 consecutive wakes to
  `crew-pa` declined across 106 hours**, the age in the reason string climbing
  monotonically, `nudge_sent` for `pa` in the same window **3**, all of them after
  a human ran `pogo agent stop`/`start` out of band. Nothing inside the system
  could have recovered it, because the scheduler is the only thing that wakes
  crew agents.

  **The population contained its own control.** Over the same window
  `crew-mayor`'s suppressions all read `already woken 0s ago` — two scheduler
  fires landing in one wake cycle, the debounce firing and releasing normally.
  Same rule, same daemon, two regimes, and nothing in the rule distinguished 0s
  from 106h. Re-derived independently while fixing it: 90 suppressions to
  `crew-pa` (ages 100h19m → 106h29m, one per 10-minute mail-check) and 38 to
  `crew-mayor` (every one at 0s) in the preceding 24h.

  **Where the bound sits, and why not inside rule 1.** It is applied above the
  rules, to the RUN rather than to any one decline: the rules answer about the
  agent, the bound decides how long any such answer may keep a wake from landing.
  That covers the sibling rule for free, which mattered — the ticket asked
  whether the same shape existed elsewhere and it did. Rule 2's per-agent flag is
  cleared by `internal/claude`'s modal hook only when the agent's event log
  ADVANCES, so an agent that never speaks again holds the flag, and the flag
  suppresses the wakes that would make it speak. A rule added later inherits the
  bound without having to remember to.

  **The bound is on elapsed time, not on a count of suppressions.** A count means
  different things at different fire cadences — ten declines is 100 minutes on a
  `*/10` mail-check and ten seconds for something firing every second — so it
  would be either useless or would defeat the debounce depending on traffic
  nobody controls at that layer. Fifteen minutes costs the debounce nothing real
  (the intended case is seconds apart) and cannot be mistaken for chatter: every
  harness pogo drives animates its PTY while it works, so fifteen minutes without
  one byte is not a working agent under any provider we ship.

  **A remedy is subject to the defect it remedies, so the release was checked for
  the same shape.** The run ends ONLY on a delivered wake — not on the decision to
  release — so a release that failed on a busy PTY is re-offered on the next fire
  rather than spending the period; resetting on the decision would have meant one
  transient write error buying another full bound of silence, which is the defect
  in miniature. The release's own clock is wall-clock elapsed time and depends on
  nothing the suppressed agent does, which is the property the rule it overrides
  lacks. A backwards clock step reads as zero elapsed rather than negative, so it
  cannot extend a run. And after a delivery the debounce restarts from zero, so a
  permanently silent agent gets at most one wake per bound period, not one per
  fire.

  **New event `wake_suppression_released`**, and `nudge_suppressed` now carries
  `consecutive` and `suppressed_for_seconds` as structured fields. The
  load-bearing number in this incident was "143 consecutive over 106h" and
  reading it required regexing an age out of an English sentence — which is how a
  run past 100 hours stayed something you had to already suspect in order to see.
  Both are documented in `docs/event-log.md` with the `jq` that finds a latched
  run.

  Not done here, and deliberately: handing a released-but-still-silent target to
  the stall/restart path. The bound restores reachability; deciding to restart an
  agent is a trigger, and this file's report-only stance says a trigger needs its
  own ticket.
