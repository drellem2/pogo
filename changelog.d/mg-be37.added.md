- **Work that already exists is now found on a CLOCK instead of by accident, and
  an item whose branch merged is CLOSED whatever submitted it (mg-be37).** New
  `internal/strandwatch` and `pogo check-stranded`, the eleventh report-only
  detector; plus a fix in pogod's merge path that removes the second row's cause.

  **THE GAP.** A spawn-time guard already refuses to dispatch a polecat at an item
  with pushed, unmerged work, and it prevented real loss. But it is **triggered by
  dispatch**, so it can only fire when somebody tries to work the item. On
  2026-08-09 four branches were stranded across three repos: one caught by that
  guard, one by a person reconciling something else, **two by the accident of
  somebody looking next door**. From both directions the state is invisible — the
  board reads `available`, the repo holds finished work, and the polecat that did
  it is dead so nothing will mail anybody. Meanwhile priority-wake advertises the
  item and the action it advertises re-derives the work; mg-9a19 lost 1026 lines
  that way.

  **THE SECOND ROW IS WORSE AND IT GOT THE PRIMARY FIX.** pogod closed an author's
  work item at merge only when a polecat had CLAIMED it, so a coordinator
  submitting a stranded branch by hand left the item in `available/` with its work
  on main — four times that night (mg-51f4, mg-00b3, mg-6c90, mg-56ac).
  priority-wake then told the mayor to "claim or dispatch now: mg-6c90" **four
  minutes after that branch merged as b9e1d1b with 1116 insertions already on
  main**. While a branch is unmerged the spawn-time guard refuses the dispatch;
  the moment it merges the guard correctly stops refusing and the item is still
  open, so the window opens at merge and never closes. `reapMergedPolecat` now
  completes the item whenever the MR's author is SHAPED like a work-item id, with
  no registry lookup — a hand-submitted branch has no polecat by construction, so
  requiring one was requiring the condition the case cannot satisfy. Crew authors
  (`mayor`, `pm-pogo`) are excluded by that same shape test, and the
  post-merge-work probe now runs on authorless merges too, or the new close would
  have bypassed mg-d86e's declaration check.

  **THE SWEEP IS ITEM-DRIVEN, and that is what makes it readable.** A branch-first
  sweep of this repo's origin finds **57 of 634** polecat branches with unmerged
  patches — 48 on archived items, 2 on no item at all. Walking the ~115 OPEN items
  instead produced **three rows**, one of them a live instance nothing else had
  found (`mg-65d2`, merged as 0640bc7, item still `available`; `mg-f3ae` likewise).
  Rank is on item status, `available` first, because that is the status
  priority-wake advertises.

  **THE INSTRUMENT AND ITS BLIND SPOT — corrected against the filed prediction.**
  `git rev-list --count main..<branch>` does not work and reports every healthy
  merged branch as stranded forever; `git cherry` compares patch ids and gets that
  right. The ticket predicted a residual blind spot for "a branch the refinery
  rebased through a CONFLICT" and flagged it as unvalidated. **Both halves of that
  turned out wrong, and the truth is worse:** the refinery ABORTS on a rebase
  conflict (mg-eac0) and never merges through one — but it rebases into its own
  copy without force-pushing the branch, so origin keeps the commit as written and
  the target gets it as replayed. **A patch id covers the diff's context lines, so
  no conflict is required — only a neighbouring change.** `polecat-79dc` is the
  exact control:

  ```
  77e012c (origin/polecat-79dc)   patch-id 959d2fa2…
  1e1292f (main)                  patch-id 5a479b4d…
  identical --stat; every added and removed line byte-identical
  ```

  So a **content-level second opinion** runs on every stranded candidate: what
  fraction of the substantive lines the unmerged commits ADD does the target
  already hold? Context drift moves the lines around a change without touching the
  change. At ≥95% (and ≥20 countable lines) the row becomes `conflict_suspect`,
  which recommends **neither** remedy — the two instruments disagree, and closing
  an unmerged branch throws the work away. The threshold is deliberately
  conservative: branches measured at 0.88, 0.91 and 0.94 are also on main and are
  also not demoted. Under-demoting costs a line of report; over-demoting costs a
  branch. The conflict case itself is now a **constructed fixture in the gate**,
  asserting the blind spot in one test and the fallback's catch of it in another.

  **A FAILED READ IS ITS OWN ROW.** The natural predicate —
  `git cherry <target> <branch> | grep -q '^+'` — answers CLEAN whenever git
  FAILS, because a failed git prints nothing and "no output" is how that predicate
  spells clean (measured against an unresolvable ref on mg-b6d1). On a sweep that
  silently converts a stranded branch into an all-clear: this ticket's own defect
  rebuilt inside its own remedy. Anything unreadable is reported as `unjudged`,
  ranked immediately behind `stranded`, and exits 1. Folding it into either
  verdict was rejected in both directions — one hides strandings, the other cries
  wolf on every network blip, and the fleet has measured ~40-minute connectivity
  waves.

  **Two exclusions, both counted and nameable with `--all`:** a running polecat's
  branch (it has unmerged commits on a claimed item because that is what work in
  progress *is* — `polecat-qfa70` was mid-flight during the manual sweep and looked
  identical to a strand) and a branch already in the refinery queue (the remedy for
  a stranded branch is to submit it). An unreachable agent registry is an
  instrument failure (exit 3), not a clean run: without it every live worker in the
  fleet reads as a strand.

  **It REPORTS; it never submits and never closes.** Both remedies are one command
  by hand once you know, and each is destructive in the wrong direction. Documented
  in [docs/operations.md](../docs/operations.md).
