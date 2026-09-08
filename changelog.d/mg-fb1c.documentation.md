- **The ancestor rule was already shipped verbatim in the mayor prompt on the
  night mg-cbee happened, and mg-cbee happened anyway. The half that was
  missing is that pgrep's SIBLINGS stay visible (mg-fb1c).**

  mg-cbee established that `pgrep` cannot see pogod from any agent, and was
  archived still asking why. The answer is `man pgrep` — ancestors are excluded
  — and that sentence was in `prompts/mayor.md` and in a crew memory note the
  whole time. A rule that is true, shipped and read still lost, and this is why:

  Re-measured 2026-09-08 from polecat `tfb1c` (pid 81231, a child of pogod
  6610), one pattern against eight processes that differ only in ancestry:

  ```
  $ pgrep -f claude          -> 7 of pogod's 8 `claude` children
  $ ps -Ao pid,ppid,comm | grep '[c]laude'
    6958 architect  6961 mayor  6964 pa  6988 pm-onethird
    6995 pm-pogo    6999 pm-riemann  41272 polecat t5049   ALL VISIBLE
   81231 polecat tfb1c (THIS SHELL'S OWN PARENT)           INVISIBLE
  $ pgrep -x pogod           -> (empty) rc=1
  $ pgrep -ax pogod          -> 6610    rc=0    (control: it exists, and pgrep can name it)
  ```

  Same executable, same argv shape, same user, same pattern. So an agent that
  sanity-checks the instrument sees seven healthy rows, concludes pgrep works on
  this box, asks it about pogod, and reads `rc=1` as *the daemon is down*. The
  instrument **passes its own smoke test** and then answers one specific question
  wrongly — always, on every box, for every agent. The failure is structural, not
  intermittent, so "it worked when I checked" is guaranteed and worthless.

  The siblings clause now ships in all eight prompts that carry the pgrep bullet
  (`mayor.md`, `crew/doctor.md`, and the six polecat templates), pinned by
  `TestShippedPromptsWarnPgrepIsNotALivenessInstrument` with a failure message
  that names mg-fb1c rather than mg-cbee — the two halves fail for different
  reasons and a reader chasing the wrong ticket finds a closed one. Negative
  control run before commit: deleting the clause from one template fails the test
  on exactly that file, and restoring it passes.

- **Surveyed who still asks `pgrep` about liveness, which mg-fb1c deliberately
  did not do (mg-fb1c).**

  Full report in
  `docs/investigations/pgrep-siblings-and-liveness-caller-survey-2026-09-08.md`.
  **4 live `pgrep` invocations** remain in the repo and all are sound; exactly
  **one is in production code** — `scripts/launchd/pogo-reclaim.sh:466`, which
  uses `-ax` (mg-19e4). The other three are in tests:
  `scripts/signal-sender_test.sh:120` targets the test's own background child, a
  descendant the exclusion cannot reach, and the `pgrep -P` in
  `scripts/pogo-deploy_test.sh:3061` plus the `-x`/`-ax` pair in
  `scripts/pogo-reclaim_test.sh:287` are deliberate controls that reconstruct the
  blind walk in order to demonstrate it. `pogo-deploy.sh` itself has walked
  children with `ps` since mg-19e4. No Go file executes `pgrep` at all —
  every occurrence in `*.go` is a doc comment, an asserted prompt string, or the
  `workerenv` display-label note. Everything else in the corpus is prose.

  **Two findings reported rather than fixed**, both outside this ticket's change:

  1. `internal/agent/prompts/pm/pm-template.md` carries **none** of the pgrep
     liveness guidance — not the mechanism, not the `$(pgrep …)` substitution
     hazard, not the replacement — and every `pm-*` crew agent is an
     `extends pm-template` stub, so no PM prompt on the fleet has it. What it
     does carry is the mg-710c display-label line, a *different* defect with the
     same symptom, which teaches that an empty `pgrep` is a naming artifact.
     mg-fb1c's original measurement was taken from **pm-pogo**, a pm stub.
     Counted with a positive control (`doctor.md` returns 1 for the same
     pattern) so the zero means absence rather than a broken sweep. Mailed to
     pm-pogo and mayor.
  2. The deployed `~/.pogo/bin/pogo-deploy.sh` — the file launchd actually runs
     — is dated **Aug 19** and still carries the pre-mg-19e4 blind
     `pgrep -P` walk (line 1563), 1263 diff lines behind
     `main:scripts/launchd/pogo-deploy.sh`. A survey that read source alone
     would have reported that defect fixed. Remedy is
     `pogo service install-deploy`; not run here.

  Not established: Linux. All of this is macOS — `procps-ng`'s `pgrep -a` means
  "list the full command line", not "include ancestors", so `pgrep -a -f pogod`
  must not be written into anything that runs there.
