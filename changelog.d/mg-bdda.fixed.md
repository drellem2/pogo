- **A dead polecat's worktree is reclaimed on ITS OWN work item, not on
  whatever branch is checked out inside it (mg-bdda).** The GC's two worktree
  phases classified ticket state from different strings: phase 1 read the
  checked-out branch of a registered worktree, phase 1b read the directory name
  of an orphan dir. For one dead owner and one directory name they reached
  opposite verdicts, decided purely by whether git still held the worktree
  registration — owner `0047` archived and dead, tree parked on foreign
  in-flight `polecat-a773`, was KEPT while the identically-named orphan dir was
  removed. A `git worktree prune` between two sweeps flipped the answer on the
  same files, and a dead tree could be pinned indefinitely by a foreign ticket
  that never concludes.

  Both phases now key on the **owner** — the directory's name, the same key
  `mg-dd92` moved liveness to. That is what the directory is a fact about: the
  tree was created for that polecat's work and nothing else comes back to it.
  Nothing is lost by reaping it, because the ref keeps its own gate (phase 2
  still classifies branches by their own name), so a tree parked on an
  unconcluded foreign branch is merely un-checked-out with every commit still
  reachable, and uncommitted files are held back by the mg-ee02 dirty guard.

  **The branch is now a fallback, not a second gate.** Keying on the owner
  alone would strand every worktree whose basename resolves to no work item —
  legacy layouts, hand-made review trees — which is the symmetric defect gh #94
  warned against: never reaping a dead tree. When the owner resolves to nothing,
  the branch decides and the GC line says which key it used (`branch's ticket
  archived (owner "workshop" resolves to no work item)`). It is deliberately not
  a must-*also*-be-concluded condition; that direction would preserve the pin
  this removes.

  One direction becomes more conservative as a result: a tree whose owner is
  still in flight is now kept even when the branch inside it has concluded, since
  a respawn for that work item lands on the same path.

  Phase 1b's lack of a `WorktreeDirty` guard is now stated rather than implicit:
  `WorktreeDirty` shells out to `git status`, and an orphan dir has no `.git` by
  construction, so there is no index or HEAD to call its files "uncommitted"
  against.
