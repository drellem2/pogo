- **A deferred polecat that dies between its merge and `mg done` no longer
  strands its work item (mg-c8d5).** Since the PR-flow classification landed,
  the ordinary end for a merged polecat is to be left running so it can open its
  pull request and call `mg done` itself. pogod's exit hook treated every such
  exit as a clean completion: it disarmed the bounded backstop and returned. But
  a process that exits on its own never goes through `Registry.Stop`, which is
  the only place a polecat's work-item claim was released — so a polecat that
  crashed, was OOM-killed, or was killed by hand after its branch merged left
  `work/claimed/<id>.md.<dead-pid>` behind. That item is never dispatched again
  and is invisible to stall-watch, which scans `available/` only: the same
  silent-absence failure as mg-fb13, through the one door mg-fb13 did not close.

  The exit hook now settles the exit instead of merely disarming it. If the item
  is still claimed, the claim is released — scoped to that polecat's own work
  item, never a sweep — and the mayor is mailed that a merged branch is short
  the PR it owed. If no claim is held, the polecat finished (or the mayor
  stopped it, and `Registry.Stop` already released), and nothing is sent; a
  fleet drain does not page anyone. The window is constructed against a real
  process and a real macguffin store in `cmd/pogod/deferreddeath_test.go`, so
  the release is observed rather than argued.

- **The docs no longer claim the result sidecar records `pr_flow` (mg-c8d5).**
  `ARCHITECTURE.md` and mg-7746's changelog entry both said the sidecar carried
  `pr_flow` "when true". It never did and it cannot: a PR-flow merge returns
  before the sidecar writer, because pogod deliberately does not run `mg done`
  on that path. A sidecar written by the refinery is therefore, by construction,
  a default-branch completion. `pr_flow` is recorded on the **merge request** —
  read it with `pogo refinery show <id> --json`. Both claims were corrected
  rather than implemented: making the sidecar true would mean writing one on the
  PR-flow path, which is exactly the `mg done` that path exists to withhold.
