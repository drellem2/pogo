- **A work item can declare that merging is not completion, and the refinery
  stops truncating the tickets whose work comes after the merge (mg-d86e).**
  pogod's merge-success path marked the authoring polecat's work item `done` and
  stopped the process the moment a branch landed (gh #35). That is right for the
  common case, where the branch **is** the deliverable, and wrong whenever the
  merge is a step: a release still has to tag and publish, a change may have to
  be verified in its installed state, an announcement or an issue close happens
  afterwards. For those the completion was premature by construction, and
  because the same path also stopped the agent, the remaining work could not
  happen even in principle.

  Measured twice in ten minutes on 2026-07-29: **mg-ca3c** (pogo v0.7.0) and
  **mg-9f17** (macguffin v0.3.0). Both merged a version bump to `main`, both were
  marked done, both polecats were stopped before the tag step, and neither
  release existed. The end state read as success from every angle — `mg show`
  said `done`, the result sidecar said `completed_by: refinery`, the refinery
  mailed `MERGED`, CI was green on the bump commit — while `git describe` still
  said `v0.6.0`. It was caught only because a human checked the tag instead of
  believing the mail. This is worse than a missed step, because the completion is
  positively wrong rather than absent: an item left `claimed` shows up in every
  sweep, whereas `done` + sidecar + `MERGED` mail is an assertion of success that
  suppresses exactly the checks that would catch it.

  **The refinery cannot know whether a merge completes a ticket; the ticket
  knows.** An item tagged `post-merge-work` now declares that merging it is a
  step, and pogod reads that tag before acting on the merge. A declaring item
  takes the lane `--defer-done` and PR flow already use: the refinery merges and
  mails as usual, the item stays `claimed`, the polecat keeps running and calls
  `mg done` itself, and the bounded 15-minute backstop still reaps and escalates
  one that never ends its lifecycle.

  ```bash
  mg new --title="Cut v0.8.0" --tags=release,post-merge-work ...
  mg edit mg-XXXX --add-tags=post-merge-work   # on an item that already exists
  mg list --tag=post-merge-work                # every outstanding one
  ```

  The declaration is set by the **filer**, on the item, which is the point.
  `--defer-done` and PR-flow classification both depend on the *submitter*
  knowing this merge is not the end — one is a flag passed at submit time, the
  other is inferred from a target ref chosen at submit time — and a release
  polecat merging a version bump to the default branch gets both wrong while
  doing exactly what its ticket said. A tag was chosen over an item-schema field
  for the same reasons macguffin's `declares-remainder` is one: `mg list --tag=`
  finds every outstanding one, `mg show` renders it, `mg edit --add-tags` reaches
  an item that already exists, and an operator can retract it. It **composes**
  with `declares-remainder` rather than duplicating it — that tag says something
  *else* must carry the work forward, this one says *this* item is not finished.

  **An item pogod cannot read takes the same lane as a declaring one.** "I could
  not read the ticket" is not evidence that the merge completed it, and this
  whole defect is a completion asserted without the standing to assert it. The
  cost of declining wrongly is bounded — the polecat completes itself as its own
  protocol already tells it to, and the backstop catches it inside 15 minutes;
  the cost of completing wrongly is a silently truncated ticket that nothing
  catches. The two are not symmetric, so they are not treated symmetrically.

  **Event-driven completion is not removed, and the tests prove it in both
  directions.** The negative half — a declaring item survives its own merge — is
  worth nothing on its own, because it passes just as well with the completion
  path deleted, which would re-open the polecat leak gh #35 exists to close. So
  every case is run against the same probe and asserted both ways: `mg-ca3c` and
  `mg-9f17` survive their merges, and `mg-bdda` (an ordinary fix that merged the
  same day) is still auto-completed and stopped, with the probe recorded as
  actually consulted. Two of those cases run end to end against a real bare
  origin, a real refinery loop, and a real fast-forward merge, because the
  mg-7746 version of this bug survived a passing unit suite.

  The coordinator's `MERGED` mail now says so too. A declaring item's mail
  carries a `Completion: NOT THIS MERGE` line naming the item and the tag, and
  tells the reader not to archive on the strength of the mail — the same
  obligation the PR-flow line already carries, for the same reason.

  **Not changed:** the shipped polecat template (`internal/agent/prompts/**` is a
  protected path the refinery refuses), which still tells a polecat it will be
  stopped at merge. That remains true for every item that declares nothing. A
  polecat working a declaring item outlives its merge and finishes under steps
  7–8 of its own protocol, which already cover exactly that case.
