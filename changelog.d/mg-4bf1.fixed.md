- **The do-not-dispatch signal was CONTRADICTED, not missing — and only the
  recommendation repeated (mg-4bf1).**

  At 2026-09-08 00:54Z two of pogod's own signals about `mg-a932` sat in one
  inbox pointing opposite ways:

  ```
  priority-wake:    "ready and unclaimed — claim or dispatch now: mg-a932"
  [stranded-push]:  "mg-a932 ... polecat-ta932 pushed=true ... do NOT dispatch"
  ```

  Both were correct about what they knew. The prohibition is precise — it names
  the polecat, the item, the branch, the ref, the target and the commit, and its
  body even states the failure in its own words: *"the item it belongs to is back
  in the pool describing itself as untouched."* **What decided the arbitration
  was cadence.** The recommendation is re-derived from `available/` every tick
  and repeats on a backoff; the prohibition is mailed **once**, at release. A
  reader who reads their mail unevenly sees the dispatch advice several times and
  the do-not-dispatch advice never.

  So this is not a new detector. `internal/stallwatch` gained a third
  `available/` probe — `Stranded`, wired in `cmd/pogod` to
  `strandedwork.PolecatBranches` + `Inspect`, the same package the spawn gate and
  `pogo check-stranded` already read — sampled once per tick beside the in-flight
  and preserved-worktree probes. Items it names are dropped from **both** dispatch
  checks and re-reported by a `stranded_push` notice that says the opposite thing
  and **repeats on the same shape of schedule the recommendation used to win
  with**. The category name matches the `work_item_stranded_push` event pogod
  emits at release on purpose, so "detected once and then advertised anyway" and
  "detected and held" are countable apart in `events.log`.

  **It is not a race and not a window, which is what the ticket's earlier
  framings all assumed.** Two polecats were stopped on 2026-09-07, both following
  the pre-deploy quiesce procedure exactly, both with their work confirmed durable
  before the stop, and **both** items were advertised this way (`mg-a932`,
  `mg-d788`). `pogo agent stop` releases the claim — correctly (mg-fb13) — so the
  item *always* re-enters the pool and priority-wake *always* picks it up. That
  also rules out a class of fix: it cannot be addressed by telling coordinators to
  be careful when stopping polecats, because being careful is what produced it.

  **`pogo check-stranded` was the other half, and its exclusion took the
  prohibition with it.** A branch already in the refinery queue was dropped from
  the report entirely — defensible on its own terms (the remedy is *submit it*,
  and it is already submitted), except that no other signal took over. Measured
  2026-09-07 22:47Z, on two branches submitted minutes earlier and both still
  queued: `pogo check-stranded | grep -cE 'mg-daf4|mg-a854'` → **0**, while both
  had carried `# do NOT dispatch at mg-<id>` before the submit. Those are now
  `in_flight` rows: no submit line, no `mg done`, the merge-request id named so
  the reader can settle it in one command. The row is keyed on the item's status
  rather than on the queue alone — a queued branch under a `claimed` item is every
  healthy submit in the fleet and stays an exclusion, because a finding that fires
  on the steady state is one readers learn to skip.

  The report also gained `queue_consulted`, and a coverage line for it: with the
  queue unasked, a branch already awaiting merge renders as an ordinary
  `stranded` row whose remedy would queue a duplicate, and the absence of any
  `in_flight` row reads as *nothing is in flight*. That is mg-8baa's collapse of
  "not asked" into "asked and empty", one field away from being re-learned by
  this very change.

  **The ticket's open question is answered, and the precaution attached to it
  rested on a false premise.** The unknown was whether `spawn-polecat` would
  actually allow the dispatch, left untested because attempting it is the thing
  that causes the harm. It is refused: `strandedWorkRefusal` reads the branch off
  disk and no merge-request state is one of its inputs, so submitting the branch
  does not disarm it — pinned now, because *"skip the git work when the refinery
  is already merging it"* is a one-line optimisation that reads as obviously safe.
  The mayor's constraint said a repo at cap would mask the experiment because
  *"the cap refusal and the stranded refusal are both a 409"*. They are not: the
  stranded refusal is **409** and names the branch, the per-repo cap's is **503**
  and names the cap, and the stranded gate runs **first**. Both properties are
  tested, with the cap refusal as its own positive control so a 409 cannot be read
  as the stranded gate when the cap gate simply was not armed.

  That makes the whole finding a **reporting** defect rather than a data-loss one
  — but a coordinator holds a slot, queues the dispatch and waits out a gate run
  measured in tens of minutes on this box before learning that, and a channel
  which recommends refused actions is one the reader learns to skim (mg-dd77 made
  the same argument about the cap).

  **The remedy is checked against the defect it repairs.** The new notice prints a
  paste-ready `pogo refinery submit` only for a branch that is on origin: the
  refinery refuses one that is not (mg-586d), so an unconditional command would
  tell the reader two false things at once — that the work is durable, and that a
  command which cannot run is the remedy. That is mg-bfe0's defect, and it is
  exactly the shape this change could have committed while fixing its own.
