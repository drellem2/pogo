- **The `[stranded-push]` mail said the second opinion "agrees the work is
  absent" directly above a number saying 90% of it was already on the target,
  then printed an imperative, paste-ready resubmit (mg-aa7d, drellem2/pogo#174).**
  `strandedwork.Corroborate` now has a partly-present tier (50–95%: "partly
  present — NOT corroborated", check by hand), and below 50% it reports what was
  measured ("N of M lines (P%) present — consistent with absent"). The 50% line is
  `ContentAbsentRatio`, labelled in code as an unmeasured guess. The mail's remedy
  says "resubmit" only in the consistent-with-absent cell. Every other cell says
  "check by hand first", names a grep for the item on the target, and keeps the
  submit line only as conditional. A branch carrying a rescue commit gets no
  submit command at all, matching `check-stranded`'s `rescue_unbuilt` row. "Do not
  dispatch" is unchanged everywhere.
