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

  The sweep now routes through the guard, and an unreadable worktree is **kept
  and reported**: a pinned worktree is recoverable by a human where deleted files
  are not. `--force` still discards, unchanged — an operator's explicit
  `--force` is a positive reason, and is now the only route by which such a tree
  is ever removed.

- **A tree we could not read is never collected — under any ownership, at any
  age (mg-fd39).** Two narrower designs were tried and withdrawn, and both are
  worth knowing about because both are tempting. A **drain** — reclaim once
  dead *and* old *and* previously refused — fell to *age is not emptiness*: a
  30-day-old `irreplaceable.go` is exactly as unrecoverable as a 30-second-old
  one. An **mtime veto** — refuse, even with death evidence, when the tree was
  written to recently — replaced it and fell to something subtler: death
  evidence is precisely what a recent write contradicts, and a veto that
  *expires* has not resolved that contradiction, it has stopped being able to
  see it. *Absence of evidence is not evidence of absence*, one layer in.

  So the rule is singular, and the cannot-enumerate case needs no clause of its
  own: *if we cannot read the tree, we do not act on it.* `pogo gc --apply
  --force` remains the operator's way to overrule it.

- **A refused worktree reports how long it has been untouched (mg-fd39).**
  `kept: … untouched 30 days` is what makes an operator's decision cheap, and
  reporting is now mtime's **only** job here: it authorises nothing and vetoes
  nothing, so the worst it can do is print an unhelpful number rather than lose
  a file. The age comes from a **walk** and never from the worktree root's
  mtime, which was measured blind to edits below it — a live agent editing
  `pkg/deep/work.go` leaves the root untouched, and an operator told "untouched
  30 days" about that tree would clear the pin on the strength of it. Where the
  tree cannot be listed at all, the line says the age is unknown rather than
  omitting it.

  **The cost is accepted deliberately: more pins, and they never self-clear.** A
  pinned worktree pins its branch too. A visible pin a human can clear is a
  categorically better failure than an invisible deletion they cannot, and every
  attempt to bound the pin automatically failed on the same rock.

- **The GC log stops naming an innocent reason for a destructive act (mg-fd39,
  gh #97).** The per-action log shipped as gh #94's remedy — *the* way to find
  out whether the GC took your worktree and why — reported `removed worktree
  <path> (owner damaged, branch polecat-damaged): ticket archived` on exactly
  the path where `git status` had errored, with the discarded error appearing
  nowhere. A forensic log that names an innocent cause is worse than silence: a
  missing line prompts investigation, a plausible one ends it. Cannot-tell
  outcomes now say the status could not be read, and refusals reach the log
  alongside removals instead of only being counted.
