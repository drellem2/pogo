- **Every "is the daemon current" alarm was indexed to the deploy job's own exit
  code, so a night the job never fired raised nothing. Added a POSITIVE
  staleness detector that does not go through the job (mg-5bd2).** pogod's
  drift-check runner (`internal/driftwatch`) now also samples its own build
  stamp on the existing coarse interval and mails `human` when the commit it was
  built from is older than N; new `[drift_watch] self_stale_after` / `self_repo`
  keys, new `revision_stale` and `revision_stale_disarmed` events.

  The old question was a proxy — *did last night's deploy exit zero?* — and it
  goes dark exactly when the job stops running. On 2026-08-01..08-04 the job
  never fired, so there was no exit code, so there was no alarm; four silent
  nights looked identical to four healthy ones, because health is also "no
  alarm". Measured 2026-08-07: the running daemon was on
  `d31297f4` (commit dated 2026-07-30), **85 commits behind `main`**.

  **It reads the running binary, not the job.** `vcs.revision`/`vcs.time` from
  `debug.ReadBuildInfo` — the same pair `GET /version` reports — so it fires
  under all three deploy failure modes, because all three produce the same
  observable: the running revision stops advancing. Tests cover each, and each
  also asserts what the *legacy* exit-code alarm would have done: it sees the
  loud failure and is silent for the other two. The third — a run that exits 0
  without the new binary reaching the daemon — is the one no existing instrument
  covered.

  **Read in-process, not over the loopback.** A `curl localhost:10000/version`
  can be answered by something that is not pogod (mg-e314, whose doctor row says
  it cannot prevent that); a stamp read out of our own binary cannot. It also
  means the check needs no repo, no network and no config to be armed — the
  mg-5701 lineage's recurring defect is shipping a detector that stays inert
  until somebody remembers to configure it. An unstamped binary (any `go test`
  build) disarms itself and **says so once**, because a blind detector and a
  healthy daemon otherwise produce identical silence.

  **`vcs.time` is the COMMIT's time, and that is the point.** Neither uptime nor
  a recent restart answers this question: pogod's `start_time` was 2026-08-04
  while its binary was built from a 2026-07-30 commit — a bounce re-launched the
  same stale binary. Only the revision says which code is running.

  **N = 7 days.** The deploy is nightly, so a healthy daemon is on last night's
  commit; seven days is seven consecutive missed deploys, past any single bad
  night or weekend. It would have fired on 2026-08-06 — a day before the gap was
  noticed by hand — and the state as measured was 8 days. Not a threshold picked
  to be safely un-fireable.

  **The commits-behind count is context and NEVER a gate.** `origin/main` is a
  remote-tracking ref that only a fetch refreshes, so suppressing on "0 behind"
  would go dark on an unfetched repo — the same proxy-goes-dark shape, one layer
  down. The accepted cost is a false positive if `main` goes quiet longer than N
  while the daemon is current; a false positive is a mail saying "0 commits
  behind", a false negative is another four silent nights.

  **Both arms were run against real daemons, not just replayed.** Against the
  live daemon: `age=8d12h behind_main=85 STALE=true`, notice printed. Against a
  second pogod built from this branch and started alongside it: quiet. The
  positive control is also pinned as a test replaying the measured reading, so it
  keeps firing after the box is fixed.

  **Bounded, because the condition lasts DAYS.** At the 15-minute cadence an
  uncapped alarm would mail ~96 times a day. Notices double instead — detection,
  +1d, +3d, +7d — then stop for that revision; a restart onto a still-stale
  binary re-arms them, and every notice says which one it is and that its own
  silence afterwards is not the condition clearing. The `revision_stale` **event**
  is emitted on every sample regardless, so the mail cap never makes the
  condition invisible: mail is the scarce channel, the event log is the complete
  one.

  **Where it goes, stated deliberately, because requirement 5 deserves an honest
  answer.** To `human` — the same maildir the five `pogo-deploy` REDs of this arc
  reached. All five were delivered *and* notified (5/5 matching lines in
  `~/.pogo/reminders/notify.log`, 02:01–04:01 local), into an inbox now at 1169
  unread, and nothing happened for a week. So the route works and the route is
  not the problem. What differs here: it fires on the nights that produced no
  deploy mail at all, its subject states the consequence with numbers that
  **grow** (`pogod is running 8-day-old code — revision d31297f4 (2026-07-30), 85
  commits behind main`) instead of repeating a constant string a filter can
  match, and it is capped so it cannot train the filtering it needs to avoid. If
  it is filtered anyway, **that is a finding about the channel rather than about
  the detector**, and it is recorded here as one.

  **Found while building the acceptance: `go build` inside a polecat worktree
  stamps the wrong repo.** `~/.pogo` is itself a git working tree (mg-3610), so a
  build under `~/.pogo/polecats/*` walks up and embeds `~/.pogo`'s HEAD —
  `ec68dc1a`, dated 2026-07-07, a SHA that does not exist in this repo at all.
  Rather than folding that into a vague "count unavailable", the detector
  distinguishes it and says so in the subject (`NOT a commit in the pogo repo`),
  because otherwise the notice would report a confident age measured against an
  unrelated project's history. Not fixed here — it is a property of the host
  layout, not of this code.

  Not in scope, deliberately: the three deploy faults (mg-853a drain narrowing,
  mg-b201 missing retry fires, mg-0155 alert misreporting). This is the backstop
  that makes their absence visible, not a replacement for any of them. It is
  REPORT-ONLY and has no seam through which it could redeploy — a reconcile loop
  fighting a genuinely-broken build artifact is the unbounded-reaper shape
  mg-345b ruled against.
