- **`spawn-polecat` based every worktree on the repo's default branch unless the
  dispatcher remembered to copy the work item's `branch:` field into `--branch`
  (mg-bb0d, drellem2/pogo#176).** One forgotten flag sent the worktree base, the
  refinery submit target and the PR base to the default branch together, so work
  aimed at an integration branch started tens of commits behind it. pogod now
  reads `branch:` from the item and uses it when `--branch` is omitted. An explicit
  `--branch` that contradicts the item is refused with a 409 naming both values.
  The spawn response (`base`) and the CLI print the resolved base. When the target
  is not on origin yet, the worktree still falls back to the default branch,
  because the refinery creates the target at submit, but pogod now says so:
  `target <X> not on origin — based on origin/<default>; the refinery will create
  <X> at submit`. An adopted stranded branch's base still wins over the target.
