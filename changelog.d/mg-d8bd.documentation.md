- **`pogo gc --help` stops listing an unmounted volume among the worktrees it
  keeps (mg-d8bd).** The help text named three ways a `git status` can fail to be
  readable — a damaged `.git`, a bad permission, an unmounted volume — and said
  all three are kept and reported as unreadable. The third does not hold in the
  presentation where the mount takes the directory with it:
  `checkWorktreeRemoval` stats the worktree first and short-circuits on an absent
  directory, deliberately, so that "there are no files" never reaches the
  cannot-tell arm. In that shape gc proceeds rather than keeping.

  The clause is **dropped, not replaced**. The gh #97 triage asked that the
  unmounted-volume shape be left unestablished, because simulating a mount means
  real system state on a live machine and nobody has measured it — so stating
  what *does* happen there would be the same defect wearing new clothes. The
  other two cases are correct and stay. No behaviour change: an unestablished
  shape asserted as fact in user-facing help is the same failure as a confident
  wrong log line, and the fix is to stop asserting it.
