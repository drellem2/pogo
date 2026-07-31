- **The QA and review fires-check gains the reporting path: a runner's positive
  control is that the RUNNER EXITS NON-ZERO, not that its self-test can fail
  (mg-ce5e).** The consolidated evidence section already asks whether a check can
  fire (`869348a`, mg-0d85) and whether it can fire *the way the defect arrives*
  (`999792b`, mg-ae41). Neither covers the last point on the path — whether the
  failure reaches the exit code. **Printing failures and exiting non-zero are
  trivially separated by a pipe.**

  **The evidence, measured.** A runner had
  `python3 -B selftest.py | tee out_selftest.txt`. Under `set -e` a pipeline's
  status is its LAST command's, and `tee` always exits 0. When its self-test went
  red it printed **six `*** FAILED ***` lines and exited 0** — a clean run by every
  signal a caller reads. pm-onethird then swept the arc: **23 of 63 `run_all.sh`
  pipe to `tee`, and exactly 1 sets `pipefail`.** A population, not an anecdote.

  **Scope, checked on the platform side so it is not re-derived.** pogo and
  macguffin have **zero** `| tee` in any `.sh`, and the four refinery gate scripts
  (`build.sh`, `test.sh` in both repos) contain no pipelines at all and every one
  sets `-e`. An initial grep flagged five apparent pipes in `pogo/build.sh`;
  reading them, they are `||` and a `case` pattern. **The refinery's gates are not
  affected** — this clause is prophylactic for the fleet, and the live remediation
  belongs to the owning PM.

  **A clause, not a bullet, and not a lint.** The concrete hazard is a shell bug
  with a one-line fix (`set -o pipefail`, or redirect and guard on the exit code),
  which is lint material — but the platform repos are already clean, so a fleet
  lint would have no current subject. What generalises past the shell is the
  control question, and it belongs beside the existing fires-check text:
  *can it fire* → *can it fire the way a defect arrives* → **does the failure reach
  the exit code**. Three points on one path.

  **Length, measured.** `polecat-qa.md` 247 → 249 lines; `polecat-review.md`
  247 → 247 (its bullets are one line each, so the whole clause lands in place).
  **Net +2 lines**, against the section's +10 for four rules (mg-0d85) and +2 for
  the invariance half (mg-ae41). The section still has **five** bullets, counted
  structurally by the existing check rather than trusted from the heading.

  **Still not a gate.** Nothing verifies a runner was ever made to fail on purpose;
  the forbid list is unchanged and the clause adds no refusal. The scope pin is
  unchanged: `polecat.md`, `polecat-build-pr.md`, `polecat-triage.md` and
  `polecat-architect.md` are untouched, as are the other four bullets and the
  "five habits" framing.

  **Positive control, predicted before the run.** Prediction: reverting the two
  templates fires 10 errors, five new literals × two files. Observed: exactly 10,
  one per literal per file. The literals are matched on fragments contiguous in
  BOTH files, since `polecat-qa.md` wraps the clause across three lines and
  `polecat-review.md` keeps it on one.

  **Near-misses, mine — both are the clause's own subject, in its own
  verification.** (1) The first positive-control run reported **0** firing pins
  against a prediction of 10. The pins were fine; the run was
  `go test -run TestPolecatPrompt`, which matches no test in this file and exits
  **0** printing `ok ... [no tests to run]`. A control that ran nothing and looked
  green. (2) The first gate run was `./build.sh 2>&1 | tail -20; echo
  "EXIT=${PIPESTATUS[0]}"`, which printed `EXIT=` — zsh spells it `$pipestatus[1]`,
  so the runner's exit code was lost behind a pipe while the tail read as a clean
  pass. Both were caught by predicting the outcome first; the gate was re-run
  redirecting to a file, and it exits 0.
