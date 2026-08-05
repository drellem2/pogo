- **The refinery's default gates stop running a repo's test suite twice per
  merge — measured at 34% of all gate wall-clock (mg-da30).** With no
  `[gates] commands` configured, `defaultGateCommands` appended every
  conventional script it found at the worktree root, and on this repo both
  exist. So each merge ran `./build.sh` — which itself runs `./test.sh`
  (`build.sh:60`) — and then ran `./test.sh` again as a second gate. Nothing
  overrode it: there is no `.pogo/refinery.toml` in the worktree, in
  `/Users/daniel/dev/pogo`, or in the refinery's own checkout, so
  `loadGateConfig` fell through to the default every time.

  **The two runs were confirmed identical before anything was changed**, since
  a difference in either would have meant they were not duplicates and this was
  not a defect: both are `sh -c "./test.sh"` with no arguments, both run with
  `cmd.Dir` set to the same worktree, both inherit the same environment plus
  `POGO_REFINERY=1` (`runGate`, gaterun.go), and both therefore run the same
  packages and the same dozen bash suites. The running daemon was doing it too,
  not just the source: 496 `gate=./build.sh (1/2)` and 277
  `gate=./test.sh (2/2)` heartbeats in `pogod.log`.

  **What it cost, measured rather than estimated.** From pogod's own gate
  heartbeats across 49 two-gate merges, taking each gate's last reported
  `elapsed`:

      gate 1  ./build.sh    median 5m00s
      gate 2  ./test.sh     median 2m30s   <- the duplicate
      duplicate share of all gate wall-clock: 34.0% (7,560s of 22,260s)

  On the four of those merges positively identified as this repo, 29.0%. The
  refinery holds a single slot, so this came off the queue every other merge
  waits in.

  **It is a third, not a half, and the reason is worth recording.** The ticket
  expected the fix to halve the gate. It does not, because Go's test cache
  makes the second `go test ./...` nearly free — measured on this host, the
  first run cached 0 of 50 packages and the run immediately after it cached 49.
  What the duplicate actually re-paid was `test.sh`'s dozen bash suites, several
  of which stand up real sandboxed daemons and cache nothing. Locally, under
  ordinary fleet load, a cold `./test.sh` took 340s and the identical repeats
  139s and 210s — the same asymmetry the production median shows, and a
  reminder that runtime here is dominated by contention, not by the change.

  **Coverage is not reduced, and the change is conditional on that.** The fix is
  *not* a blanket "prefer `./build.sh`": of the seven repos on this fleet
  carrying both scripts, five (`bridget`, `libdig`, `macguffin`,
  `pogo-sleepwake`, `rent-a-programmer-api`) have a `build.sh` that only
  compiles, and dropping their `./test.sh` would not halve their gate — it would
  stop testing them. `./test.sh` is omitted only where `build.sh` is measured to
  invoke it. `TestDefaultGatesKeepIndependentTestScript` is the control for
  exactly that case, and the acceptance test counts executions rather than
  inspecting the gate list: against the previous defaults it fails with
  `test.sh ran 2 times, want exactly 1`.

  **The detection fails in the safe direction on purpose.** `buildScriptRunsTests`
  is textual, and its two errors are not equally costly: a missed invocation
  leaves both gates listed (the status quo, a suite run twice), while a phantom
  one would remove a real gate. So comments are stripped before matching —
  over-stripping can only lose a match — and only executable forms count
  (`./test.sh`, `bash test.sh`), never a bare mention in an `echo`. Conditional
  invocations count: this repo's `build.sh` runs `./test.sh` inside
  `if [ "$skip_tests" = false ]`, and the gate passes no arguments, so the tests
  run. `TestThisRepoBuildScriptIsDetected` pins the claim against the real
  `build.sh` rather than a fixture, so the day that call is removed the defaults
  stop dropping a gate that is no longer redundant.

  **A dropped gate is said out loud** in the merge's own gate output —
  `(omitting gate ./test.sh: ./build.sh runs it, and running it twice per merge
  tests nothing new)` — because a shorter gate list that nothing explains is
  indistinguishable from coverage quietly going missing.

  One consequence to note: a repo whose gates collapse from two to one also goes
  from two 60-minute timeout budgets to one. The work bounded is now the work
  that is actually done once.
