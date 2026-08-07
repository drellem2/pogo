- **A worker's own verdict now survives its merge, instead of being destroyed by
  the machinery that records completion (mg-dfea).** `pogo refinery submit`
  takes `--verdict` / `--verdict-file`; the refinery carries that JSON object
  on the merge request and pogod's reap merges it into the work item's result
  sidecar under a `verdict` key.

  **What was actually happening, which is not what the ticket said.** The ticket
  reports that pogod "overwrites" the polecat's `mg done --result`. It does not,
  and the difference decides the fix. Driven through the real `mg` binary
  (`TestMGDoneRefusesASecondResultRatherThanOverwritingIt`):

      worker writes first  -> pogod's later `mg done` is REFUSED, worker's result stands
      pogod writes first   -> worker's later `mg done` is REFUSED, pogod's result stands

  `mg done` on a done item is a refusal, never a clobber. pogod destroys the
  verdict by **preemption**: it closes the item the instant the merge lands and
  stops the polecat about half a second later, so the polecat's own call is
  always the one turned away — and step 7 of the polecat prompt told it that
  refusal was success. Nothing overwrote anything and nothing complained.

  That distinction rules out the obvious repair. "Have pogod merge the
  polecat's result rather than overwrite it" cannot be implemented at the
  sidecar writer, because pogod is never second and there is nothing there to
  merge with. The merge has to be with something the author handed over while it
  was still running, and submit time is the only such moment: before the merge
  the author cannot call `mg done` (that closes the item early), after it the
  item is closed and the author is usually stopped. Its verdict is about the
  work it did, not about the merge, so it is knowable then.

  **Measured, over the live store on 2026-08-06:**

      landed items with a result sidecar         : 149
      written by the refinery, not by the worker : 139  (93%)
      carrying ANY field beyond branch/mr/target :  10

  **Verified against the detector rather than by reading the diff**, since the
  whole failure mode is that nothing complains. The predicate is mg-bf3f's own
  (`d2_cause.py` D2.5: `set(sidecar) - {branch, completed_by, mr, target}` is
  non-empty), applied to sidecars produced by the real reap path closing real
  work items through the real `mg` binary:

      BEFORE (author hands over no verdict)  DROPPED — records no verdict
      AFTER  (author hands over a verdict)   CARRIES A VERDICT, verbatim

  The BEFORE arm is the pre-fix behaviour byte for byte: the field is new, no
  earlier pogod could set it, and the merge is gated on its presence. **Both
  directions are asserted, and the negative one matters more:** an author that
  records nothing still produces a verdict-free sidecar, so a real drop stays
  reportable. A fix that made every sidecar read as answered would have removed
  the instrument instead of the defect.

  **The verdict is nested, not flattened.** Merging the author's keys into the
  sidecar's top level would put its claims and the refinery's measurements in
  one namespace, where an author writing `branch` either overwrites the branch
  that actually merged or is silently dropped in favour of it — a smaller
  instance of this same defect. Under `verdict` the author's object is preserved
  verbatim and stays identifiable as the author's.

  **The prompts stop teaching workers to produce a verdict-free result.** This
  half is why the defect went unnoticed, and shipping the mechanism without it
  would leave a channel nobody uses. `polecat.md` step 5 now writes a verdict
  with a `verdict` / `summary` / `evidence` / `unverified` template and passes it
  to `--verdict-file`; step 7 no longer describes `already done` as unqualified
  success — it says the close succeeded, that this is not the same as the
  verdict having been recorded, and that a worker which skipped step 5 is
  looking at the moment its verdict was lost. `polecat-architect.md` (shape D)
  gets the same treatment. `mayor.md` gets it too, for the gh-issue track where
  the coordinator submits on the build worker's behalf and is therefore the only
  actor that can record the reviewer's pass on the build ticket at all.

  **A verdict is validated at submit, while its author is still alive to be
  told.** It must be a non-empty JSON object: a bare scalar is storable but not
  answerable, and `{}` is far more often a shell expansion that produced nothing
  than a statement anyone meant. Nothing beyond the shape is checked — no key is
  required and no value enumerated, because a merge queue is not the right actor
  to rule on what a worker concluded.

  **Scope, stated because the ticket's own instrument invites the wrong
  reading.** `verdictwatch.py` measures the MAIL channel — "an item reaching
  done with no verdict mail received by its filer" — and this change does not
  touch mail. Its counts (30 of 58 for `mayor`, 9 of 13 for `pm-pogo`) are
  unmoved by it and cannot be used to claim the fix worked. The result channel
  is a separate half of the same instruction, measured by D2.5, and that is the
  half repaired here. Also unchanged: the detector's `daniel` row reads 999, and
  that is CREATOR UNKNOWN, not Daniel's dropped verdicts — the creator field
  only became per-agent at mg-ddf4, so every item filed before 2026-07-30 05:00Z
  stores the unix user instead.
