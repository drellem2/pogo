- **Four code paths restart or verify pogod and only ONE of them checked which
  revision came back. The other three now share its check (mg-ed4a).** New
  `internal/revcheck` holds the `/version`-against-expected predicate as one
  three-valued answer; `pogo service install`, `pogo service restart` and
  `scripts/launchd/pogo-recovery.sh` all call it, and a new
  `pogo service verify-revision` exposes it as a gate with an exit code.

  What each path verified before:

  | path | what it verified |
  |---|---|
  | `scripts/pogo-self-deploy` `verify_running()` | polls `/version` against `main` — the only real check |
  | `verifyLaunchdRunning()` (`pogo service install`) | `launchctl list` + `/health` — never `/version` |
  | `restartLaunchd()` (`pogo service restart`) | nothing |
  | `scripts/launchd/pogo-recovery.sh` | the kickstart's own exit code |

  **`/health` answers "is something listening", not "is the RIGHT thing
  listening".** `launchctl list` says a job is registered; `launchctl kickstart`
  exiting 0 says launchd accepted the request. A kickstart re-execs whatever is
  on disk, so silently reinstating a stale binary is what a restart *does* when
  the disk is stale — the failure mode is the normal path, not an edge. Measured
  on this box 2026-08-07: `/version` = `d31297f` (a 2026-07-30 build),
  `origin/main` = `73757a8`, **92 commits behind**, alive and healthy and passing
  all three of those checks, for eight days.

  **Three values, and UNKNOWN is not a pass** (the mg-e605 shape). `AGREES`
  requires both revisions to have been *read*; a daemon that will not answer, a
  daemon that answers without a `vcs.revision`, a binary that is not on disk and
  a binary with no stamp are each a distinct `UNKNOWN` with its own reason
  string. `Result.OK()` is true for `AGREES` alone, and there is no boolean
  projection that could collapse the other two — a check that goes green because
  it measured nothing is the defect this closes, so it is closed at the type
  level rather than by convention.

  **It reuses `internal/selfdrift` rather than re-deriving it.** The sentinels,
  the `/version` reader and the on-disk build-stamp reader are selfdrift's, used
  as-is (its `runningRev` is now exported as `RunningRev`). revcheck adds only
  what selfdrift has no reason to have: the *poll*. A single sample of a
  restarting daemon is not an answer — for a few seconds the old process is
  still answering with the old revision, and then nothing is answering at all —
  so `Wait` polls through both transient states to the deadline. Copying the
  vocabulary into a second package would have recreated, one layer down, the
  exact "repair landed in one place, the other copies kept the old shape" defect
  this ticket is about; a test asserts the two packages' sentinels are the same
  constants.

  **The expectation is the plist's binary, not `$PATH`'s.** The service paths
  compare against the vcs stamp of the pogod named in
  `com.pogo.daemon.plist` — what launchd actually execs — read with
  `debug/buildinfo`, so no Go toolchain is required and the check is armed under
  launchd's minimal PATH. A second `pogod` earlier on `$PATH` does not change
  what launchd runs and so must not change what the check expects. The
  expectation is an *argument*, which is what lets the deploy script's `main`
  HEAD and the service paths' on-disk stamp be the same check.

  **`install` and `restart` REPORT; they do not fail — stated explicitly because
  the ticket asked for it to be.** `pogo service install` still exits 0 against a
  stale daemon; installs currently succeed in that state and something may depend
  on it, so the observation ships first and gating is a separate, deliberate
  change. The same conservatism is extended to `pogo service restart`, for a
  second reason: `pogo server start` calls it when `/health` is down, and failing
  a server start over a revision mismatch would refuse to start a server for a
  reason that is not about starting one. Both paths now *print* the verdict,
  which is what makes the state unable to pass unremarked.

  **Tier 3 does gate.** `pogo-recovery.sh`'s exit code now carries the verdict
  (1 DIFFERS, 3 UNKNOWN), so `recovery.log` no longer ends at `kickstart
  succeeded`. Requests are still archived on the *kickstart's* result — a
  kickstart that happened has been serviced, and re-queueing against an artifact
  a restart cannot fix is how a recovery loop starts. No respawn risk:
  `com.pogo.recovery` is `KeepAlive=false`, the queue is already drained and
  `last_restart` already written before the check runs. `POGO_RECOVERY_VERIFY_REVISION=0`
  opts out, and says `revision check SKIPPED` rather than going quiet — a
  disabled check that looked like a passing one would be the original defect with
  an extra step. A `pogo` predating this change exits 1 on the unknown
  subcommand, which is the *same code* that means DIFFERS, so the script probes
  `--help` first and reports an old CLI as UNKNOWN: a confidently wrong alarm
  from this particular check would be worse than no check.

  **`scripts/pogo-self-deploy` is untouched** — not one byte. It is the path that
  already had the check, and its `verify_running()` is the reference revcheck
  mirrors (including the `<unreachable>`/`<unstamped>`/`<missing>` vocabulary,
  which now lives in Go as the same three sentinels). Tonight's deploy depends on
  mg-853a's drain narrowing in that file being verifiably unchanged through
  03:00.

  **Tested where it runs.** `pogo-recovery.sh` is exercised as a subprocess with
  stubbed `launchctl` and `pogo` on a controlled PATH — the artifact launchd
  actually execs, not a Go re-implementation of it — across all five outcomes
  (agrees, differs, no CLI, old CLI, opted out) plus a regression guard that a
  *failed* kickstart still archives to `failed/` and never reaches the revision
  check, because there is no daemon to ask about.

  **This is defence in depth, and not the fix for the eight-night arc.** Measured
  under mg-2def: **0 of the 4 deploy failures reached a restart path at all** —
  every one died at or before the drain. This hardens a path none of those nights
  got to. An unverified restart is a real gap and worth closing on its own terms;
  it is not what cost those nights.
