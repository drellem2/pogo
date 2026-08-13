- **The nightly deploy runner READS its fire hours out of the loaded launchd
  job instead of carrying a copy of them (mg-8dcb).** `pogo-deploy.sh` had
  `FIRE_HOURS="${POGO_DEPLOY_FIRE_HOURS:-3 4 5}"` with a comment conceding that
  "a drifted list makes one sentence of one alert optimistic". That was not a
  hypothetical: the plist installed on this box carried a **single 03:00 fire**
  while the constant said `3 4 5` (mg-fc99, corrected by mg-b201 on 2026-08-07).
  Against that world the runner answers "yes, the 04:00 fire will retry" to a
  stalled drain — measured by running the old constant against a reconstruction
  of it — which takes the branch that logs "Not alerting yet." and suppresses
  the RED mail, for a fire that does not exist. The duplication was bought to
  stop the alert being wrong about retries and could only ever make it wrong in
  the more expensive direction.

  The constant is gone rather than guarded: `resolve_fire_hours` reads the
  schedule from `launchctl print gui/<uid>/com.pogo.deploy` — the job that will
  actually fire — and cross-checks it against
  `~/Library/LaunchAgents/com.pogo.deploy.plist`. A value read from the world
  cannot drift from the world. `POGO_DEPLOY_FIRE_HOURS` survives as a pin for
  tests and manual runs, and is labelled in the log as a pin rather than as the
  world.

  **The loaded job is the authority, not the file.** A plist corrected and never
  bootstrapped is byte-identical to a working one and does nothing at 04:00. Where
  the two disagree the run uses the loaded list and says so, naming both lists
  and the command that reconciles them.

  **Failure degrades to no claim, not to a wrong claim.** The read is under
  `run_bounded` like every other exec in the script and is never fatal — a run
  that cannot read its own schedule still deploys. It loses only the right to
  assert anything about later fires, and the alert then says it could not read
  the schedule instead of asserting "no fire is left tonight" about fires it
  never saw. That is a third case the old two-branch sentence did not have.

  **Both readers are shape-agnostic, and that is the whole trap.** What was
  actually installed was `StartCalendarInterval` as a **bare DICT**, not an array
  of one:

      Dict  { Hour = 3, Minute = 0 }     <- what was actually installed
      Array [ Dict { Hour = 3 } ]        <- NOT what was installed

  A reader that walks array elements finds no mismatching element on the dict
  because it finds no elements at all — a naive implementation of this very check
  would have reported GREEN against the state that motivated it. Both readers
  collect `Hour` values from anywhere under the key and never index into an
  array, so the two shapes are indistinguishable to them.

  **Both arms of the positive control are constructed, because the free one was
  spent.** mg-fc99 named the live plist drift as its control and warned it was
  perishable; the assertion was never built, and on 2026-08-07 14:03:28 mg-b201
  installed the corrected plist. Landing that fix was right; what was missing was
  the record. So `scripts/pogo-deploy_test.sh` now builds a bare-dict single-03:00
  plist, shows the check going **RED** on it (`retry_will_follow` false — no retry
  promised), and shows it green against the three-fire array. The trap is
  demonstrated rather than asserted: the suite runs
  `PlistBuddy -c 'Print :StartCalendarInterval:0:Hour'` against both fixtures and
  records that it fails on the dict and succeeds on the array. A live arm reads
  the job actually loaded on the host and cross-checks the installed file against
  it, so the fixtures — which are transcriptions of `launchctl print` output, and
  therefore a duplication of the same kind this ticket removes — cannot go stale
  unnoticed.

  `TestDeployFireHoursAgreeAcrossEveryArtifactThatCarriesThem` no longer compares
  the runner's list to the installer's: there is no list. It now pins the
  **absence** of the constant, and separately that the reader that replaced it is
  still present — deleting both would pass the first check for the wrong reason.
