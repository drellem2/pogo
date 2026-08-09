- **The external redeploy witness ran nowhere. Armed it with a launchd job of
  its own (mg-a03d).** mg-ce10 landed `scripts/revision-probe.sh` — the check
  that the redeploy actually reached the daemon — and wired it to nothing.
  Measured on the merge commit: 501 lines, referenced by a changelog fragment, a
  docs section and `test.sh`, and by zero schedules, zero plists and zero
  callers. New `scripts/launchd/com.pogo.revisionprobe.plist` and
  `scripts/install-revision-probe.sh` arm it hourly.

  **This is the limiting case of the rule the probe implements.** mg-ce10's rule
  is that a detector for "X did not happen" must not be ACTIVATED BY X — that a
  guard can be *present by existence and absent by effect*. An uncalled probe is
  not merely activated by the wrong path; it is activated by nothing. The ticket
  reproduced its own finding in its own landing, which is recorded here rather
  than quietly fixed.

  **The substrate is a deliberate choice, and the other two candidates were
  refused.** `pogo schedule` was the obvious answer: its scheduler lives inside
  `pogod` and its only delivery mechanisms are a nudge or a mail to an AGENT, so
  arming the probe that way needs both a live `pogod` and an agent turn to
  execute the instruction. A stopped `pogod` is the state the probe most needs
  to report (mg-6d2f), and "turns that never run" is why the probe mails itself
  rather than leaving *"then mail human"* in a scheduler message. The deploy
  runner was refused by the rule itself: a probe invoked by the deploy cannot
  witness the deploy that never fired, which is four of the eight failing nights
  (mg-2def) — that is driftwatch's shape (mg-5bd2), not a fix for it. launchd is
  triggered by the OS clock, independent of `pogod`, the deploy, the refinery and
  any agent turn.

  **The replay policy is declared, not inherited.** launchd has no field for it,
  so the behaviour is the OS's and this is where it is chosen:
  `StartCalendarInterval` is DEFERRED-ONCE across sleep — one run on wake for any
  number of fires missed while asleep. That is right here because the report is
  not a per-interval sample: the age comes from a persisted stamp read against
  wall clock, so a late report is still true and still names the correct age. A
  skip policy would destroy the first report after a wake, which is the one most
  likely to carry news. **What it still cannot cover, stated rather than
  implied:** a host that is POWERED OFF misses the fire outright — launchd defers
  across sleep, not across shutdown — so this witness would have been dark on the
  2026-08-07 no-fire nights, which were a power-off.

  **Hourly, because the clock can only mature as fast as the probe samples.** A
  once-a-day probe first *sees* a divergence up to a day after it starts, so a
  24h threshold would need three consecutive failed nights to fire. The cost of
  sampling hourly is 24 identical notifications a day for one unchanged fact, so
  `revision-probe.sh` gained `--renotify` (default 12h): the sampling rate and
  the notification rate answer different questions and one schedule field cannot
  serve both. A FAILED send is not recorded as a notification — the alert reached
  nobody, so the next run tries again.

  **It reports either way.** `--log FILE` appends one line per run — `OK`,
  `DIVERGED`, `ALERT`, `UNREACHABLE`, `NO-REVISION` — from an EXIT trap rather
  than from each terminal branch, so "either way" is structural instead of
  remembered. A witness that writes only when unhappy cannot be told apart from
  one that is not running, and the ledger's newest-line age is the only thing on
  this box that answers *"is the witness itself still firing?"*.

  **The arming is a tracked shell script for the same reason the probe is.**
  `pogo service install-recovery` and `install-deploy` are the house pattern and
  both live in the `pogo` BINARY, which the redeploy installs — an arming step
  that needs a current `pogo` cannot arm the box whose `pogo` is ten days stale,
  which is the box that needs arming. The installer renders the tracked plist
  rather than mirroring it in a second copy (the drift class mg-b201 paid for),
  refuses rather than half-installing, and verifies with `launchctl print`
  instead of trusting `bootstrap`'s exit code.

  **The load-bearing control executes the job.** A plist can parse, install and
  appear in `launchctl list` while naming a command line nobody has run.
  `scripts/install-revision-probe_test.sh` reads `ProgramArguments` back out of
  the plist the installer wrote and runs that exact vector twice — against a live
  stub daemon and against a dead port — asserting a distinct ledger line from
  each. Writing it caught two defects that `plutil -lint` passes: an XML comment
  containing a flag name (a double hyphen is invalid inside a comment) and a
  control that only ran under a homebrew bash.

  **The circularity, so it is not rediscovered as a bug.** This job reaches a box
  through a merge and an install, and the install is part of the deploy it
  watches. It can never witness the deploy that installed it — only the first one
  after that, and every one thereafter.

  **Scoped to `pogod` on purpose.** The same defect exists for every long-lived
  process defined by committed code: the running bridget reader was two days
  older than the merge that changed its behaviour and nothing reported it
  (mg-c2f5 / mg-8158). A pogod-only witness that runs beats a general one that
  does not; the general case is filed rather than implied.

  **architect's one-night stopgap `deploy-verify-architect` can now be retired.**
  Its retirement condition was never "mg-ce10 merged" — it was "something INVOKES
  `revision-probe.sh` on a schedule". That is now true.
