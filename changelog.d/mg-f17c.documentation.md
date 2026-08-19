- **The merged-not-closed alert's gated exclusion was reviewed and upheld, with
  the evidence written down (mg-f17c).** mg-9d4e suppressed the coordinator alert
  for a work item pogod declined to close because it is gated
  (`parked`/`human`/`blocked:<agent>`), and flagged the decision itself: the test
  that covered it asserted the judgement, not that the judgement was right.
  Reviewed on three questions.

  **Is the undispatchability structural?** Yes, and not by coincidence.
  `client.ErrMGWorkItemGated` is produced by `config.IsDispatchGated` — the same
  predicate `agent.MGDispatchGate.DispatchGated` refuses spawns on and the same
  one `stallwatch.watchedForDispatch` excludes from the priority-wake population,
  so a gated item is not advertised as ready either. That is now pinned as an
  **iff** rather than described: a new test asserts the gated refusal covers
  exactly the population the predicate gates, driven off the predicate rather
  than a literal list, so a gate value added next year is covered for free and a
  local vocabulary grown at either end fails immediately.

  **Would the alert's remedy be right?** No. Its body prescribes
  `mg done <id> --successor=<...>` and "DO NOT WEAKEN THE GUARD". A parked item
  owes no remainder and no guard refused it, so that text sends a reader after a
  successor that does not exist.

  **Does suppressing it lose the fact?** No — measured rather than assumed. Over
  the live window in which the gated decline has actually been running
  (2026-08-13 to 2026-08-20) there were 18 `MERGED BUT NOT CLOSED` events: 13
  declares-remainder and 5 gated, all five `blocked:mayor`. **All five produced a
  routed mail to the item's filer** quoting the gated reason verbatim, timestamps
  matching pogod's log lines to the second. The suppression costs a duplicate,
  not the fact.

  **The residual, recorded rather than papered over:** in 3 of those 5 the filer
  was a PM rather than the mayor, so the agent named in the gate was not the one
  told. That is a gap in `blocked:<agent>` routing and raising this alert would
  close it only by coincidence, since it routes to the coordinator regardless of
  who the block names — right for `blocked:mayor`, wrong for every other one, and
  carrying the wrong remedy text besides.

  **And the alert still has no runtime observation.** It has never fired: zero
  `work_item_merged_not_closed` events across the retained event spine and zero
  mails carrying its subject prefix, and the running pogod does not contain the
  code. The pre-existing filer-addressed `MERGED BUT NOT CLOSED:` mails that do
  arrive come from a different path, which is now documented beside the alert
  with the subject prefix and event type that tell them apart — so the redeploy
  cannot be scored as verified by a mail that fires either way.
