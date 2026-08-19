- **An unmet `depends:` is now a DISPATCH GATE, closing the last of the three
  "deliberately not ready" conditions that had no check at the spawn point
  (mg-e7ff).** `spawn-polecat` already refused a gated assignee
  (`non_dispatchable_assignees`, `blocked:<agent>`) and a carrier at
  `stage: gated`. Unmet dependencies were the same class of condition and the
  only one with nothing at the spawn point: the gate was keyed on **status**, and
  status is what mg's claim-release path got wrong.

  Measured on 2026-08-19: two items carried the *identical* unmet dependency and
  sat in different lifecycle directories. The one that acquired the edge while
  AVAILABLE was demoted to `pending/` and held. The one that acquired it while
  CLAIMED — `mg edit --add-depends` deliberately does not demote a claimed item,
  because there is a worker on it — came back to `available/` when its polecat
  was stopped, because `mg unclaim` did not consult the edge. stall-watch and
  priority-wake then advertised it as *"1 item high-priority, unclaimed"*, which
  is advice to dispatch an item whose dependency was deliberately unmet, and
  nothing refused it.

  The placement bug itself is fixed in macguffin (`mg unclaim` now asks
  `gateOpen` where the item belongs). This gate is the **defence in depth**: mg's
  rule is expressed as a DIRECTORY, and a directory is a placement that some path
  can get wrong. Reading the `depends:` field at the dispatch point means a wrong
  placement no longer decides the question on its own.

      $ pogo agent spawn-polecat cat-836c --id=mg-836c --repo=/Users/daniel/dev/pogo
      409 Conflict
      work item mg-836c declares `depends: mg-12aa (claimed)` and it has not
      finished, so it is deliberately not ready. Finish or drop the dependency
      (`mg edit mg-836c --rm-depends=...`); `mg schedule` releases the item by
      itself once every parent is done. It is also sitting in available/, which
      mg's own placement rule says it should not be while a parent is
      outstanding: the store is inconsistent, and `mg schedule` reports every
      item in that state

  It is answered by the SAME gate object and the same read of the item as the
  assignee and stage gates, per the mg-4798 ruling — a second gate object asking
  "may this be executed at all" is how two rules begin to drift apart. The
  assignee gate still wins when both apply: that value was set by hand and states
  an intent a dependency cannot, and its way out differs.

  **It fails OPEN in the two directions that matter, by construction rather than
  by a check.** A parent is unmet only when it is found sitting in `available/`,
  `claimed/` or `pending/`; every other answer — `done/`, the archive, a typo, an
  unreadable store — is treated as satisfied. `internal/workitem` does not scan
  the archive and mg archives completed work within minutes of `mg done`, so a
  gate that read "absent" as "unfinished" would refuse nearly every dispatch
  whose item has any dependency at all, which is how a gate gets disarmed rather
  than fixed. mg remains the authority on satisfaction; this is the subset of it
  pogo can verify without one.
