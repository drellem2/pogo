- **`pogo check-stranded`, the `[stranded-push]` mail and the dispatch refusal
  each had their own decision table, and they disagreed about the same branch
  (mg-8cda, drellem2/pogo#174 follow-up).** After #174, a branch 90% present on
  the target got "check by hand first" in the mail, a paste-ready submit from
  `check-stranded`, and "PARTLY PRESENT — check by hand" followed by an
  unconditional "Get the branch merged instead" from the dispatch refusal. All
  three now read one table, `strandedwork.Decide`, with four cells: resubmit /
  check by hand / suggests landed / rescue. The threshold constants are shared,
  not copied. A `check-stranded` `stranded` row that is not corroborated as
  absent (unmeasured, unavailable, or 50–95%) now leads with the hand check
  (`git log --grep=<item>`) and carries the submit in its comment as "only if it
  did NOT land". Its `conflict_suspect` row leads with the same hand check. At
  ≥95% the mail no longer prints a conditional submit, matching `conflict_suspect`.
  Rows gain a `cell` field in `--json`. `TestStrandedSurfacesAgreeOnEveryBand`
  runs all three surfaces over one real branch per band and fails when any two
  disagree. Two cosmetic fixes: the mail's release-footer wrap is no longer
  ragged, and the closed-item paragraph no longer says the branch "still needs
  submitting". Refinery history and queue (`refused_before`, `in_flight`) are
  still check-stranded only. docs/operations.md, "One decision table", records
  what it would take to change that.
