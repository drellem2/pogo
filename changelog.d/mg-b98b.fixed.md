- **A duplicate `spawn-polecat` dispatch could destroy a live polecat's worktree
  — and the destruction ran as the *cleanup* for the refusal it had just issued
  (drellem2/pogo#167, mg-b98b).** Two defects compounded, and the second is the
  one that makes the first fatal.

  `reclaimStalePolecatBranch` asked which worktree had `polecat-<name>` **checked
  out** and treated an empty answer as "no live polecat". That is the same
  liveness proxy gh #94 reported against the gc sweep and fixed there
  (`gitgc/sweep.go`), never carried to this second caller. The two questions
  differ the moment a polecat works a **foreign branch** — which our own shipped
  review and QA roles instruct it to do: the tree is live, `polecat-<name>` is
  checked out nowhere, and the branch reads as an unowned leftover with nothing
  unmerged on it. So it was deleted. `git worktree add` then failed on the
  occupied path, and the rollback for *that* failure — `cleanupFailedPolecatSpawn`
  — force-removed the directory. The tree's untracked files are on no branch, in
  no stash and on no remote, so that tree was their only copy on the machine.
  Meanwhile the agent kept running and `pogo agent list` kept reporting it
  healthy.

  The sharpest form is not the proxy. That same destructor also ran on the two
  paths that had **already established a live owner** — a claim conflict
  (`already claimed by PID N`) and `Registry.Spawn`'s "already running" — so
  pogod refused the dispatch and then destroyed the running agent's tree as the
  cleanup for its own refusal. Nor was `--preserved-override` a precondition: a
  live polecat whose tree is clean and whose branch carries nothing trips no gate
  at all, so reading that flag as the exposure reads it too narrowly.

  Four things changed.

  **A live-owner gate, first of the dispatch gates and NOT overridable.** A spawn
  is refused when its **name** or its **work item** is already held by a live
  polecat — liveness read as the registry unioned with the persisted witness, the
  same answer `gitgc` and stall-watch are gated on, so a polecat that outlived the
  pogod that spawned it is visible too. It sits ahead of every side effect, so a
  refused duplicate creates no worktree, no branch and no prompt file: the paths
  that did the destroying are no longer reached at all. It is **first** because
  three of the gates below it *can* be overridden and each of those overrides is
  reached exactly when a live worker is standing in the tree — a flag typed at a
  message about work that was *left behind* must not carry past a refusal about
  work **in progress**. And there is no flag for this one: every other gate here
  protects throughput, so overriding one costs at worst a wasted worker, while
  this one protects work that may exist in no other copy. The refusal names the
  live owner, both exits in the order they must be taken (`pogo agent stop`
  first — `mg unclaim` on an item its worker is still on strands that worker),
  and says *why* there is no override, so a reader does not go looking for one.
  Alone among these gates it fails **closed** on a witness it cannot read: an
  unreadable store is not an empty fleet, and `gitgc` already skips its whole
  sweep on that same failure.

  **A destructor that no longer removes what it did not create.** "`git worktree
  add` made this tree moments ago and no agent ever ran in it" was the sentence
  the `--force` rested on, and nothing established it. The caller now states it:
  the directory is stat'd before the add, and on the add-failure path — the one
  path where the sentence can be false — the directory is **left behind** and the
  leak is logged with its path. A leak is visible, recoverable and costs disk;
  the alternative is unrecoverable and silent. The four post-add call sites are
  provably this spawn's own and still clean up in full, so gh #27's "branch
  already exists" retry poisoning stays fixed.

  **Ownership by path, reusing gh #94's predicate rather than restating it.**
  `reclaimStalePolecatBranch` now asks `gitgc.PolecatNameForWorktree` which tree
  **owns** the branch's polecat name, whatever is checked out inside it, and
  refuses. Being checked out anywhere is kept beside it as an independent reason
  — `git branch -D` refuses it regardless, and a refusal that names its cause
  beats git's. An unreadable worktree list is now an error rather than an empty
  answer: this check is the last thing between a live worker's branch and
  `git branch -D`, so "I could not look" must not be spendable as "nobody is
  there". Reclamation of genuinely spent branches (mg-d22a) is unchanged.

  **The preserved-worktree refusal now leads with liveness.** Its read-it-first
  list named `git status`, `git log --not --remotes` and `pogo gc
  --list-preserved` — three instruments that describe a tree's *contents* and none
  that says whether somebody is still standing in it. Read in that order, a live
  polecat's tree presents exactly as abandoned residue, and the disposition an
  operator then reaches for is the destructive one. `pogo agent list` and the
  item's own claim come first now, with the sentence that says why they change
  what the rest means.
