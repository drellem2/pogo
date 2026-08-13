# The pogod displacement episode had a start and an end: 2026-08-05 12:45:49 → 2026-08-07 18:37:18

Filed for mg-fa79, 2026-08-13, from the polecat that took the ticket.

mg-fa79 was opened 2026-08-04 on a live condition ("pogod runs OUTSIDE launchd
while com.pogo.daemon restart-loops on the lock — 129 failures"). Between then
and now **four** separate readings — architect on 08-07 and 08-09, doctor on
08-13 06:20, architect again on 08-13 06:50 — each measured the box, found the
condition absent, and each correctly declined to close on it, because each was a
single point-in-time observation and the ticket describes a race. Four absences
do not compose into a resolution.

This is the fifth reading, and it is a different KIND: not "is the condition
present now" but "when did it start, when did it stop, and has it recurred". The
rotated daemon log answers all three.

## The episode, bounded

`~/Library/Logs/pogo/pogod.log.1` is size-gated (`RotatePogodLogIfNeeded`, 
`fi.Size() < maxPogodLogSize`), **not** rotated per start — so one file spans
many pogod runs and the whole episode survives in it. That file covers
2026-08-05 12:45:49 through 2026-08-13 02:34:37 local.

    grep -c "Cannot acquire pogod lock"          -> 19274
    first occurrence, line 4      preceded by     2026/08/05 12:45:49
    last  occurrence, line 77096  preceded by     2026/08/07 18:37:18
    every occurrence names                        pid 4368

Ten seconds after the last failure, the job won the lock:

    line 77098:  2026/08/07 18:37:28 pogod: starting (pid=32415)

and the eleven starts at 18:35:47 … 18:37:28, exactly ten seconds apart, are the
tail of the respawn loop being logged as it finally succeeded.

**Zero occurrences after 18:37:18.** The current `pogod.log` (started
2026-08-13 03:01:29) has zero. The starts on 08-09, 08-10, 08-11, 08-12 and
08-13 are all clean.

So: the episode ran **≈46 hours**, produced **19,274** failed lock acquisitions,
and ended 2026-08-07 18:37:28 — which is precisely the instant architect
recorded the premise as overtaken, from the other direction. It has not recurred
in the 5 days 16 hours since. The ticket's counts (129 at filing, 7,870 at the
mayor's escalation) sit on the same curve; the mayor's extrapolation from a ~10s
respawn interval was right, and 19,274 is where it stopped.

**This does not say the defect is fixed.** Nothing identified here would prevent
a recurrence: `internal/client.StartServer` still spawns a `setsid()` pogod when
the port is free, and mg-2def records that the Emacs spawn recurs on every Emacs
launch. What it says is that the episode is a closed interval with a measured
length, not an open condition — and that the next occurrence will look exactly
like this one.

## Consequence 3 — the urgent one — is already defended, at the code level

The ticket's most severe claim: a redeploy installs a new binary, restarts via
launchd, the orphan keeps serving OLD code, and the deploy reports success.

`scripts/pogo-self-deploy` does not do that, and has not for some time:

    verify_running() { … rev="$(running_rev)"; [ "$rev" = "$MAIN" ] … }
    running_rev()    { curl -sf "$(base_url)/version" … }        # the PROCESS
    cmd_redeploy:      verify_running || exit 8                  # fatal

The reading is taken from the running process over its own API, never from the
binary's mtime — exactly the technique mg-fa79 asked mg-dd49 to adopt. In the
displaced state the orphan answers with its own (old) revision, the comparison
fails, and the deploy exits 8. Tonight's transcript shows the healthy path:
`verified: new pogod running at 082ec38b0159`.

**The residual is narrow and real.** `verify_running` compares REVISIONS. A
displacing pogod usually execs the same on-disk binary, so if the orphan is
already at main's HEAD — an orphan that outlived a previous deploy — the check
reads green about a daemon the kickstart never touched. Revision cannot see
identity. That gap is what this ticket's fix addresses.

## The mayor's retry hypothesis: FALSE

Dispatched with the instruction to report it false if false. It is false.

> "If a redeploy restarts the launchd JOB while an orphan holds the port, then a
> retry policy might faithfully re-run something that cannot change the running
> binary — three attempts instead of one."

`scripts/launchd/pogo-deploy.sh` gates retries on the EXIT CODE, and on exactly
two of them:

    rc_reopens_night() { case "${1:-}" in 7|10) return 0 ;; *) return 1 ;; esac }

7 is a stalled drain, 10 is a sync that never reached the tree. Both "established
nothing"; everything else settles the night on the first attempt and the 04:00
and 05:00 fires exit 0. A restart that cannot take exits **8** (verify_running),
6 or 11 — none of which reopens the night. Observed in tonight's log at 03:00:01Z
and 04:00:04Z: *"tonight is already settled by attempt 1 … Exit 0."*

The hypothesis was plausible and is unsupported. One attempt, not three.

## New, live, and previously unrecorded: the kickstart can fail on a launch constraint

Chasing `last exit reason = OS_REASON_CODESIGNING` — flagged by architect as the
most likely survivor, and unexplained by a deliberate `kickstart -k` — turned up
a real event in the unified log, one second after tonight's restart:

    2026-08-13 03:01:19.062 launchd[1] (gui/501/com.pogo.daemon [77764]):
      xpcproxy exited due to OS_REASON_CODESIGNING | Launch Constraint Violation,
      error info: c[5]p[1]m[1]e[0], (Constraint not matched) launch type 0,
      failure proc [vc: 10]: /Users/daniel/go/bin/pogod
    … removing service since it exited with consistent failure - OS_REASON_CODESIGNING …
    … exited with exit reason (namespace: 3 code: 0x4) … ran for 29ms

So tonight's deploy restart took **two** spawns. The first, pid 77764, died 29ms
in on a launch-constraint violation; pid 77880 started successfully ten seconds
later and is the daemon running now. The job carries `managed LWCR | has LWCR`,
and `go install` had replaced `/Users/daniel/go/bin/pogod` 54 seconds earlier —
consistent with AMFI evaluating the constraint against a binary whose identity
at that path had just changed, though this write does not establish that
mechanism.

Two things make it worth recording rather than filing as noise:

1. **launchd said "removing service since it exited with consistent failure."**
   Had the second spawn also failed, `com.pogo.daemon` would have been removed —
   no job, no daemon, no supervision. That is strictly worse than displacement
   and would have presented as "the nightly deploy killed the fleet."
2. **Nothing in pogo recorded it.** The deploy transcript shows a clean restart.
   The only surviving trace is a `launchctl print` field that keeps its value
   forever afterward — which is the ticket's consequence 2 exactly: a field that
   reads alarming on a healthy daemon, and would read identically on a sick one.

BOUND, and it matters: `Df` (debug) unified-log messages are not persisted, so
the query that found one occurrence in a 3-day window was really searching an
in-memory buffer. **One occurrence is a floor, not a rate.** Whether this
happens on every deploy, occasionally, or happened once is not established here.
It needs a persistent record, which is a separate piece of work.

## What was shipped

`pogo service supervision` (and the same reading inside `pogo service status`,
and a report-only line in the nightly deploy transcript): compare the pid
launchd attributes to `com.pogo.daemon` against the pid holding the pogod
lockfile, and say whether they are the same process. Three exit codes; UNKNOWN
is not a pass.

It reads IDENTITY, which is the property nothing else read. `launchctl list`
reported "no PID, last exit 1" for a daemon that was up. `/health` said
something was listening. `pogo service verify-revision` compares revisions and
reads AGREES for a displacement whenever both processes came off the same
binary. Every one of them was green for 46 hours.

Two readings ride in the report **labelled with what they do not prove**, both
because they have already been misread on this box:

- **ppid.** Three readings of this ticket used `ps -o ppid` and took ppid 1 as
  showing launchd started the process. It does not. An orphan reparents to
  launchd too — and the 2026-08 displacer was `setsid()` out of a CLI
  (`internal/client.newServerCmd`), so it had ppid 1 from its first instant and
  was indistinguishable this way from a launchd-started daemon.
- **`last exit reason` / `runs`.** Lifetime fields that keep their values across
  a repair. This box reads `runs=24991` and `OS_REASON_CODESIGNING` on a daemon
  that is healthy and current.

Both are report-only, and there are tests asserting that neither can reach the
verdict.

## What was NOT done

- **The ask "make it exactly one owner" is not implemented.** The mechanism is
  identified — `internal/client.StartServer` spawns a detached pogod whenever
  the port is free, which is a second owner by construction — but making the CLI
  defer to launchd is a behaviour change with a real hazard behind it (mg-50e0:
  this host dispatches no nondemand spawns, so deferring to KeepAlive alone can
  produce no daemon at all). That wants a ruling, not a polecat's judgement.
  The detector shipped here is what makes the failure visible if it recurs; it
  is not a repair.
- **No periodic alarm.** The check is on-demand plus one line per nightly
  deploy. Nothing polls it. That is a real limit and it is the ticket's own
  shape — a signal that existed 19,274 times and was read zero times — so it
  should be said plainly rather than implied away.
- **The launch-constraint violation is unquantified**, per the retention bound
  above, and nothing alarms on it.
- **No deploy was tested.** Every claim about the deploy path here is read from
  its source and from tonight's transcript. Running a deploy would bounce the
  fleet, and the ticket forbids it.
