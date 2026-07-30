- **Two expected refinery outcomes stop being logged as errors — demoted on the
  full message, so a new variant of either still surfaces (mg-5d3f).**
  Measured over one 50,603-line `pogod.log`, grouped by the full error suffix:

      "rebase --abort] failed: ..."             245 occurrences
        -> "fatal: no rebase in progress: exit status N"     245  (100%)
        -> any other cause                                     0

      "failed to reopen work item ...: ..."      18 occurrences
        -> "not done — it is already claimed (in progress)"    18  (100%)
        -> any other cause                                      0

  Zero variants on either, ever. **The method carries a positive control**: the
  same grouping surfaced several genuinely distinct shapes elsewhere in the same
  file (the unstaged-changes failures among them), so it could have shown a
  variant if one existed.

  Both are expected by construction. The refinery calls `git rebase --abort`
  unconditionally to clear crash debris, and on a clean clone there is nothing to
  abort. It reopens a failed merge's work item so the author can retry, and a
  live polecat's item is still claimed — the refusal means the item is already in
  the state the reopen wanted. Neither is a problem; 263 lines said `failed`
  anyway. **Benign errors train a reader to skip error lines**, which is the cost
  this pays down — not the volume.

  **What changed**

  - `gitCmdOutput` classifies a git failure whose COMPLETE output is a
    measured-benign outcome and logs it as `git [rebase --abort]: no rebase in
    progress, nothing to abort (expected outcome)`. The error still reaches the
    caller unchanged; only the reporting moves.
  - `client.ReopenMGWorkItem` wraps the "still claimed" refusal in a new
    `ErrMGWorkItemNotDone` sentinel, and pogod's failed-merge handler logs it as
    `work item mg-XXXX already claimed (in progress), no reopen needed`.
  - **The match is on the full message, never on the command or a prefix.** This
    is the whole change, not a detail. Suppressing by command would silence the
    class, so a genuinely new `rebase --abort` failure would be swallowed on the
    day it first appeared. Matching the exact text silences only the outcome that
    was measured, so any other wording fails the equality test and still logs as
    a failure — which is where a new variant belongs. The safe form
    self-invalidates when the world changes; the unsafe form does not.
  - The classifier takes only git's output, so it *cannot* be given the command
    to match on. For the reopen message, whose one variable field is the item id,
    the expected text is built from the id and compared for equality — a bound
    field, not a prefix.
  - Scope held to the two measured cases. No general log-level audit; anything
    else needs its own measurement.

  **A DIFFERENT `rebase --abort` failure is demonstrated to still log at error
  level**, because that is the only thing that tells the two implementations
  apart — they behave identically on all 245 benign lines.
  `TestDifferentRebaseAbortFailureStillLogsAsError` builds a real conflicting
  rebase, plants a `.git/index.lock` as a concurrent git process would, and
  asserts the resulting failure is not classified benign and still logs as a
  failure carrying git's output. **That failure is not contrived** — the refinery
  runs git against worktrees that other agents and `gitgc` also touch, so the
  control rehearses a real failure mode rather than a fixture.

  Both arms were checked against the unsafe implementation, and both reject it:
  demoting by command (`args == "rebase --abort"`) fails the acceptance test with
  the git output it swallowed printed alongside; demoting the reopen refusal by
  substring fails on a different state (`it is available (unclaimed)`), on the
  benign wording for a different id, and on the benign wording followed by a
  second problem. `TestBenignGitOutcomeMatchesFullMessageOnly` pins the near
  misses directly — the benign text as a prefix of a longer failure, embedded in
  a longer failure, and with `fatal:` swapped for `error:`.

  If git's wording ever changes, the demotion stops applying and the line returns
  to error level. That is the intended behaviour: an unrecognized message is one
  nobody has measured.
