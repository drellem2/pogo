- **git GC no longer deletes a LIVE polecat's worktree when it has checked out
  a branch other than its own (mg-3b7c).** The sweep decided a worktree was in
  use by taking its CHECKED-OUT BRANCH, stripping `polecat-`, and looking the
  remainder up in the live set. That assumed every polecat stays on its own
  `polecat-<name>` branch for its whole life — but a polecat dispatched to fix
  or rebase an **existing pull request** must check out that PR's head branch,
  because that is the only way to update a PR in place. The moment it did, its
  worktree resolved to the *other* polecat's name, missed the live set,
  inherited that other ticket's concluded state, and was removed **together
  with the branch ref** out from under a running agent that pogod was still
  reporting healthy. On 2026-07-29 that took out polecat `caa65`'s worktree
  five minutes after it was created; the work survived only because the commit
  was still loose in the shared object store, which a `--prune=now` would have
  reaped. This is a whole class of ticket, not an edge case: every
  fix-the-conflicts-on-PR-#N dispatch produces it, and the workflow cannot be
  changed away without abandoning PR continuity.

  In-use is now decided by **worktree PATH ownership**, which is what pogod
  knew all along — it records `Agent.WorktreeDir` at spawn and logs it — and
  never passed to the sweep. pogod now supplies it as
  `gitgc.Options.LiveWorktrees`, and gitgc additionally re-derives
  `<PolecatsDir>/<name>` for every live NAME, so a polecat that outlived the
  pogod which spawned it (registry empty after a restart, only the on-disk
  witness left, which stores names and not paths) is covered too. Paths
  compare through symlinks, since git canonicalizes the worktree paths it
  reports and `/var` vs `/private/var` would otherwise read as two trees.

  Three further guards ride along:

  - a branch checked out in any **surviving** worktree is never deleted —
    `freed` used to be set *before* the removal was attempted, so a removal
    that FAILED still told the branch phase the ref was loose;
  - a worktree holding another polecat's branch is kept even when nobody is
    live, because that ticket's conclusion is not evidence about this
    directory;
  - pogod logs **which** worktree or branch it destroyed and why, instead of
    only `deleted N branches, removed N worktrees` — the count-only line is
    why diagnosing this took a log grep plus the affected polecat's own
    incident report.

  Prune stays conservative; nothing moves toward `--prune=now`.
