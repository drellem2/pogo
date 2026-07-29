- **The GC sweep no longer destroys a worktree it could not read (mg-fd39,
  gh #97).** Phase 1 ran its own dirty check and called `RemoveWorktreeForce`,
  so the cannot-tell guard built into `RemoveWorktree` never ran and a `git
  status` that **errored** was treated identically to one reporting **clean**.
  The error was discarded, never inspected and never logged. Reproduced on the
  shipped tree: a damaged `.git` pointer and an EACCES on `.git` each destroyed
  an untracked file, took the worktree directory, and then let phase 2 delete
  the branch ref — so the work was unreachable by any route. The correlation is
  what made this the worst arm to fail open on: `git status` fails precisely
  when `.git` is damaged or the disk is unhappy, which is when the working files
  are least reproducible.

  The sweep now routes through the guard. An unreadable worktree is **kept and
  reported** unless the caller has passed **positive evidence that its owner is
  dead**; a pinned worktree is recoverable by a human where deleted files are
  not. `--force` still discards, unchanged — an operator's explicit `--force` is
  a positive reason.

- **Death evidence is passed in, never inferred from absence (mg-fd39).** The
  new `gitgc.Options.OwnerVerdicts` carries it, keyed by polecat name. pogod
  fills it from the polecat witness, mapping **only** `WitnessDead` — the
  store's one positive-evidence verdict — to `OwnerGone`; `WitnessNoRecord` and
  `WitnessUnreadable` both refuse. Absence from the live set is what a caller
  has when it did not look, or looked with an instrument that failed, so it
  never licenses a removal. `pogo gc` leaves the field unset and so degrades to
  "refuse" **by construction** rather than by anyone remembering: knowing who is
  alive never establishes that a particular absent name is dead.

- **A timestamp can now forbid a deletion, and still never authorise one
  (mg-fd39).** Even with positive death evidence, an unreadable worktree written
  to within the last day is refused — death evidence plus a file written ninety
  seconds ago is a contradiction, and it resolves in favour of the files. The
  check walks the tree rather than statting its root, because root mtime is
  measurably blind to edits below it: a live agent editing `pkg/deep/work.go`
  leaves it untouched. A tree that cannot be listed at all gets its own refusal
  instead of falling back to that blind signal.

  Read the other way, the same window is the **drain**: a refused tree that is
  cannot-tell, death-evidenced and quiet for a day collects on a later sweep
  with no operator action and no persisted state, which is what keeps the
  refusal a delay rather than an unbounded leak. Nothing collects *because* it
  is old — that would delete a live agent's work for the crime of thinking hard.

- **The GC log stops naming an innocent reason for a destructive act (mg-fd39,
  gh #97).** The per-action log shipped as gh #94's remedy — *the* way to find
  out whether the GC took your worktree and why — reported `removed worktree
  <path> (owner damaged, branch polecat-damaged): ticket archived` on exactly
  the path where `git status` had errored, with the discarded error appearing
  nowhere. A forensic log that names an innocent cause is worse than silence: a
  missing line prompts investigation, a plausible one ends it. Cannot-tell
  outcomes now say the status could not be read, and refusals reach the log
  alongside removals instead of only being counted.
