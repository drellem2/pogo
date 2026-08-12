- **A trust-dialog watcher that is starved past its budget stops answering the
  dialog anyway (mg-effc).** All three providers' `watchForTrustDialog` selected
  over the deadline timer and the poll ticker as **equal candidates**, and Go
  picks uniformly at random among ready `select` cases. A goroutine starved past
  its budget woke with both channels long ready and took the scan branch about
  half the time — dismissing a dialog the budget had already given up on. Because
  `time.After` delivers once while the ticker keeps firing, each loop iteration
  was a fresh coin flip, so under sustained starvation the wrong branch won nearly
  always: the observed rate was 12/12, not the ~50% a single flip would give. The
  cost landed in the merge gate, where `internal/claude`,
  `internal/codex` and `internal/cursor` failed one `./build.sh` together on a
  branch that was fine — a **merge failure that reads as a defect**, which the
  refinery classed `class=defect` and did not retry. The budget is now held as an
  INSTANT and the timer is only a wakeup hint: the branch that would otherwise act
  checks whether the deadline has passed and takes the spent-budget path if it
  has, so the outcome no longer depends on which of two ready channels the
  scheduler picked. This is the pattern `dispatchScannerIdle` already used for its
  idle window (mg-872b), applied identically in all three files rather than three
  ways. The narrowing is deliberate — a tick arriving after the deadline no longer
  dismisses a dialog it can see — and in production the budget is the whole
  initial-nudge cold-start window, past which nothing is waiting for the composer.
  The same `select` had a second uniform tie-break, between the deadline and agent
  exit, whose exposure this widened: an agent that dies mid-watch is inconclusive,
  not evidence that the sentinel drifted, so the spent-budget path now prefers the
  exit rather than recording a false drift sample about half the time.
- **And the control for it stops depending on a race of its own (mg-effc).**
  `TestLateRenderingDialogIsNeverDismissed` — the positive control in all three
  packages — asked what happens when the hook's budget expires *before* the dialog
  renders, but staged that by starting a 0.7s shell timer against a 250ms budget
  measured from a later moment. The shell starts partway through `Registry.Spawn`,
  and Spawn keeps working afterwards (persona injection shells out to git), so on a
  loaded host the watch can begin with the dialog **already on screen** and dismiss
  it inside its own fresh budget: correct hook behaviour meeting a broken premise.
  That is a **separate** sensitivity from the `select` tie-break, and fixing the
  tie-break does nothing for it — it failed this ticket's own merge gate
  (internal/codex, spawn to auto-accept in one second flat) with the tie-break fix
  already in place and the tie-break's own test passing in the same run. The render
  delay is now gated on a `read` the test releases immediately before starting the
  watch, so it is measured from the watch's start: every source of delay — slow
  spawn, starved shell, late release — pushes the dialog LATER relative to the
  budget and strengthens the premise instead of inverting it. Measured under an
  identical 1.2s process stall: 20/20 failures unfixed, 10/10 with the tie-break
  fix and the old staging, **12/12 passes** with the gate. The control still fails
  when its subject genuinely breaks — a watcher mutated to ignore its budget is
  caught — and the premise is now asserted rather than assumed, so a displaced
  start reports as a broken premise instead of as a dismissal bug.
