- **A polecat that STOPS mid-drain no longer silences the deploy's unpushed-work
  check by leaving its denominator — the drain now carries a holder ledger
  across polls and reconciles departures against the source repo's branch refs
  (mg-ea06).** `drain_wait` in `scripts/pogo-self-deploy` re-derives its subject
  set from the live registry on every 15s poll, and `Registry.Polecats()` filters
  on `alive()`. So a polecat holding commits that existed only in its worktree
  could stop between two polls and *leave the denominator instead of satisfying
  it*: `drain_durability` had no line for it, `drain_unpushed_holders` counted no
  line for it, and the deploy printed `no polecat holds unpushed work — N
  running, 0 holding commits that exist only locally` over work that existed in
  exactly one place on the machine. The check was not satisfied, it was
  **silenced**, and the two read identically in the run log. Sharper than
  reported: when the *last* holder stopped, the `count -eq 0` early return fired
  before the durability check ran at all, so that run produced no per-polecat
  report and not even the `0 holding` line — zero evidence rather than misleading
  evidence. The drain now records every holder it sees (name, work item and
  source repo, all already present in each `/agents/drain` snapshot) and, before
  **both** exits, reconciles the ledger against the current snapshot: a recorded
  holder that is no longer there is asked about in the **source repo**, which is
  the handle that survives a stop. A departure whose commits some origin ref now
  holds is `satisfied` — it pushed on its way out, and the stop discharged the
  check. One whose branch resolves with no origin ref holding it is
  `departed-unsatisfied`; one we could not ask about is `unknown`, never
  `satisfied`. The clearing line carries the new count whether or not it is zero,
  so `0 holding` can never again stand alone over a departure. **The seat is
  load-bearing:** the reconciliation runs above the `count -eq 0` early return as
  well as the `held -eq 0` clear, because a fix placed only at the latter misses
  the single-holder fleet entirely — the commonest shape, and one poll from the
  reporter's own. **The reporter's own suggested fix — snapshot the holders and
  block — cannot work,** and this was measured rather than argued: by the time
  the drain would re-ask, the worktree is gone (pogod reaps a *clean* tree on
  exit, and committed-but-unpushed is clean), `durability_of` on a reaped path
  returns `unknown`, and `unknown` holds — so blocking burns the whole window and
  exits 7 over a holder that is already dead, rebuilding mg-853a's failure mode
  and re-creating gh#134's symptom. **So a departed-unsatisfied holder is
  reported loudly and does NOT hold**: it goes out through `err`, reaching
  `ERR_LOG`, the reason record and the nightly's RED alert, while the deploy
  proceeds — mg-797d's rule 1 applied to a new subject, since waiting protects
  nothing once the process that would have pushed is gone. The reconciliation
  reuses gh#134's containment predicate (does **any** ref under
  `refs/remotes/origin/` contain this head, `origin/HEAD` excluded as the
  default-branch symref) rather than ancestry of `main`: the refinery rebases
  before merging, so a branch whose work landed perfectly is not an ancestor of
  `main` afterwards, and a stopped *reviewer*, whose commits live under the
  builder's ref, would otherwise become a false RED on every nightly. **The alert
  names its own deadline**, because not-holding is safe only until archival:
  those commits live solely on `refs/heads/polecat-<name>` in the source repo,
  and `internal/gitgc/sweep.go:346-348` deletes an *archived* item's polecat
  branch with no merge check and no durability check of any kind — routine
  coordinator tidying is what arms that. The deadline goes in the alert rather
  than in a ticket body because the alert is the half that travels; the gitgc
  defect itself is filed separately as mg-0a43 and is not fixed here.
  Refs drellem2/pogo#135.
