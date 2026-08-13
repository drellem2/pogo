- **The deploy drain reconciles its holder ledger at the WEDGED-POGOD exit too,
  so a bounce over branches that exist only on this host is no longer reported
  as a clean drain (mg-8bb1).** `drain_wait` has four exits that let the deploy
  proceed. gh#135 seated the cross-poll holder ledger at three of them; the
  fourth — pogod stops answering and `witness_alive_count` reports no live
  polecat — returned rc=0 with an **empty `ERR_LOG`** while printing *"the fleet
  is idle: bouncing a wedged pogod strands nothing and IS the repair."*

  **Half of that sentence was checked.** The witness answers a question about
  **processes**; the sentence makes a claim about **commits**. A polecat that
  stopped earlier in the same drain still holding local-only work is invisible
  to the witness for exactly the reason it is at risk — it has no process left
  to see and no registry entry left to read. p7182 measured the gap rather than
  arguing it: poll 1 with two unpushed holders, then HTTP 000 with
  `witness_alive_count 0`, gave rc=0 and a silent error log over two branches
  whose commits existed nowhere else. An empty error log there was not evidence
  that nothing was stranded; it was evidence that nothing was asked.

  **It still does not hold, and that is the point.** The bounce is the repair,
  and it is most likely to be the right move precisely here — blocking would
  rebuild mg-853a's failure mode for the third time. What changes is that the
  operator is told what is at risk *while the bounce happens*: each departed
  holder is named through `err`, so it reaches the reason record and the
  nightly's RED path, carrying the same archival deadline the other three seats
  carry (`internal/gitgc/sweep.go:346-348` deletes an archived item's branch with
  no durability check of any kind — mg-0a43).

  **The remedy is checked against the defect it remedies.** An empty ledger has
  two causes that must not read alike, and reporting "0 departures" over the
  wrong one would restore this bug one level up with the reconciliation as its
  new alibi. A ledger that reconciles clean after at least one readable poll now
  prints the repair claim as a *checked* one; a drain where pogod never answered
  at all says so instead — *"this is NOT a report that nothing is at risk, it is
  a report that nothing could be asked."* That case stays a log line rather than
  an alert, because a wedge before the first sample is the ordinary shape of
  this exit and an alert that fires on every wedged bounce is one nobody reads.

  Sixteen assertions cover it, including the reporter's own reproduction, a
  negative control that pushes as pogod wedges (which is what proves the
  reconciliation *ran* rather than being silent because nothing was asked), the
  never-sampled case, and guards that both rc=2 refusals next door are unmoved.
  Every substantive one fails against the pre-fix driver.
