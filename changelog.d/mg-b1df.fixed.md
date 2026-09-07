- **`build.sh` reported exit status `1` for every failure it ever saw, including
  the ones that were killed — flattening the only machine-readable evidence
  that a gate step was SIGTERMed rather than red (mg-b1df).**

  All four gate steps read `|| exit 1`. A POSIX shell reports a child that died
  of signal N as 128+N, so a SIGTERM'd `go test` reaches `build.sh` as 143 — and
  `build.sh` turned it into a 1, which is what a failing test suite looks like.

  On `mr-dafhg22tjv1hjkm2144g` (branch `polecat-t2127`, 2026-09-07T20:06Z) the
  kill was reported correctly by three frames and destroyed by the fourth:

  | frame | what it did |
  |---|---|
  | `bash` | printed `Terminated: 15` |
  | `scripts/tmpdir-leak-guard.sh` | captured 143, said so in its own report, exited 143 |
  | `test.sh` (`set -e`) | propagated 143 |
  | `build.sh` `\|\| exit 1` | **flattened it to 1** |

  The refinery's error field on that merge reads `./build.sh failed: exit status
  1`. Nothing downstream could have classified it better — it was not misreading
  the evidence, it was never given any.

  Each step now exits `$?`. `build_test.sh` Test 10 pins the contract at three
  statuses — 143, 7, and a **real** SIGTERM relayed end-to-end through the
  shipped script and its `EXIT` trap — with a passing build as the control.
  Measured: `bash -c 'bash -c "exit 143" || exit 1'` gives 1 and `|| exit $?`
  gives 143; against the old form all three status cases go red with `build.sh
  reported 1 for a step that exited 143`, reproducing the incident, while the
  control stays green.

  **What this deliberately does NOT change.** The refinery still classifies a
  gate that exits 143 as a `DEFECT`. mg-0502 ruled that the exit NUMBER cannot
  distinguish a kill a shell relayed from a status a program chose — *"a fix
  that keyed off the exit NUMBER instead of the wait status would look correct
  and would excuse a whole band of real failures"* — and pm-pogo upheld that
  ruling against this ticket, so
  `TestAGateThatChoseItsOwnHighExitStatusIsStillADefect` stands unmodified. The
  fix here is that the status stops being destroyed on the way out; what a
  consumer concludes from an accurate 143 is a separate question, and this
  change is correct on its own terms either way.

  **Not measured:** what sent the SIGTERM at 20:06Z. The four-concurrent-loads
  reading is the ticket's, and a `pogo host load` read minutes later says
  nothing about that moment.
