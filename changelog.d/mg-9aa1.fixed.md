- **An agent whose process writes and exits fast stops losing its ENTIRE output
  to a tty revocation, and the flake that exposed it is now reproducible on
  demand instead of by loading the host (mg-9aa1).**
  `TestInitialPromptViaArgvAppendsToCommand` failed under fleet load with
  `process exited with <nil>, complete output: ""` — a clean exit, in 0.61s,
  having apparently written nothing. mg-ceae had already removed this test's
  wall-clock deadline, correctly; the failure mode changed rather than went away,
  which is the part worth keeping: fixing how a flaky test REPORTS is not the
  same as fixing what makes it flaky.

  The cause is in `Spawn`, not in the test. A child that writes to its PTY and
  exits does not exit straight away if nothing is draining that PTY — it sits in
  `?Es` and `cmd.Wait()` blocks. With no slave fd open anywhere but the dying
  child, that wait ends after roughly 0.6s in a tty revocation which **discards**
  the buffered output, so the reader that finally arrives gets a clean EOF and
  zero bytes. `pty.StartWithSize` closes the parent's slave as soon as the child
  is started, so pogod was always in that shape and was simply winning the race:
  `readOutput` normally drained the tty before the stall elapsed. On a loaded
  host the goroutine was not scheduled in time, and losing that race cost the
  whole output rather than delaying it. The 0.61s in the report is the revocation
  stall, not a scheduling delay.

  `startPTY` replaces `pty.StartWithSize` on both the spawn and respawn paths and
  keeps a slave fd in the parent, which holds the revocation off; `waitAndHandle`
  releases it once the child is reaped, and that release is what now gives
  `readOutput` its EOF. `readOutput` is also started BEFORE the attach-listener
  bind rather than after, so the socket bind is no longer spent inside the window
  where nothing is draining. The process-group isolation of gh #22 is preserved
  explicitly (`Setsid`+`Setctty`) and still guarded by
  `TestSpawnProcessGroupIsolation`.

  **Measured, both directions.** Output left undrained for 2s was lost in 5 of 5
  trials on the old shape and survived 5 of 5 with the parent holding a slave.
  Both new tests fail against the old shape and pass against the fix, and the
  agent-level one reproduces the reported string exactly — `exit=<nil>, complete
  output: ""`.

  **Load was not what reproduced it.** A sweep of 350 runs of the failing test
  from load 5.5 to 11.7 produced zero failures, so the loaded-host measurement
  the ticket asked for came back negative at the loads reachable here; what
  reproduces it is a test hook that forces the reader past the revocation stall.
  That hook is the point of `internal/agent/ptydrain_test.go` — this flake cost
  three tickets precisely because it could only be met by making the host busy
  and hoping.

  **The trade is stated rather than hidden.** A reader that wedges entirely now
  holds the child in `?Es` instead of letting it exit after 0.6s having lost its
  output. The old code had the same exposure one step later — `waitAndHandle`
  blocked on `outputDone` forever — so this deepens an existing hang rather than
  introducing a new one, and it trades silent data loss for a visible stall.
