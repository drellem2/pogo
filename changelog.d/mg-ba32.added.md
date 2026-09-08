- **The stranded-work dispatch gate has a second exit, and it is the one a
  branch that is GOOD needs (mg-ba32).** The gate refuses a spawn at an item
  whose polecat branch already holds unmerged work, and until now it had exactly
  one way past it — `--stranded-override`, whose own text told the operator to
  use it "if this branch is genuinely spent". But the gate refuses **two
  populations that want opposite handling**, and only one of them is that one:

  - **spent, discard** — the attribution was wrong or the work is dead. Start
    over from the target; the branch is left behind.
  - **good, adopt** — the branch holds the work and somebody has to *land* it:
    rebase it, finish it, resubmit it. That dispatch is the **opposite** of a
    re-derivation, and frequently the only route the work has left, because "get
    the branch merged" is not something a gate can do and a merge that needs a
    rebase needs a worker.

  The second had **no cell** anywhere in the guard. On 2026-08-14 at ~02:21Z a
  dispatch to adopt and rebase `mg-5058`'s branch was refused with `do NOT
  dispatch a worker at this item, it would re-derive work that already exists` —
  of a dispatch whose whole purpose was not to. The honest action then required
  `--stranded-override`, so a paragraph of prose had to be written into a flag
  that asserted the opposite of what the operator held, and afterwards neither
  the log nor the flag's help could tell the two dispositions apart.

  **`pogo agent spawn-polecat --stranded-adopt="<why>"`** is the missing one, and
  it is not a rename of the override: it **bases the new polecat's worktree on
  the stranded ref** instead of on the target, so the worker starts standing on
  the work rather than in front of it. The worker still gets its own
  `polecat-<name>` branch — the adopted branch is never rewritten under whoever
  else may be reading it, and an inherited pre-registration commit arrives as an
  **ancestor**, which is what makes it unamendable. The adoption is **measured**
  after the worktree is created, not assumed from having passed the argument: a
  worktree that turns out not to carry the branch fails the spawn, because a flag
  named "adopt" over a tree that adopted nothing is the same defect this closes.
  The reason reaches the worker as the first block of its prompt, above
  everything else, since every other instruction is read against what tree it
  believes it is in.

  Passing both flags is **refused** rather than resolved by precedence: they are
  the two halves of a decision that has to have been made, and picking one would
  record a decision nobody took.

  **The record now distinguishes them too.** An adopt dispatch emits
  `dispatch_stranded_work_adopted` — naming the adopted branch, its ref, the
  disposition, and any stranded branch it did **not** adopt — never
  `dispatch_stranded_work_overridden`. A reader asking later what happened to a
  stranded branch gets "a worker was sent to continue it" or "a worker was sent
  past it", not the flattened "somebody overrode the gate".

  **And the text stops asserting a reason the operator may not hold.** The
  refusal names both exits and what each one *does to the branch*, instead of
  offering one and a claim about the branch the gate cannot make;
  `--stranded-override`'s help says plainly that its worker starts from the
  target and inherits nothing. The one-line finding shared by every stranded-work
  instrument (`strandedwork.Finding.Summary`) no longer says a dispatch here is
  necessarily a re-derivation — it names the base ref that makes it one.
