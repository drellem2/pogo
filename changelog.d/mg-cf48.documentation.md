- **Tier-3 recovery restarts pogod; it does not redeploy it — now said where it is
  read (mg-cf48).** The fleet-triggerable restart trigger was believed missing and
  slated to be built. It already exists and ships: `pogo recovery request` plus the
  `com.pogo.recovery` LaunchAgent. What no document said is the thing that matters
  most to a caller — the recovery agent runs `launchctl kickstart -k` and nothing
  else (no build, no install), so it relaunches the binary already on disk and
  activates **zero merged commits**. An agent that had just merged a pogod change
  would reasonably read "signal a restart" as the mechanism for making it live. It
  is not, which is sufficient to explain merged pogod work sitting inert while a
  working restart trigger was available the whole time.

  Stated now in `docs/operations.md` (tier 3) and in `pogo recovery request --help`,
  by **pointing at `scripts/pogo-self-deploy check`** — safe from anywhere, never
  acts — rather than restating the guard's rule in prose. A prose copy of an
  executable rule can rot; the executable copy is the one that fires.

  **Extending the trigger to redeploy: recommended AGAINST.** It is mechanically
  possible — `pogo-recovery.sh` is launchd-parented and its plist environment
  carries no `POGO_AGENT_NAME`, so it clears *both* arms of `assert_out_of_band`.
  Nothing blocks it, which is why the argument has to. Three mechanisms read off the
  script make a shared queue degrade the safety net: the non-blocking lock exits 0
  on contention assuming the holder will drain what it would have seen — sound for a
  sub-second peer, **false** for a deploy holding it through `do_prove`, so a genuine
  recovery request is silently dropped; `STALE_LOCK_MIN=5` is tuned to that
  sub-second script and would **reclaim the lock out from under a live deploy and
  kickstart it mid-build**; and coalescing, correct for restarts, defeats the audit
  requirement of *which revision was activated*. Beyond the mechanisms, the purposes
  have opposite preconditions — recovery needs pogod unresponsive, the deploy's drain
  needs it responsive enough to report — so a deploy on this trigger **refuses
  precisely in recovery's design case**. mg-18d0 removes most of the motivation: the
  dominant fleet-death class is credential expiry, which neither restart nor redeploy
  fixes.

  **Failure visibility, reframed.** The requester is only guaranteed dead *after* the
  kickstart. Every pre-kickstart failure — build, gates, `do_prove`, drain refusal —
  exits with the drain trap armed and the old pogod running, leaving the requester
  alive to report. The blind window is narrow, but it is narrow *because* the deploy
  script front-loads its refusals for a human-confirmed caller — not a property a
  trigger inherits for free.

  Documentation only: `assert_out_of_band` and `pogo recovery`'s restart path are
  unchanged, both guard arms re-verified (117 passed, 0 failed), and no shipped agent
  prompt was touched (mg-96ad still holds that half). Record:
  `docs/investigations/recovery-trigger-restart-not-redeploy-2026-07-23.md`, which
  also surfaces the product question — `mg` self-installs on merge and `pogod` does
  not, so any consumer's daemon silently drifts from its own repo — recommending
  drift *visibility* through `pogo service status` / `diagnose` as the safe first
  increment, for its own design item. Not built here.
