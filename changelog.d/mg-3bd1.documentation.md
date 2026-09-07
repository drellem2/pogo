- **The merge gate now records WHO ELSE WAS ON THE BOX when a signal kills its
  test step — and the SIGTERM that has ended three gates here is measured back
  to 2026-08-19, not 2026-09-07 (mg-3bd1).**

  Three merge gates on this host have been ended by a SIGTERM the refinery did
  not send. The cause is **still not established** and this change does not
  establish it; what it does is stop the next occurrence arriving as bare as the
  last three did.

  **It predates the day it was noticed.** Measured over the whole event log
  (2026-08-16..2026-09-07, 13 recorded gate failures against 75 merges): a third
  occurrence on **2026-08-19 at 178s**, alongside the known 85s and 264s. It was
  never connected to the other two because it was filed under a different name —
  mg-0502's `gateSignalError`, class `indeterminate` — while the September pair
  came through as `exit status 1`, class `defect`. Same phenomenon, opposite
  classifications, which is exactly why "no prior reports" was not evidence.

  **The elapsed time varies for a reason.** All three kills land at the same
  POSITION in the run: `go test` has printed results through
  `internal/ackwatch` and is inside `internal/agent`, the next package in the
  print order and the slowest in the tree. The time varies because that
  package's runtime varies with load — not because the signal arrives at random.

  **The delivery shape is now measured.** Reconstructed from the ORDER of three
  lines of gate output and confirmed with a four-arm control on this host: only
  a signal delivered to the leak guard, the budget shell AND the test process
  reproduces the recorded ordering, and it reproduces the two details the other
  arms get wrong (a *bare* `Terminated: 15` rather than bash's annotated form,
  and the per-package breakdown *absent*). Meanwhile `test.sh`, `build.sh` and
  the gate shell all survived and ran their EXIT traps — so the signal was
  **not** delivered to the gate's process group, which `runGate` creates with
  `Setpgid` and which all of them share. Something addressed a subtree.

  `scripts/signal-witness.sh` now wraps that row and captures both readings on
  any signal: which ancestors are still alive (the group-vs-subtree
  discriminator), and the process table at that instant — ~870 lines here, so it
  goes to `${POGO_HOME:-~/.pogo}/signal-witness/` rather than inline, where it
  would blow the refinery's 8 KB persisted-output cap and evict the failure text
  around it. The file also outlives the refinery worktree.

  It states its own certainty rather than inferring one. `OBSERVED` means the
  signal was caught in a trap; `AMBIGUOUS` means only that the wrapped command
  returned 128+N, which a shell cannot separate from a child that chose
  `exit 143` — calling that a kill would manufacture the same false fact in the
  other direction. It is silent on every run that is not signalled, sees nothing
  through SIGKILL, and reads a reused ancestor pid as alive; all three are said
  out loud in the file and in the report.

  Full record, including what is ruled out and the measurement that rules it
  out, in `docs/investigations/gate-sigterm-variable-elapsed-2026-09-07.md`.
  The classification half — `build.sh` flattening 143 to 1, which is why
  `signalThatKilled` saw an ordinary exit — is mg-b1df's and is deliberately
  untouched here.
