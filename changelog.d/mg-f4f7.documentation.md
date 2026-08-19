- **The clean-worktree half of the unpushed-work question is answered: nothing
  is stranded (mg-f4f7).** `mg-11fa`'s rescue predicate was `git status`
  dirtiness, so a worktree that had *committed* its work and pushed nothing was
  outside the population by construction — `bf3ae` was one, found by looking
  rather than by any sweep. All 48 live worktrees under `~/.pogo/polecats/`
  (of 67 directory entries; the other 19 are reaped shells holding only
  `.pogo/`) were swept in **their own** repo across five distinct origins.
  **47 of 48 have HEAD contained in a branch that exists on that origin right
  now.** The one exception, `pc-rev-c5d5a10`, holds a commit reachable from no
  remote ref that `git cherry` reports as already upstream — the same revert
  landed as `2cd6bd5` via a second attempt branch. **Nothing was pushed,
  because nothing needed to be.** Two trees (`622f`, `p5058`) would have been
  misreported as unpushed by the obvious query: their work is on origin under
  an `mg-11fa` `rescue-*` branch, not under their own name. Eight worktrees
  were reaped by gitgc *during* the ~20-minute sweep, so the census is only
  valid at its timestamp.
  See `docs/investigations/unpushed-worktree-census-2026-08-19.md`.
