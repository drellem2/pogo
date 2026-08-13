- **The preserved-worktree notice points at the standing list, and stops
  claiming the work would be re-derived (mg-f4c0).** Two changes to a mail that
  was otherwise doing its job.

  It now says, in so many words, that it **fires once and is never repeated**,
  and names `pogo gc --list-preserved` as the standing list of every retained
  tree on the machine. A one-shot alarm that a reader assumes will re-raise is a
  reader who defers, and nothing re-raises. It also states that the reclaim
  command it recommends — `pogo gc --repo=<repo> --apply --force` — is
  **repo-scoped and forced**, so it takes every eligible retained tree in that
  repository rather than the one the notice is about. The alternative to saying
  that is not "they run it"; it is "they correctly decline to run it and the tree
  stays forever", which is how the population reached twenty-three.

  And the do-not-dispatch warning no longer asserts that "a worker sent at this
  item re-derives it from scratch." That is true of `qbe37`, whose tree held a
  1450-line package existing nowhere else, and **false** of `p687f`, whose seven
  modified files were regenerated suite output a re-run reproduces in seconds —
  a systematic shape, not a one-off, since one repo's own gate leaves tracked
  files modified on every merge and so every polecat working it preserves a tree.
  The prohibition is untouched: this is the only guard in the daemon defined over
  an *uncommitted* tree (every other one — the spawn-time refusal, `git cherry`,
  `pogo check-stranded`, both stranded-push reporters — is defined over PUSHED
  commits and is blind here by construction), and weakening it over a false
  sentence would trade a real 1450-line catch for tidiness. What changed is that
  the notice now names **both** recorded shapes and says the difference is
  visible only by reading the files, so the prohibition holds until somebody has.
