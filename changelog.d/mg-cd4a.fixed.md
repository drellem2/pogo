- **A work item whose `repo` field is a bare NAME no longer reports its
  repository as EMPTY, so stall-watch stops telling the coordinator to dispatch
  into a repository that is full (mg-cd4a).** mg-dd77 taught the aging-item
  notices to say "this is a throughput observation, not a dispatch request … do
  not preempt a worker or snooze an item" when the target repository is at its
  worker cap. That clause resolves on the item's `repo` field, and the field is
  free text. Spelled `/Users/daniel/dev/pogo` it worked all night; spelled
  `pogo` it did not, and the plain "claim or dispatch them" went out instead.

  **The refusal was not the harm — the missing clause was.** The mayor attempted
  the dispatch stall-watch asked for on 2026-08-20 and `spawn-polecat` refused:
  the repository held its three workers. That refusal is loud and
  self-explaining. What went missing with it is the guidance: at cap, "dispatch
  now" has exactly two satisfying moves — preempt a working polecat, or snooze
  the item — and both are damaging, and neither produces a refusal to correct
  the mistake. The cap detection is only what triggers the payload.

  **The mechanism, since nobody had read the code when this was filed.** Every
  comparison the cap makes runs through `config.SameRepo`, which compares
  `filepath.Clean`'d strings — so `SameRepo("/Users/daniel/dev/pogo", "pogo")`
  is false, `RepoOccupancyFor("pogo")` finds no occupants, and a saturated
  repository reports `Count: 0, WouldRefuse: false`. The mayor's inference from a
  2-vs-5 split and one measured refusal was correct, and it holds one layer
  deeper than filed: the same zero is served on `/agents/hostload` and consulted
  by the spawn gate itself, so the *report* was wrong everywhere, not only in the
  notice built on it.

  **Measured on the live daemon, one command apart.** The ticket was filed on an
  inference from a 2-vs-5 split and a single refused dispatch, by someone who
  had not read the code. Both are confirmed, and the reading is a good deal
  blunter than the split — the same repository, the same instant, the running
  (pre-fix) pogod:

  ```
  $ pogo host load --repo=pogo
  Repo:       pogo
  Cap:        3
  Workers:    0

  $ pogo host load --repo=/Users/daniel/dev/pogo
  Repo:       /Users/daniel/dev/pogo
  Cap:        3
  Workers:    3 — pcd4a, pcfeb, pd4a7
  `pogo agent spawn-polecat --repo=/Users/daniel/dev/pogo` would currently be refused with 503
  ```

  So `pogo host load` had the same hole, and its own doc comment already argued
  why that matters: it prints the under-cap state rather than staying silent
  because "the caller here is deciding whether to dispatch, and silence would
  read as permission." A fabricated `Workers: 0` reads as permission just as
  loudly. It now prints `NOT COUNTED` and says the workers were never looked
  for.

  **Two answers, because there are two questions.** A bare name is now resolved
  against the project index before the count is taken, so `pogo` reads the same
  as the path 883 other items spell out and the notice carries the full clause.
  A name that resolves to **no single repository** is reported as
  `Unresolvable` — and that is deliberately *not* the same as an empty `repo`
  field. Empty means "contends for nothing" and zero is the true count; 280
  items are in that state and they keep the imperative, which is correct for
  them. Unresolvable means the count was never **taken**, and reporting it as
  free slots is the same fabrication with a different cause, so stall-watch
  renders it as "could not be determined … attempt the dispatch and read the
  refusal", never as a clean go-ahead.

  **Ambiguity answers nothing, on purpose.** The resolver takes a
  component-aligned suffix match and refuses to pick when two repositories
  answer to a name — `pogo` does not match `pogo-reminders`, and `riemann`,
  indexed at both `~/files/riemann` and `~/research/riemann`, stays unresolved.
  A wrong resolution would not raise an error; it would produce a confident
  sentence about the wrong repository's occupants, which is this defect again
  with a nicer cause. Pogo's **own** checkouts are dropped from the candidate
  list first, and that filter is load-bearing rather than cosmetic: on this
  machine's live 35-entry index, `one_third_width_three`, `union_closed` and
  `one_third` each collide with a refinery worktree of the same name, and
  without it the two largest bare populations in the store — 108 items and 47 —
  would resolve to nothing at all.

  **The fix is checked against the defect it repairs.** The new "is this path
  actually there" probe is guarded on a zero count, because a transient stat
  failure on a **saturated** repository would demote it to "could not be
  determined" and drop the at-cap guidance for exactly the repositories that
  need it — mg-cd4a re-entered through mg-cd4a's fix. Live workers are
  themselves proof the repository is real.

  **Scope, stated.** The counts quoted above are pm-pogo's census of the item
  store on 2026-08-20 and are repeated, not re-derived. Nothing here normalises
  any existing item's `repo` field: 42 bare-`pogo` items remain, deliberately,
  and the local reproducer with them.
