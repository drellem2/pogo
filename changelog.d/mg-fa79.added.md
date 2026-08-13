- **A displaced pogod becomes detectable: `pogo service supervision` (mg-fa79).**
  Between 2026-08-05 12:45:49 and 2026-08-07 18:37:18 this box ran a pogod that
  launchd had not started. It held the lockfile and served `:10000`; the loaded
  `com.pogo.daemon` job could not acquire that lock, exited 1, and KeepAlive
  respawned it about every ten seconds for **forty-six hours**. The rotated
  daemon log holds **19,274** `Cannot acquire pogod lock … held by pid 4368`
  lines, all inside that window and none after it. Nothing in the fleet reacted
  to any of them.

  It went unnoticed because every instrument anyone had was green. `launchctl
  list` reported *no PID, last exit 1* for a daemon that was up and serving —
  so a check built on it reads a healthy box as dead and, worse, would read a
  **dead** pid 4368 as unchanged. `/health` said something was listening;
  something was. `pogo service verify-revision` compares REVISIONS, and a
  displacing pogod usually execs the same on-disk binary, so it answers with the
  revision the job would have and the check reads AGREES. It is not wrong — it
  is a different question. All three read a *property* of whatever answers.
  None reads *identity*, and identity is the entire defect: the wrong process is
  healthy, current and listening, it is simply not the one launchd restarts when
  it wedges. KeepAlive was faithfully restarting a process that exited in
  milliseconds; a hung pid 4368 would have been restarted by nobody.

  So the new check compares **pids, not properties**: the pid `launchctl print`
  attributes to the job against the pid holding `$POGO_HOME/pogo.pid`. The
  lockfile is the second reading rather than the `:10000` listener because the
  lockfile is the component that *noticed* — its refusal is the record the whole
  episode was reconstructed from, and a check built on the thing that already
  works needs no new mechanism to be trusted. Three exit codes: 0 SUPERVISED,
  1 UNSUPERVISED, 3 UNKNOWN. **UNKNOWN is not a pass**; a check that goes green
  because it measured nothing would reproduce this defect one layer up. A lock
  holder with **no** job loaded reads UNKNOWN and says so in words: that is the
  fleet-owns-it half of the ticket's either/or, a valid single-owner
  configuration, but not one this command can call supervised.

  **UNSUPERVISED does not mean broken.** Throughout the episode pogod was up,
  current and serving. It means the supervision anyone believes
  `com.pogo.daemon` provides is not being provided — and that a restart issued
  through launchd, which is how `scripts/pogo-self-deploy` restarts pogod, acts
  on a process nobody is using.

  **Two readings ride in the report labelled with what they do not prove**,
  because both have already been misread on this box. *ppid*: three separate
  readings of this ticket used `ps -o ppid` and took ppid 1 as showing launchd
  started the process. It does not — an orphan reparents to launchd too, and the
  2026-08 displacer was `setsid()` out of a CLI (`client.newServerCmd`), so it
  had ppid 1 from its first instant and looked exactly like a launchd-started
  daemon. *`last exit reason` and `runs`*: lifetime fields that keep their values
  across a repair, reading `runs=24991` and `OS_REASON_CODESIGNING` on a daemon
  that is healthy and current. Both are report-only, and tests assert that
  neither can reach the verdict.

  The verdict also appears in `pogo service status` and as a report-only line in
  the nightly deploy transcript, after the restart. That placement is the point:
  an on-demand check nobody runs is the same shape as the defect it detects.
  Full measurement, including the two findings below, is in
  `docs/investigations/pogod-displacement-episode-closed-2026-08-13.md`.

- **REFUTED: a deploy that cannot restart the running daemon does NOT retry
  three times (mg-fa79).** Raised as a hypothesis and dispatched to be
  established or refuted. `scripts/launchd/pogo-deploy.sh` gates the 04:00 and
  05:00 fires on the exit code — `rc_reopens_night()` reopens the night for
  **7** (stalled drain) and **10** (a sync that never reached the tree) and
  nothing else, because those two established nothing while every other outcome
  is exactly as true an hour later. A restart that cannot take exits 8
  (`verify_running`), 6 or 11, none of which reopens. One attempt, not three.

- **The nightly redeploy's success has not depended on the binary's mtime for
  some time (mg-fa79).** The ticket's most urgent consequence — install a new
  binary, restart via launchd, orphan keeps serving old code, deploy reports
  success — is already defended: `verify_running()` polls `GET /version` on the
  **running process** and requires main's revision, and `verify_running || exit 8`
  is fatal. The residual is narrow and is what the check above closes: that
  comparison is about revision, and a displacing pogod usually execs the same
  binary, so an orphan already at main's HEAD reads green about a daemon the
  kickstart never touched.
