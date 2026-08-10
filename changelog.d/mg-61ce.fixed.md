- **the stall-watch mail fallback gets a damping term, because its load ROSE
  with the load it was responding to (mg-61ce).** The fallback (mg-79dc) fires
  when the coordinator is too busy to go idle, and answers by adding work to
  that coordinator's inbox. The busier it is, the more often the PTY refuses;
  the more it refuses, the more stall-watch mails the agent it has just observed
  to be overloaded. A new `mail_fallback_backlog_cap` (default 3) bounds
  consecutive fallbacks per recipient, and a successful PTY delivery — direct
  evidence the agent went idle and can drain — clears the run.

  **THE `unread_mail` CATEGORY WAS A CLOSED LOOP, NOT MERELY A PERVERSE ONE.**
  Its notice reads *"your inbox is too full"* and is delivered **as one more
  message in that inbox**, so the remedy re-arms its own trigger: gain ≥ 1 with
  no damping anywhere in the cycle. Measured over the last 20 000 events, 1814
  stall fires took a mail road (720/559/530 fallbacks across `priority_wake`,
  `unclaimed_items`, `unread_mail`), and the coordinator's maildir holds 766
  stall-watch messages — 742 of them the "(undelivered to terminal)" fallback,
  the largest subject line in a 5978-message mailbox by nine times — **of which
  179 are the self-referential unread-mail notice**. The design doc offered this
  loop as the reason mail was safe ("an ignored notice escalates rather than
  vanishing"); that sentence was written from inside the loop and is now
  corrected in place rather than left to rot.

  **NOT A WEDGE REPORT, AND IT ARGUES AGAINST A RESTART POLICY.** Load average
  4.14 on a box that normally idles, coordinator at 16.8% CPU actively
  computing, a substantive mail reply sent fifteen minutes earlier. *"Still
  producing output after 30s" is the instrument seeing a BUSY agent, not a stuck
  one* — the same property recorded in
  `a-spinner-defeats-both-liveness-instruments`. The gate was reporting
  correctly; what was missing was a term that pushes back.

  **THE FIX IS THE DAMPING TERM, NOT A BETTER GATE.** Stripping spinner glyphs
  was rejected because the evidence is a coordinator genuinely computing, so it
  would not have prevented one of these fires. Dropping the gate for
  high-priority fires was rejected because it breaks the gh #61 never-interrupt
  guarantee and converts a mail flood into a PTY flood at the moment the
  coordinator is most loaded — the same feedback direction in a louder channel.
  Per-recipient rate limiting is the only candidate that acts on what was
  actually measured. The counter is deliberately **not** keyed on inbox depth:
  the coordinator's is the one mailbox here where real traffic outweighs noise,
  and damping on total unread would let other agents' legitimate mail silence
  the watcher.

  **WITHHOLDING IS NOT SILENCE, AND ITS SIGNALS ARE OUTSIDE THE LOOP BY
  CONSTRUCTION** — a suppression notice sent by mail would be the same defect
  wearing a disguise. The transition is logged loudly **once per run**, and every
  suppressed fire stamps `nudge_suppressed_consecutive` on `stall_watch_fired`:
  a counter climbing across fires means the coordinator has not gone idle once
  in that whole span, which is sharper than the flood it replaces. A suppressed
  fire carries `nudge_delivery = "suppressed"` and **no** `nudge_error` —
  nothing was delivered, but that was a decision, and only a fault should read
  as an outage.

  **NOTHING IS DISCARDED.** mg-79dc's first-attempt doctrine ("the cooldown is a
  rate limiter, not a retry queue") is about notices reaching *nobody*.
  Suppression happens only when a capful of same-channel notices already sits in
  front of the recipient undrained, so the marginal notice reaches nobody either
  way — and the watcher re-derives every condition from scratch each tick and
  never queues, so the moment the recipient is reachable the **current** state
  fires, not a stale replay.

  **THE FIX IS AN ARTIFACT OF THE SAME KIND AS THE DEFECT, so its own ways of
  repeating "load rises with load" are closed.** (1) The loud log line fires once
  per saturation run, not once per fire, which would be the identical flood in a
  different channel. (2) No new events are emitted — the suppressed fire reuses
  the `stall_watch_fired` it would have written anyway, plus one field. (3) The
  counter is O(1) per fire with no I/O; this ruled out the otherwise-attractive
  design of probing the recipient's maildir for undrained notices, a read whose
  cost grows with the backlog and would be performed exactly when the backlog is
  largest. (4) The map is pruned on the offline road, so it does not grow
  without bound as unique polecat names come and go.

  **THE 30s WAIT-IDLE BUDGET WAS RE-CHECKED AND STANDS.** The ticket asked
  whether it still fits the current fleet, having been chosen against a smaller
  one. Across 1702 recorded fallbacks the gap since the coordinator's last PTY
  write *at the moment the deadline expired* had a median of **218 ms** and a
  p99 of **941 ms** against a 2 s idle threshold; only 10 of 1702 (0.6%) reached
  even one second, max 2.58 s. The coordinator is not almost-quiet at the
  deadline — it is writing continuously — so a longer budget buys nothing and
  holds the heartbeat longer for it. Same conclusion mg-79dc reached from 18
  samples, now at ~100× the n.

  **What this does NOT fix, stated rather than implied.** The offline road
  (recipient not running) has the same flooding shape — 303 of the measured
  fires — and is left undamped, because it has no reset signal and a cap there
  would latch permanently the first time a coordinator went down; it needs a
  different mechanism. This bounds stall-watch's contribution to the
  coordinator's inbox, not the inbox: the scheduler's own wake-up mail
  (mg-5168 / mg-af83) is a separate source with a separate fix. And nothing here
  makes the PTY gate able to tell a busy agent from a stuck one — mg-8772 is the
  neighbour to read if the 30s timeouts turn out to be a regression rather than
  genuine busyness. Documented in
  [docs/design/stall-watch-design.md](../docs/design/stall-watch-design.md) and
  [docs/CONFIGURATION.md](../docs/CONFIGURATION.md).
