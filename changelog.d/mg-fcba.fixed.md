- **A work item whose only copy of its work is COMMITS in a preserved worktree
  stops being advertised as ready (mg-fcba).** mg-836c taught the dispatch path
  to see a preserved worktree holding *uncommitted* work, and both its surfaces
  work. What it could not see is the state a polecat reaches when it gets one
  step further — it commits, and is stopped before it pushes. `git status` is
  clean then, so the probe skipped the tree entirely, and the item read
  dispatchable to everything. That is not an edge case: **the nightly pre-deploy
  stop creates that population on purpose.** On the 2026-08-20 stop, priority-wake
  advertised such an item as "ready, claim or dispatch now" **four times**, and
  the only thing between that advice and duplicated work was a note somebody had
  written from memory eight minutes earlier.

  **Three instruments, blind for three different reasons — and one that was not
  blind at all.** The report said the spawn guard "checks branches, and there is
  no branch". That is wrong and worth correcting, because it would send this
  ticket's worker down a wrong path: `strandedwork.Scan` reads
  `refs/heads/polecat-*` as well as `refs/remotes/origin/polecat-*`, and a linked
  worktree's branch is a ref in the **source repo's** namespace — so an unpushed
  polecat *branch* has refused a spawn since mg-bfe0. What genuinely has no ref
  is a worktree on a **detached HEAD**: its commits are reachable from that
  tree's own HEAD and from nothing else, so no ref scan can see them by
  construction (`~/.pogo/polecats/p6b2d` is in that state). And **stall-watch —
  the thing that actually advertises the item — reads no refs whatever**, so
  priority-wake was blind regardless of what the spawn gate would have done.

  **It is a wiring job, as filed.** The population is now carried by the same
  `PreservedForItems` probe both surfaces already consult, so the spawn refusal
  and the priority-wake suppression inherit it with no new plumbing. The
  predicate is `gitgc.BranchDurable` asked about the *worktree's HEAD* instead of
  a named branch — deliberately **not** `git log HEAD --not --remotes`, because
  the refinery rebases before merging, so a SHA test calls every landed-and-
  preserved tree unpushed forever (71 of this repo's 146 polecat branches read
  `--no-merged main` while demonstrably having landed). Verified against the live
  retained population: all five real preserved trees on this box read `durable`,
  i.e. zero false refusals.

  **It flags; it never decides.** `pogo gc --list-preserved` opens by saying that
  nothing it prints is a verdict and that whether a tree may be reclaimed "needs
  someone to READ the files". Nothing here discards, resumes or commits anything
  — the spawn is refused (overridable, recorded) and the notice asks for a
  decision, which is the same stance the rest of this surface takes.

  **The remedy is worded per tree, because the wrong one destroys the work.**
  `refinery submit` is not prescribed: it merges `origin/<branch>` and refuses a
  branch that is not on origin (mg-586d), which is exactly this population. A
  detached tree is told to give its commits a ref first (`git -C <tree> switch -c
  <branch>`), since naming a branch to push would be a remedy with a hole in it.
  And a count that could not be read never renders as `0 commit(s)` — the number
  is the half a skimming reader keeps, and a zero there says the opposite of its
  own verb.

  **Cost, which the report named as explicitly untimed:** 92µs per probe in the
  steady state (no retained tree names a wanted item — one `os.ReadDir` and
  nothing else), and ~60ms per candidate tree on top of the ~18ms the removal
  guard already spent there.
