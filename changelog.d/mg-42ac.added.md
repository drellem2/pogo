- **The nightly redeploy now has a caller: `com.pogo.deploy` (mg-42ac).** The
  deploy pipeline was already complete — `scripts/pogo-self-deploy redeploy`
  drains, builds, proves the detector against the built artifact, kickstarts,
  and verifies the mail-checks came back. What it did not have was anything
  allowed to run it. Its first line refuses any caller inside `pogod`'s process
  tree (mg-1bbf), because the `kickstart -k` it ends with kills that tree
  including the caller, mid-deploy, with nothing left running to report what
  happened — and every crew agent and every polecat is such a descendant. So the
  only redeploy path was a human at a keyboard, and merged `pogod` work sat
  inert for days at a stretch. Not a broken mechanism: an absent one.

  Added: a LaunchAgent firing daily at 03:00 local, plus a thin out-of-tree
  runner at `~/.pogo/bin/pogo-deploy.sh`. launchd parents the job, so it clears
  the ancestry guard by construction. Install with `pogo service install-deploy`.

  **It is a trigger, not a second deployer.** Every gate that matters stays in
  `pogo-self-deploy`; the runner decides only whether to call it, and hands off
  with `--yes` (`confirm()` exits 3 without a tty) and never `--force`. The
  no-force rule is not a convention — the flag is not passed, not plumbed, and
  not settable by env, and `scripts/pogo-deploy_test.sh` asserts both halves,
  because the failure it guards against is a one-word edit somebody makes at 2am
  to get a stuck deploy through.

  **A third job, not a mode of `com.pogo.recovery`.** mg-cf48 examined extending
  the recovery trigger to redeploy and recommended against it, and this honours
  that: a deploy holding recovery's lock through `do_prove` silently drops
  genuine recovery requests, recovery's 5-minute stale-lock reclaim would
  kickstart a live deploy mid-build, and the two have opposite preconditions —
  recovery needs `pogod` unresponsive, the drain needs it responsive enough to
  report, so a deploy on recovery's trigger would refuse in exactly recovery's
  design case. Two labels, two plists, two logs, two lock directories, nothing
  shared.

  **The three ways an unattended deploy goes wrong, and what stops each:**

  *It silently no-ops forever.* `StartCalendarInterval` does not promise 03:00 —
  it promises the job *runs*. A mac asleep at 03:00 has the fire delivered on the
  next wake, which is whenever the lid opens. So the runner drops any fire
  outside `[02:00, 05:00)` and logs why; a range rather than an instant, because
  too narrow and the job never deploys at all, which looks identical to a job
  that was never installed. `date +%H` emits `08`/`09`, which bash reads as
  invalid *octal* — untreated, the two likeliest lid-open hours of the day would
  crash the runner instead of skipping it, and page someone over a non-failure.
  Pinned base-10, with tests.

  *It clobbers the developer's tree.* The build runs from a dedicated checkout at
  `~/.pogo/deploy-src` that nothing else writes to — never `~/dev/pogo`, which at
  03:00 may hold a half-finished edit or an in-progress rebase. Even in the
  dedicated tree, dirty **aborts** rather than resets (a reset destroys the
  evidence of whatever made it dirty) and diverged aborts too (`--ff-only`:
  merging would deploy commits nobody meant to build).

  *It forces a bad deploy.* It cannot. `do_prove` runs after the build and before
  the kickstart, so its RED (exit 9) leaves the running `pogod` untouched — a
  clean refusal, not an outage. The runner classifies each exit code into the
  operator response it actually implies and alerts `pm-pogo` **and** `human`;
  collapsing them into "the deploy failed" is what makes a nightly unactionable
  at 08:00.

  **Two gates that exist to protect the fleet from the job itself.** A drift
  check (read-only, and reusing `pogo-self-deploy check`'s verdict rather than
  forming a second opinion — its notion of clean already covers CLI drift,
  mg-ddf1) exits 0 without bouncing when nothing is owed: a fleet-wide bounce
  costs every agent its session, and doing that for a no-op is strictly worse
  than not running. And after a successful deploy the runner re-reads the
  mail-check schedules once the grace period is up, alerting on any that existed
  before the bounce and did not return — on 07-17 they were present immediately
  after the kickstart and were reaped minutes later as `agent_gone`, leaving crew
  with no mail loop while every health signal read green.

  **`GH_TOKEN` is sourced at run time, never from the plist.**
  `~/Library/LaunchAgents` is world-readable, so a token there is a token
  disclosed to every process on the box until somebody notices and rotates it. It
  is matched out of `~/.zshenv` one line at a time and `eval`'d alone — not
  sourced wholesale, because that file's own `export PATH=` would strip `go` and
  reproduce the 07-23 `go: command not found` failure verbatim. The value is
  never logged, and a test asserts it is not.

  Every external tool is called by absolute path after an identity check. Bare
  `mg` on macOS binds `/usr/bin/mg`, the Micro-Emacs editor: it satisfies
  `command -v`, panics headless, and delivers no alert at all (mg-015f, mg-dd5f).
  The alert path is resolved before anything that can fail — a job whose first
  failure is "I cannot tell you about failures" is the silent nightly again.
