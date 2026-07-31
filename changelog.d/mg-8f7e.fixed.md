- **The nightly redeploy's drain budget is derived from the deploy window
  instead of being a 30-minute constant, and a stalled night gets two more
  chances instead of waiting 24 hours (mg-8f7e).**
  The 2026-07-31 nightly exited **7 — drain stalled**. The box was healthy and
  dispatch was correctly restored, but ~24h of merges did not activate and the
  next attempt was a day away.

      02:00:04Z  enabling drain mode
      02:00:04Z  draining: 5 polecat(s) still active — waiting...
      02:20:08Z  draining: 3 polecat(s) still active — waiting...
      02:30:17Z  ERROR: 3 polecat(s) still active after 1800s drain timeout

  The three that blocked it had uptimes of **1h33m, 1h19m and 38m**. Two had
  individually been running longer than the entire budget before the drain
  started. The 2026-07-30 deploy had drained **0 polecats in 3m50s**, so 1800s
  was not a calibration that expired — it was a guess whose first real exercise
  was the night it failed.

  **Two things were measured before anything was designed**, because the ticket
  was filed without them and its leading hypothesis depended on both.

  - `--drain-timeout` already existed on `pogo-self-deploy`. The nightly wrapper
    was not passing it, so the unattended run — the only one with all night
    available — was using the default meant for a human at a terminal.
  - **New spawns cannot begin once `draining=true`.** `handleSpawnPolecat`
    503s them before any spawn work, it is the only path that creates a polecat,
    and it is live in the running daemon (`1b1f12d` is an ancestor of the running
    `d31297f`). The 5→4→3 was pure drain progress. The proposed fix "stop
    dispatching before the drain starts" was already shipped, and the failure
    needs no explanation beyond the one the numbers give.

  **What changed**

  - The nightly passes a **window-derived** `--drain-timeout`:
    `(seconds until the window closes) - POGO_DEPLOY_RESERVE`, capped at
    `POGO_DEPLOY_MAX_DRAIN` (2h), and the fire **skips entirely** below
    `POGO_DEPLOY_MIN_DRAIN` (10m) rather than starting a drain it cannot finish —
    a timed-out drain has still stopped dispatch for its whole length and
    delivered nothing. A 03:00 fire now gets 2h where it got 30 minutes. The
    budget is 0 rather than negative past the window's end; a negative handed to
    `--drain-timeout` would read as an expired deadline and manufacture an
    instant exit 7 out of nothing being wrong except the hour.
  - The deploy window widened from `2-5` to `2-6`. That is not slack: the
    window's width is now the deploy's patience.
  - `com.pogo.deploy` fires at **03:00, 04:00 and 05:00**. At most one deploy
    happens per night — the later fires are **retries**, gated on a recorded
    outcome in `~/.pogo/deploy-attempt.stamp`. Only **exit 7** reopens the night:
    it is the one exit whose cause is "the fleet was busy", and the only one that
    built nothing and bounced nothing. A build failure or a `do_prove` RED fails
    identically an hour later and would mail a duplicate alert, so it settles the
    night. An absent or unparseable record reads as "first attempt" — a corrupt
    stamp costs one extra attempt rather than silently disabling the nightly.
  - A fire that skips (late, locked out, already settled) records **nothing**.
    An EXIT trap that recorded an attempt for a skipping fire would settle a
    night whose real attempt was still running on the lock.

  **Retries are the weaker half and are deliberately second.** A drain is
  monotone only while it runs; the moment an attempt gives up, dispatch is
  restored and the fleet refills, so three 30-minute attempts are strictly worse
  than one 90-minute attempt against a busy fleet. Under the production numbers
  a 03:00 attempt using its full budget is still draining when the later fires
  land, and they exit 0 on the lock. That ordering is the design, not a
  side effect.

  **The cost of waiting is now measured, not assumed.** `draining=true` refuses
  all new dispatch, so a drain is an interval in which no new work starts for
  anyone — which made "give it longer" look free. It mostly is: the freeze ends
  when the fleet quiesces, not when the budget expires (07-30 drained in 3m50s
  under the same 1800s), so a larger budget costs nothing on a night that would
  have succeeded. The cost falls only on nights that **stall**, which now report
  the frozen interval in the log, in the RED alert, and as `dispatch_frozen_s` on
  the retry event — for exit 7 only, since every other outcome's elapsed time
  includes the build and the bounce. `POGO_DEPLOY_MAX_DRAIN` then becomes a
  decision with data behind it rather than an argument.

  **The RED alert explained the wrong failure.** It carried one paragraph, about
  exit 9, under every exit code — so the 07-31 exit 7 was told *"the control
  suite went RED before the kickstart ... the artifact is the problem"* for a
  failure that never reaches the build. The closing advice was right by accident,
  which is worse than wrong: a reader who trusts the reasoning goes and reads a
  build log that does not exist. `remedy_for_exit` now returns the paragraph true
  of the code it got. The alert also asserted "did not retry" unconditionally;
  that claim is now computed from the exit code, whether a fire remains tonight,
  and whether that fire would get a usable budget, and a stall with a real retry
  behind it emits `deploy_nightly_retry_pending` instead of mailing.

  **Every assertion was confirmed to fail against the pre-fix behaviour** —
  the one-paragraph remedy, a floorless budget (which goes negative and lets an
  under-window fire reach the deploy path), a retry gate that cannot tell a
  stalled night from a failed build, a dropped `--drain-timeout`, and a plist
  back to a single fire.

  **Deploying without a full drain** (letting in-flight polecats survive the
  restart) was considered and **not** done: it changes the safety property the
  drain exists to hold, and mg-46a4 §5 records what survivors cost — a polecat
  that outlives a kickstart `setsid`s out of the process group and is invisible
  to every registry after it. See
  `docs/investigations/redeploy-drain-budget-2026-07-31.md`.
