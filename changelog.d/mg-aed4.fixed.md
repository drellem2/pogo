- **`pogo check-stranded` printed a paste-ready `pogo refinery submit` for
  branches that have NEVER BEEN BUILT, and the tickets saying otherwise lost to
  the runnable command (mg-aed4).** On 2026-08-19 the sweep emitted, for five
  branches:

        -> pogo refinery submit polecat-p516e --repo=/Users/daniel/dev/pogo --author=mg-516e   # do NOT dispatch at mg-516e

  while each of those five items' own body said, in bold, **"Do not `pogo
  refinery submit` this branch. It has never been built."** Both artifacts were
  correct about what they knew. The tickets knew the commits came from the
  mg-51bf rescue and bypassed the pre-commit hook **deliberately** — a rescue of
  possibly-partial work must not be gated on whether that work compiles, or the
  half-implementation (exactly the case the hook refuses) stays uncommitted and
  unbacked-up. The sweep knew only that commits existed on a branch and not on
  the target, which is true. The sweep is the one you can paste.

  **The failure mode is the gate PASSING, not failing.** A failed gate costs a
  run and a `failed` row a later reader must interpret. A passing gate merges
  half-implemented, never-reviewed rescue code to main on the authority of a
  command a detector printed — and a rescue branch is precisely the population
  where "the gate passed" is the weakest evidence available, because the commit
  bypassed the hook that would have had an opinion.

  **The report's closing caveat was already there and was not the missing
  piece.** It still ends with *"This command submitted nothing and closed
  nothing. Both remedies are one command and both are destructive in the wrong
  direction, so they stay with a reader."* That applies to every row equally, so
  a reader who trusted it had nothing telling them WHICH rows it was about: the
  five unbuildable branches rendered identically to a branch genuinely ready to
  submit.

  **The repair is a row kind, a count, and one string that is no longer
  printed.** `rescue_unbuilt` leads the findings header and the row ordering (the
  row whose ordinary remedy is destructive has to be the one a reader who stops
  at the first row has read), states the mechanism per branch, and its remedy
  reads the branch instead of submitting it:

        4 FINDING(S) — 4 RESCUE-UNBUILT, 0 stranded, 0 landed-but-not-closed, …

          rescue_unbuilt    available  mg-516e
            branch polecat-p516e (pushed) vs refs/remotes/origin/main
            RESCUE COMMIT d84e002a2993, rescue tracked at mg-51bf — committed with `--no-verify` and NEVER BUILT.
            -> git -C … log -p …   # READ IT, THEN BUILD IT. … NO submit command is printed for this row on purpose

  mg-bfe0's lesson was that a prose caveat beside a runnable command loses to the
  command, and its fix was to **chain** the missing prerequisite with `&&`. That
  fix is unavailable here: the prerequisite is "somebody builds and reads this",
  the build command is repo-specific, and no string makes a human review
  runnable. So the prerequisite is made unskippable the only other way — the
  paste-ready submit is not printed at all for this one population. It is not
  withheld as a secret: the row names the branch, the repo and the target, and
  every other stranded row still carries the submit line.

  **Measured before and after, on the live board rather than in a fixture.** The
  installed (pre-fix) binary prints 4 submit lines for `--repo=/Users/daniel/dev/pogo`;
  the fixed binary prints 4 `rescue_unbuilt` rows and 0 submit lines over the same
  refs. A full sweep finds **6** such rows across 3 repositories — the 5 the ticket
  named plus `polecat-ca397` in `onethird_program`, which the ticket did not — and
  **1 ordinary `stranded` row that keeps its submit line**, which is the negative
  control the change most needed.

  **The convention this keys on is measured, and it is bigger and messier than
  the ticket's five.** `git log --all --grep='^RESCUE'` across the three
  repositories finds **32** commits from **two** rescue events, and they disagree
  about what goes in the parentheses:

        mg-51bf    5 commits   RESCUE(mg-516e): … recovered from preserved worktree p516e — UNREVIEWED … (mg-51bf)
        mg-11fa   27 commits   RESCUE(p6b2d): 2 uncommitted path(s) from a retained worktree (mg-11fa)

  The first parenthesises the **work item** whose work was recovered, the second
  the **agent** whose worktree it came out of. Only the prefix is common, which is
  why the predicate is the prefix and nothing else — a rule keyed on either
  payload would have covered one event and silently missed the other, and the
  missed one is 27 of the 32. Nothing in this repository emits a `RESCUE` subject;
  `strandedwork.RescuePrefix` is the first place the convention is written down in
  code, and it says so rather than implying a format exists.

  Detection is on **unmerged** commits only. A rescue commit already on the target
  was built by whatever gate merged it, so the label is not permanent and a landed
  rescue branch keeps its ordinary `mg done` remedy.

  ---

  **Ways this repair could commit the defect it repairs, enumerated and checked —
  two of the three were real and are why this list is not decoration.**

  - **The remedy text named the command it was withholding.** The first draft
    read *"NO `refinery submit` line is printed for this row on purpose"*, which
    puts the command's own name on the row it is being kept off. Caught by the
    test that asserts the string's ABSENCE, which is the unusual assertion this
    ticket requires: the defect was never a missing warning, it was a present
    command.
  - **`RescueTracker()` returned the redundant one of the two ids.** A rescue
    subject names two different items and the report already knows one of them —
    it is the row's own subject. The first draft returned that one and passed its
    test, because the fixture had collapsed both ids to the same string. Caught by
    running the tool against the real board, where the printed id was visibly the
    row's own. The fixture now transcribes a real subject with two distinct ids,
    and `RescuedItem()`/`RescueTracker()` are separate methods.
  - **The doc comment claimed a population it had not measured** ("five branches,
    one author, one event"). The count above replaced it. It also flipped the
    conclusion: the majority form is the one the ticket never mentioned.
  - **Does the new kind SILENCE anything?** No. `rescue_unbuilt` rows are still
    rows, still count toward `Actionable()`, still exit 1. Asserted.
  - **Does it steal rows from `stranded`?** Only where an unmerged commit carries
    the marker; the mixed-population test asserts both halves, and the live sweep
    keeps its one ordinary stranded row with its submit line intact.
  - **Precedence against `conflict_suspect`.** The new kind displaces
    `stranded` only. `conflict_suspect` already recommends neither remedy and
    carries something the rescue row cannot — the target may already hold this
    work — so it keeps its kind, and `Row.Rescue` is populated anyway so the
    rescue paragraph prints on whichever cell the row lands in.
  - **`Finding.Disposition` did not move.** A rescue branch IS stranded, so the
    field the spawn-time dispatch guard switches on stays `resubmit` and that
    guard refuses exactly what it refused before. The rescue signal is a new
    field, not a new state, which also leaves it available to mg-ba32 (the
    spawn-time guard's own rescue cell) without this package changing again.

  **Not fixed here, and neither implies the other.** mg-441f is the remedy not
  consulting **refinery history**, so it prints submit for a branch the refinery
  already refused — a different cause, and measured *not* to be what happens here
  (none of these branches has ever been submitted at all). mg-ba32 is the
  **spawn-time** guard having no rescue cell — a different instrument. Both share
  a repair site with this one.

  Also corrected while in the table: `docs/operations.md` still described "four
  row kinds" and listed four, having missed `repo_unreadable` and `orphan_branch`
  when mg-ded2 added them.
