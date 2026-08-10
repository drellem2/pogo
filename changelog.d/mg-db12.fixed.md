- **The fourth and fifth load-sensitive gate assertions are fixed, and the one in
  the profiler's own test suite was comparing a SLEEP against a CPU BURN
  (mg-db12).** Both went red on a green tree while mg-db58 was measuring the
  merge gate, and both were found by profiling rather than by triage. One of them
  then failed a real merge and was classed **DEFECT** — "establishes a fact about
  the branch" — against a branch that had never touched either file.

  **`scripts/gate-profile_test.sh` Test 3 asserted that a fixed 1s `sleep`
  outranks a fixed amount of CPU work.** Those two quantities are only ordered on
  an idle host: load stretches wall-clock and leaves CPU-seconds alone. Measured
  PASSING at load 47; at load 129 the burner took 2.43s and took rank 1, and at
  load 53.84 it took 1.81s and did it again. The profiler was right both times —
  what broke was the test's belief about which step is slowest. The file's own
  header already forbade this class of assertion, in a paragraph explaining why
  Test 4's thresholds are on CPU seconds and not on `cores`; Test 3, eleven lines
  above it, was the thing being warned about.

  **The rewritten check never names a step.** It reads the table's own `wall`
  column, asserts the property the ranking actually claims — non-increasing down
  the ranks — and cross-checks every row against the JSON record of the same run.
  Both sides of every comparison are wall-clock, so contention moves them
  together and the verdict does not move.

  **It is strictly stronger, demonstrated in both directions rather than
  asserted.** With the library's `sort` replaced by `cat` so the profile is not
  ranked at all, the retired assertion **passes** (`PASS: rank 1 is the 1s
  sleep`) and the new one **fails**, naming the rows. With the burner enlarged
  until it outlasts the sleep — a deterministic stand-in for what load 129 did —
  the retired assertion is the exact failure this ticket was filed for and the
  new one passes. The old form also constrained only row 1; the new one
  constrains all N.

  Two supporting changes make that possible. The fixture's steps are **reordered
  so `QUICK STEP` runs first**: it is the only step whose wall-clock is bounded
  above, and `SLEEPING STEP` cannot come in under 0.9s, so insertion order is
  guaranteed not to coincide with ranked order on any host — which is why the
  unsorted-library case is now catchable at all. And **Test 3b is a negative
  control**: the monotonicity check is pointed at a hand-written ascending table
  and must reject it, because a check that cannot fail is the inert control this
  suite exists to prevent. The header now states the rule the file broke — wall
  against wall, CPU against CPU, a LOWER bound on wall is fine because contention
  can only add; an upper bound or any cross-unit ordering is not to be written
  here.

  **`internal/orphanwatch`'s live probe read one boolean where the report beside
  it had the answer.** At load 33 the constructed orphan was *seen* by the
  detector and binned `cwd_unreadable` — `lsof` would not answer for that pid —
  and the test read only `Reported == false` and declared the detector broken.
  The failure message printed `cwd_unreadable=1` while claiming the orphan "was
  NOT reported". That is not a flaky threshold; it is a probe that could not tell
  a verdict from an instrument limit.

  `Report` now carries **`Dispositions`, the bucket each busy pid landed in**,
  keyed by pid — the same information the four existing counters hold, per
  process instead of in aggregate, with the identity between them pinned by a
  test. A pid absent from the map was never a candidate (below the rate floor,
  or born inside the window), which is deliberately a fifth *state* and not a
  fifth bucket: it is a fact about the process, while every bucket is a fact
  about what the attribution step could do with it.

  The probe reads its own two pids out of that map and distinguishes three
  outcomes instead of two. `orphan` passes. **`unattributable` and `live_owner`
  still FAIL** — those are the rule reaching a wrong verdict about an input
  somebody constructed on purpose, and the failing arm has not been softened.
  **Absent, and `cwd_unreadable`, set `Blind`** — the probe reporting that it
  measured nothing, in the same idiom `verdictwatch.ProbeResult` already uses.
  `Passed()` requires both arms to have been *conducted*, so a positive control
  that was never observed can no longer be trivially green. Before giving up, the
  probe **re-scans up to three times against the same two burners**: nothing
  about the constructed input changes between attempts, only the host's
  willingness to let it be observed, which is what makes retrying legitimate
  rather than a way of rolling the dice.

  A blind run now **skips** in `go test`, loudly and with the disposition
  printed, and exits `pogo check --check orphans --probe` as an **INSTRUMENT
  FAILURE** rather than an error. The new classification is unit-tested on a
  synthetic report, because the state that failed the gate needed a load of 33
  and an `lsof` that would not answer — a rule exercisable only by loading the
  machine is a rule nobody checks.

  **Why the classification mattered more than the flake.** An `infrastructure`
  class says "establishes nothing about the branch; resubmit". `defect` says "a
  fix is warranted" and is not retried. So a load-sensitive assertion failing
  under load blames an unrelated branch, tells its author to fix something that
  is not theirs, and suppresses the retry that would have passed. c0208 recovered
  only because it read the assertion and recognised it was not its own.

  These are the fourth and fifth known members of the family whose first three
  are mg-5551, mg-6092 and mg-6c90. Two things this pair adds: they fire often —
  two of six gate runs during mg-db58 went red on a green tree from these two
  alone — and **the flakiness threshold is lower than the slowness threshold**.
  mg-db58 measured the gate as flat to ~1.07x across load 2.5-47 and only ~1.9x
  at load 138; the orphanwatch probe failed at load 33, inside the band where the
  gate is not measurably slower at all. Slowness and flakiness are not the same
  problem and do not have the same trigger point.

  **This is not a ruling on mg-eed9's strategies A-E**, which remain unranked. It
  is evidence bearing on strategy C (hermeticity first); it does not choose it.
  The suggestion in the ticket that a gate failure printing a high load figure
  should class as `infrastructure` rather than `defect` is deliberately **not**
  done here — a real defect can also fail under load, and that is a question for
  whoever rules, not a task.
