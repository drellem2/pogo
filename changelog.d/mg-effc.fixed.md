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
