- **`pogo check-stranded`'s unit of report was the OPEN work item, so the
  population most likely to hold forgotten work could not appear in it
  (mg-ded2).** Every row the sweep could produce was a row about an open item.
  Three things therefore had no row available to them at all — and the third one
  fabricates agreement.

  All three were measured on 2026-08-19 by running the tool, not by reading it.
  Three agents had spent an afternoon arguing about what the guard would report
  and none of them had run it; one command settled it.

  **Gap 1 — a branch whose work item is closed could not be reported, even in a
  fully covered repository.** `polecat-pc-rev-c5d5a10` held a commit that exists
  on no remote ref. Its repository was in scope and clean — `items=3,
  fetched=true, error=-` — and `PolecatBranches()` enumerated the branch
  correctly. It still did not appear, because the report joins branches to *open*
  items and this one's item was closed months ago.

  That is the inverse of the risk. A polecat whose item is still open has an
  owner, a queue entry and an agent that may yet push. A polecat whose item
  closed months ago has none of those, is the likeliest thing on the box to be
  forgotten, and is precisely what `git gc` reaps.

  **Gap 2 — a repository named by no open item produced NO row, not even an
  error.** `/Users/daniel/dev/macguffin` held a polecat worktree and appeared
  nowhere: not in `repos[]`, not as an error, not as a count. The output was
  indistinguishable from one where every repository on the box had been scanned.
  Measured coverage that day: 5 repositories held polecat worktrees, the sweep
  covered 8, and 1 repository holding worktrees was uncovered.

  Not dressed up as a near-miss: `gt-ffbd` was clean — 0 unpushed commits, 0
  dirty files, branch present on origin, control of 154 heads from `ls-remote`.
  Nothing would have been lost. The defect is that the report could not
  distinguish that from the other case.

  **Gap 3 — `--repo` accepted a name matching nothing, scanned zero
  repositories, and exited 0.** The worst of the three:

      pogo check-stranded --repo one_third_width_three               -> "136 open work item(s) scanned across 0 repo(s)"
                                                                        "No open work item has work already sitting on a branch."  EXIT 0
      pogo check-stranded --repo this-repo-does-not-exist-anywhere   -> BYTE-IDENTICAL OUTPUT                                       EXIT 0
      pogo check-stranded --repo /Users/daniel/research/one_third_width_three
                                                                    -> 1 finding                                                    EXIT 1

  A bare name, an abbreviation and a fictional repository were indistinguishable
  from a clean repository. The `0 repo(s)` count *was* printed — one line above a
  summary sentence that contradicted it — so the information existed and lost to
  the reassuring sentence next to it, which is the same defect as gaps 1 and 2
  rather than a different one. It was found inside evidence that had been mailed
  arguing a conclusion the command appeared to support: **a wrong command that
  prints an all-clear does not merely fail to check a claim, it manufactures
  support for whichever claim it was quoted to support.**

  ---

  **Three repairs, and the cheapest has the widest catch.**

  **1. The report states its own boundary, on every run.** A new
  `WHAT THIS REPORT CANNOT SEE` block, in the human output and as `frame` in the
  JSON, naming what is outside the join and how much of it there is. It prints on
  a clean run, a blind run and a run with findings.

  This is the only one of the three that would have caught the macguffin case:
  that worktree is clean, so no finding fires on it under either row-level
  repair. It is also the only one that addresses what actually happened — at
  06:55Z a coordinator read 2 findings across 121 open items and 8 repositories
  as the population of at-risk work, submitted both, and moved on. The report
  could not have shown a branch whose item was already closed, and showed nothing
  at all for macguffin, which is where `mg-e7ff`'s fix later merged. **The output
  was right and it was read correctly; the failure was entirely in what the
  output did not say about itself.** An instrument that names its boundary is
  checkable; one that does not gets read as a census.

  **2. A new `orphan_branch` row, bounded by the WORKTREE and not by the
  branch.** "Also scan closed items" is *not* the fix — that population is
  unbounded and abandoned by design, at 435 polecat branches in
  `one_third_width_three` alone, and a report that grew by 435 rows would be
  strictly worse than one that omits them. The at-risk population is the
  worktrees present on this host: 39 live ones (of 58 directories; 19 are reaped
  shells), enumerable from disk, the exact population `git gc` reaps, and
  independent of item state.

  The predicate is `git rev-list <branch> --not --remotes` — commits on **no
  remote ref** — and not `git cherry`'s unmerged-vs-target. That distinction is
  what makes the row shippable: on the full sweep it produces **one row**.

        orphan_branch     NO ITEM    polecat-pc-rev-c5d5a10
          in /Users/daniel/research/one_third_width_three — THE WORK IS NOT ON ORIGIN: ...
          1 commit(s) on NO remote ref, and no OPEN work item names this branch:
            revert: drop hStep variant from width3_one_third_two_thirds (revert c5d5a10, mg-b329 …)
          -> git -C ... push origin polecat-pc-rev-c5d5a10

  The remedy is a push and deliberately **not** `pogo refinery submit`: submit
  takes an `--author` and there is no open item to pass it, which is the same
  fact that made the row impossible before. Live polecats' branches and branches
  some open item names are both excluded, the first so the detector does not fire
  on healthy input and the second so one branch is never reported under two
  opposite remedies.

  **3. Repositories are discovered from the worktrees on disk, and a `--repo`
  matching nothing exits 3.** The repo set is now "every repo the open items
  name" ∪ "every repo a polecat worktree points at" ∪ "every `--repo` that
  resolves as a git repository", and a coverage line says which repositories are
  there for the second reason:

        /Users/daniel/dev/macguffin — 0 item(s), 158 polecat branch(es), 1 worktree(s),
          refs refreshed from origin — NO OPEN ITEM NAMES THIS REPO; it is here because
          a polecat worktree on this host points at it

  A `--repo` value that resolves to no repository now exits `3` — the command's
  own help already defined that as *"this run measured nothing"* — and renders
  `MATCHED NOTHING` above a `NO VERDICT` line instead of the all-clear. That
  holds even when other `--repo` values matched: the caller named a repository
  and got no answer about it, and a typo is the commonest way to produce one.
  `--repo <a real repository the board happens not to name>` still resolves and
  is still a legitimate narrower sweep.

  ---

  **Severity, stated plainly rather than left for the next reader to discover.**
  Nothing on this box gates on `check-stranded`'s exit status: `grep` over
  `scripts/` finds 0 invocations, against a positive control of 116 hits for
  `drain` in the same file. The nightly deploy's actual protection against losing
  unpushed work is not a detector at all — the drain removes worktrees without
  `--force`, git refuses to remove one holding unpushed commits, and the drain
  stalls and names the polecats involved. That is a structural guard, and it
  **cannot fail toward all-clear because it is not asking a question**. Gaps 1–3
  mislead readers; they do not disarm a guard. The harm this tool can do is to a
  decision, not to a file — which is exactly what happened at 06:55Z, and is why
  the frame is the whole fix and the exit code a tidy-up.

  Blast radius of gap 3 is likewise bounded: 0 shipped prompts pass `--repo` to
  `check-stranded`, against a positive control of 5 bare `pogo check-stranded`
  occurrences in the same files. `mayor.md` teaches the bare form, which is
  correct and unaffected. No prompt sweep and no fleet notice were needed.

  Both sweeps above are architect's, re-derived here rather than repeated: 0/5 in
  `internal/agent/prompts` + `docs/examples`, 0/116 in `scripts/`, and the single
  live `--repo` occurrence under `~/.pogo/agents` is doctor's turnlog line
  *describing this finding*. A broad `--repo` grep looks alarming and means
  nothing — the many `--repo=<owner>/<repo>` and `--repo={{.Repo}}` hits belong to
  `gh`, `mg create` and `spawn-polecat`, different commands with different
  semantics. The narrow grep is the question.

  ---

  **Ways this fix could commit the defect it repairs, enumerated and checked
  rather than assumed** — and this pass was run *after* the repair demonstrably
  worked, which is where the enumeration normally gets skipped. The fix
  introduces a new population (the worktrees), so it inherits new ways to lose a
  member:

  - The enumerator's natural shape is `if err != nil { continue }`, which makes
    an unreadable worktree indistinguishable from a reaped shell — and 19 of the
    58 directories legitimately *are* reaped shells, so the silent branch is the
    well-travelled one. A directory that has a `.git` and still will not answer,
    and a worktree on a detached HEAD, are now named in
    `worktrees_unreadable_list` and printed as `WORKTREE NOT READ`.

    **This one has a live instance, found by the audit rather than by the
    repair.** `~/.pogo/polecats/p6b2d` is a polecat worktree on
    `/Users/daniel/research/onethird_program` sitting on a detached HEAD, so
    there is no branch ref to measure. The first draft of this very fix dropped
    it silently — 1 of 39 — which is this ticket's defect committed inside its
    own remedy. Nothing is at risk there (`rev-list --count HEAD --not --remotes`
    is 0, checked), and that is the point: the report can now say it could not
    measure the tree, instead of producing output identical to one where it had.
  - The frame first quoted the *enumeration* count where a coverage count
    belongs — under `--repo` it read "39 worktrees enumerated" over a run that
    entered 0 of them. That is mg-8baa's defect in worktree units. It now states
    both numbers.
  - The frame first asserted that polecat branches with no worktree "hold no
    local-only copy" — a claim about hundreds of refs the run never reads, in the
    voice of a measurement. It now says only that they were not looked at. A
    boundary statement may say what was not examined; it may never say what the
    unexamined thing contains.

  Each of the three gap repairs also has an explicit **negative control** that
  runs the identical fixture through the pre-fix population and asserts the
  report is empty and prints the same all-clear it printed on 2026-08-19 —
  without which "the orphan row fires" would prove only that some row exists, not
  that the row was previously impossible.
