- **git GC no longer deletes a live polecat's worktree when it works a foreign
  branch (gh #94, mg-dd92).** The sweep decided whether a worktree was occupied
  from the polecat name embedded in the branch *checked out inside it*, never
  from the worktree's path — whose basename is the polecat that owns the tree.
  A live polecat on any branch but its own was therefore invisible to the
  liveness gate: it inherited the foreign, concluded ticket's state, its
  worktree was removed mid-task, and freeing its branch waived the branch guard
  so the ref went too. The agent kept running and `pogo agent list` kept
  reporting it healthy; the work survived only while the commit was still loose
  in the shared object store. Liveness is now keyed on the worktree **path**
  (`gitgc.PolecatNameForWorktree`) and branch deletion on the branch suffix —
  they answer different questions and neither substitutes for the other. Not a
  v0.6.0 regression: the branch-keyed check has been present since `gitgc`
  existed. A normal polecat exit still reaps its tree; the test that inverts the
  reproduction carries that control in the same sweep.

- **git GC says what it removed and why (gh #94, mg-dd92).** The sweep already
  assembled path, owner, branch and reason for every decision and logged only
  counts, so a removal in a multi-megabyte pogod log was a bare number and "did
  the GC take my worktree" was unanswerable for any past incident. Every
  destructive action — and every tree deliberately preserved — now logs a line
  naming the repo, the tree, its owner, the branch checked out in it, and the
  reason. `pogo gc`'s summary carries the same detail. Keeps are not logged per
  tick; only actions.

- **A failed worktree removal no longer tells the branch phase the branch was
  freed (mg-dd92).** `freed[branch]` was set before removal was attempted and
  survived a failure, so the sweep went on to attempt a branch deletion git was
  always going to refuse — reporting an error in place of the correct "kept:
  checked out in a worktree". Found by code read during the gh #94 triage and
  never observed in the wild.
