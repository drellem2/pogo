- **A nightly deploy that STARTS and does not FINISH was counted as a night that
  ran. Bounded every call the run makes, bounded the run itself, gave every exit
  path a terminal line, and taught the did-not-run witness to judge a run on
  whether it completed (mg-56ac).** `scripts/launchd/pogo-deploy.sh` gains a
  per-step git timeout, git-level transport bounds, a whole-run wall-clock
  watchdog and a `pogo-deploy: end (rc=N after Ns)` line on every path;
  `internal/staleness/nofire.go` gains a hang verdict with its own horizon;
  `internal/driftwatch` mails it and puts it in the subject; `pogo
  check-staleness` prints it. New `deploy_nightly_deadline` event and `hung*`
  fields on `deploy_nofire`.

  **The record, from `~/Library/Logs/pogo/pogo-deploy.log`.** The 2026-08-08 fire
  landed on time, wrote nine lines in one second, and then wrote nothing for
  31 hours 39 minutes:

      [2026-08-08T02:00:05Z] pogo-deploy: start (window=2-6 dry_run=false)
      [2026-08-08T02:00:05Z] GH_TOKEN: sourced from ~/.zshenv (present, 40 chars)
        ... 31h39m ...
      [2026-08-09T09:39:43Z] sync: ~/.pogo/deploy-src at main 738e322
      [2026-08-09T09:43:23Z] pogo-deploy: done — pogod redeployed to 738e322

  No exit code, no ALERT, no RED mail, and no drain-timeout line either — the
  7200s drain cap could not bound it, because the run never reached the drain.
  The crew had been stopped at 00:44Z and stayed stopped for 33 hours, because
  the run that would have brought it back was still sitting in that gap.

  **It was ONE process, and that is a measurement rather than an inference.**
  Across the 54 log lines after the 08-08 start there is exactly one
  `pogo-deploy: start`, one `window:`, one `attempt:` and one `budget:` line, all
  at 02:00:05Z. This script cannot proceed without logging a start line first, so
  a line at 09:39:43Z with no start line above it cannot come from a second
  invocation. The process that started at 2026-08-08T02:00:05Z was therefore
  alive and executing 31h39m later. It blocked and it resumed; it did not die.
  (Three mechanisms proposed for these nights were retracted during the
  investigation — "launchd did not fire", "rung 1 consumed rungs 2/3", and
  "hung", the last of which this restores on the above evidence. What remains
  genuinely unestablished is WHICH call blocked: the gap brackets it to
  `sync_src`, whose four git calls are fetch, `status --porcelain`, `checkout`
  and `merge --ff-only`. Nothing here picks one, and nothing here needs to.)

  **The contrast is the whole defect.** The same step three nights earlier:

      08-05  git fetch FAILED       -> rc=1, four lines, two mails, night settled loudly
      08-08  it never RETURNED      -> silence, and every instrument read GREEN

  So "no exit code" is not a missing detail in the record. It is the fault. Our
  instruments understand a run that fails and have no way to express a run that
  stops.

  **`deploy_nofire` reported the wrong nights, in both directions.** On the
  morning of 08-09 it fired and named `[2026-08-09, 2026-08-04, 2026-08-03,
  2026-08-02, 2026-08-01]`. 08-08 is absent — it had a start line, so a
  {ran, did-not-run} detector put the worst night of the window on the good
  branch. And 08-09 is present although a deploy had just landed on it, because
  the run stamped its attempt with the date it woke up on. The single worst night
  was the one night the instrument reported nothing about.

  **Three bounds, at three layers, because a bound that shares a failure mode
  with the thing it bounds is not a bound.** (1) every git call runs under a
  wall-clock cap (`POGO_DEPLOY_GIT_TIMEOUT`, 300s) and a killed step is
  classified `timeout` — retryable, like every other class that established
  nothing about the tree, so a flaky link retries rather than settling the night;
  (2) git bounds its own transport (`GIT_HTTP_LOW_SPEED_LIMIT/_TIME`, ssh
  `ConnectTimeout` + keepalives), preferred because a git that gives up ITSELF
  returns an error the classifier can read and the alert can print verbatim — an
  08-05, not an 08-08; (3) the WHOLE run is bounded by a watchdog in a separate
  process which alerts and then kills the tree, because the next unbounded call
  will not be a git one. Daniel's read of the environment — "probably just
  shitty WiFi", corroborated by two DNS-shaped wifi-guard radio cycles the same
  evening — is why the fetch is where the bounding starts.

  **THE FIX HAD THE DEFECT IT WAS FIXING, and the suite caught it.** The first
  version bounded the four calls that go through `git_step` and left the queries
  bare — `remote get-url origin`, `status --porcelain`, `rev-parse --short HEAD`.
  Two of those run on the failure path *immediately after* a fetch that has just
  been killed for hanging, against the same remote. The suite hung, with the
  runner sitting in `remote get-url origin` having already been killed once for
  hanging. The reasoning that left them bare — "it only reads `.git/config`, it
  is local" — is the reasoning that leaves any call unbounded, so the guard is
  now structural: every git invocation goes through `git_step` or `git_q`, and a
  test greps for a bare one and has a positive control proving the grep can see
  it.

  **A terminal line on EVERY path.** The EXIT trap moved to the top of `main` and
  now writes `pogo-deploy: end (rc=N after Ns)` last, after the stamp and the
  lock. It covers the skips too — a fire that is late, locked out or already
  settled exits in milliseconds and is healthy, and without a line of its own it
  is indistinguishable to any outside reader from a run that never came back.
  `LOCK_HELD` is what makes the earlier arming safe: an unlocked fire must not
  remove the lock a running deploy holds.

  **The witness now judges START AND FINISH, in two arms, because they rest on
  different evidence.** TERMINATED-LATE — the run wrote a terminal line more than
  six hours after its start — is positive evidence and needs no new runner, so it
  is RED on the real 08-08 record today (31h43m end to end, a 31h39m silence, and
  it names `GH_TOKEN: sourced` as the last thing the run said). NEVER-TERMINATED
  — no terminal line at all, the branch a deadline cannot catch because there is
  no process left to kill — is armed only once the log contains a
  `pogo-deploy: end` line. Before that, a missing terminal line is a fact about
  the runner's VERSION rather than about the run: judged unconditionally it would
  have reported the real 2026-07-31 run, which exited 9 at 02:30 under an older
  runner, as a five-day hang. Runs it cannot judge are counted and named, never
  folded into either verdict — the same horizon argument this file already makes
  about log rotation.

  **Proven RED against a hang, not only GREEN against a healthy run.** The Go
  tests replay the real 08-08 record and require the finding, paired at the same
  instant with the same log where the run finishes on time. The shell suite
  drives an actual `pogo-deploy.sh` against the 08-08 condition reproduced
  exactly — an unbounded git call that never returns, with the per-step bound
  switched OFF on purpose so the whole-run deadline is what is under test — and
  requires the four things the 08-08 run did not produce: a loud log line, a
  mail, a dead process, and a terminal line naming an exit code. Its negative
  control is the same runner with a git that returns, which must not be killed
  and must write the same terminal line.

  **Not covered, and deliberately not claimed.** The bound does not "restore the
  retry ladder": on 2026-08-05, rung 1 exited in one second with the lock free
  and rungs 2 and 3 still logged nothing, so the ladder is broken for a reason
  nobody has established (mg-01f7). 08-01..08-04 produced no lines at all and are
  non-invocations, not hangs. And a run killed by the watchdog before its trap
  runs writes no terminal line — which is the case the witness reports, so the
  two repairs deliberately do not depend on each other.
