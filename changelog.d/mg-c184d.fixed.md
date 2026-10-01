- **A branch with no commits of its own now FAILS the merge instead of
  resolving as "already merged".** The refinery's already-merged guard used to
  decide on ancestry alone, so a branch pushed at the target's tip with its
  work uncommitted read exactly like a landed one: the MR resolved as merged
  naming someone else's commit, pogod closed the item `done` and reaped the
  polecat (mg-3b86e). "Already merged" now requires a prior merged MR on the
  refinery's own record, for the same branch or work item, whose merged SHA
  contains the head. Otherwise the MR fails `class=defect` at stage
  `empty-branch` — "branch carries no commits ahead of <target>" — with no
  MERGED event, `merged_sha` or `already_merged`, so the item stays open and the
  polecat stays alive to commit and resubmit. Restart recovery applies the same
  rule, crediting a contained head to the in-flight MR only when its new
  `target_at_start` record shows the head was ahead of the target before the
  push. The polecat templates (`polecat.md`, `polecat-architect.md`) now run a
  `presubmit_check` that refuses to submit a dirty worktree, a branch with
  nothing ahead of the target, or an unpushed HEAD. Upgrade note: a legitimate
  resubmit whose earlier MR has aged out of refinery history (100 entries / 7
  days) is now refused too; confirm with `git log` and close the item by hand.
