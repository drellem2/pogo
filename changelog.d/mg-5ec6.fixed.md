- **The stranded-work reports carry their own SECOND OPINION — `git cherry`
  over-reports on branches that landed through an ordinary CLEAN rebase, and
  three of the four detectors said "unmerged" with no way for the reader to see
  otherwise (mg-5ec6).** `strandedwork.MeasurePresence` was built for exactly
  this blind spot and shipped with one consumer, `pogo check-stranded`. The three
  routes inside pogod — the spawn-time dispatch refusal, the release-time
  reporter, and the boot sweep — acted on `git cherry`'s bare verdict. The
  refusal is the sharpest of them: it *refuses a dispatch*, and a refusal a
  reader can demonstrate is wrong is how a gate gets overridden by habit. All
  three now render `strandedwork.Corroborate` into the log, the
  `work_item_stranded_push` event and the alert mail — **above** the paste-ready
  submit line, because it is the paragraph that changes how that line reads.

  **It never clears a row, and that restriction is the whole design.** A wrong
  "already present" verdict turns `pogo refinery submit` into `mg done` and
  throws a branch away — the loss this detector family exists to prevent,
  rebuilt inside its own remedy. A failed measurement renders a loud
  `SECOND OPINION UNAVAILABLE` sentence rather than silence or a confident 0%.

  **The mechanism was misattributed, and the correction makes the case commoner.**
  It was filed as evidence of a branch that merged *through a conflict*. There
  are no such branches: the refinery runs a plain `git rebase origin/<target>`,
  `rebase --abort`s on any failure, and classifies every conflict signal as a
  non-retryable defect, so a conflicted branch fails its merge request instead of
  landing. What actually happened to `polecat-a3d4` (onethird_program, landed as
  `2919d28`) is that the rebase **dropped hunks the target already had** — a
  `.gitignore` and five `.pyc` deletions main had independently made — so the
  landed commit is a strict subset of what the branch wrote and the patch id
  moved. The remaining 14 files are byte-identical at `-U0`. That is a second
  route alongside the context drift already documented, and it needs no conflict
  and no unusual history: only that two branches touched the same cleanup.
  Measured: 2008 of 2063 countable added lines already on main, ratio 0.973.
  `conflict_suspect` is now documented as narrower than the row it names.

- **The stranded-work gate and the release-time reporter are confirmed NOT
  open-item-scoped, and there are now tests that keep them that way (mg-5ec6).**
  `pogo check-stranded` iterates `strandwatch.OpenStatuses`
  (`available`/`claimed`/`pending`), so a branch whose item was closed by a
  *sibling's* merge falls outside it by construction — that is documented
  behaviour, and it was raised as a possible hole in the whole family. It is not
  one. The dispatch refusal reads only the spawn request's id, repo and target;
  the release-time reporter runs from `releasePolecatClaim` before that function
  so much as probes `claimed/`. Neither can be silenced by a status. The status
  *is* read once, by `sendStrandedAlert`, and only to word the alert (mg-1af2).

  The shape behind the question: a polecat's submit failed terminally on a DNS
  error with its branch already pushed, its claim was released, a second polecat
  was dispatched onto the same item four seconds later and re-derived the ticket
  for 43 minutes, and it was stopped two seconds after the first one's branch
  merged and closed the item. Today the second dispatch is **refused** — the
  first polecat's branch is attributable to the item by branch-name suffix, so
  the sibling's agent letter does not matter — and the loser's still-unmerged
  branch is **still reported** at release, on a `done` item. Both halves are
  pinned by tests, because "skip the git work if the item is already done" is a
  one-line optimisation that reads as obviously safe and would rebuild the hole.
