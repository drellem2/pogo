- **`pogo refinery history` names the repo each merge landed in, and both
  `history` and `queue` take `--repo` (mg-ff3a).** One refinery queue serves
  several repositories. `history` printed branch, author, status and time — and
  never which repo — so `polecat-pXXXX  status=merged` did not say which `main`
  had moved. The field was in `--json` (`repo_path`) the whole time; only the
  human view omitted it.

  On the evening of 2026-08-07 that omission produced **three** confident wrong
  conclusions from three different agents, two escalated as URGENT against a
  03:00 deploy:

  - four queued merge requests were cancelled on the belief that all were `pogo`
    branches. Two were `pogo-reminders` and `bridget`, unaffected by the red
    `main` that prompted the cancellation, and would have passed. The repo had
    been inferred from the **work item**, which is simply the wrong source: one
    work item can legitimately produce branches in three repos.
  - "6 MRs report merged but main has not moved since 20:58Z — possible lost
    merges" was raised as urgent. Every one had landed in `onethird_program`'s
    main, which moved to `11b22a6` at 22:52 with matching timestamps, while
    `pogo`'s main correctly sat still. No merges were lost.
  - "refinery STALLED, 12 queued, nothing merged" was raised **five seconds
    after** a merge completed and immediately picked up the next one — in a repo
    the reader was not watching.

  None of the three was careless; all three were reasoning correctly from a view
  missing the discriminating field. The documented workaround — `pogo refinery
  show <mr-id>` prints a `Repo:` line — costs a command per row and requires
  already suspecting the problem, which none of them did. It also interacts
  badly with the *merged is not live* reflex the fleet is deliberately trained
  on: checking that a reported merge really landed is the right habit, and it is
  the habit that manufactures the false alarm when the view hides which `main`
  to check.

  `queue` already carried the column (mg-37ad). `history` now renders it in the
  same position and the same form — the refinery's own lane key, i.e. the repo
  basename, so the column shows the thing merges actually contend for rather
  than a second notion of "which repo" that could drift from it. An MR carrying
  no repo path renders `?`, not `.`: `filepath.Base("")` is `.`, which reads as
  a real relative path and would be a confident wrong answer of exactly the kind
  this column exists to remove.

  **`--repo=<name|path>` narrows either view**, accepting a bare name (`pogo`),
  a full path — so `repo_path` pasted out of `--json`, or the path given to
  `refinery submit --repo=`, works unchanged — and `.` for the checkout you are
  standing in. Prefix matches are not matches: `--repo=onethird` does not
  capture `onethird_program`.

  **Three ways the filter could have re-created the bug it fixes, checked rather
  than assumed:**

  - *An empty filtered result is not an empty refinery.* `No merge history.`
    under a filter that matched nothing is byte-identical to a refinery that has
    done nothing — so a typo, or `.` run from an agent worktree whose basename
    is not the repo's, would produce this ticket's exact failure with an extra
    step. An unmatched filter instead names the lane it compared against and
    lists the repos that **are** present; a genuinely empty pipeline says so and
    explicitly exonerates the filter.
  - *The `NOTHING IN FLIGHT` alarm stays whole-pipeline.* `--repo` narrows the
    rows only. Counting in-flight merges within the filter would print a stall
    to anyone whose own lane happened to be idle while the refinery merged
    steadily elsewhere — which is the third incident above, manufactured on
    demand by the fix for it. Lane positions and pending counts are likewise
    still computed over the whole queue.
  - *A narrowed view says it is narrowed.* Filtering prints how many rows were
    hidden and which repos they are in, so a reader who filters to their own
    repo and then reasons about "the queue" can see the edge of what they are
    looking at. The repo being filtered to is never listed among what was
    hidden *from* it.
  - *`--repo` cannot suppress the alarm either.* The obvious implementation
    returns early on a filter that matched nothing — which would mean a reader
    who filtered to an idle repo never sees `NOTHING IN FLIGHT` even when the
    refinery really has stopped. A view hiding an alarming state behind a field
    the reader chose is this ticket's defect wearing the fix's clothes, so the
    unmatched path still prints the alarm, counts it across all repos, and says
    outright that it is `NOT specific to --repo=<x>`.

  One further consequence, stated because it is invisible and wrong-by-default:
  history's retention cap (100 entries) is enforced **across all repos**, so a
  `--repo`-filtered window reaches back less far than its row count suggests —
  a busy neighbour silently shortens yours. When the cap has bitten and a filter
  is active, the command now says so and names the repos that consumed it. The
  existing retention notes were reworded from "showing N" to "the retained
  window holds N (all repos)" for the same reason.
