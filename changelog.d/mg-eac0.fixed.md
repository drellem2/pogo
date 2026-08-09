- **The refinery read the wreckage of the conflict it was reporting on, called
  it gate output, and told the author to `.gitignore` the production source
  their branch was trying to land (mg-eac0).** A rebase that hits `CONFLICT
  (content)` leaves modified tracked files in the refinery's checkout. The
  gate-dirt detector runs immediately after that rebase, saw the modifications,
  and attributed them to the quality gate. On `mr-d9s7c8atjv1ge6rrj5eg` it
  produced a report that was wrong three separate ways at once, while quoting
  its own captured `CONFLICT (content)` two paragraphs below.

  **The detector cannot be downstream of the thing that dirties the tree.** It
  sat after the rebase, so it could only ever fire on a tree the rebase had
  already touched, and it had no way to tell "the gate wrote this" from "the
  rebase left this". `classifyGateDirt` now declines outright when the failing
  step reports a conflict or has left rebase/merge state on disk — the conflict
  explains the dirt completely, so the dirt says nothing about the gate. The
  suppression is deliberately one-directional: a dirty tree with no conflict to
  explain it is still reported as gate dirt, which is the mg-393f case and is
  unchanged.

  **The classification inverted the outcome.** `failed(infrastructure)` carries
  "establishes nothing about the branch. Resubmit; do NOT dispatch a fix" — but
  the branch genuinely conflicted with main after mg-0155 and mg-5515 landed on
  the same files. Every resubmit re-ran the same deterministic conflict,
  re-dirtied the tree, and re-reported infrastructure: an infinite retry loop
  wearing a transient label. Conflicts now reach `classifyFailure`'s existing
  conflict table and come out `ClassDefect`, and its reason says that
  resubmitting unchanged re-runs the same conflict forever. `classifyFailure`
  carries a second lock: it took the infrastructure branch on the error TYPE
  alone, without reading the git output that same error carried, so it now
  refuses that branch for any output reporting a conflict. Both readings of
  "conflict" go through one `outputReportsConflict`, so the classifier and the
  detector cannot drift into disagreeing — one report containing both `CONFLICT
  (content)` and `failed(infrastructure)` was the tell.

  **"THIS IS NOT YOUR CHANGE: none of those paths are touched by the submitted
  branch" was the exact negation of the truth**, and it was asserted rather than
  computed. The probe diffed `origin/<target>...HEAD`, which is the submitted
  branch only when the worktree sits on its tip — and mid-conflict it does not:
  HEAD is detached at the target with the branch's commits unapplied, so the
  diff came back empty about a branch whose sole commit touched every one of the
  six named paths. It now asks origin about `origin/<branch>` directly, and
  returns whether it could answer at all: a probe that failed and a branch that
  genuinely touches nothing used to be the same empty slice, and the message
  turned that silence into a claim. When ownership is unknown the message says
  so instead.

  **The remedy was destructive if followed.** "Add the paths to `.gitignore`"
  aimed at `scripts/pogo-self-deploy` and `scripts/launchd/pogo-deploy.sh` is an
  instruction to untrack production source — the very files the author was
  trying to land. That suggestion is now withheld whenever any dirty path
  belongs to the branch, and whenever ownership could not be established.
