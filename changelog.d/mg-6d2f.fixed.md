- **The nightly redeploy now verifies that the SERVER came back, not just that a
  process answers — and a deploy that leaves the fleet stopped no longer exits
  looking ordinary (mg-6d2f).** Daniel, first-hand, the morning after 2026-08-07:
  "with the pogod restart tonight - it was a bit broken, didnt actually start the
  server and i had to manually start it." The fleet dispatched nothing from
  01:56Z until he intervened at 12:35Z — 10h39m — and nothing on the box said so.

  **The sequence, established rather than inferred.** `pogod` had been up since
  2026-08-04T20:49:17+01:00 and never restarted; `/version` still reports that
  start time. It was orchestration that was down, not the process. `server.New`
  hard-codes `ModeFull` and no config key selects index-only, so the only way to
  reach index-only is a POST to `/server/stop-orchestration` on a running daemon
  — which means the daemon was flipped after 08-04 and stayed flipped. At
  02:00:10Z the nightly's first live action, `POST /agents/drain`, hit
  `RequireOrchestration` and returned HTTP 503. The script refused and exited 6
  **after 0s — before `do_build`, before `do_prove`, before `do_restart`.** The
  restart step never ran at all.

  **Two defects, and only the second is about that night.**

  *`verify_running` could not see this, and never could.* It was the deploy's
  only post-restart check, and it polls `/version` — which is deliberately NOT
  behind `RequireOrchestration`, because the drift check has to be able to read
  an index-only daemon. So on any night the kickstart *does* run, a pogod that
  comes back index-only answers `/version`, reports main's revision, passes
  verification, and the deploy logs `redeploy complete` over a fleet that
  dispatches nothing. That is Daniel's sentence surviving the check that exists
  to catch it. `verify_orchestration` now reads the unguarded `/server/mode`
  after `verify_running` and fails the deploy (exit 11) unless the daemon came
  back in `full`. It **names the layer it judges**: `full` means `/agents`,
  `/refinery` and `/scheduler` are past the guard and dispatch can happen — it is
  not a claim that every crew agent is running, which is a different layer with
  its own ticket (mg-060c).

  *The refusal that did happen was anonymous.* A 503 fell through to `error:503`
  and shared exit 6 with the bootstrap case and the not-answering case. It now
  classifies as `stopped`, is **confirmed against `/server/mode`** before
  anything is announced — a status code is the wrong evidence to declare an
  outage from, and 502/504 deliberately stay `error:<code>` — and exits 12 with
  the fact stated in those words: the fleet is down, this deploy did not restart
  it, and here is the one command that ends it. The refusal itself is correct and
  stays: a deploy must not `kickstart -k` a fleet it could not drain.

  **Loud where it travels.** The 08-07 alert was sent, delivered to `human`, and
  skimmed as ordinary, because its subject was `[pogo-deploy] RED: nightly
  redeploy exited 6` — the same line a build failure gets, and a build failure
  can wait until morning. Exits 5, 8, 11 and 12 (kickstart failed / no pogod came
  back / came back index-only / already stopped) now get
  `[pogo-deploy] FLEET DOWN: ... pogod is NOT serving the fleet`, a banner at the
  top of the body ahead of the attempt/drain/elapsed bookkeeping, and
  `"fleet_down":true` in the emitted event so a detector can filter on it rather
  than on prose. Exits 4, 7 and 9 keep the ordinary RED: they exit with the old
  pogod alive and dispatching, and a banner that fires on every failure is the
  generic subject again.

  **One misdirection removed.** On the exit-12 path the drain restore POSTs to
  the same guarded endpoint that just refused, so it cannot succeed — and the
  08-07 log therefore *ended* on `pogod may STILL be draining and dispatching NO
  polecats`, a confident claim about a flag that was never set, printed last and
  aimed at the wrong problem. `restore_drain` now recognises its own 503 and says
  the drain was never enabled.

  **Verified by exercising a restart, not by reading the script.** The live
  control (`pogo-self-deploy_live_test.sh` §9) stands up a real sandbox pogod,
  flips it into index-only through its own `/server/stop-orchestration`, and
  **reproduces the RED on the real wire**: `verify_running` passes a daemon whose
  `/agents/drain` really returns 503. Then the fix refuses that same daemon, the
  recovery goes GREEN again (a detector that latched RED would pass §9 forever
  and be worthless the next night), and the daemon is really killed and really
  replaced with both checks driven against the successor. The one thing a sandbox
  cannot do is `launchctl kickstart -k` a job it does not own; that single line
  is covered by the unit file asserting the wiring, and §9 says so rather than
  implying more coverage than it has.
