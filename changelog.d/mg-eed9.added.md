- **The merge gate now ends every run with a ranked per-step profile, and the
  ranking carries a `cores` column that separates a step which is COMPUTING
  from a step which is WAITING (mg-eed9).** `build.sh` is the refinery's
  quality gate. It runs at least twice per merge — once in the polecat's
  worktree, once as the gate — and again on every retry; it is on the critical
  path of the nightly deploy; and it is what sets the fleet's per-repo
  concurrency cap of 3, because "what saturates is one repo's test suite run
  concurrently" (mg-3977). Build time is therefore not an annoyance, it is the
  dispatch queue.

  And nobody could say what it was spent on. The gate emitted a flat stream of
  `echo` banners and hundreds of lines of suite output with no timings in it at
  all, so every proposal to make it faster — select by what changed, move the
  live suites onto a schedule, parallelise the shell suites, fix hermeticity
  first — was an argument about a distribution nobody had measured. This change
  is the instrument and the first measurement. It deliberately makes the gate no
  faster.

  **The first measurement, and it reorders the ticket's own strategy list.**
  Two consecutive full gate runs, 10-core host, ordinary fleet load:

  * **The gate is not a compute problem.** `test.sh` spent **180.62 CPU-seconds
    over 286.89 wall-seconds — 0.63 cores busy of 10, 6.3% of the machine.** The
    `cpu: 0.0 cores busy` reading that opened the ticket is the steady state, not
    one bad night. `fmt.sh` and `go build ./cmd/...` together are **1.03s of
    288.07s (0.4%)** — and are the only two phases that use the box, at 5.39 and
    4.00 cores.
  * **Three live-daemon suites outweigh all ~70 Go packages.** The condition
    annunciator's `NEG A2` controls plus the two `pogo-self-deploy` live suites
    total **119.27s — 41.6% of `test.sh`, against 113.84s for the entire Go
    suite — at 0.11 cores.** On a third run with the cache fully warm they are
    **ranks 1, 2 and 3 at 149.07s of 230.15s — 64.8% — at 0.095 cores**, against
    11.1% for all of Go. Their cost is almost entirely *not on the host*, so
    moving them off the merge path returns ~42-65% of gate wall-clock and returns
    almost no compute contention — and their share *grows* as every other lever
    succeeds, because they respond to none of them. That is the ticket's strategy
    B, promoted from "cheapest to try" to the largest available win.
  * **The Go step's wall-clock is one package.** In both runs the slowest single
    package is essentially the whole step: `internal/agent` 319.19s of 322.71s
    (**98.9%**), then `internal/refinery` 109.34s of 113.84s (96.0%). `ps`
    sampled `internal/agent` at **0.0-1.0% CPU** five times over four minutes —
    the package that sets the duration is waiting, not computing. So selection
    (strategy A) buys nothing unless the dependency map is right about that one
    most-connected package; skipping the other 60 is worth seconds.
  * **Caching works, and the fleet is never in the cached case.** Two identical
    consecutive runs with no Go source change went **1/61 → 55/62 cached**, Go
    step **322.71s → 113.84s (-65%)**. The gate runs at least twice per merge in
    two *different* trees, plus once per retry, and each is a first run in its
    tree. The **mechanism is unverified** and is flagged as the next measurement,
    because it is what decides whether hermeticity (strategy C) buys the cache
    back for every gate or only for reruns.
  * **The concurrency cap's stated premise is inconsistent with the
    measurement.** Load average was observed at **40.60** while that same step
    held **0.93 cores of 10**. mg-3977 set the per-repo cap at 3 because "what
    saturates is one repo's test suite"; what saturates is measured here as not
    being CPU. Recorded as an observation — strategy E is still not recommended
    on this evidence — but the gap should be closed deliberately rather than
    inherited.

  Two corrections to the ticket's own description of the gate: `test.sh` has
  **20 steps, not the 8 described**, and `scripts/pogo-condition-controls.sh` is
  **not** excluded from every merge as stated — its `NEG A2` subset is **rank 2
  at 51.12s**, the most expensive suite after Go. The full breakdown, with the
  ranked conclusion, is in
  `docs/investigations/gate-step-profile-2026-08-09.md`.

  **The instrument's first output was a finding against itself.** Run 1 failed:
  `internal/testsandbox`'s adoption ratchet refused the new
  `scripts/gate-profile_test.sh` because it did not route through
  `scripts/pogo-sandbox` and would have read the developer's live `~/.pogo`. The
  tool built to measure a problem whose stated root cause is 48 suites reading
  live state was, on its first run, a 49th. It was converted rather than
  ledgered. That failing run also exercised the hardest part of the design:
  `set -e` aborted at step 1 of 20 and the profile still printed, `[FAILED]`
  marked.

  **Why the profile measures CPU and not only wall-clock.** The observation that
  opened the ticket was a gate run at 7m19s showing `cpu: 0.0 cores busy` — the
  gate was *waiting*, not computing. Wall-clock alone cannot tell those apart and
  they take opposite remedies: a compute-bound step contends with every other
  gate on the host and only gets faster by doing less work, while a wall-clock-
  bound step (a sleep, a daemon boot wait, a poll interval) costs the gate its
  full duration and costs the host almost nothing, so it parallelises nearly free
  and is the cheapest thing to move off the merge path. A table ranked by
  wall-clock alone puts both in one column and invites one fix for both. Every
  row therefore carries `cores` = cpu/wall, and each row also samples the host's
  1-minute load average as the step *started*, so a row measured under fleet load
  is distinguishable from a quiet one after the fact.

  **The CPU number's silent failure mode, and the control that pins it.** The
  reading comes from bash's `times` builtin, which reports the cumulative
  user+sys time of children this shell has waited for — a step's whole reaped
  process subtree lands in it. The trap is that `t=$(times)` DOES NOT WORK and
  fails silently: a command substitution forks, and a forked child's
  children-times start at zero, so the capture reads `0m0.000s` forever and every
  step is reported as pure waiting. Measured on bash 3.2.57: the same call reads
  0.242s from a function body in the main shell and 0.000s from a subshell one
  line later. `times > "$file"` is a builtin with a redirection and does not
  fork, so every reading goes through a file. `scripts/gate-profile_test.sh`
  Test 4 is the positive control: reintroducing that one substitution takes the
  CPU-burning step's reading from 0.26s to 0.00s and three assertions RED with
  it (demonstrated, not asserted). Its thresholds are on **CPU seconds** rather
  than on `cores`, deliberately — wall-clock grows with host contention while CPU
  does not, so a floor on `cores` would be a floor that passes on a quiet box and
  fails under fleet load on a byte-identical binary, which is mg-6c90's disease
  in the file sent to measure it.

  **The report is armed on `EXIT`, not appended at the bottom.** The run whose
  profile is most worth having is the one that FAILED — a gate that died at step
  12 of 20 is exactly where "which step was the time in" gets asked — and `set -e`
  means that run never reaches the bottom of the script. `gate_step` suspends
  errexit for the duration of the step so the failure is *recorded* before it
  propagates, then returns the step's status unchanged, so a failing suite still
  aborts the gate exactly as before (pinned by `build_test.sh` Test 8 and
  `gate-profile_test.sh` Test 5, both of which assert the non-zero exit and the
  `[FAILED]` row together — an instrument that swallowed a failure would
  reintroduce mg-59d5's defect while looking like an improvement).

  **The Go step is broken down further**, because it is the largest single row
  and one row for ~70 packages is the same undifferentiated blob at finer grain.
  `scripts/go-test-budget.sh` now also reports how many packages ran, **how many
  Go served from cache**, and the ten slowest with their share. The cache count
  is there because the claim that non-hermetic tests make the suite uncacheable
  is testable, and this is the measurement rather than the assertion.

  **On by default, and it writes nothing.** A profile nobody runs is not an
  instrument, and the interesting distribution is the one under fleet load, which
  cannot be requested after the fact — it has to already be recording when the
  load happens. The cost is two `date`s, one `uptime` and one `times` per step,
  under 10ms against steps measured in seconds and minutes. `POGO_GATE_PROFILE=0`
  suppresses the table; `POGO_GATE_PROFILE_JSON=<path>` appends one JSON object
  per run to a file of the caller's choosing. There is **no default file path**:
  the gate runs in ephemeral polecat worktrees under a developer's live `HOME`,
  and a default write location would be a new instance of exactly the live-state
  coupling this ticket is about (mg-5551), added by the tool sent to measure it.
  stdout is the durable channel — the refinery already captures a running gate's
  output text (mg-9adc), so the table lands in the record of the run it describes.

  Adding a suite to `test.sh` without wrapping it in `gate_step` is now a test
  failure rather than a silent omission: a suite added outside the profile joins
  the gate's cost and is invisible in the table built to rank it.
