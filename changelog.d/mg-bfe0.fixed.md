- **A stranded-work verdict now says whether the work is on ORIGIN, and prints a
  remedy that can actually run (mg-bfe0).** The four instruments that report
  stranded work — the dispatch refusal, `Finding.Summary()` (quoted by the
  release log, the `work_item_stranded_push` event and the stranded-work mail),
  the `check-stranded` sweep's remedy line, and the mail's WHAT TO DO block —
  all opened with *"already has PUSHED, UNMERGED work"* and all prescribed
  `pogo refinery submit <branch>`. On a branch that was never pushed, both
  halves are wrong, and wrong in the dangerous direction.

  **"PUSHED" told the reader the work was durable at the exact moment it was
  not.** A pushed branch survives the worktree, is discoverable by anyone
  reading `git ls-remote`, and is recoverable at leisure. A local-only one
  exists in a single worktree on a single host, git-gc reaps that worktree, and
  no stranded-work instrument on any other host can see it. So the message
  described the *urgent* case in the words of the *lesser* one.

  **And the command it offered is refused.** `pogo refinery submit` rejects a
  branch that is not on origin (mg-586d) — the merge worker checks it out as
  `origin/<branch>` — so a reader who pasted the remedy got a rejection and no
  instruction about what to do instead. The remedy is now built by one function,
  `strandedwork.SubmitRemedy`, which chains `git -C <repo> push origin <branch>
  && …` when and only when the branch is unpushed. Chained rather than described
  in prose, because the reader of a stranded-work remedy is deciding what to
  paste, and a pasteable command beats a paragraph that qualifies it — the mail
  already carried a correct *warning* two paragraphs below its incorrect
  *command*.

  **The ticket's premise was that the guard could not SEE a local-only branch,
  and that is false — measured, not argued.** `spawn-polecat`'s refusal was
  believed to be defined over pushed branches, on the evidence that it fired at
  mg-0fc6 only after mayor happened to push the branch minutes earlier. In fact
  `strandedwork.Scan` has enumerated `refs/heads/polecat-*` alongside
  `refs/remotes/origin/polecat-*` since it shipped (mg-b468), `resolveBranchRef`
  falls back to the local head, and a polecat's branch lives in the *source
  repo's* ref namespace because `git worktree add -b` puts it there. Reproduced
  end to end with nothing whatsoever on origin, the dispatch is refused —
  `TestSpawnRefusedForLocalOnlyPreRegistration` is that case, and it asserts the
  empty `ls-remote` first so it cannot pass against a pushed-only guard. Nor is
  the coverage accidental: git refuses to delete a branch a worktree has checked
  out, so a *preserved* worktree pins its branch ref for as long as it exists —
  the population the ticket was worried about is the one whose ref is hardest to
  lose.

  **Which relocates the defect rather than dismissing it.** The gate fired
  correctly on the population the ticket was worried about and then handed the
  reader two false statements. A refusal nobody can act on is the failure a
  blind guard would have had anyway — so the ticket's instinct was right about
  the population and wrong about the layer, and the fix is at the layer where
  the fault actually is.

  **What was checked for not becoming the defect it fixes.** This change is
  itself a set of printed remedies, so the printed command was *run*: `git -C
  <source repo> push origin polecat-p0fc6` on a branch whose commits were made
  in a separate preserved worktree pushes it, and leaves that worktree's
  uncommitted work untouched. Both provenance branches carry negative controls
  (`TestPushedRefusalIsUnchanged`, `TestPushedSummaryIsNotDressedAsLocalOnly`,
  `TestPushedStrandedRowStillPrescribesABareSubmit`) so the common case has not
  grown a push it does not need or a warning that is false for it, and each new
  assertion was confirmed to fail against the pre-change source. `SubmitRemedy`
  and `LocalOnlyWarning` are single definitions in `internal/strandedwork` for
  the reason `BranchMatchesItem` gives: two callers depending on one rule beats
  a second copy that diverges the day one of them changes.

  Scope note, recorded rather than fixed: the remedy strings are not shell-quoted,
  so a repository path containing spaces renders a command that needs manual
  quoting. Pre-existing — the bare `--repo=<path>` form had it too — and no
  fleet path has a space.
