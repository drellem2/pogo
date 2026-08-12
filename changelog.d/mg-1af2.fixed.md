- **The stranded-push alert stops firing on every gh-issue reviewer, and the
  remedy it printed stops being a double-submit (mg-1af2).** A review polecat
  reviews by checking the branch under review out, so its own worktree branch
  ends up a **pointer at the builder's head**. `git cherry` then reports the
  builder's commits as work the target does not have — true, and not stranding.
  Verified on 2026-08-12 by SHA: `polecat-p1c60` and `polecat-paaf6` were the
  same commit, all four "stranded" commits were `(mg-aaf6)`, already reviewed,
  and submitted under mg-aaf6 two minutes later. On the gh-issue track this is
  not a rare race — a reviewer's branch is a pointer **every time**, so the
  detector had a false positive on every review polecat that ever ran.

  It was not merely noisy. The notice's remedy is `pogo refinery submit
  <branch> --author=<this item>`, so following it would have submitted the
  builder's work a **second** time, under the reviewer's authorship, racing the
  builder's own submission. The only thing that caught it was a human noticing
  that every "stranded" commit subject named a different work item — an
  inconsistency spotted, not a control.

  `internal/strandedwork` gains a third disposition, `carried`, and the
  discriminator is **ownership rather than containment**. The obvious rule — "is
  any other branch already carrying these commits?" — is symmetric and was
  measured to be wrong: when a reviewer points at a builder, the reviewer's
  branch contains the builder's head just as surely as the reverse, so that rule
  goes quiet on the **builder** too. That is the mg-9a19 case, the one this
  detector exists for and the one that cost 1026 lines. The rule instead uses the
  repo's two naming conventions — commits say whose work they are via the
  trailing `(mg-xxxx)`, a polecat branch says whose item it serves via
  `polecat-<agent name>` — and treats a branch as a pointer only when its own
  name does not claim the work its commits name **and** a branch carrying those
  same commits does. `TestBuilderBranchStaysStrandedWhenAReviewerPointsAtIt` is
  the negative control and it has been observed failing against the symmetric
  rule. The `pre_registration` verdict still outranks the new one: it is the
  verdict whose absence is silent.

  All four readers move together — the release-time alert, the startup sweep,
  the dispatch refusal (which attributed the reviewer's branch to the reviewer's
  item by name, so a review item could never be dispatched at twice), and `pogo
  check-stranded`. Every one of them **records** the suppression rather than
  going quiet: a `work_item_push_carried` event on the release path, a `carried`
  count on the sweep report, an `Excluded` row with the owning branch named in
  `check-stranded`. A check that can only ever remove an alert has to be
  observable, or "correctly identified as a pointer" and "the detector died" are
  the same silence.

  Also fixed, from the same instance: the notice's most emphatic paragraph —
  "the board shows the item as available and priority-wake will advertise it as
  unclaimed" — was **false**, because mg-1c60 was already `done`. A closed item
  now gets a different subject and a different paragraph naming what is actually
  wrong (a branch that never reached the target) instead of a re-dispatch risk
  that cannot happen. The status probe is best-effort and an unreadable answer
  leaves the wording exactly as it shipped.
