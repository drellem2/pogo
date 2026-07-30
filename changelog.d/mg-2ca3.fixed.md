- **The coordinator's step-3 health check stops telling it to reopen completed,
  merged work (mg-2ca3).** `internal/agent/prompts/mayor.md`'s refinery
  cross-reference said: if a `done` item's branch "shows as failed in refinery
  history", reopen it so it can be re-dispatched. That predicate tests for a
  **row** when the property is a **relationship**. The refinery appends every
  attempt to history, so one transient gate failure leaves a permanent
  `status=failed` row even after the branch merges seconds later — and
  retry-then-merge is the ordinary case, not the exception.

  Measured against this machine's history on 2026-07-30: ten failed MRs exist,
  and **all ten later merged.** The rule's hit rate on real data was 0/10 and
  its false-positive rate 10/10. Following it would have returned nine `done`
  items and one `archived` item — all with code on `main` — to `available`,
  where the same prompt's step-2 dispatch loop hands them to fresh workers that
  redo landed work. So a false positive here does not produce a wrong *report*,
  it produces duplicated work against `main`; and because the action reads as
  diligence, a coordinator that ran the check and reopened ten items would have
  reported having repaired something.

  Two independent defects, both fixed:

  - **The condition.** It is now stated as a relationship — flag only if a
    `failed` MR exists for the branch **and** no `merged` MR exists for that
    same branch — and the section ships a runnable classifier
    (`pogo refinery history --json | jq …`) that groups by branch and compares
    counts. No count threshold is encoded: `merged >= 1` is the property, and a
    branch may legitimately take several attempts. Empty output is named as the
    expected healthy answer.
  - **The action.** A heuristic running unattended reports; it does not mutate.
    The remedy is now to surface the candidate with its evidence (the failed MR
    id, the absence of any later `merged` row, the item's status) and confirm
    the work is genuinely missing — `pogo refinery show`, plus a `git log main`
    check for the work-item id — before touching anything. `mg reopen` is
    reserved for a case someone has confirmed.

  The section already contained a "do not reopen blindly" caution; it was simply
  keyed to **repeat failure** instead of to **whether the work had landed**. The
  fix re-points that caution rather than bolting a second condition beside it,
  so the section no longer contains both a blind reopen and a warning against
  blind reopens.

  Also corrected in the same paragraph: the surrounding text had the coordinator
  reading history by eye. A **positive-showing** instrument is now specified —
  `pogo refinery history | grep <branch>`, never `| tail`. A bounded window put
  a merged MR one line below the cut and read as "not in history", which is this
  same defect with the sign flipped.

  **No behaviour change.** The refinery was right; only the instruction was
  wrong. New tests pin both halves and exercise **both arms**: the jq program is
  extracted from the shipped prompt and executed against a fixture built from
  the real failed-then-merged rows (`polecat-ebee`, `polecat-1eb6`,
  `polecat-3122`) plus constructed failed-with-no-merge cases — no such case
  exists in real history, which is why a rule verified only against the happy
  path is what shipped. Over that same fixture the old predicate flags six
  branches and the new one flags three; the three it drops are exactly the
  retries that landed.
